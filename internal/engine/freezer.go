// freezer.go — 公式/代码/图片/表格冻结为占位符，译后回填。
//
// 占位符用 U+27E6/U+27E7（数学白
// 方括号），学术正文几乎不会出现。
//
// 冻结顺序：代码块 → 行间公式 $$…$$ → HTML 表格 → HTML img →
// Markdown 图片 → 行内公式 $…$。顺序先长后短，防止大块内部的小模式
// 被先替换导致截断。
//
// 对外区间（ProtectedSpans）与内部切分一律使用 rune 字符坐标；
// 标准库 regexp 返回的字节下标经 runeIndex 换算，保证多字节字符位置正确。
package engine

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	regexp2 "github.com/dlclark/regexp2"
)

// PlaceholderRE 匹配 ⟦F数字⟧ 占位符。
var PlaceholderRE = regexp.MustCompile(`⟦F(\d+)⟧`)

// freezePatterns 各类冻结模式，顺序即冻结顺序（先长块后短块）。
// 这些模式使用 RE2 语法，行内公式单独处理。
var freezePatterns = []*regexp.Regexp{
	// 围栏代码块 ```…``` 或 ~~~…~~~（可跨行）
	regexp.MustCompile("(?sm)^(?:```|~~~).*?(?:```|~~~)$"),
	// 行间公式 $$…$$（可跨行）
	regexp.MustCompile(`(?s)\$\$.+?\$\$`),
	// HTML 表格整体（MinerU 输出的表格是 HTML）
	regexp.MustCompile(`(?si)<table\b.*?</table\s*>`),
	// HTML img 标签
	regexp.MustCompile(`(?i)<img\b[^>]*>`),
	// Markdown 图片 ![alt](url)
	regexp.MustCompile(`!\[[^\]]*\]\([^)\s]*(?:\s+"[^"]*")?\)`),
}

// inlineFormulaRe 行内公式 $…$（单行内，非空；不吃 \$ 转义）。
// 含两处 (?<!\\) 反向断言——RE2 不支持，走 regexp2。
var inlineFormulaRe = regexp2.MustCompile(`(?<!\\)\$[^\$\n]+?(?<!\\)\$`, regexp2.None)

// runeIndex 把字节下标换算为 rune 下标（s[:byteIdx] 必须落在字符边界）。
func runeIndex(s string, byteIdx int) int {
	return utf8.RuneCountInString(s[:byteIdx])
}

// Frozen 是 Freeze 的结果：Text 为冻结后文本（喂给模型），Mapping 为
// 占位符编号 → 原始内容。
type Frozen struct {
	Text    string
	Mapping map[int]string
}

// span 是 rune 坐标下的半开区间 [Start, End)。
type span struct{ Start, End int }

// ProtectedSpans 返回冻结区（代码/公式/表格/图片）的 rune 区间，重叠已
// 合并。供切段使用：切点不得落进区间内部——把 $$…$$ 或围栏代码从中间
// 切断会让定界符不配对，冻结正则匹配不到，裸 LaTeX 喂给模型被改写。
func ProtectedSpans(text string) []span {
	var spans []span
	for _, pat := range freezePatterns {
		for _, loc := range pat.FindAllStringIndex(text, -1) {
			spans = append(spans, span{runeIndex(text, loc[0]), runeIndex(text, loc[1])})
		}
	}
	// 行内公式：regexp2 的 Index 即 rune 坐标，与上面换算后的坐标系一致
	runes := []rune(text)
	m, _ := inlineFormulaRe.FindRunesMatch(runes)
	for m != nil {
		spans = append(spans, span{m.Index, m.Index + m.Length})
		m, _ = inlineFormulaRe.FindNextMatch(m)
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].Start < spans[j].Start })
	var merged []span
	for _, s := range spans {
		if len(merged) > 0 && s.Start < merged[len(merged)-1].End {
			if s.End > merged[len(merged)-1].End {
				merged[len(merged)-1].End = s.End
			}
		} else {
			merged = append(merged, s)
		}
	}
	return merged
}

// Freeze 把 text 中的公式/代码/图片/表格替换为占位符。start 为起始编号，
// 方便多段共用一套编号空间（本工程按段独立，通常用默认值 1）。
func Freeze(text string, start int) Frozen {
	mapping := make(map[int]string)
	counter := start
	sub := func(matched string) string {
		// 展开本次外层匹配内已冻结的区段，避免映射相互嵌套。
		original := Unfreeze(matched, mapping)
		ph := fmt.Sprintf("⟦F%d⟧", counter)
		for strings.Contains(text, ph) {
			counter++
			ph = fmt.Sprintf("⟦F%d⟧", counter)
		}
		mapping[counter] = original
		counter++
		return ph
	}

	out := text
	for _, pat := range freezePatterns {
		out = pat.ReplaceAllStringFunc(out, sub)
	}
	// 行内公式：regexp2 逐个替换（ReplaceFunc 从头扫到尾，计数 -1 不限）
	out2, err := inlineFormulaRe.ReplaceFunc(out, func(m regexp2.Match) string {
		return sub(m.String())
	}, 0, -1)
	if err != nil {
		// 正则引擎自身异常（理论上不会发生）：按未匹配处理，保持原文
		out2 = out
	}
	return Frozen{Text: out2, Mapping: mapping}
}

// Unfreeze 把占位符回填为原始内容。未知编号原样保留（validator 会抓住）。
func Unfreeze(text string, mapping map[int]string) string {
	return PlaceholderRE.ReplaceAllStringFunc(text, func(m string) string {
		groups := PlaceholderRE.FindStringSubmatch(m)
		num, err := strconv.Atoi(groups[1])
		if err != nil {
			return m
		}
		if v, ok := mapping[num]; ok {
			return v
		}
		return m
	})
}

// Placeholders 按出现顺序列出文本中的占位符编号。
func Placeholders(text string) []int {
	groups := PlaceholderRE.FindAllStringSubmatch(text, -1)
	out := make([]int, 0, len(groups))
	for _, g := range groups {
		n, _ := strconv.Atoi(g[1])
		out = append(out, n)
	}
	return out
}
