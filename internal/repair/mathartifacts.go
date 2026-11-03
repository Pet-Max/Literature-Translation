// mathartifacts.go — MinerU 数学区系统性伪影的通用归一（多份实证语料，
// 按形态设计、不绑定具体文档）：
//
//	R1 `\ :` → `\:`　　间距宏被裂成「反斜杠+空格+冒号」，KaTeX 渲染出真冒号
//	                   （实证语料全覆盖；文本层证实原文无冒号，源是间距宏 \:）
// 引号恢复由 symbols.go 执行，须有原文引号证据；数字上标本身不作为判据。
//
//	R3 $\operatorname{Eq.}$ . (3) → Eq. (3)　公式引用编号被甩出 math，下游
//	                   翻译产生「由 Eq. 计算得到。(3) 考虑了」的句界撕裂
//	                   （多份语料命中；点在 math 内/外两种变体都收）
//	R4 $^{4 \mathrm{V} ,}$ → 4 V,　正文片段被整体上标。判据：行内公式以 ^
//	                   开头且内容以逗号/分号结尾——真实上标从不如此
//	R5 $…$ <sub>x</sub> → $…$　公式已含同一下标时的冗余残片（重复性校验
//	                   防误删：仅当前式确有该下标才删）
//
// 本层随 Symbols 开关执行；未启用时保留数学内容。
package repair

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var (
	// R1 间距宏裂开
	mathColonRe = regexp.MustCompile(`\\ :`)
	// R3 引用撕裂：$\operatorname{Eq.}$ . (3) 与 $\operatorname{Eq} .$ . (2)。
	// 带编号与裸引用两条分写：裸引用不能吞掉公式后的空格（per Eq. below）
	eqRefNumRe  = regexp.MustCompile(`\$ ?\\operatorname ?\{ ?E ?q ?\.? ?\} ?\.? ?\$ ?\.? ?\((\d+)\)`)
	eqRefBareRe = regexp.MustCompile(`\$ ?\\operatorname ?\{ ?E ?q ?\.? ?\} ?\.? ?\$`)
	// R4 伪上标：$^{…,}$ / $^{…;}$（内容以句读收尾，句读单独捕获以便还原）
	bogusSupRe = regexp.MustCompile(`\$\^ ?\{ ?([^$]*?[^ ,;\$]) ?([,;]) ?\}\$`)
	// R5 公式后紧跟的 sub/sup 残片（RE2 无反向引用，分写两条；公式组含闭合 $）
	dupSubFragRe = regexp.MustCompile(`(\$[^$\n]+?\$) ?<sub>([^<]{1,30}?)</sub>`)
	dupSupFragRe = regexp.MustCompile(`(\$[^$\n]+?\$) ?<sup>([^<]{1,30}?)</sup>`)
	// R4 还原内容时剥 \mathrm 包装（惰性捕获避开花括号内空格；嵌套形态原样保留）
	mathrmWrapRe = regexp.MustCompile(`\\mathrm ?\{ ?([^{}]*?) ?\}`)
)

// fixMathArtifacts 数学区伪影归一入口。围栏与 URL 先冻结（与 freeze 同序），
// 防 disp/inline 正则跨围栏误匹配。
func fixMathArtifacts(text string, st *Stats) string {
	prot := map[string]string{}
	n := 0
	keep := func(s string) string {
		if strings.ContainsRune(s, 0) {
			return s
		}
		n++
		key := fmt.Sprintf("\x00M%d\x00", n) // M 前缀防与 freeze 的 \x00N\x00 键碰撞
		prot[key] = s
		return key
	}
	text = fenceRe.ReplaceAllStringFunc(text, keep)
	text = urlRe.ReplaceAllStringFunc(text, keep)

	// 跨公式边界的两条先做
	text = reattachEqRef(text, st)
	text = dropDupSubFragment(text, st)

	// 公式区内三条：display 修完冻结（防 inline 正则二次进入其内部），
	// 再处理 inline
	text = dispRe.ReplaceAllStringFunc(text, func(m string) string {
		return keep(fixDisplayMath(m, st))
	})
	text = inlineRe.ReplaceAllStringFunc(text, func(m string) string {
		return fixInlineMath(m, st)
	})

	keys := make([]string, 0, len(prot))
	for k := range prot {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return numOfKey(keys[i]) > numOfKey(keys[j]) })
	for _, k := range keys {
		text = strings.ReplaceAll(text, k, prot[k])
	}
	return text
}

