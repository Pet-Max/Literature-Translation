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
