"""engine.py — Python 翻译引擎边车：stdio JSON-RPC 服务循环。

协议（每行一个 JSON 对象，UTF-8）：
  请求   {"id": 1, "op": "translate", "payload": {...}}
  响应   {"id": 1, "ok": true, "result": {...}}
         {"id": 1, "ok": false, "error": "可读的错误文本"}
  事件   {"event": "progress", "done": 12, "total": 34, "failed": 1,
          "done_idx": [0,1,2], "failed_idx": [5]}

支持的操作：
  ping                                   自检
  pdf_text  {pdf_path}                   原文 PDF 文本层抽取（fitz，用于拼写校准词表）
  segment   {md_text}                    只切段，返回段数（估进度用）
  translate {md_text|md_path, out_path,
             llm{base_url,api_key,model,concurrency},
             glossary[[en,zh]], user_rules, bilingual}
                                         全流程翻译并写出译文 MD

stdout 只走协议，日志一律 stderr。
"""

from __future__ import annotations

import json
import os
import re
import sys
import threading
import time
import traceback
from concurrent.futures import ThreadPoolExecutor, as_completed

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import checkpoints  # noqa: E402
import prompt as prompt_mod  # noqa: E402
import segmenter  # noqa: E402
import validator  # noqa: E402
from freezer import freeze, unfreeze  # noqa: E402
from translator import LLMClient, LLMError  # noqa: E402

VERSION = "0.1.0"
QUALITY_ATTEMPTS = 4  # 1 次初译 + 3 次质量重试
SWEEP_ATTEMPTS = 2    # 收尾补翻时每段的重试次数
SWEEP_PAUSE = 2       # 补翻前的错峰秒数：网络层已有指数退避自我节流，
                      # 补翻又是串行低并发，等太久对确定性失败毫无收益

_write_lock = threading.Lock()


def emit(obj: dict) -> None:
    with _write_lock:
        sys.stdout.write(json.dumps(obj, ensure_ascii=False) + "\n")
        sys.stdout.flush()


def log(msg: str) -> None:
    sys.stderr.write(f"[engine] {msg}\n")
    sys.stderr.flush()


# ---------------------------------------------------------------- 各操作

_REF_HEAD = re.compile(r"^\[(\d{1,3})\]\s")
_AUTHOR_INITIALS = re.compile(r"\b[A-Z]\. (\s*[A-Z]\. )+")
_YEAR = re.compile(r"\((?:19|20)\d{2}[a-z]?\)")


def make_reference_detector(enabled: bool):
    """返回逐段判定函数：段首 [N] + 编号单调递增 + 文献特征 → 文献条目。

    防误伤三重保险：编号必须严格递增（正文偶发段首编号不会恰好接在
    上一个文献号之后）、段内须有作者缩写或年份、条目长度受限。
    判定必须在切段后、进入线程池前按顺序单线程执行（内部有状态）。
    """
    state = {"last": 0, "started": False}

    def detect(chunk: str) -> bool:
        if not enabled:
            return False
        m = _REF_HEAD.match(chunk.lstrip())
        if not m:
            return False
        n = int(m.group(1))
        if state["started"]:
            if not (state["last"] < n <= state["last"] + 3):
                return False
        if len(chunk) > 800:
            return False
        if not (_AUTHOR_INITIALS.search(chunk) or _YEAR.search(chunk)):
            return False
        state["started"] = True
        state["last"] = n
        return True

    return detect


def op_ping(payload: dict) -> dict:
    return {"pong": True, "version": VERSION, "python": sys.version.split()[0]}


# LaTeX CM 字体的连字码点：PyMuPDF 文本层里 "effects" 常以 ﬀ (U+FB00) 出现，
# 按 ASCII 字母分词会把词切成 "e"+"ects" 毒化校准词表，先还原为普通字母序列。
_LIGATURES = str.maketrans({
    "\ufb00": "ff", "\ufb01": "fi", "\ufb02": "fl",
    "\ufb03": "ffi", "\ufb04": "ffl", "\ufb05": "st", "\ufb06": "st",
})


def op_pdf_text(payload: dict) -> dict:
    """抽取 PDF 文本层（PyMuPDF）。用于拼写校准：原文本层是干净参照。"""
    p = payload.get("pdf_path")
    if not p:
        raise ValueError("payload 需要 pdf_path")
    try:
        import fitz  # PyMuPDF
    except ImportError as err:
        raise RuntimeError("PyMuPDF 未安装（pip install pymupdf），拼写校准不可用") from err
    doc = fitz.open(p)
    try:
        text = "\n".join(page.get_text() for page in doc)
    finally:
        doc.close()
    # 连字码点还原；CM 字体还会把 ≠ 映射为"组合删除符+等号"，一并归一
    text = text.translate(_LIGATURES)
    text = text.replace("\u0338=", "\u2260")
    return {"text": text}


def op_segment(payload: dict) -> dict:
    text = _load_md(payload)
    chunks = segmenter.split(text)
    return {"count": len(chunks)}


