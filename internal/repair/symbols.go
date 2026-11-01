// 本文件自 repair.go（v1.1.1 引擎校准域）按既有分段接缝切分而来，
// 逻辑与切分前逐行一致（同包纯移动，零语义变化）。

// 符号修复：ket/bra 还原、引号公式、\right.\left.、公式内补箭头。
package repair

import (
	"regexp"
	"strings"
)

// ---------------------------------------------------------------- 符号修复

// 符号损伤的白名单规则。Go 正则不支持 lookbehind/lookahead，ket/bra 的
// 边界用下标手工校验；除 \right.\left.（纯结构替换）外，其余替换都必须
// 能在原文文本层找到锚点。
var (
	ketRe       = regexp.MustCompile(`\|([0-9A-Za-z]{1,4})i`)
	braRe       = regexp.MustCompile(`h([0-9A-Za-z]{1,4})\|`)
	quoteRe     = regexp.MustCompile(`\^\s*\{\s*6\s*6\s*\}([\s\S]*?)\^\s*\{\s*(?:,\s*9|9\s*,?\s*9)\s*\}`)
	mathrmRe    = regexp.MustCompile(`\\mathrm\s*\{([^}]*)\}`)
	rightLeftRe = regexp.MustCompile(`\\right\.\s*\\left\.`)
	gapRe       = regexp.MustCompile(`([0-9A-Za-z}\)\]])[ \t]{2,}([0-9A-Za-z\(\[\{])`)
)

// normalizeSymbols 修复符号级损伤。行文部分做 ket/bra 还原，行内公式
// 单独走定界符/引号/箭头规则；围栏代码、行间公式与标签已冻结。
func normalizeSymbols(md, orig string, st *Stats) string {
	frozen, protect := freeze(md, false)
	locs := inlineRe.FindAllStringSubmatchIndex(frozen, -1)
	var b strings.Builder
	last := 0
	for k, l := range locs {
		b.WriteString(fixKetBra(frozen[last:l[0]], orig, st))
		// 箭头锚定用的散文上下文：取相邻行内公式之间的纯文本，
		// 避免把相邻公式里的字母片段（如 \Sigma 附近的符号）当上下文
		afterEnd := len(frozen)
		if k+1 < len(locs) {
			afterEnd = locs[k+1][0]
		}
		b.WriteString(fixInlineSpan(frozen[l[0]:l[1]],
			contextWords(frozen[last:l[0]], false, 2),
			contextWords(frozen[l[1]:afterEnd], true, 2), orig, st))
		last = l[1]
	}
	b.WriteString(fixKetBra(frozen[last:], orig, st))
	return unfreeze(b.String(), protect)
}

func isAlnumByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// fixKetBra 还原 Dirac 记号：|00i→|00⟩（ket）、h01|→⟨01|（bra）。
// 仅当原文文本层存在 |label⟩ / ⟨label| 时才替换（锚定），否则记录跳过。
func fixKetBra(prose, orig string, st *Stats) string {
	prose = ketFix(prose, orig, st)
	return braFix(prose, orig, st)
}

func ketFix(s, orig string, st *Stats) string {
	locs := ketRe.FindAllStringSubmatchIndex(s, -1)
	if len(locs) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, l := range locs {
		label := s[l[2]:l[3]]
		// '|' 前不能是字母/数字/'|'/'⟨'；末尾 'i' 后不能是字母/数字
		if l[0] > 0 {
			c := s[l[0]-1]
			if isAlnumByte(c) || c == '|' || strings.HasSuffix(s[:l[0]], "⟨") {
				continue
			}
		}
		if l[1] < len(s) && isAlnumByte(s[l[1]]) {
			continue
		}
		anchor := "|" + label + "⟩"
		if !strings.Contains(orig, anchor) {
			st.detail("跳过: |" + label + "i（原文无 |" + label + "⟩ 锚）")
			continue
		}
		b.WriteString(s[last:l[0]])
		b.WriteString("|" + label + "⟩")
		last = l[1]
		st.Syms++
		st.detail("|" + label + "i → " + anchor)
	}
	b.WriteString(s[last:])
	return b.String()
}

func braFix(s, orig string, st *Stats) string {
	locs := braRe.FindAllStringSubmatchIndex(s, -1)
	if len(locs) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, l := range locs {
		label := s[l[2]:l[3]]
		// 'h' 前不能是字母/数字（排除 which|、sh| 等英文词尾）
		if l[0] > 0 && isAlnumByte(s[l[0]-1]) {
			continue
		}
		anchor := "⟨" + label + "|"
		if !strings.Contains(orig, anchor) {
			st.detail("跳过: h" + label + "|（原文无 " + anchor + " 锚）")
			continue
		}
		b.WriteString(s[last:l[0]])
		b.WriteString(anchor)
		last = l[1]
		st.Syms++
		st.detail("h" + label + "| → " + anchor)
	}
	b.WriteString(s[last:])
	return b.String()
}

