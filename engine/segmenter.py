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

import freezer

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
    """超长单块的硬切：优先在换行处断开。

    切点不落进冻结区（代码/公式/表格/图片）内部——$$…$$ 被从中间
    切断后定界符不配对，freeze 匹配不到，裸 LaTeX 会被模型改写。
    区间本身超限时宁可整段超长也不截断。
    """
    spans = freezer.protected_spans(block)

    def span_at(pos: int) -> tuple[int, int] | None:
        # 严格落在区间内部（恰好在边界上不算被切）
        for s, e in spans:
            if s < pos < e:
                return (s, e)
        return None

    parts: list[str] = []
    rest = block
    base = 0  # rest 在 block 中的起始偏移（spans 用 block 坐标）
    while len(rest) > max_chars:
        window = rest[:max_chars]
        nl = window.rfind("\n")
        cut = nl if nl > max_chars // 2 else max_chars
        hit = span_at(base + cut)
        if hit is not None:
            s, e = hit
            if s - base > max_chars // 2:
                cut = s - base  # 切在冻结区起点，区间整体留给下一段
            else:
                cut = min(e - base, len(rest))  # 区间跟着本段走，宁可超长
        if cut <= 0 or cut >= len(rest):
            # 单个超限冻结区独占：整段保留（同时防死循环）
            parts.append(rest)
            rest = ""
            break
        parts.append(rest[:cut])
        nxt = rest[cut:].lstrip("\n")
        base += len(rest) - len(nxt)
        rest = nxt
    if rest:
        parts.append(rest)
    return parts
