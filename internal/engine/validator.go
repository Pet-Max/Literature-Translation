// validator.go — 译文机械校验（纯代码检查，不依赖模型自觉）。
//
// 校验分为两层：
//   - Validate(src, dst)：冻结态校验，翻译质量重试的依据；
//   - ValidateFinal(md)：回填后的成品校验，$ 配对与 \begin/\end 配对。
//
// 兼容要求：问题文案须与既有黄金样例逐字节一致；列表
// 格式化为 "[1, 2, 3]" 形态；visible 长度按 rune 计。
package engine

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	regexp2 "github.com/dlclark/regexp2"
)

// dollarRe 行内/行间 $ 定界符：$$ 优先（消费两字符），不吃 \$ 转义。
// 含 (?<!\\) 反向断言，走 regexp2。
var dollarRe = regexp2.MustCompile(`\$\$|(?<!\\)\$`, regexp2.None)

// beginEndRe \begin{x} / \end{x} 配对检查。
var beginEndRe = regexp.MustCompile(`\\(begin|end)\{([^}]+)\}`)

// phCleanRe 计算可见长度时剔除占位符。
var phCleanRe = regexp.MustCompile(`⟦F\d+⟧`)

// splitLines 按常见行分隔符分行（\r\n、\r、\n）。\r\n 算一次分行；
// 行尾分隔符不产生末尾空行；中间空行保留。MD 文本中不会出现
// \v/\f/\x85/\u2028 等罕见分隔符。
func splitLines(text string) []string {
	var out []string
	start, i := 0, 0
	for i < len(text) {
		switch text[i] {
		case '\n':
			out = append(out, text[start:i])
			i++
			start = i
		case '\r':
			if i+1 < len(text) && text[i+1] == '\n' {
				i++
			}
			out = append(out, text[start:i])
			i++
			start = i
		default:
			i++
		}
	}
	if start < len(text) {
		out = append(out, text[start:])
	}
	return out
}

