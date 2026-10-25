"""prompt.py — 翻译指令拼装：规则正文（预设可覆盖）+ 术语表 + 旧版补充规则。

规则正文默认用内置铁律；前端规则预设传 system_rules 时整体替换，
用户在设置里对规则全文负责（机械校验仍兜底占位符完整性）。
"""

from __future__ import annotations

IRON_RULES = """你是一名物理学/学术文献翻译引擎。把用户消息中的 Markdown 学术文本翻译成简体中文，严格遵守：

1. 只翻译自然语言文字；⟦F1⟧、⟦F2⟧ 这类 ⟦F数字⟧ 占位符（代表公式、代码、图片、表格）必须原样保留：一个不能少、一个不能改、位置不能移动，也不要试图翻译或解读占位符内容。
2. Eq. (5)、Fig. 3、Table 1、Ref. [12]、arXiv:2306.00417、附录编号 S IV 等引用编号保留原样，不要翻译 Eq./Fig. 等标记。
3. Markdown 结构保持不变：标题层级（# 数量）、列表、引用块、加粗斜体、链接与原文一一对应；标题译文务必简短。
4. 专有名词、人名、变量名（如 PSD、Langevin、Sz(ω) 中的记号）按学界惯例处理：有通用中译的用中译，没有的保留原文。
5. 术语严格按下方术语表统一译名。
6. 只输出译文本身。不要任何解释、前言、结语、代码围栏包裹。"""

USER_RULES_HEADER = "\n\n用户补充规则（与上述铁律同等效力，须一并遵守）："
GLOSSARY_HEADER = "\n\n术语表（原文 → 统一译名）："


def build_system(glossary: list[list[str]] | None,
                 system_rules: str | None = None,
                 user_rules: str | None = None) -> str:
    """拼装 system 提示词。glossary 为 [en, zh] 二元组列表。

    system_rules 为规则预设全文（非空时替换内置铁律）；user_rules
    为旧版追加式补充规则，保留以兼容旧调用方。
    """
    base = (system_rules or "").strip()
    parts = [base if base else IRON_RULES]
    gl = [(str(en).strip(), str(zh).strip())
          for en, zh in (glossary or []) if str(en).strip() and str(zh).strip()]
    if gl:
        lines = [f"- {en} → {zh}" for en, zh in gl]
        parts.append(GLOSSARY_HEADER + "\n" + "\n".join(lines))
    rules = (user_rules or "").strip()
    if rules:
        parts.append(USER_RULES_HEADER + "\n" + rules)
    return "\n".join(parts)


def build_user(frozen_chunk: str) -> str:
    """用户消息就是冻结后的待译文本本身，避免多余包装。"""
    return frozen_chunk


def build_correction(reasons: list[str]) -> str:
    """质量重试时追加的纠错消息（拼接在下一次请求的 user 消息尾部）。"""
    joined = "；".join(reasons)
    return (f"\n\n——你上一次的输出违反了规则，问题：{joined}。"
            f"请重新输出**完整的修正版全文译文**，严格遵守全部规则，"
            f"尤其保证 ⟦F数字⟧ 占位符与原文完全一致。")
