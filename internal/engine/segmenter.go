// segmenter.go — 把 Markdown 按标题层级切成适合喂给 LLM 的段。
//
// 自 v1.1.1 引擎母本移植（117 行）。规则：
//  1. 遇到标题行（# ~ ######）且当前段非空 → 开新段（标题随新段走）；
//  2. 段内按空行分节累加，超过 max_chars 上限 → 收束当前段；
//  3. 单个段落本身超上限（罕见，如超长表格冻结后仍超）→ 在上限附近
//     最近的换行处硬切。
//
// 切段保持原文逐字拼接（"\n\n" 连接），保证回填组装后与原文只差段与段
// 之间的分隔符——不对内容做任何改写。
//
// 移植注意：Python 的 len()/切片按"字符"计，这里一律用 rune 计数与切片
// （[]rune），保证与母本在含希腊字母/重音符/中日韩字符的文本上结果一致。
package engine

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// headingRe 标题行（# ~ ######）。Python 母本带 (?m)；对单行匹配时
// ^ 即行首，语义一致；validator 在整篇文本上使用时 (?m) 必需。
var headingRe = regexp.MustCompile(`(?m)^#{1,6}\s`)

// refHeadRe 文献条目段首（[N] 开头）。
var refHeadRe = regexp.MustCompile(`^\[\d{1,3}\]\s`)

// blockSplitRe 空行分块（Python re.split(r"\n\s*\n", ...)）。
var blockSplitRe = regexp.MustCompile(`\n\s*\n`)

// DefaultMaxChars 默认切段上限（字符数，rune 口径）。
const DefaultMaxChars = 3000

// runeLen 字符数（对齐 Python len()）。
func runeLen(s string) int { return utf8.RuneCountInString(s) }

// Split 返回切段列表；空文本返回 []。
func Split(text string, maxChars int) []string {
	text = strings.Trim(text, "\n")
	if strings.TrimSpace(text) == "" {
		return nil
	}

	lines := strings.Split(text, "\n")

	// 第一步：按标题行切成"节"（标题 + 后续内容直到下一标题）
	sections := [][]string{{}}
	for _, line := range lines {
		last := len(sections) - 1
		if headingRe.MatchString(line) && len(sections[last]) > 0 {
			sections = append(sections, []string{line})
		} else {
			sections[last] = append(sections[last], line)
		}
	}
	kept := sections[:0]
	for _, s := range sections {
		if strings.TrimSpace(strings.Join(s, "")) != "" {
			kept = append(kept, s)
		}
	}
	sections = kept

	// 第二步：节内按空行分小块，贪心装袋到 max_chars
	var chunks []string
	var bag []string
	bagLen := 0

	flush := func() {
		if len(bag) > 0 {
			chunks = append(chunks, strings.Join(bag, "\n\n"))
			bag, bagLen = nil, 0
		}
	}

	for _, section := range sections {
		joined := strings.Join(section, "\n")
		blocks := blockSplitRe.Split(strings.Trim(joined, "\n"), -1)
		for _, block := range blocks {
			if strings.TrimSpace(block) == "" {
				continue
			}
			if refHeadRe.MatchString(block) {
				// 文献条目（[N] 开头）独立成段：不被前后文打包，
				// 便于上层逐条甄别（保留原文或单独翻译）
				flush()
			}
			if runeLen(block) > maxChars {
				flush()
				chunks = append(chunks, hardSplit(block, maxChars)...)
				continue
			}
			if bagLen+runeLen(block)+2 > maxChars {
				flush()
			}
			bag = append(bag, block)
			bagLen += runeLen(block) + 2
		}
		// 标题节边界处收束，避免翻译段横跨两个章节
		if strings.HasPrefix(strings.TrimLeftFunc(joined, unicode.IsSpace), "#") {
			flush()
		}
	}
	flush()
	return chunks
}

// hardSplit 超长单块的硬切：优先在换行处断开。
//
// 切点不落进冻结区（代码/公式/表格/图片）内部——$$…$$ 被从中间切断后
// 定界符不配对，freeze 匹配不到，裸 LaTeX 会被模型改写。区间本身超限时
// 宁可整段超长也不截断。
func hardSplit(block string, maxChars int) []string {
	spans := ProtectedSpans(block)
	rest := []rune(block)

	spanAt := func(pos int) (int, int, bool) {
		// 严格落在区间内部（恰好在边界上不算被切）
		for _, s := range spans {
			if s.Start < pos && pos < s.End {
				return s.Start, s.End, true
			}
		}
		return 0, 0, false
	}

	var parts []string
	base := 0 // rest 在 block 中的 rune 起始偏移（spans 用 block 坐标）
	for len(rest) > maxChars {
		window := rest[:maxChars]
		nl := -1
		for i := len(window) - 1; i >= 0; i-- {
			if window[i] == '\n' {
				nl = i
				break
			}
		}
		cut := maxChars
		if nl > maxChars/2 {
			cut = nl
		}
		if s, e, ok := spanAt(base + cut); ok {
			if s-base > maxChars/2 {
				cut = s - base // 切在冻结区起点，区间整体留给下一段
			} else {
				cut = e - base // 区间跟着本段走，宁可超长
				if cut > len(rest) {
					cut = len(rest)
				}
			}
		}
		if cut <= 0 || cut >= len(rest) {
			// 单个超限冻结区独占：整段保留（同时防死循环）
			parts = append(parts, string(rest))
			rest = nil
			break
		}
		parts = append(parts, string(rest[:cut]))
		nxtRunes := rest[cut:]
		i := 0
		for i < len(nxtRunes) && nxtRunes[i] == '\n' {
			i++
		}
		nxt := nxtRunes[i:]
		base += len(rest) - len(nxt)
		rest = nxt
	}
	if len(rest) > 0 {
		parts = append(parts, string(rest))
	}
	return parts
}
