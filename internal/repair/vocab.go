// 本文件自 repair.go（v1.1.1 引擎校准域）按既有分段接缝切分而来，
// 逻辑与切分前逐行一致（同包纯移动，零语义变化）。

// 词表与归一：连字/重音符/行尾连字符与 BuildVocab。
package repair

import (
	"regexp"
	"strings"
)

// hyphenBreakRe 行尾连字符断行（"differ-\nent" 或悬挂式 "short-\nand"）。
var hyphenBreakRe = regexp.MustCompile(`(?m)([A-Za-z]{2,})-\r?\n([a-z]{2,})`)

// functionWords 常见功能词：行尾连字符后跟功能词，几乎总是悬挂连字符
// （"short-\nand" 即 "short- and"），应保持两词分离；跟其他内容则是断词
// （"configura-\ntion"→configuration、"differ-\nent"→different），应接回。
var functionWords = map[string]struct{}{
	"and": {}, "or": {}, "the": {}, "of": {}, "to": {}, "in": {}, "a": {},
	"an": {}, "with": {}, "for": {}, "on": {}, "at": {}, "by": {}, "from": {},
	"is": {}, "are": {}, "was": {}, "were": {}, "not": {}, "but": {}, "as": {},
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
	type acc struct {
		mark rune
		base string // 该音符可作用的字母（大小写同映射，大小写字母各自列出）
	}
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

// composeAccents 把 MD 中的分离重音符合成预组合字符。"H¨anggi" 是
// LaTeX \'a 的退化输出，拆开的音符在渲染和翻译里都是噪声。英文正文里
// "音符紧跟字母" 几乎不合法，故无需原文锚。
func composeAccents(md string, st *Stats) string {
	res := accentRe.ReplaceAllStringFunc(md, func(s string) string {
		if r, ok := accentMap[s]; ok {
			return string(r)
		}
		return s
	})
	if n := len(accentRe.FindAllString(md, -1)); n > 0 {
		composed := len(accentRe.FindAllString(md, -1)) - countUncomposed(md, res)
		st.Accents = composed
	}
	return res
}

func countUncomposed(orig, res string) int {
	// 仍未被合成的（不在映射表内的组合）数量
	n := 0
	for _, l := range accentRe.FindAllString(res, -1) {
		if _, ok := accentMap[l]; !ok {
			n++
		}
	}
	return n
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
		_, aFunc := functionWords[p[1]]
		_, bFunc := functionWords[p[2]]
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