def op_translate(payload: dict) -> dict:
    text = _load_md(payload)
    out_path = payload["out_path"]
    llm_cfg = payload["llm"]
    bilingual = bool(payload.get("bilingual", False))

    chunks = segmenter.split(text)
    total = len(chunks)
    log(f"切段完成：{total} 段")

    # 参考文献甄别：线程池前按序单线程判定（内部有编号状态）
    detect_ref = make_reference_detector(bool(payload.get("skip_references", True)))
    ref_flags = [detect_ref(c) for c in chunks]
    n_refs = sum(ref_flags)
    if n_refs:
        log(f"识别参考文献条目 {n_refs} 段，按原文保留不翻译")

    system = prompt_mod.build_system(payload.get("glossary"),
                                     payload.get("system_rules"),
                                     payload.get("user_rules"))
    # 缓存指纹：规则/术语/开关/模型任一变化即弃用旧进度，防止改规则后复用旧译文
    cache_key = checkpoints.chunk_hash(json.dumps({
        "system": system,
        "bilingual": bool(payload.get("bilingual", False)),
        "skip_refs": bool(payload.get("skip_references", True)),
        "model": llm_cfg.get("model", ""),
    }, ensure_ascii=False, sort_keys=True))
    progress = checkpoints.Progress(
        payload.get("progress_path") or (out_path + ".progress.jsonl"),
        cache_key,
    )
    reused = sum(1 for i in range(total) if progress.reusable(i, chunks[i]))
    if reused:
        log(f"断点续跑：复用已完成的 {reused} 段（源段哈希匹配）")

    client = LLMClient(llm_cfg["base_url"], llm_cfg["api_key"],
                       llm_cfg["model"])
    concurrency = max(1, min(int(llm_cfg.get("concurrency", 3)), 8))
    failed: list[dict] = []
    results: dict[int, str] = {}
    ok_set: set[int] = set()  # 已完成段（文献保留/断点复用/翻译成功）

    def emit_progress(sweep: bool = False) -> None:
        bad = {f["i"] for f in failed}
        # done 发「成功数」而非「已处理数」：与最终响应 done=total-failed
        # 同口径，前端「N 段译文」不会因失败段虚高
        ev = {"event": "progress", "done": len(ok_set - bad), "total": total,
              "failed": len(failed), "done_idx": sorted(ok_set - bad),
              "failed_idx": sorted(bad)}
        if sweep:
            ev["sweep"] = True
        emit(ev)

    def accept_segment(i: int, frozen, zh_raw: str) -> None:
        zh = unfreeze(zh_raw, frozen.mapping)
        progress.record(i, chunks[i], zh)
        results[i] = zh
        ok_set.add(i)

    def attempt_segment(i: int, frozen, attempts: int) -> str | None:
        """单段翻译 attempts 次（重试附上次的译文与纠错提示），仍不过则登记失败。

        重试请求带上上次的 assistant 译文：请求本是无状态的，不带的话
        纠错提示指向的「上一次输出」模型根本看不到，等于盲重试。
        """
        user_msg = prompt_mod.build_user(frozen.text)
        history: list[dict] = []
        last_reasons: list[str] = []
        try:
            for attempt in range(1, attempts + 1):
                resp = client.complete(system, user_msg, history=history)
                reasons = validator.validate(frozen.text, resp)
                if not reasons:
                    if attempt > 1:
                        log(f"第 {i + 1} 段第 {attempt} 次尝试通过")
                    return resp
                last_reasons = reasons
                log(f"第 {i + 1} 段校验未过（第 {attempt} 次）：{'；'.join(reasons)}")
                history = [
                    {"role": "assistant", "content": resp},
                    {"role": "user",
                     "content": prompt_mod.build_correction(reasons)},
                ]
        except LLMError as e:
            log(f"第 {i + 1} 段调用失败：{e}")
            last_reasons = [str(e)]
        progress.record_failed(i, "；".join(last_reasons))
        failed.append({"i": i, "reasons": "；".join(last_reasons)})
        return None

    def translate_one(i: int) -> None:
        chunk = chunks[i]
        try:
            if ref_flags[i]:
                results[i] = chunk  # 文献条目：原文保留，不计费不进校验
                ok_set.add(i)
                return
            if progress.reusable(i, chunk):
                results[i] = progress.done[i][1]
                ok_set.add(i)
                return
            frozen = freeze(chunk)
            zh = attempt_segment(i, frozen, QUALITY_ATTEMPTS)
            if zh is None:
                # 最终失败：保留原文占位，标记转人工
                results[i] = chunk
                return
            accept_segment(i, frozen, zh)
        except Exception as e:  # noqa: BLE001 — 单段内部错误只损失该段
            # 兜底（翻译/回填/断点落盘的意外异常）：登记该段失败并保留
            # 原文，不让一个畸形响应或磁盘错误杀掉整轮 run。
            # 宽捕获触发时该段必然尚无失败条目（attempt_segment 只在自身
            # 兜底路径登记且不外抛），直接 append（GIL 原子），无竞态。
            log(f"第 {i + 1} 段内部错误：{type(e).__name__}: {e}")
            traceback.print_exc(file=sys.stderr)
            failed.append({"i": i, "reasons": f"内部错误: {type(e).__name__}"})
            results[i] = chunk

    with ThreadPoolExecutor(max_workers=concurrency) as pool:
        futures = {pool.submit(translate_one, i): i for i in range(total)}
        for fut in as_completed(futures):
            fut.result()  # worker 已兜底一切异常
            emit_progress()

    # 收尾补翻：全池跑完后对失败段集中再捞一轮——此时并发已归零，上游限流/
    # 瞬时故障大多已恢复；成功段已在断点里，不受影响。
    if failed:
        log(f"收尾补翻：{len(failed)} 段未过，{SWEEP_PAUSE} 秒后集中重试")
        time.sleep(SWEEP_PAUSE)
        retry_idx = [f["i"] for f in failed]
        # 先发事件再清空：事件如实带上待补翻段（failed 计数与编号），
        # 不闪「失败 0」的假事件
        emit_progress(sweep=True)
        failed.clear()  # attempt_segment 对仍失败的段会重新登记，避免重复条目
        for i in retry_idx:
            frozen = freeze(chunks[i])
            zh = attempt_segment(i, frozen, SWEEP_ATTEMPTS)
            if zh is None:
                continue
            accept_segment(i, frozen, zh)
            log(f"收尾补翻：第 {i + 1} 段已救回")
            emit_progress(sweep=True)
        if failed:
            log(f"收尾补翻后仍有 {len(failed)} 段失败，保留英文原文待人工处理")
        else:
            log("收尾补翻：失败段全部救回")
        emit_progress(sweep=True)

    client.close()

    # 组装：完成段用译文，失败段保留英文原文
    parts: list[str] = []
    for i in range(total):
        zh = results[i]
        if bilingual and not ref_flags[i]:
            # 文献段（原文保留）不再附加引用块，避免同段英文出现两遍
            orig = chunks[i].strip()
            zh_block = zh.strip()
            quoted = "\n".join("> " + ln for ln in orig.split("\n"))
            parts.append(f"{zh_block}\n\n{quoted}\n")
        else:
            parts.append(zh.strip())
    final_md = "\n\n".join(parts) + "\n"

    os.makedirs(os.path.dirname(os.path.abspath(out_path)), exist_ok=True)
    with open(out_path, "w", encoding="utf-8", newline="\n") as f:
        f.write(final_md)

    warnings = validator.validate_final(final_md)
    if warnings:
        log("成品校验警告：" + "；".join(warnings))

    return {
        "total": total,
        "done": total - len(failed),
        "failed": failed,
        "kept_references": n_refs,
        "warnings": warnings,
        "out_path": out_path,
    }


