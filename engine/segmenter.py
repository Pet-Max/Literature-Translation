"""segmenter.py — 把 Markdown 按标题层级切成适合喂给 LLM 的段。

规则：
1. 遇到标题行（# ~ ######）且当前段非空 → 开新段（标题随新段走）；
2. 段内按空行分节累加，超过 max_chars 上限 → 收束当前段；
3. 单个段落本身超上限（罕见，如超长表格冻结后仍超）→ 在上限附近
   最近的换行处硬切。

切段保持原文逐字拼接（"\n\n" 连接），保证回填组装后与原文只差
段与段之间的分隔符——不对内容做任何改写。
"""

from __future__ import annotations

import re

HEADING_RE = re.compile(r"(?m)^#{1,6}\s")
REF_HEAD_RE = re.compile(r"^\[\d{1,3}\]\s")
DEFAULT_MAX_CHARS = 3000


def split(text: str, max_chars: int = DEFAULT_MAX_CHARS) -> list[str]:
    """返回切段列表；空文本返回 []。"""
    text = text.strip("\n")
    if not text.strip():
        return []

    lines = text.split("\n")

    # 第一步：按标题行切成"节"（标题 + 后续内容直到下一标题）
    sections: list[list[str]] = [[]]
    for line in lines:
        if HEADING_RE.match(line) and sections[-1]:
            sections.append([line])
        else:
            sections[-1].append(line)
    sections = [s for s in sections if "".join(s).strip()]

    # 第二步：节内按空行分小块，贪心装袋到 max_chars
    chunks: list[str] = []
    bag: list[str] = []
    bag_len = 0

    def flush() -> None:
        nonlocal bag, bag_len
        if bag:
            chunks.append("\n\n".join(bag))
            bag, bag_len = [], 0

    for section in sections:
        blocks = re.split(r"\n\s*\n", "\n".join(section).strip("\n"))
        for block in blocks:
            if not block.strip():
                continue
            if REF_HEAD_RE.match(block):
                # 文献条目（[N] 开头）独立成段：不被前后文打包，
                # 便于上层逐条甄别（保留原文或单独翻译）
                flush()
            if len(block) > max_chars:
                flush()
                chunks.extend(_hard_split(block, max_chars))
                continue
            if bag_len + len(block) + 2 > max_chars:
                flush()
            bag.append(block)
            bag_len += len(block) + 2
        # 标题节边界处收束，避免翻译段横跨两个章节
        if "".join(section).lstrip().startswith("#"):
            flush()
    flush()
    return chunks


def _hard_split(block: str, max_chars: int) -> list[str]:
    """超长单块的硬切：优先在换行处断开。"""
    parts: list[str] = []
    rest = block
    while len(rest) > max_chars:
        window = rest[:max_chars]
        nl = window.rfind("\n")
        cut = nl if nl > max_chars // 2 else max_chars
        parts.append(rest[:cut])
        rest = rest[cut:].lstrip("\n")
    if rest:
        parts.append(rest)
    return parts
