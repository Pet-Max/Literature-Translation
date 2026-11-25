// validator.go — 译文机械校验（纯代码检查，不依赖模型自觉）。
//
// 校验分为两层：
//   - Validate(src, dst)：冻结态校验，翻译质量重试的依据；
//   - ValidateFinal(md)：回填后的成品校验，$ 配对与 \begin/\end 配对。
//
// 约定：校验提示文案受 validator_golden.json 回归测试约束；列表
// 格式化为 "[1, 2, 3]" 形态；剔除占位符后的正文长度按 rune 计。
package engine

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	regexp2 "github.com/dlclark/regexp2"
)

// dollarRe 行内/行间 $ 定界符：$$ 优先（消费两字符），不吃 \$ 转义。
// 含 (?<!\\) 反向断言，走 regexp2。
var dollarRe = regexp2.MustCompile(`\$\$|(?<!\\)\$`, regexp2.None)

// beginEndRe \begin{x} / \end{x} 配对检查。
var beginEndRe = regexp.MustCompile(`\\(begin|end)\{([^}]+)\}`)

// phCleanRe 计算可见长度时剔除占位符。
var phCleanRe = regexp.MustCompile(`⟦F\d+⟧`)

var (
	// thinkTagRe 识别部分推理模型泄漏到正文里的思考过程标签。
	thinkTagRe = regexp.MustCompile(`(?i)</?think>`)
	// asciiLowerWordRe 统计英文正文小写词，用于区分自然语言散文与纯化学/表格符号串。
	asciiLowerWordRe = regexp.MustCompile(`\b[a-z]{3,}\b`)
	// inlineCodeRe / mdLinkURLRe / bareURLRe 在检测英文回显前剔除行内代码、链接目标 URL 与裸 URL（按规则均原样保留）。
	inlineCodeRe = regexp.MustCompile("`[^`\n]+`")
	mdLinkURLRe  = regexp.MustCompile(`\]\([^)\n]+\)`)
	bareURLRe    = regexp.MustCompile(`https?://\S+`)
	// standaloneRefAuthorHeadRe 在无参考节上下文时识别段首作者引文条目形态（如 Surname, A. 或 A. Author, / A. Author and B. Author,）。
	standaloneRefAuthorHeadRe = regexp.MustCompile(`^(?:\p{Lu}\p{L}+(?:-\p{Lu}?\p{L}+)*,\s+[A-Z]\.|[A-Z](?:\.?-[A-Z])?\.\s+(?:(?:de|del|della|di|du|la|le|van|von|der|den)\s+){0,2}\p{Lu}\p{L}+(?:,|\s+and\s+[A-Z]\.))`)
)

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
	} else if len(srcHeads) > 0 {
		srcLvls := headingLevels(srcHeads)
		dstLvls := headingLevels(dstHeads)
		if !intSlicesEqual(srcLvls, dstLvls) {
			reasons = append(reasons, fmt.Sprintf(
				"标题层级不一致（原文 %s，译文 %s），保持每个标题的 # 数量不变",
				formatIntList(srcLvls), formatIntList(dstLvls)))
		}
	}

	// 表格结构：管道表格的逐行竖线数必须与原文一致（规则 4：
	// 只译单元格文字）。模型多写/少写一个 | 整张表渲染就废，且
	// 占位符校验查不到——表格不是占位符，是可译文本。
	srcPipes := tablePipes(src)
	dstPipes := tablePipes(dst)
	if !intSlicesEqual(srcPipes, dstPipes) {
		if len(srcPipes) != len(dstPipes) {
			reasons = append(reasons, fmt.Sprintf(
				"表格结构被改动（原文 %d 行，译文 %d 行），逐行保持 | 与列数不变",
				len(srcPipes), len(dstPipes)))
		} else {
			rowIdx := firstDiffIndex(srcPipes, dstPipes)
			reasons = append(reasons, fmt.Sprintf(
				"表格结构被改动（第 %d 行竖线数不一致：原文 %d 个 |，译文 %d 个 |），逐行保持 | 与列数不变",
				rowIdx+1, srcPipes[rowIdx], dstPipes[rowIdx]))
		}
	}

	// 输出格式校验：禁止外层代码围栏包裹或泄漏推理过程标签
	if hasOuterCodeFence(src, dst) {
		reasons = append(reasons, "译文被外层代码围栏（``` 或 ~~~）包裹，请去掉首尾围栏直接输出纯译文")
	}
	if !thinkTagRe.MatchString(src) && thinkTagRe.MatchString(dst) {
		reasons = append(reasons, "译文包含思考过程标签（<think>），请只输出译文本身")
	}

	// 剔除占位符后复用于异常简短检测与纯英文回显检测，避免重复正则扫描
	srcClean := strings.TrimSpace(phCleanRe.ReplaceAllString(src, ""))
	dstClean := strings.TrimSpace(phCleanRe.ReplaceAllString(dst, ""))
	if runeLen(srcClean) > 200 && float64(runeLen(dstClean)) < float64(runeLen(srcClean))*0.2 {
		reasons = append(reasons, "译文相对原文异常简短，疑似未完整翻译")
	}
	if isUntranslatedProse(srcClean, dstClean) {
		reasons = append(reasons, "译文未包含任何中文内容，疑似原样回显原文而未翻译")
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

// firstDiffIndex 返回两个整数切片首个不等元素的下标；若前缀与长度完全一致则返回 -1。
func firstDiffIndex(a, b []int) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return -1
}

