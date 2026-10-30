"""validator.py — 译文机械校验（纯代码检查，不依赖模型自觉）。

两层：
- validate(src, dst)：冻结态校验，翻译质量重试的依据；
- validate_final(md)：回填后的成品校验，$ 配对与 \\begin/\\end 配对。
"""

from __future__ import annotations

import re
from collections import Counter

from freezer import placeholders

HEADING_RE = re.compile(r"(?m)^#{1,6}\s")
DOLLAR_RE = re.compile(r"\$\$|(?<!\\)\$")
BEGIN_END_RE = re.compile(r"\\(begin|end)\{([^}]+)\}")


def _table_pipes(text: str) -> list[int]:
    """按出现顺序收集管道表格行的竖线数。

    只计同时以 | 开头且以 | 结尾的行：物理文献正文/公式常以狄拉克记号
    打头（|00⟩、|p⟩ = …，本库实测 57 行），只看行首会把它们当成表格行，
    模型重排换行后就误报"表格结构被改动"——真表格行必然首尾都有竖线。
    """
    out: list[int] = []
    for line in text.splitlines():
        s = line.strip()
        if s.startswith("|") and s.endswith("|"):
            out.append(s.count("|"))
    return out


def validate(src: str, dst: str) -> list[str]:
    """冻结态对比原文/译文，返回问题列表（空 = 通过）。"""
    reasons: list[str] = []

    src_ph = Counter(placeholders(src))
    dst_ph = Counter(placeholders(dst))
    if src_ph != dst_ph:
        missing = sorted((src_ph - dst_ph).elements())
        extra = sorted((dst_ph - src_ph).elements())
        if missing:
            reasons.append(f"占位符缺失 ⟦F{','.join(map(str, missing))}⟧")
        if extra:
            reasons.append(f"占位符多出 ⟦F{','.join(map(str, extra))}⟧")

    src_heads = HEADING_RE.findall(src)
    dst_heads = HEADING_RE.findall(dst)
    if len(src_heads) != len(dst_heads):
        reasons.append(f"标题数不一致（原文 {len(src_heads)}，译文 {len(dst_heads)}）")

    # 表格结构：管道表格的逐行竖线数必须与原文一致（规则 3：
    # 只译单元格文字）。模型多写/少写一个 | 整张表渲染就废，且
    # 占位符校验查不到——表格不是占位符，是可译文本。
    src_pipes = _table_pipes(src)
    dst_pipes = _table_pipes(dst)
    if src_pipes != dst_pipes:
        reasons.append(
            f"表格结构被改动（原文 {len(src_pipes)} 行竖线数 {src_pipes[:6]}，"
            f"译文 {len(dst_pipes)} 行 {dst_pipes[:6]}），逐行保持 | 与列数不变"
        )

    # 译文异常短（占位符除外几乎没内容）大概率是拒答/半途而废
    def _visible(t: str) -> int:
        return len(re.sub(r"⟦F\d+⟧", "", t).strip())

    if _visible(src) > 200 and _visible(dst) < _visible(src) * 0.2:
        reasons.append("译文相对原文异常简短，疑似未完整翻译")

    return reasons


def validate_final(md: str) -> list[str]:
    """回填后的成品校验。"""
    reasons: list[str] = []

    # 行间公式 $$ 成对（总数须为偶数）
    display = md.count("$$")
    if display % 2 != 0:
        reasons.append(f"行间公式定界符 $$ 数量为奇数（{display}）")

    # 行内 $ 总数（含 $$ 折算）须为偶数
    dollars = len(DOLLAR_RE.findall(md))
    if dollars % 2 != 0:
        reasons.append(f"$ 定界符总数为奇数（{dollars}），行内公式未闭合")

    # \begin{x} 与 \end{x} 每种环境配对
    envs: Counter = Counter()
    for kind, name in BEGIN_END_RE.findall(md):
        envs[name] += 1 if kind == "begin" else -1
    bad = sorted(n for n, c in envs.items() if c != 0)
    if bad:
        reasons.append("\\begin/\\end 不配对: " + ", ".join(bad))

    # 残留占位符说明回填漏了
    leftover = placeholders(md)
    if leftover:
        reasons.append(f"回填后仍残留占位符 ⟦F{sorted(set(leftover))}⟧")

    return reasons
