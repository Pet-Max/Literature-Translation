// prompt.go — 翻译指令拼装：规则正文（预设可覆盖）+ 术语表 + 旧版补充规则。
//
// 自 v1.1.1 引擎母本移植（54 行）。规则正文默认用内置铁律；前端规则
// 预设传 system_rules 时整体替换，用户在设置里对规则全文负责（机械校验
// 仍兜底占位符完整性）。
package engine

import "strings"

// IronRules 内置铁律（与 Python 母本逐字节一致）。
const IronRules = `你是一名物理学/学术文献翻译引擎。把用户消息中的 Markdown 学术文本翻译成简体中文，严格遵守：

1. 只翻译自然语言文字；⟦F1⟧、⟦F2⟧ 这类 ⟦F数字⟧ 占位符（代表公式、代码、图片）必须原样保留：一个不能少、一个不能改、位置不能移动，也不要试图翻译或解读占位符内容。
2. Eq. (5)、Fig. 3、Table 1、Ref. [12]、arXiv:2306.00417、附录编号 S IV 等引用编号保留原样，不要翻译 Eq./Fig. 等标记。
3. Markdown 结构保持不变：标题层级（# 数量）、列表、引用块、加粗斜体、链接、表格与原文一一对应；标题译文务必简短。表格逐行保持竖线 | 与列数不变，只翻译单元格内的文字，单元格内的占位符原样保留。
4. 物理记号原样保留：狄拉克记号（|00⟩、⟨01|）、希腊字母（Λ、Ω、ψ）、上下标变量，以及无法识别的乱码字符——一律不翻译、不删除、不猜测含义、不改写。
5. 专有名词、人名按学界惯例处理：有通用中译的用中译（如 Langevin→朗之万），没有的保留原文。
6. 术语严格按下方术语表统一译名。
7. 只输出译文本身。不要任何解释、前言、结语、代码围栏包裹。`

// userRulesHeader / glossaryHeader 与 Python 母本逐字节一致。
const (
	userRulesHeader = "\n\n用户补充规则（与上述铁律同等效力，须一并遵守）："
	glossaryHeader  = "\n\n术语表（原文 → 统一译名）："
)

// BuildSystem 拼装 system 提示词。glossary 为 [en, zh] 二元组列表。
//
// systemRules 为规则预设全文（非空时替换内置铁律）；userRules 为旧版
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
			continue // Python 侧此处 unpack 长度必为 2；防御性跳过
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

// BuildCorrection 质量重试时追加的纠错消息（拼接在下一次请求的 user
// 消息尾部）。
func BuildCorrection(reasons []string) string {
	joined := strings.Join(reasons, "；")
	return "\n\n——你上一次的输出违反了规则，问题：" + joined +
		"。请重新输出**完整的修正版全文译文**，严格遵守全部规则，" +
		"尤其保证 ⟦F数字⟧ 占位符与原文完全一致。"
}
