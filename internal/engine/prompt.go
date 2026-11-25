// prompt.go — 翻译指令拼装：规则正文（预设可覆盖）+ 术语表 + 旧版补充规则。
//
// 规则正文默认用内置规则；前端规则
// 预设传 system_rules 时整体替换，用户在设置里对规则全文负责（机械校验
// 仍兜底占位符完整性）。
package engine

import "strings"

// IronRules 内置规则（与 config.DefaultRules 逐字节一致，变更须同步）。
const IronRules = "你是一名物理学/学术文献翻译引擎。将用户消息中的 Markdown 学术文本完整、准确地翻译为流畅严谨的简体中文，严格遵守：\n\n" +
	"1. 占位符与公式：⟦F1⟧、⟦F2⟧ 这类 ⟦F数字⟧ 占位符（代表公式、代码块、图片）必须原样保留，不得增删、改写或解读；独立成段的占位符保持原段落位置，行内占位符可随中文语序自然调整但须保持准确对应。不要擅自增删 $ / $$ 公式定界符。\n" +
	"2. 忠实与单位：逐句完整翻译，不遗漏、不概括、不增补；按中文学术习惯合理调整长难句语序。首次出现的「全称（缩写）」保留英文缩写；数值后的国际单位与统计记号（如 nm、m/s、mg/kg、p < 0.05）保持原符号，不译成汉字单位。\n" +
	"3. 引用与结构标签：Eq./Equation、Fig./FIG./Figure、Table/TABLE、Ref.、arXiv/DOI 及附录编号（如 Fig. S1、S IV）等交叉引用标记连同编号保留原样，不翻译标记词，也不在前面重复加「如图/见表」（写作「见 Fig. 1」而非「如图 Fig. 1」）；章节与习题标签统一中译（Chapter→第 N 章、Section/Sec.→第 N 节、Exercise→习题）。\n" +
	"4. Markdown 与表格：保持标题层级（# 数量）、列表、引用块、链接、表格与原文一一对应；标题译文简洁忠实。链接仅译显示文字，URL 保持原样且 ] 与 ( 间不加空格；加粗/斜体紧贴中文引号或括号时将全角标点置于标记外侧。表格逐行保持竖线 |、列数、分隔行及单元格内 \\| 不变，单元格内不换行，不新增 <br> 等 HTML 标签；<sup>/<sub> 仅用于短角标或化学式，切勿将普通中文词句放入 <sup>/<sub> 内。\n" +
	"5. 物理记号与代码：狄拉克记号（|00⟩、⟨01|）、希腊字母（Λ、Ω、ψ）、上下标变量、行内代码（反引号 `...`），以及无法识别的乱码字符均原样保留，不翻译、不删除、不猜测含义、不改写。\n" +
	"6. 人名与专名：历史著名学者与定名概念统一采用学界通用中译（如 Schrödinger→薛定谔、von Neumann→冯·诺依曼、Bayes→贝叶斯）；正文引用的论文作者与作者栏保留原文姓氏（如 Smith et al.→Smith 等），英文姓名间保留半角逗号；无通用中译的专名保留原文。若遇参考文献条目，保留原文不译。\n" +
	"7. 术语统一：若下方提供术语表，严格按表统一译名（含单复数与大小写变体）；未列出的专业术语采用该学科规范中文译名并保持全篇前后一致，不随意混用不同译法或中途夹杂英文原词。\n" +
	"8. 只输出译文本身。不要任何解释、前言、结语、思考过程或外层代码围栏包裹。"

// userRulesHeader / glossaryHeader 的字节内容属于提示词兼容约定。
const (
	userRulesHeader = "\n\n用户补充规则（与上述铁律同等效力，须一并遵守）："
	glossaryHeader  = "\n\n术语表（原文 → 统一译名）："
)

// BuildSystem 拼装 system 提示词。glossary 为 [en, zh] 二元组列表。
//
// systemRules 为规则预设全文（非空时替换内置默认规则）；userRules 为旧版
// 追加式补充规则，保留以兼容旧调用方。
func BuildSystem(glossary [][]string, systemRules, userRules string) string {
	base := strings.TrimSpace(systemRules)
	var parts []string
	if base != "" {
		parts = append(parts, base)
	} else {
		parts = append(parts, IronRules)
	}
	var gl [][]string
	for _, pair := range glossary {
		if len(pair) != 2 {
			continue // 术语表条目长度恒为 2；防御性跳过
		}
		en, zh := strings.TrimSpace(pair[0]), strings.TrimSpace(pair[1])
		if en != "" && zh != "" {
			gl = append(gl, []string{en, zh})
		}
	}
	if len(gl) > 0 {
		lines := make([]string, len(gl))
		for i, pair := range gl {
			lines[i] = "- " + pair[0] + " → " + pair[1]
		}
		parts = append(parts, glossaryHeader+"\n"+strings.Join(lines, "\n"))
	}
	rules := strings.TrimSpace(userRules)
	if rules != "" {
		parts = append(parts, userRulesHeader+"\n"+rules)
	}
	return strings.Join(parts, "\n")
}

// BuildUser 用户消息就是冻结后的待译文本本身，避免多余包装。
func BuildUser(frozenChunk string) string {
	return frozenChunk
}

// BuildCorrection 质量重试时追加的纠错消息（作为独立 user 消息附在上一轮
// assistant 译文之后，供模型对照修订）。
func BuildCorrection(reasons []string) string {
	joined := strings.Join(reasons, "；")
	tail := "请重新输出**完整的修正版全文译文**，严格遵守全部规则。"
	// 仅在问题涉及占位符时附加占位符专项提醒，其余问题保持直指焦点
	if strings.Contains(joined, "占位符") {
		tail = "请重新输出**完整的修正版全文译文**，严格遵守全部规则，尤其保证 ⟦F数字⟧ 占位符与原文完全一致。"
	}
	return "你上一次的输出违反了规则，问题：" + joined + "。" + tail
}
