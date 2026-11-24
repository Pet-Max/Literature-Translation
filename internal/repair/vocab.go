// 词表与归一：连字/重音符/行尾连字符与 BuildVocab。
package repair

import (
	"fmt"
	"regexp"
	"strings"
)

// hyphenBreakRe 行尾连字符断行（"differ-\nent" 或悬挂式 "short-\nand"）。
var hyphenBreakRe = regexp.MustCompile(`(?m)([A-Za-z]{2,})-\r?\n([a-z]{2,})`)

// functionWords 常见功能词，两处使用：
//   - 行尾连字符判定：连字符后跟功能词几乎总是悬挂连字符（"short-\nand"
//     即 "short- and"），应保持两词分离；跟其他内容则是断词
//     （"configura-\ntion"→configuration、"differ-\nent"→different），应接回。
//   - 上下文锚定功能词闸（context.go）：功能词不做真词替换——PDF 文本层
//     里的两字母符号（BN/Ni/Hf/ii）经词表放行，会把 and/of/for 类功能词
//     改成符号碎片。
var functionWords = map[string]struct{}{
	"and": {}, "or": {}, "the": {}, "of": {}, "to": {}, "in": {}, "a": {},
	"an": {}, "with": {}, "for": {}, "on": {}, "at": {}, "by": {}, "from": {},
	"is": {}, "are": {}, "was": {}, "were": {}, "not": {}, "but": {}, "as": {},
	"be":  {},
	"its": {}, "this": {}, "that": {}, "these": {}, "those": {},
}

// ligatureMap LaTeX CM 字体的连字与组合符：原抽取引擎文本层里 "effects" 常以
// ﬀ (U+FB00) 出现，按 [A-Za-z] 分词会把词切成 "e"+"ects" 毒化词表；
// "≠" 被映射为组合删除符+等号。先归一再建词表/做锚点。
var ligatureMap = strings.NewReplacer(
	"\uFB00", "ff", "\uFB01", "fi", "\uFB02", "fl",
	"\uFB03", "ffi", "\uFB04", "ffl", "\uFB05", "st", "\uFB06", "st",
	"\u0338=", "≠",
)

// accentMap 分离重音符 → 预组合字符（MinerU 把 LaTeX \'e 类重音输出成
// "´e" 形态，音符在字母前）。键为两字符 "音符+字母"，值为目标字符。
// 组合表覆盖欧洲语言常见重音；未收录组合保持原样。
var accentMap = buildAccentMap()

func buildAccentMap() map[string]rune {
	m := map[string]rune{}
	add := func(mark rune, pairs string) {
		// pairs 形如 "aäeë..."：偶数位原字母，奇数位（下一字符）目标
		r := []rune(pairs)
		for i := 0; i+1 < len(r); i += 2 {
			m[string(mark)+string(r[i])] = r[i+1]
		}
	}
	add('¨', "aäeëiïoöuüyÿAÄEËIÏOÖUÜYŸ")
	add('´', "aáeéiíoóuúyýnńcćzźsśAÁEÉIÍOÓUÚYÝNŃCĆZŹSŚıí")
	add('`', "aàeèiìoòuùAÀEÈIÌOÒUÙ")
	add('ˆ', "aâeêiîoôuûAÂEÊIÎÔÛ")
	add('˜', "aãnãoõAÃNÃOÕ")
	add('ˇ', "cčeěrřsštťdďlľnňzžCČEĚRŘSŠTŤDĎLĽŇZŽ")
	add('˘', "aăgğuŭAĂGĞUŬ")
	add('˝', "oőuűOŐUŰ")
	add('¯', "aāeēiīoōuūAĀEĒIĪOŌUŪ")
	return m
}

// accentRe 分离重音符（音符 + 单字母）。
var accentRe = regexp.MustCompile(`[¨´` + "`" + `ˆ˜ˇ˘˝¯][A-Za-zı]`)