// formatIntList 格式化整数切片："[]" / "[1, 2, 3]"。
func formatIntList(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprintf("%d", x)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// tablePipes 按出现顺序收集管道表格行的竖线数。
//
// 只计同时以 | 开头且以 | 结尾的行：物理文献正文/公式常以狄拉克记号
// 打头（|00⟩、|p⟩ = …），只看行首会把它们当成表格行，模型重排换行后
// 就误报"表格结构被改动"——真表格行必然首尾都有竖线。
func tablePipes(text string) []int {
	var out []int
	for _, line := range splitLines(text) {
		s := strings.TrimSpace(line)
		if strings.HasPrefix(s, "|") && strings.HasSuffix(s, "|") {
			out = append(out, strings.Count(s, "|"))
		}
	}
	return out
}

// intCounter 多重集计数。
func intCounter(xs []int) map[int]int {
	m := make(map[int]int)
	for _, x := range xs {
		m[x]++
	}
	return m
}

// subCounter 多重集减法 a-b（非正剔除）。
func subCounter(a, b map[int]int) map[int]int {
	out := make(map[int]int)
	for k, v := range a {
		d := v - b[k]
		if d > 0 {
			out[k] = d
		}
	}
	return out
}

// counterEqual 多重集相等。
func counterEqual(a, b map[int]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// expandMultiset 计数表 → 排序后的元素列表。
func expandMultiset(m map[int]int) []int {
	var out []int
	for k, v := range m {
		for i := 0; i < v; i++ {
			out = append(out, k)
		}
	}
	sort.Ints(out)
	return out
}

// Validate 冻结态对比原文/译文，返回问题列表（空 = 通过）。
func Validate(src, dst string) []string {
	var reasons []string

	srcPh := intCounter(Placeholders(src))
	dstPh := intCounter(Placeholders(dst))
	if !counterEqual(srcPh, dstPh) {
		if missing := expandMultiset(subCounter(srcPh, dstPh)); len(missing) > 0 {
			parts := make([]string, len(missing))
			for i, x := range missing {
				parts[i] = fmt.Sprintf("%d", x)
			}
			reasons = append(reasons, "占位符缺失 ⟦F"+strings.Join(parts, ",")+"⟧")
		}
		if extra := expandMultiset(subCounter(dstPh, srcPh)); len(extra) > 0 {
			parts := make([]string, len(extra))
			for i, x := range extra {
				parts[i] = fmt.Sprintf("%d", x)
			}
			reasons = append(reasons, "占位符多出 ⟦F"+strings.Join(parts, ",")+"⟧")
		}
	}

	srcHeads := headingRe.FindAllString(src, -1)
	dstHeads := headingRe.FindAllString(dst, -1)
	if len(srcHeads) != len(dstHeads) {
		reasons = append(reasons, fmt.Sprintf("标题数不一致（原文 %d，译文 %d）", len(srcHeads), len(dstHeads)))
	}

	// 表格结构：管道表格的逐行竖线数必须与原文一致（规则 3：
	// 只译单元格文字）。模型多写/少写一个 | 整张表渲染就废，且
	// 占位符校验查不到——表格不是占位符，是可译文本。
	srcPipes := tablePipes(src)
	dstPipes := tablePipes(dst)
	if !intSlicesEqual(srcPipes, dstPipes) {
		reasons = append(reasons, fmt.Sprintf(
			"表格结构被改动（原文 %d 行竖线数 %s，译文 %d 行 %s），逐行保持 | 与列数不变",
			len(srcPipes), formatIntList(clamp6(srcPipes)), len(dstPipes), formatIntList(clamp6(dstPipes))))
	}

	// 译文异常短（占位符除外几乎没内容）大概率是拒答/半途而废
	visible := func(t string) int { return runeLen(strings.TrimSpace(phCleanRe.ReplaceAllString(t, ""))) }
	if visible(src) > 200 && float64(visible(dst)) < float64(visible(src))*0.2 {
		reasons = append(reasons, "译文相对原文异常简短，疑似未完整翻译")
	}

	return reasons
}

// intSlicesEqual 逐元素相等。
func intSlicesEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// clamp6 取前 6 个元素。
func clamp6(xs []int) []int {
	if len(xs) > 6 {
		return xs[:6]
	}
	return xs
}

// ValidateFinal 回填后的成品校验。
func ValidateFinal(md string) []string {
	var reasons []string

	// 行间公式 $$ 成对（总数须为偶数）
	display := strings.Count(md, "$$")
	if display%2 != 0 {
		reasons = append(reasons, fmt.Sprintf("行间公式定界符 $$ 数量为奇数（%d）", display))
	}

	// 行内 $ 总数（含 $$ 折算）须为偶数
	dollars := countDollars(md)
	if dollars%2 != 0 {
		reasons = append(reasons, fmt.Sprintf("$ 定界符总数为奇数（%d），行内公式未闭合", dollars))
	}

	// \begin{x} 与 \end{x} 每种环境配对
	envs := make(map[string]int)
	for _, g := range beginEndRe.FindAllStringSubmatch(md, -1) {
		if g[1] == "begin" {
			envs[g[2]]++
		} else {
			envs[g[2]]--
		}
	}
	var bad []string
	for name, c := range envs {
		if c != 0 {
			bad = append(bad, name)
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		reasons = append(reasons, "\\begin/\\end 不配对: "+strings.Join(bad, ", "))
	}

	// 残留占位符说明回填漏了
	if leftover := Placeholders(md); len(leftover) > 0 {
		uniq := uniqueSorted(leftover)
		reasons = append(reasons, "回填后仍残留占位符 ⟦F"+formatIntList(uniq)+"⟧")
	}

	return reasons
}

// countDollars 统计 $ 定界符总数（$$ 折算两个，\$ 跳过）。
func countDollars(md string) int {
	n := 0
	runes := []rune(md)
	m, _ := dollarRe.FindRunesMatch(runes)
	for m != nil {
		n++
		m, _ = dollarRe.FindNextMatch(m)
	}
	return n
}

// uniqueSorted 排序去重。
func uniqueSorted(xs []int) []int {
	cp := append([]int(nil), xs...)
	sort.Ints(cp)
	out := cp[:0]
	for i, x := range cp {
		if i == 0 || x != cp[i-1] {
			out = append(out, x)
		}
	}
	return out
}