// fixInlineSpan 处理单个行内公式（span 含 $ 定界符）。before/after 是
// 公式两侧的散文上下文词（各至多 2 个），供箭头修复锁位置。
func fixInlineSpan(span string, before, after []string, orig string, st *Stats) string {
	content := span[1 : len(span)-1]
	// 1) LaTeX 引号被误认为公式：^{66}…^{,9} → “…”。候选文本（去空格/
	//    收缩句点等变体）必须在原文中找得到 “…” 才替换。
	if m := quoteRe.FindStringSubmatchIndex(content); m != nil {
		mid := mathrmRe.ReplaceAllString(content[m[2]:m[3]], "$1")
		tight := strings.ReplaceAll(strings.ReplaceAll(mid, " .", "."), ". ", ".")
		cands := []string{
			strings.ReplaceAll(mid, " ", ""),
			strings.ReplaceAll(tight, "  ", " "),
			strings.TrimSpace(mid),
		}
		for _, c := range cands {
			c = strings.TrimSpace(c)
			if c != "" && strings.Contains(orig, "“"+c+"”") {
				st.Syms++
				st.detail("引号公式 → “" + c + "”")
				return "“" + c + "”"
			}
		}
		st.detail("跳过: 引号公式（原文无 “…” 锚）")
	}
	// 2) ⟩⟨ 被写成空定界符：\right.\left. → \rangle\langle（纯结构替换）
	fixed := rightLeftRe.ReplaceAllStringFunc(content, func(string) string {
		st.Syms++
		st.detail(`\right.\left. → \rangle\langle`)
		return `\rangle\langle`
	})
	// 3) 行内公式丢 →：双空格两侧 token 在原文中找 L→R 锚，且散文上下文
	//    在命中位置附近按序出现，才回填
	fixed = fixArrows(fixed, orig, before, after, st)
	return "$" + fixed + "$"
}

// fixArrows 行内公式内的连续 2+ 空格多半是丢掉的箭头（MinerU 对 cmsy
// 箭头常整字符丢弃）。短 token 的 "L→R" 模式（如 1→1）在原文里太常见，
// 单靠符号锚会误补，故要求公式两侧的散文上下文词在命中位置附近按序
// 出现；上下文找不到锚时保持原样。
func fixArrows(s, orig string, before, after []string, st *Stats) string {
	locs := gapRe.FindAllStringSubmatchIndex(s, -1)
	if len(locs) == 0 {
		return s
	}
	type repl struct {
		start, end int
		text       string
	}
	var repls []repl
	for _, l := range locs {
		left, right := s[l[2]:l[3]], s[l[4]:l[5]]
		arrowRe, err := regexp.Compile(regexp.QuoteMeta(left) + `\s*→\s*` + regexp.QuoteMeta(right))
		if err != nil {
			continue
		}
		matches := arrowRe.FindAllStringIndex(orig, -1)
		anchored := len(matches) > 0 && len(before) == 0 && len(after) == 0
		for _, m := range matches {
			if arrowContextOK(orig, m[0], m[1], before, after) {
				anchored = true
				break
			}
		}
		if !anchored {
			st.detail("跳过: 公式内空格 " + left + " " + right + "（原文无带上下文锚的 " + left + "→" + right + "）")
			continue
		}
		repls = append(repls, repl{l[3], l[4], ` \to `})
		st.Syms++
		st.detail("公式补箭头: " + left + " → " + right)
	}
	for k := len(repls) - 1; k >= 0; k-- {
		r := repls[k]
		s = s[:r.start] + r.text + s[r.end:]
	}
	return s
}

// contextWords 取 prose 段首/尾至多 n 个词（小写、按阅读顺序）。
func contextWords(prose string, head bool, n int) []string {
	locs := alignWordRe.FindAllStringIndex(prose, -1)
	if len(locs) == 0 {
		return nil
	}
	if len(locs) > n {
		if head {
			locs = locs[:n]
		} else {
			locs = locs[len(locs)-n:]
		}
	}
	words := make([]string, len(locs))
	for k, l := range locs {
		words[k] = strings.ToLower(prose[l[0]:l[1]])
	}
	return words
}

// arrowContextOK 判断原文中某个 "L→R" 命中点是否被公式两侧的散文上下文
// 支持：before 词按序出现在命中点之前的窗口内，after 词按序出现在命中点
// 之后的窗口内（各 ±300 字符）。
func arrowContextOK(orig string, ms, me int, before, after []string) bool {
	if len(before) == 0 && len(after) == 0 {
		return true
	}
	lo, hi := ms-300, me+300
	if lo < 0 {
		lo = 0
	}
	if hi > len(orig) {
		hi = len(orig)
	}
	window := strings.ToLower(orig[lo:hi])
	pos := 0
	for _, w := range before {
		p := tokenPosFrom(window, w, pos, ms-lo)
		if p < 0 {
			return false
		}
		pos = p + len(w)
	}
	pos = me - lo
	for _, w := range after {
		p := tokenPosFrom(window, w, pos, len(window))
		if p < 0 {
			return false
		}
		pos = p + len(w)
	}
	return true
}

// tokenPosFrom 在 s[from:until] 范围内找整词 w 的首个出现位置。
func tokenPosFrom(s, w string, from, until int) int {
	re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(w) + `\b`)
	for _, l := range re.FindAllStringIndex(s, -1) {
		if l[0] >= from && l[1] <= until {
			return l[0]
		}
	}
	return -1
}