def _load_md(payload: dict) -> str:
    if payload.get("md_text"):
        return payload["md_text"]
    p = payload.get("md_path")
    if not p:
        raise ValueError("payload 需要 md_text 或 md_path")
    with open(p, encoding="utf-8") as f:
        return f.read()


OPS = {
    "ping": op_ping,
    "pdf_text": op_pdf_text,
    "segment": op_segment,
    "translate": op_translate,
}


def main() -> None:
    sys.stdout.reconfigure(encoding="utf-8", newline="\n")
    sys.stderr.reconfigure(encoding="utf-8", newline="\n")
    # stdin 同样钉死 UTF-8：调用方未设 PYTHONUTF8 时，Windows 会按
    # ANSI 代码页解码管道输入，中文载荷直接变成孤立代理字符。
    sys.stdin.reconfigure(encoding="utf-8")
    log(f"engine {VERSION} 就绪，等待指令")
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            req = json.loads(line)
        except json.JSONDecodeError as e:
            emit({"id": None, "ok": False, "error": f"非法 JSON：{e}"})
            continue
        if not isinstance(req, dict):
            # 合法 JSON 但不是对象（数组/字符串/数字）：req.get 会抛
            # AttributeError 带崩引擎，回错误响应继续服务
            emit({"id": None, "ok": False, "error": "请求必须是 JSON 对象"})
            continue
        rid, op = req.get("id"), req.get("op", "")
        fn = OPS.get(op)
        if fn is None:
            emit({"id": rid, "ok": False, "error": f"未知操作：{op}"})
            continue
        try:
            result = fn(req.get("payload") or {})
            emit({"id": rid, "ok": True, "result": result})
        except Exception as e:  # noqa: BLE001 — 边车必须把错误报回而不是崩溃
            log(f"op={op} 失败：{type(e).__name__}: {e}")
            emit({"id": rid, "ok": False,
                  "error": f"{type(e).__name__}: {e}"})
    log("stdin 关闭，退出")


if __name__ == "__main__":
    main()
