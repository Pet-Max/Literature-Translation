// 本文件自 repair.go（v1.1.1 引擎校准域）按既有分段接缝切分而来，
// 逻辑与切分前逐行一致（同包纯移动，零语义变化）。

// 伪行间公式升级：独立成行的 $…$ → $$…$$ 块。
package repair

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// ---------------------------------------------------------------- 行间公式升级

// loneInlineRe 整行恰为一个行内公式（行内不含第二个 $）。
var loneInlineRe = regexp.MustCompile(`^\$([^$]+)\$$`)

// promoteDisplayMath 把"独立成行的 $…$ 伪行间公式"升级为 $$…$$ 块。
// MinerU 偶尔把多行 display 拆成若干单 $ 行：连续 ≥2 行的合并为一个
// $$ 块（内容逐行保留）；孤行且内容 ≥15 字符的也提升。行内短公式、
// 一行多个公式、代码围栏与既有 $$ 块内部一律不动。本库 75 篇实测命中
// 0 处（公式定界符规范），此规则是给其他文献源的前瞻兜底。
func promoteDisplayMath(md string, st *Stats) string {
	lines := strings.Split(md, "\n")
	out := make([]string, 0, len(lines)+8)
	var run []string // 连续伪行间公式的原始行缓冲
	fence, disp := false, false
	flush := func() {
		if len(run) == 0 {
			return
		}
		contents := make([]string, len(run))
		for i, l := range run {
			contents[i] = loneInlineRe.FindStringSubmatch(strings.TrimSpace(l))[1]
		}
		if len(run) >= 2 || (len(run) == 1 && utf8.RuneCountInString(contents[0]) >= 15) {
			out = append(out, "$$")
			out = append(out, contents...)
			out = append(out, "$$")
			st.Display++
		} else {
			out = append(out, run...) // 未达提升条件：原始行原样（含 $ 定界符）
		}
		run = run[:0]
	}
	for _, line := range lines {
		s := strings.TrimSpace(line)
		if strings.HasPrefix(s, "```") || strings.HasPrefix(s, "~~~") {
			flush()
			fence = !fence
			disp = false
			out = append(out, line)
			continue
		}
		if fence {
			out = append(out, line)
			continue
		}
		if !disp && strings.Count(s, "$$") == 1 {
			flush()
			disp = true
			out = append(out, line)
			continue
		}
		if disp {
			if strings.Count(s, "$$") == 1 {
				disp = false
			}
			flush()
			out = append(out, line)
			continue
		}
		if m := loneInlineRe.FindStringSubmatch(s); m != nil {
			run = append(run, line) // 原始行入缓冲，内容在 flush 时再解析
			continue
		}
		flush()
		out = append(out, line)
	}
	flush()
	return strings.Join(out, "\n")
}