// fixDisplayMath 行间公式：只做内容级归一（R1），R4 按规格仅限行内。
func fixDisplayMath(m string, st *Stats) string {
	out := m
	if fixed := mathColonRe.ReplaceAllLiteralString(out, `\:`); fixed != out {
		st.MathFix++
		st.detail("间距宏归一：\\ : → \\:")
		out = fixed
	}
	return out
}

// fixInlineMath 行内公式：R1 + R4 伪上标还原。
func fixInlineMath(m string, st *Stats) string {
	out := fixDisplayMath(m, st)
	if g := bogusSupRe.FindStringSubmatch(out); g != nil {
		content := unwrapMathrm(g[1]) + g[2] // 句读还原到内容尾部
		st.MathFix++
		st.detail("伪上标还原：" + g[0] + " → " + content)
		out = strings.Replace(out, g[0], content, 1)
	}
	return out
}

// reattachEqRef R3：把甩出的公式引用编号接回，整体还原为普通文本。
// 带编号优先，裸引用其次（裸形态不带尾部可选段，避免吞掉后面的空格）。
func reattachEqRef(text string, st *Stats) string {
	text = eqRefNumRe.ReplaceAllStringFunc(text, func(m string) string {
		ref := "Eq. (" + eqRefNumRe.FindStringSubmatch(m)[1] + ")"
		st.MathFix++
		st.detail("公式引用还原：" + m + " → " + ref)
		return ref
	})
	return eqRefBareRe.ReplaceAllStringFunc(text, func(m string) string {
		st.MathFix++
		st.detail("公式引用还原：" + m + " → Eq.")
		return "Eq."
	})
}

// dropDupSubFragment R5：公式后紧跟的 sub/sup 残片，仅当前式已含同一
// 脚标（下标查 _、上标查 ^）才判定冗余删除。
func dropDupSubFragment(text string, st *Stats) string {
	for _, d := range []struct {
		re   *regexp.Regexp
		tag  string
		mark byte
	}{
		{dupSubFragRe, "sub", '_'},
		{dupSupFragRe, "sup", '^'},
	} {
		text = d.re.ReplaceAllStringFunc(text, func(m string) string {
			g := d.re.FindStringSubmatch(m)
			formula, frag := g[1], g[2]
			if !formulaHasScript(formula, frag, d.mark) {
				return m
			}
			st.MathFix++
			st.detail("冗余脚标残片移除：<" + d.tag + ">" + frag + "</" + d.tag + ">（前式已含）")
			return formula
		})
	}
	return text
}

// formulaHasScript 判断公式（去空格归一后）是否已含脚标 c（mark 为 '_' 或 '^'）：
// 支持 _{c}、_{\mathrm{c}} 与 _c（后者要求其后随非字母，防 _rate 吃掉 r）。
func formulaHasScript(formula, c string, mark byte) bool {
	norm := strings.Map(func(r rune) rune {
		if r == ' ' {
			return -1
		}
		return r
	}, formula)
	if strings.Contains(norm, string(mark)+"{"+c+"}") ||
		strings.Contains(norm, string(mark)+"{\\mathrm{"+c+"}}") {
		return true
	}
	for pos := 0; pos+1+len(c) <= len(norm); pos++ {
		if norm[pos] == mark && strings.HasPrefix(norm[pos+1:], c) {
			end := pos + 1 + len(c)
			if end >= len(norm) || !isASCIILetter(norm[end]) {
				return true
			}
		}
	}
	return false
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// unwrapMathrm 剥内容里的 \mathrm{X} 包装（无嵌套花括号的简单形态）。
func unwrapMathrm(s string) string {
	for i := 0; i < 4; i++ { // 有限次，防意外深嵌套死循环
		next := mathrmWrapRe.ReplaceAllString(s, "$1")
		if next == s {
			break
		}
		s = next
	}
	return s
}