// headingLevels 提取各标题行前导 # 的数量，用于校验标题层级结构是否被改动。
func headingLevels(heads []string) []int {
	lvls := make([]int, len(heads))
	for i, h := range heads {
		n := 0
		for n < len(h) && h[n] == '#' {
			n++
		}
		lvls[i] = n
	}
	return lvls
}

// hasOuterCodeFence 检测译文是否被整体包裹在冗余代码围栏内（原文自身以围栏开头时豁免）：
// 信息串为 markdown/md 时直接判定命中；其余围栏需同时满足末行同标记闭合，以放行行首行内代码。
func hasOuterCodeFence(src, dst string) bool {
	sTrim := strings.TrimSpace(src)
	dTrim := strings.TrimSpace(dst)
	if sTrim == "" || dTrim == "" {
		return false
	}
	if strings.HasPrefix(sTrim, "```") || strings.HasPrefix(sTrim, "~~~") {
		return false
	}
	if !strings.HasPrefix(dTrim, "```") && !strings.HasPrefix(dTrim, "~~~") {
		return false
	}
	lines := splitLines(dTrim)
	if len(lines) < 2 {
		return false
	}
	first := strings.TrimSpace(lines[0])
	mark := first[:3]
	rest := strings.TrimLeft(first, mark[:1])
	if (mark == "```" && strings.Contains(rest, "`")) ||
		(mark == "~~~" && strings.Contains(rest, "~~~")) {
		return false
	}
	info := strings.ToLower(strings.TrimSpace(rest))
	if info == "markdown" || info == "md" {
		return true
	}
	last := strings.TrimSpace(lines[len(lines)-1])
	return strings.HasPrefix(last, mark) && strings.Trim(last, mark[:1]) == ""
}

// countHan 统计字符串中的汉字个数。
func countHan(s string) int {
	n := 0
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			n++
		}
	}
	return n
}

// looksLikeStandaloneRefs 在无参考节上下文时保守判定整段是否为纯参考文献条目列表
// （与仅限参考节内部使用的 looksLikeRefSectionBody 不同，要求每段/每行同时具备条目起始形态与引文特征），
// 避免含 "(2020)" 或 "J. Smith" 等正常英文正文被误豁免。
func looksLikeStandaloneRefs(text string) bool {
	paras := blockSplitRe.Split(strings.TrimSpace(text), -1)
	count := 0
	for _, p := range paras {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if runeLen(p) > refMaxRunes {
			lines := splitLines(p)
			if len(lines) <= 1 {
				return false
			}
			for _, ln := range lines {
				ln = strings.TrimSpace(ln)
				if ln == "" {
					continue
				}
				if !refSectionNumLineRe.MatchString(ln) || runeLen(ln) > refMaxRunes || !hasRefSectionFeature(ln) {
					return false
				}
			}
			count++
			continue
		}
		if (!refSectionNumLineRe.MatchString(p) && !standaloneRefAuthorHeadRe.MatchString(p)) || !hasRefSectionFeature(p) {
			return false
		}
		count++
	}
	return count > 0
}

// isUntranslatedProse 检测模型是否将较长英文自然语言段落原样回显而未翻译：
// 要求剥离占位符后双方均超过 200 字符、双方均零汉字，且剥离行内代码、链接 URL 与裸 URL 后仍含 ≥25 个英文小写词；
// 符合独立参考文献条目特征的源段予以豁免，以免与规则 6（保留参考文献原文）冲突。
func isUntranslatedProse(srcClean, dstClean string) bool {
	if runeLen(srcClean) <= 200 || runeLen(dstClean) <= 200 {
		return false
	}
	if countHan(srcClean) > 0 || countHan(dstClean) > 0 {
		return false
	}
	if looksLikeStandaloneRefs(srcClean) {
		return false
	}
	prose := bareURLRe.ReplaceAllString(mdLinkURLRe.ReplaceAllString(inlineCodeRe.ReplaceAllString(srcClean, " "), "]"), " ")
	return len(asciiLowerWordRe.FindAllString(prose, 25)) >= 25
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
