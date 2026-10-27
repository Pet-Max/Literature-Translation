"""checkpoints.py — 断点续跑：每段译文即时落盘 JSONL。

文件格式（每行一个 JSON 对象）：
  {"meta": {"key": "任务指纹"}}                        首行：规则/术语/模型指纹
  {"i": 3, "h": "源段内容哈希", "zh": "……译文……"}   该段完成
  {"i": 5, "failed": "占位符缺失 ⟦F7⟧"}              该段最终失败
重启同一任务时，仅当源段哈希与当前段一致才复用——源文本被校准/修改
过的情况下不会错误配对。任务指纹不一致（改过规则预设/术语表/模型）
则整体弃用旧进度重译，避免静默复用旧规则的译文。
"""

from __future__ import annotations

import hashlib
import json
import os
import sys
import threading


def chunk_hash(text: str) -> str:
    return hashlib.sha1(text.encode("utf-8")).hexdigest()[:12]


class Progress:
    def __init__(self, path: str, cache_key: str | None = None):
        self._path = path
        self._cache_key = cache_key
        self._meta_stamped = False
        self._lock = threading.Lock()
        self.done: dict[int, tuple[str, str]] = {}  # i -> (源哈希, 译文)
        self.failed: dict[int, str] = {}
        self._load()

    def _load(self) -> None:
        if not os.path.exists(self._path):
            return
        stale = False
        with open(self._path, encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    rec = json.loads(line)
                except json.JSONDecodeError:
                    continue
                if "meta" in rec:
                    if (self._cache_key is not None
                            and rec.get("meta", {}).get("key") != self._cache_key):
                        stale = True
                elif "zh" in rec:
                    # 旧格式无 h 字段：哈希记为空，永不匹配（强制重译）
                    self.done[rec["i"]] = (rec.get("h", ""), rec["zh"])
                    # 同段可能先失败后成功（重试成功），成功行覆盖失败标记
                    self.failed.pop(rec["i"], None)
                elif "failed" in rec:
                    self.failed[rec["i"]] = rec["failed"]
        if stale:
            self.done.clear()
            self.failed.clear()
            try:
                os.remove(self._path)
            except OSError:
                # 删除失败（杀软/句柄占用，Windows 上常见）：截断兜底。
                # 只吞异常不截断的话，旧 meta 会留在文件头混进后续追加流，
                # 下一轮 resume 又整体作废、文件还只增不减
                try:
                    with open(self._path, "w", encoding="utf-8"):
                        pass
                except OSError:
                    pass

    def reusable(self, i: int, chunk: str) -> bool:
        """仅当已记录且源段哈希与当前段一致时才可复用。"""
        if i not in self.done or i in self.failed:
            return False
        return self.done[i][0] == chunk_hash(chunk)

    def record(self, i: int, chunk: str, zh: str) -> None:
        self.done[i] = (chunk_hash(chunk), zh)
        self.failed.pop(i, None)
        self._append({"i": i, "h": chunk_hash(chunk), "zh": zh})

    def record_failed(self, i: int, reason: str) -> None:
        self.failed[i] = reason
        self._append({"i": i, "failed": reason})

    def _append(self, rec: dict) -> None:
        try:
            with self._lock, open(self._path, "a", encoding="utf-8") as f:
                if self._cache_key is not None and not self._meta_stamped:
                    f.write(json.dumps({"meta": {"key": self._cache_key}},
                                       ensure_ascii=False) + "\n")
                    self._meta_stamped = True
                f.write(json.dumps(rec, ensure_ascii=False) + "\n")
        except OSError as e:
            # 磁盘满/文件被锁：断点丢失但 run 继续（译文仍在内存，最终
            # 会写进 out_path）；调用栈上抛会杀掉整轮翻译
            sys.stderr.write(f"[checkpoints] 断点写入失败（本次不落盘）：{e}\n")
