"""freezer.py — 公式/代码/图片/表格冻结为占位符，译后回填。

翻译前把不该动的整块内容替换成 ⟦F1⟧ ⟦F2⟧ … 形式的占位符，
模型只能翻译占位符之间的自然语言；译后由本模块原样回填。
占位符用 U+27E6/U+27E7（数学白方括号），学术正文几乎不会出现。

冻结顺序：代码块 → 行间公式 $$…$$ → HTML 表格 → HTML img →
Markdown 图片 → 行内公式 $…$。顺序先长后短，防止大块内部的小
模式被先替换导致截断。
"""

from __future__ import annotations

import re
from dataclasses import dataclass, field

PLACEHOLDER_RE = re.compile(r"⟦F(\d+)⟧")

# 各类模式的正则；顺序即冻结顺序（先长块后短块）
_PATTERNS = [
    # 围栏代码块 ```…``` 或 ~~~…~~~
    re.compile(r"(?m)^(?:```|~~~).*?(?:```|~~~)$", re.S),
    # 行间公式 $$…$$（可跨行）
    re.compile(r"\$\$.+?\$\$", re.S),
    # HTML 表格整体（MinerU 输出的表格是 HTML）
    re.compile(r"<table\b.*?</table\s*>", re.S | re.I),
    # HTML img 标签
    re.compile(r"<img\b[^>]*>", re.I),
    # Markdown 图片 ![alt](url)
    re.compile(r"!\[[^\]]*\]\([^)\s]*(?:\s+\"[^\"]*\")?\)"),
    # 行内公式 $…$（单行内，非空；不吃 \$ 转义）
    re.compile(r"(?<!\\)\$[^\$\n]+?(?<!\\)\$"),
]


@dataclass
class Frozen:
    """freeze() 的结果。

    text     冻结后的文本（喂给模型）
    mapping  占位符编号 → 原始内容
    """

    text: str
    mapping: dict[int, str] = field(default_factory=dict)


def protected_spans(text: str) -> list[tuple[int, int]]:
    """返回冻结区（代码/公式/表格/图片）的 (start, end) 字符区间，重叠已合并。

    供切段使用：切点不得落进区间内部——把 $$…$$ 或围栏代码从中间
    切断会让定界符不配对，冻结正则匹配不到，裸 LaTeX 喂给模型被改写。
    """
    spans: list[tuple[int, int]] = []
    for pat in _PATTERNS:
        for m in pat.finditer(text):
            spans.append(m.span())
    spans.sort()
    merged: list[tuple[int, int]] = []
    for s, e in spans:
        if merged and s < merged[-1][1]:
            merged[-1] = (merged[-1][0], max(merged[-1][1], e))
        else:
            merged.append((s, e))
    return merged


def freeze(text: str, start: int = 1) -> Frozen:
    """把 text 中的公式/代码/图片/表格替换为占位符。

    start 为起始编号，方便多段共用一套编号空间（本工程按段独立，
    通常用默认值 1）。
    """
    mapping: dict[int, str] = {}
    counter = start

    def _sub(m: re.Match) -> str:
        nonlocal counter
        mapping[counter] = m.group(0)
        ph = f"⟦F{counter}⟧"
        counter += 1
        return ph

    out = text
    for pat in _PATTERNS:
        out = pat.sub(_sub, out)
    return Frozen(text=out, mapping=mapping)


def unfreeze(text: str, mapping: dict[int, str]) -> str:
    """把占位符回填为原始内容。未知编号原样保留（validator 会抓住）。"""

    def _sub(m: re.Match) -> str:
        n = int(m.group(1))
        return mapping.get(n, m.group(0))

    return PLACEHOLDER_RE.sub(_sub, text)


def placeholders(text: str) -> list[int]:
    """按出现顺序列出文本中的占位符编号。"""
    return [int(n) for n in PLACEHOLDER_RE.findall(text)]