// composeMarkdownAccents 在冻结围栏代码块、行内反引号代码、URL 与公式后
// 合成 Markdown 正文中的分离重音符，防止把行内反引号代码（如 `env`）或三反引号代码块
// 等技术文档代码片段误改写成 è/à。
func composeMarkdownAccents(md string, st *Stats) string {
	prot := map[string]string{}
	n := 0
	keep := func(s string) string {
		if strings.ContainsRune(s, 0) {
			return s
		}
		n++
		key := fmt.Sprintf("\x00A%d\x00", n)
		prot[key] = s
		return key
	}
	text := fenceRe.ReplaceAllStringFunc(md, keep)
	text = codeSpanRe.ReplaceAllStringFunc(text, keep)
	text = urlRe.ReplaceAllStringFunc(text, keep)
	text = dispRe.ReplaceAllStringFunc(text, keep)
	text = inlineRe.ReplaceAllStringFunc(text, keep)
	text = composeAccents(text, st)
	return unfreeze(text, prot)
}

// composeAccents 把文本中的分离重音符合成预组合字符。"H¨anggi" 是
// LaTeX \'a 的退化输出，拆开的音符在渲染和翻译里都是噪声。连续或转义的
// ASCII 反引号（如三反引号代码围栏、双反引号或转义反引号）一律跳过，防破坏代码围栏。
func composeAccents(md string, st *Stats) string {
	locs := accentRe.FindAllStringIndex(md, -1)
	if len(locs) == 0 {
		return md
	}
	var b strings.Builder
	last := 0
	composed := 0
	for _, l := range locs {
		s := md[l[0]:l[1]]
		if s[0] == '`' && l[0] > 0 && (md[l[0]-1] == '`' || md[l[0]-1] == '\\') {
			continue
		}
		r, ok := accentMap[s]
		if !ok {
			continue
		}
		b.WriteString(md[last:l[0]])
		b.WriteRune(r)
		last = l[1]
		composed++
	}
	if composed == 0 {
		return md
	}
	b.WriteString(md[last:])
	st.Accents += composed
	return b.String()
}

// normalizeOrig 归一原文文本层（连字/组合符/分离重音符），词表、上下文
// 锚定与符号锚点共用。幂等。
func normalizeOrig(s string) string {
	s = ligatureMap.Replace(s)
	// 原文层与 MD 一样带分离重音符，词表需要合成后的词
	var st Stats
	s = composeAccents(s, &st)
	return s
}

// dehyphenate 行尾连字符三分法（见 BuildVocab 注释）。
// 功能词悬挂连字符（"short-\nand"）只有当后段在别处独立成词时才保持
// 分离；后段是碎片的（"of-\nfers"、"in-\nterference"、"the-\norem"——
// 断行恰好切在 offers/interference/theorem 的功能词前缀后）必须接回，
// 否则碎片进词表/对齐序列，轻则校准失效，重则把正确的词改错。
func dehyphenate(origText string) string {
	// 先按"全部拼接"口径统计独立词：后段是否在别处独立出现
	joined := hyphenBreakRe.ReplaceAllStringFunc(origText, func(m string) string {
		p := hyphenBreakRe.FindStringSubmatch(m)
		return p[1] + p[2]
	})
	standalone := map[string]struct{}{}
	for _, w := range tokenSplit.Split(strings.ToLower(joined), -1) {
		standalone[w] = struct{}{}
	}
	return hyphenBreakRe.ReplaceAllStringFunc(origText, func(m string) string {
		p := hyphenBreakRe.FindStringSubmatch(m)
		_, aFunc := functionWords[strings.ToLower(p[1])]
		_, bFunc := functionWords[strings.ToLower(p[2])]
		if aFunc || bFunc {
			if _, ok := standalone[strings.ToLower(p[2])]; ok && len(p[2]) >= 2 {
				return p[1] + "- " + p[2]
			}
			return p[1] + p[2]
		}
		if strings.Contains(origText, p[1]+"-"+p[2]) {
			return p[1] + "- " + p[2]
		}
		return p[1] + p[2]
	})
}

// BuildVocab 从原文文本构建词表（小写，长度 ≥2）。
//
// 行尾连字符三分法：
//   - A 或 B 是功能词 → 悬挂连字符（"short-\nand"、"to-\none"），保持两词；
//   - 原文存在固体连字符形式 "A-B"（如 "time-domain"）→ 保持两词（可拆分修复）；
//   - 其余 → 断词拼接进词表（"multi-\ntude"→multitude，供合并修复）。
func BuildVocab(origText string) map[string]struct{} {
	processed := dehyphenate(normalizeOrig(origText))
	vocab := make(map[string]struct{})
	for _, w := range tokenSplit.Split(strings.ToLower(processed), -1) {
		if len(w) >= 2 {
			vocab[w] = struct{}{}
		}
	}
	return vocab
}
