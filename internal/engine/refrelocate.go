// refrelocate.go — 文献归位：MinerU 按版面阅读序线性化双栏 PDF，参考文献若
// 分跨栏底会被拆散并插在正文中间（PRL 型单页论文的固有现象）。翻译前把散落
// 的 [N] 条目聚拢到文末并按编号升序排列，使每条文献都成为独立短段，稳定命中
// refDetector 的"保留原文"判定。
//
// 防误伤闸门：
//
//	R1 单条判定复用 refDetector 的三重保险（citeHeadRe + 作者/年份特征 + 长度上限）；
//	R2 文档级编号状态机：严格递增且跳距受限；
//	R3 序列门槛：接受条目不足 refMinSequence 不动手，防把正文里偶然以 [N]
//	   开头的段落搬走；
//	R4 与冻结保护区块（$$、代码围栏、表格、图片）重叠的块不是候选；
//	R5 归位时按起始编号升序写入文末（与文末原有条目合并排序，杜绝乱序尾巴）；
//	R6 幂等守卫：接受条目连同其后编号续接的拒收块已构成文末连续尾部时，
//	   逐字节原样返回（hash 不变）——特征缺失的文末文献不触发误归位；
//	R7 特征校验失败不更新编号游标（与 refDetector 语义一致）；
//	R8 多条挤进一个块时整块搬移，游标取块内最大 N，块文本逐字保留。
//
// 返回的条目数为"位置实际变化的条目数"。归位只改内存文本，绝不写回归档的
// 02_en_md；不发明标题，搬移时块间分隔规整为 "\n\n"（与切段契约一致，
// 连接可还原全文）。已知限制：文末若恰好出现 "[N] … (年份)" 样式的正文段
// 且编号与文献表续接，整文档会被判为已就位而跳过归位——保守方向，宁可
// 不搬、不可乱序。注意：重排会改变切段序列，旧断点记录按"段索引 +
// 内容 hash"双键匹配（checkpoints.go），重排后逐段 miss（内容确实变了，
// 不会误用旧译文），已翻译文档重跑会重译一次。
package engine

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// 与 refDetector 共用的文献判定口径（engine.go）。
const (
	refMaxRunes    = 800 // 单条候选的长度上限（rune 口径）
	refMaxGap      = 3   // 编号续接的最大跳距
	refMinSequence = 3   // 触发归位的最少接受条目数
)

// refLineRe 块内行首 [N]（refHeadRe 的多行版，用于 R8 识别块内挤在一起的多条）。
var refLineRe = regexp.MustCompile(`(?m)^\[(\d{1,3})\]\s`)

// refEntry 一个被接受的文献块：块下标、排序键（块内首个编号）与状态机
// 游标（块内最大编号，isDocTail 续接判定与 R2 同口径）。
type refEntry struct {
	idx    int
	firstN int
	maxN   int
}

// RelocateReferences 返回归位后的文本与移位条目数（0 表示原文未动）。
func RelocateReferences(md string) (string, int) {
	if strings.TrimSpace(md) == "" {
		return md, 0
	}
	// 剥离文末空白（空行/空格）再处理，结束后原样接回：文末空行不得
	// 干扰尾部判定，原样返回与重建输出也都保留原有结尾。
	body := strings.TrimRightFunc(md, unicode.IsSpace)
	suffix := md[len(body):]

	blocks := splitBlocks(body)

	started, last := false, 0
	var entries []refEntry
	for i, b := range blocks {
		n, ok := acceptRefBlock(b, &started, &last)
		if ok {
			entries = append(entries, refEntry{idx: i, firstN: n, maxN: last})
		}
	}

	// R3：序列门槛
	if len(entries) < refMinSequence {
		return md, 0
	}
	// R6：已构成文末连续尾部 → 原样返回
	if isDocTail(blocks, entries) {
		return md, 0
	}

	// R5：按起始编号升序
	sort.SliceStable(entries, func(a, b int) bool { return entries[a].firstN < entries[b].firstN })
	relocating := make(map[int]bool, len(entries))
	for _, e := range entries {
		relocating[e.idx] = true
	}
	var out []string
	for i, b := range blocks {
		if !relocating[i] {
			out = append(out, b)
		}
	}
	for _, e := range entries {
		out = append(out, blocks[e.idx])
	}
	// 移位条目数：原下标 ≠ 归位后下标
	kept := len(blocks) - len(entries)
	displaced := 0
	for rank, e := range entries {
		if e.idx != kept+rank {
			displaced++
		}
	}
	return strings.Join(out, "\n\n") + suffix, displaced
}

// splitBlocks 按空行切块（与 segmenter 的 blockSplitRe 同一把正则）。
func splitBlocks(md string) []string {
	locs := blockSplitRe.FindAllStringIndex(md, -1)
	blocks := make([]string, 0, len(locs)+1)
	prev := 0
	for _, loc := range locs {
		blocks = append(blocks, md[prev:loc[0]])
		prev = loc[1]
	}
	return append(blocks, md[prev:])
}

// isDocTail 报告接受条目是否已构成文末连续尾部：从首个接受条目到文末，
// 除接受条目外只允许空白块与"编号续接的 [N] 块"（特征缺失等被拒的文献
// 条目）。成立时无需归位——保守不动优先于搬移后的乱序。
func isDocTail(blocks []string, entries []refEntry) bool {
	accepted := make(map[int]int, len(entries))
	for _, e := range entries {
		accepted[e.idx] = e.maxN
	}
	cursor := 0
	for i := entries[0].idx; i < len(blocks); i++ {
		if n, ok := accepted[i]; ok {
			cursor = n
			continue
		}
		b := blocks[i]
		if strings.TrimSpace(b) == "" {
			continue
		}
		m := refLineRe.FindStringSubmatch(strings.TrimLeftFunc(b, unicode.IsSpace))
		if m == nil {
			return false
		}
		n, err := strconv.Atoi(m[1])
		if err != nil || !(cursor < n && n <= cursor+refMaxGap) {
			return false
		}
		// 编号续接的拒收块允许存在，但不推进游标（与 R7 一致）。
	}
	return true
}

// acceptRefBlock 单条候选判定（R1/R2/R4/R7/R8）。started/last 为文档级游标，
// 仅在接受时推进（R7）。返回块内首个编号与是否接受。
func acceptRefBlock(block string, started *bool, last *int) (int, bool) {
	trimmed := strings.TrimLeftFunc(block, unicode.IsSpace)
	m := citeHeadRe.FindStringSubmatch(trimmed)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	if *started && !(*last < n && n <= *last+refMaxGap) {
		return 0, false // R2：编号须严格递增且跳距受限
	}
	if runeLen(block) > refMaxRunes {
		return 0, false // R1：与 refDetector 同口径的长度上限
	}
	if len(ProtectedSpans(block)) > 0 {
		return 0, false // R4：冻结保护区块不是候选
	}
	if !authorInitialsRe.MatchString(block) && !yearRe.MatchString(block) {
		return 0, false // R1：文献特征
	}
	// R8：块内多行 [N] 时游标取最大编号
	maxN := n
	for _, sm := range refLineRe.FindAllStringSubmatch(block, -1) {
		if k, err := strconv.Atoi(sm[1]); err == nil && k > maxN {
			maxN = k
		}
	}
	*started = true
	*last = maxN
	return n, true
}
