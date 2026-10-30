// Package repair 用原 PDF 文本层校准 MinerU 解析 MD 的三类损伤。
//
//  1. 拼写损伤：MinerU 云端（vlm 与 pipeline 皆然）对页面重新做文字识别，
//     衬线体 "ff" 常被误读为单个 f（different→diferent、diffusion→difusion），
//     行尾连字符偶尔合并不当（timedomain、one-toone、configura tion）。
//     以原 PDF 文本层为词表：
//     - 词在词表中 → 保留；
//     - 词不在词表，但与相邻非词拼接后成词（configura+tion）→ 合并；
//     - 词不在词表，但可拆成两个词表词（timedomain→time domain）→ 拆分；
//     - 词不在词表，但词表中恰有一个编辑距离 ≤1 的候选（diferent→different）→ 替换；
//     - 其余不动并记录（避免误伤）。
//  2. 真词损伤：连字误读恰好产出合法英文词（offer→over、fields→yields、
//     off-diagonal→of-diagonal），词表法原理上拦不住。改用上下文锚定：
//     MD 与原文的分词序列近似平行，分歧块为"MD 单词 ↔ 原文唯一词表候选"
//     且编辑距离 ≤2 时替换（anchorRepair）。
//  3. 符号损伤：MinerU 丢失或误写数学符号——|00⟩ 丢成 |00i、⟩⟨ 误写成
//     \right.\left.、行内公式丢 →、LaTeX 引号被误认为公式。按白名单规则
//     修复（normalizeSymbols）；凡涉及具体内容的替换，一律先在原文文本层
//     找到锚点才落笔，找不到只记录不动。
//
// 原 PDF 文本层先经 normalizeOrig 归一（CM 字体连字 ﬀﬁﬂ 等还原为字母
// 序列）再建词表，否则 "eﬀects" 会被切成 "e"+"ects" 毒化词表，拼写校准
// 对 ff/fi 类损伤整体失效。
//
// 公式 $…$、代码块、<img>/<table> 标签受保护，不做任何改写。
package repair

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Stats 是一次校准的统计。
type Stats struct {
	Fixed   int      // 编辑距离替换
	Merged  int      // 相邻合并
	Split   int      // 一词拆二
	Ctx     int      // 上下文锚定替换（真词损伤）
	Syms    int      // 符号修复（ket/bra/公式内定界符/引号/箭头/<eq>）
	Tables  int      // HTML 表格转 Markdown 表格
	Accents int      // 分离重音符合成（H¨anggi→Hänggi）
	Ctrl    int      // 控制码还原为希腊字母等（PDF 文本层锚定）
	Display int      // 伪行间公式 $…$ 升级为 $$…$$
	Kept    []string // 存疑未动的词（去重，供日志）
	Details []string // 逐条修改记录（供 calibrate 命令人工抽查，封顶 maxDetails）
}

const maxDetails = 500

func (st *Stats) detail(msg string) {
	if len(st.Details) < maxDetails {
		st.Details = append(st.Details, msg)
	}
}

// wordRe 只匹配 ASCII 字母串；公式记号、URL、hex 哈希不受影响。
var (
	wordRe     = regexp.MustCompile(`[A-Za-z]{2,}`)
	fenceRe    = regexp.MustCompile("(?ms)^(?:```|~~~)[^\n]*\n.*?^(?:```|~~~)")
	dispRe     = regexp.MustCompile(`(?s)\$\$.+?\$\$`)
	inlineRe   = regexp.MustCompile(`\$[^$\n]+?\$`)
	tagRe      = regexp.MustCompile(`</?[A-Za-z][^>]*>`)
	tokenSplit = regexp.MustCompile(`[^A-Za-z]+`)
)

// hyphenBreakRe 行尾连字符断行（"differ-\nent" 或悬挂式 "short-\nand"）。
var hyphenBreakRe = regexp.MustCompile(`(?m)([A-Za-z]{2,})-\r?\n([a-z]{2,})`)

// urlRe Markdown 链接/图片 与裸 URL。URL 里合法的词片段（如域名
// openxlab.org.cn 的 "org"）绝不能进词级修复——V4 验证中 399 处图片
// CDN 的 org 被上下文锚定改成 "or"，链接全坏。
var urlRe = regexp.MustCompile(`!?\[[^\]]*\]\([^)]*\)|https?://[^\s)<>"']+`)

// functionWords 常见功能词：行尾连字符后跟功能词，几乎总是悬挂连字符
// （"short-\nand" 即 "short- and"），应保持两词分离；跟其他内容则是断词
// （"configura-\ntion"→configuration、"differ-\nent"→different），应接回。
var functionWords = map[string]struct{}{
	"and": {}, "or": {}, "the": {}, "of": {}, "to": {}, "in": {}, "a": {},
	"an": {}, "with": {}, "for": {}, "on": {}, "at": {}, "by": {}, "from": {},
	"is": {}, "are": {}, "was": {}, "were": {}, "not": {}, "but": {}, "as": {},
	"its": {}, "this": {}, "that": {}, "these": {}, "those": {},
}

// ligatureMap LaTeX CM 字体的连字与组合符：PyMuPDF 文本层里 "effects" 常以
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

// Options 控制启用哪些校准层，两层独立开关：
// 符号层修 |00⟩、公式定界符/箭头等记号损伤；词级层修拼写与真词损伤
// （over→offer 类）。
type Options struct {
	Spelling bool // 词级：上下文锚定 + 拼写规则（修正/合并/拆分）
	Symbols  bool // 符号：ket/bra、\right.\left.、引号公式、箭头回填
}

// Apply 校准 mdPath 指向的 Markdown 文件（原地写回），origText 为原 PDF
// 文本层内容。返回统计。
func Apply(mdPath, origText string, opt Options) (Stats, error) {
	md, err := os.ReadFile(mdPath)
	if err != nil {
		return Stats{}, fmt.Errorf("读取解析 MD 失败: %w", err)
	}
	res, err2 := Calibrate(string(md), origText, opt)
	if err2 != nil {
		return Stats{}, err2
	}
	if err := os.WriteFile(mdPath, []byte(res.Text), 0o644); err != nil {
		return Stats{}, err
	}
	return res.Stats, nil
}

// Result 是 Markdown 校准的结果（文本 + 统计）。
type Result struct {
	Text  string
	Stats Stats
}

// Calibrate 完整校准，按 opt 分层门控：表格/重音符/符号修复 → 伪行间
// 公式升级 → 上下文锚定 → 拼写规则。origText 为原 PDF 文本层（内部先
// 归一，重复归一幂等）。表格转换与重音符合成不受开关门控：它们是
// 无损的结构性还原，且是后续所有修复的前提（HTML 表格里的 <eq> 公式
// 要先落成 $…$ 才有符号修复）。
func Calibrate(md, origText string, opt Options) (Result, error) {
	orig := normalizeOrig(origText)
	vocab := BuildVocab(orig)
	var st Stats
	// 0) 结构性还原：HTML 表格 → Markdown 表格；残余 <eq> → $…$；
	//    分离重音符合成
	text := tablesToMarkdown(md, &st)
	text = eqToMath(text, &st)
	text = composeAccents(text, &st)
	// 1) 符号修复（行内公式内容在此阶段单独处理）
	if opt.Symbols {
		text = fixControlChars(text, orig, &st)
		text = normalizeSymbols(text, orig, &st)
	}
	// 1.5) 伪行间公式升级：必须在符号修复之后——升级成的 $$ 块会被
	//      词级冻结且符号规则只处理行内 $…$，先升级会让独立成行的
	//      $…$ 里的 |00i/⟩⟨ 损伤逃过修复（探针实证的顺序缺口）
	text = promoteDisplayMath(text, &st)
	// 2) 词级校准（公式/代码/标签冻结）：先上下文锚定（精确，可处理真词
	//    损伤），后通用拼写规则（兜住剩余非常规词损伤）
	if !opt.Spelling {
		return Result{Text: text, Stats: st}, nil
	}
	frozen, protect := freeze(text, true)
	frozen, suspect := anchorRepair(frozen, orig, vocab, &st)
	frozen = localContextRepair(frozen, orig, vocab, &st, suspect)
	frozen = repairProse(frozen, vocab, &st)
	text = unfreeze(frozen, protect)
	return Result{Text: text, Stats: st}, nil
}

// Markdown 仅拼写校准（合并/拆分/编辑距离三规则），供测试直接验证词级
// 规则；生产路径走 Calibrate（符号 + 上下文锚定 + 拼写的完整组合）。
func Markdown(md string, vocab map[string]struct{}) (Result, error) {
	var st Stats
	frozen, protect := freeze(md, true)
	frozen = repairProse(frozen, vocab, &st)
	text := unfreeze(frozen, protect)
	return Result{Text: text, Stats: st}, nil
}

// freeze 把需保护的区段替换为 \x00N\x00 占位符。键为纯数字，不会被任何
// 分词正则捕获（上下文对齐按 [A-Za-z]+ 取词，旧的 "P12" 形态会被捕获）。
// freezeInline 为 false 时保留行内公式（符号修复需要改写其内部内容）。
// 顺序：围栏 → URL → 行间公式 → 行内公式 → 标签。URL 必须先于公式类：
// 图片语法 ![](…) 与公式无关，但裸链接的域名片段不能进词级修复。
func freeze(md string, freezeInline bool) (string, map[string]string) {
	protect := map[string]string{}
	n := 0
	keep := func(s string) string {
		// 已含占位符的匹配（如 $$…$$ 夹住了代码围栏）不再冻结：
		// 嵌套冻结曾让恢复顺序错乱、占位符以字面量泄漏进正文
		if strings.ContainsRune(s, 0) {
			return s
		}
		n++
		key := fmt.Sprintf("\x00%d\x00", n)
		protect[key] = s
		return key
	}
	text := fenceRe.ReplaceAllStringFunc(md, keep)
	text = urlRe.ReplaceAllStringFunc(text, keep)
	text = dispRe.ReplaceAllStringFunc(text, keep)
	if freezeInline {
		text = inlineRe.ReplaceAllStringFunc(text, keep)
	}
	text = tagRe.ReplaceAllStringFunc(text, keep)
	return text, protect
}

// unfreeze 按键号从大到小恢复（后冻结的是外层）。map 随机序会把内层
// 占位符以字面量形式残留在正文里——V4 全库验证中 862 处正文变成
// \x00N\x00 乱码即此因；降序恢复时外层先还原，内层键随后必然被处理。
func unfreeze(text string, protect map[string]string) string {
	keys := make([]string, 0, len(protect))
	for k := range protect {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(a, b int) bool {
		return numOfKey(keys[a]) > numOfKey(keys[b])
	})
	for _, k := range keys {
		text = strings.ReplaceAll(text, k, protect[k])
	}
	return text
}

func numOfKey(k string) int {
	n := 0
	for _, c := range k {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	return n
}

// ---------------------------------------------------------------- 结构还原

// 表格与 <eq> 标签：MinerU 的 HTML 表格在 goldmark（unsafe 关闭）下被
// 静默丢弃，导出 PDF 时整张表消失——75 篇样本中 11 篇中招。转成
// Markdown 管道表格 + KaTeX 公式即可在现有渲染管线里活下来。
var (
	tableRe  = regexp.MustCompile(`(?s)<table[^>]*>.*?</table>`)
	trRe     = regexp.MustCompile(`(?si)<tr[^>]*>(.*?)</tr>`)
	cellRe   = regexp.MustCompile(`(?si)<t([dh])[^>]*>(.*?)</t[dh]>`)
	stripRe  = regexp.MustCompile(`(?si)</?(?:b|i|u|em|strong|sup|sub|small|center|font)[^>]*>`)
	entities = strings.NewReplacer(
		"&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`,
		"&#39;", "'", "&apos;", "'", "&nbsp;", " ",
	)
	cellWs = regexp.MustCompile(`\s+`)
)

// tablesToMarkdown 把文档内全部 HTML 表格转成 Markdown 管道表格。
// 单元格内 <eq>X</eq> → $X$；rowspan/colspan 按标准网格算法展开
// （跨行/跨列用重复/空单元格近似，保数据可见性）；单元格内竖线转义。
func tablesToMarkdown(md string, st *Stats) string {
	return tableRe.ReplaceAllStringFunc(md, func(block string) string {
		grid := expandGrid(block)
		if len(grid) == 0 || len(grid[0]) == 0 {
			return block
		}
		var b strings.Builder
		for r, row := range grid {
			b.WriteString("|")
			for _, c := range row {
				b.WriteString(" " + c + " |")
			}
			b.WriteString("\n")
			if r == 0 {
				b.WriteString("|")
				for range row {
					b.WriteString(" --- |")
				}
				b.WriteString("\n")
			}
		}
		st.Tables++
		st.detail(fmt.Sprintf("HTML 表格 → Markdown 表格（%d 行 × %d 列）", len(grid), len(grid[0])))
		return b.String()
	})
}

// expandGrid 解析表格块为文本网格（处理 rowspan/colspan）。
func expandGrid(block string) [][]string {
	type cell struct {
		text             string
		rowspan, colspan int
	}
	var rows [][]cell
	for _, rm := range trRe.FindAllStringSubmatch(block, -1) {
		var cs []cell
		for _, cm := range cellRe.FindAllStringSubmatch(rm[1], -1) {
			c := cell{text: cleanCell(cm[2])}
			c.rowspan = attrNum(cm[0], "rowspan")
			c.colspan = attrNum(cm[0], "colspan")
			cs = append(cs, c)
		}
		rows = append(rows, cs)
	}
	if len(rows) == 0 {
		return nil
	}
	nCols := 0
	for _, cs := range rows {
		w := 0
		for _, c := range cs {
			w += c.colspan
		}
		if w > nCols {
			nCols = w
		}
	}
	if nCols == 0 {
		return nil
	}
	grid := [][]string{}
	used := map[[2]int]bool{}
	for r, cs := range rows {
		if r >= len(grid) {
			grid = append(grid, make([]string, nCols))
		}
		c := 0
		for _, cell := range cs {
			for c < nCols && used[[2]int{r, c}] {
				c++
			}
			if c >= nCols {
				break
			}
			for i := 0; i < cell.rowspan; i++ {
				for j := 0; j < cell.colspan; j++ {
					rr, cc := r+i, c+j
					for rr >= len(grid) {
						grid = append(grid, make([]string, nCols))
					}
					if cc < nCols && !used[[2]int{rr, cc}] {
						used[[2]int{rr, cc}] = true
						// 跨行/跨列以重复内容近似，保持表格自含可读
						grid[rr][cc] = cell.text
					}
				}
			}
			c += cell.colspan
		}
	}
	return grid
}

func attrNum(s, name string) int {
	re := regexp.MustCompile(`(?i)` + name + `\s*=\s*["']?(\d+)`)
	m := re.FindStringSubmatch(s)
	if m == nil {
		return 1
	}
	n := 1
	fmt.Sscanf(m[1], "%d", &n)
	if n < 1 {
		return 1
	}
	if n > 64 {
		return 64
	}
	return n
}

// cleanCell 单元格文本化：<eq>X</eq>→$X$，剥内联标签，解实体，转义竖线。
func cleanCell(s string) string {
	s = eqInnerRe.ReplaceAllStringFunc(s, func(m string) string {
		return "$" + eqInnerRe.FindStringSubmatch(m)[1] + "$"
	})
	s = stripRe.ReplaceAllString(s, "")
	s = entities.Replace(s)
	s = cellWs.ReplaceAllString(strings.TrimSpace(s), " ")
	s = strings.ReplaceAll(s, "|", `\|`)
	return s
}

// eqInnerRe MinerU 用 <eq>X</eq> 包 LaTeX；渲染管线只认 $…$。
var eqInnerRe = regexp.MustCompile(`(?s)<eq>(.*?)</eq>`)

// eqToMath 把表格之外残留的 <eq>（图题、正文碎表）一并转为 $…$。
func eqToMath(md string, st *Stats) string {
	n := 0
	res := eqInnerRe.ReplaceAllStringFunc(md, func(m string) string {
		n++
		return "$" + eqInnerRe.FindStringSubmatch(m)[1] + "$"
	})
	if n > 0 {
		st.Syms += n
		st.detail(fmt.Sprintf("<eq> 标签 → $…$ 公式 ×%d", n))
	}
	return res
}

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

// ---------------------------------------------------------------- 控制码

// fixControlChars 把 MinerU 输出里的控制码（\x00–\x1F，排除制表/换行）
// 按 PDF 文本层锚定还原：OT1 编码里 \x03=Λ、\x08=Φ 等（"three-level Λ
// system" 在 MD 里是 "three-level \x03 system"）。取损伤处前后各 2 个词
// 在原文中定位间隙字符；间隙是字母则回填，原文该处为空则删除，其余
// （含 NUL）找不到锚时保持原样并记录。
func fixControlChars(md, orig string, st *Stats) string {
	ctrlRe := regexp.MustCompile(`[\x00-\x08\x0b\x0c\x0e-\x1f]`)
	locs := ctrlRe.FindAllStringIndex(md, -1)
	if len(locs) == 0 {
		return md
	}
	// 从后往前替换避免位移
	type repl struct {
		start, end int
		text       string
		note       string
	}
	var repls []repl
	for _, l := range locs {
		before := contextWords(md[:l[0]], false, 2)
		after := contextWords(md[l[1]:], true, 2)
		if len(before)+len(after) == 0 {
			continue
		}
		mid, n := controlAnchor(orig, before, after)
		switch {
		case n == 1:
			repls = append(repls, repl{l[0], l[1], mid,
				fmt.Sprintf("控制码 U+%04X → %q", md[l[0]], mid)})
		case n == 0:
			repls = append(repls, repl{l[0], l[1], "",
				fmt.Sprintf("控制码 U+%04X 删除（原文该处无字符）", md[l[0]])})
		default:
			st.detail(fmt.Sprintf("跳过: 控制码 U+%04X（原文锚 %v|%v 命中 %d 处）",
				md[l[0]], before, after, n))
		}
	}
	for i := len(repls) - 1; i >= 0; i-- {
		r := repls[i]
		md = md[:r.start] + r.text + md[r.end:]
		st.Ctrl++
		st.detail(r.note)
	}
	return md
}

// controlAnchor 在原文中找 before 词与 after 词夹出的间隙字符。
// 返回间隙内容（可能为空串）与唯一性（-1=未命中，0=唯一且为空，
// 1=唯一字符，>1=多义）。
func controlAnchor(orig string, before, after []string) (string, int) {
	sep := `[^\p{L}]+` // 词间分隔：任何非字母（连字符、空格等），不得吞掉希腊字母
	var b strings.Builder
	for _, w := range before {
		b.WriteString(regexp.QuoteMeta(w) + sep)
	}
	b.WriteString(`([^\s]+)?\s*`)
	for _, w := range after {
		b.WriteString(regexp.QuoteMeta(w))
		if w != after[len(after)-1] {
			b.WriteString(sep)
		}
	}
	re, err := regexp.Compile(b.String())
	if err != nil {
		return "", -1
	}
	ms := re.FindAllStringSubmatch(orig, -1)
	if len(ms) == 0 {
		return "", -1
	}
	mid := ""
	uniq := true
	for _, m := range ms {
		v := strings.TrimSpace(m[1])
		if mid == "" {
			mid = v
		} else if mid != v {
			uniq = false
		}
	}
	if !uniq {
		return "", 2
	}
	if mid == "" {
		return "", 0
	}
	// 间隙必须是单一字母类字符（希腊/拉丁等），多字符或含符号不碰
	rs := []rune(mid)
	if len(rs) != 1 || !unicode.IsLetter(rs[0]) {
		return "", 2
	}
	return mid, 1
}

// ---------------------------------------------------------------- 局部锚定

// localContextRepair 全局对齐漂移区的兜底：仅处理 anchorRepair 标记为
// suspect 的词（对齐失锁/跳步时略过的 MD 词）。W 的前词+后词若在原文中
// 夹出唯一中间词 C（C∈词表、编辑距离≤2、C≠W），替换 W。
// 不做全文扫描——夹缝 bigram 在健康区太弱（"the X of" 类搭配遍布全文），
// 无差别放开会把 the→this、coherent→incoherent 这类合法词改掉
// （首版全库验证误伤 1.9 万处即此因）。
func localContextRepair(text, orig string, vocab map[string]struct{}, st *Stats, suspect map[int]bool) string {
	locs := alignWordRe.FindAllStringIndex(text, -1)
	if len(locs) < 3 {
		return text
	}
	orgTok := alignTokens(orig)
	// 原文 "prev * next" 夹缝索引
	gap := map[string][]string{}
	for k := 1; k+1 < len(orgTok); k++ {
		key := orgTok[k-1] + "\x00" + orgTok[k+1]
		gap[key] = append(gap[key], orgTok[k])
	}
	tok := make([]string, len(locs))
	for k, l := range locs {
		tok[k] = strings.ToLower(text[l[0]:l[1]])
	}
	rep := map[int]string{}
	for i := 1; i+1 < len(tok); i++ {
		if !suspect[i] {
			continue
		}
		w := tok[i]
		if len(w) < 2 {
			continue
		}
		cands := gap[tok[i-1]+"\x00"+tok[i+1]]
		cand, amb := "", false
		for _, c := range cands {
			if c == w || len(c) < 2 {
				continue
			}
			if _, ok := vocab[c]; !ok || !distLE2(w, c) {
				continue
			}
			// 功能词闸与最短闸：漂移区里 "the→this"、"and→qd" 类误配
			// 全靠这两条拦下；of→off 这类真损伤（功能词 W、编辑距离 1、
			// 候选 ≥4 字母且非功能词）放行
			if _, cFunc := functionWords[c]; cFunc || len(c) < 3 {
				continue
			}
			if _, wFunc := functionWords[w]; wFunc && dist(w, c) != 1 {
				continue
			}
			if cand == "" {
				cand = c
			} else if cand != c {
				amb = true
				break
			}
		}
		if cand != "" && !amb {
			rep[i] = cand
		}
	}
	// 对称互改对互斥：同文档里既有 w→c 又有 c→w（coherent↔incoherent、
	// transition↔transitions），说明漂移区词序已乱，两向都放弃
	for i, c := range rep {
		for j, c2 := range rep {
			if i != j && c2 == tok[i] && c == tok[j] {
				delete(rep, i)
				delete(rep, j)
			}
		}
	}
	if len(rep) == 0 {
		return text
	}
	var out strings.Builder
	last := 0
	for k, l := range locs {
		r, ok := rep[k]
		if !ok {
			continue
		}
		word := text[l[0]:l[1]]
		out.WriteString(text[last:l[0]])
		out.WriteString(applyCase(word, r))
		last = l[1]
		st.Ctx++
		st.detail(word + " → " + r + "（局部锚定）")
	}
	out.WriteString(text[last:])
	return out.String()
}

// dist 编辑距离恰好为 1（含长度差 1 的插入/删除与等长替换）。
func dist(a, b string) int {
	la, lb := len(a), len(b)
	if la == lb {
		d := 0
		for k := 0; k < la; k++ {
			if a[k] != b[k] {
				d++
			}
		}
		return d
	}
	if la > lb {
		a, b, la, lb = b, a, lb, la
	}
	// lb = la+1：双指针一次判插入
	i, j := 0, 0
	skipped := false
	for i < la && j < lb {
		if a[i] == b[j] {
			i++
			j++
			continue
		}
		if skipped {
			return 2
		}
		skipped = true
		j++
	}
	if !skipped {
		return 1
	}
	if la-i <= lb-j {
		return 1
	}
	return 2
}

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

// ---------------------------------------------------------------- 上下文锚定

// alignWordRe 上下文对齐用分词：与原文分词粒度一致（含单字母词），
// 保证两侧 token 流平行（单字母词丢失会造成持续错位）。
var alignWordRe = regexp.MustCompile(`[A-Za-z]+`)

// anchorRepair 真词损伤的上下文锚定替换。MD 与原文 token 序列逐词对齐，
// 分歧块为"MD 单词 ↔ 原文块"时，取块内唯一的词表候选 C（C≠W、编辑距离
// ≤2）替换；多个候选或无候选一律不动。公式占位符是纯数字键，不会进入
// MD token 流；原文中公式位置的字母碎片（如 χ=i(β…) 留下的 i/B/A）会在
// 分歧块内因非词表或非唯一而不参与替换。
// 同时返回 suspect 集：对齐失锁/跳步区间里被略过的 MD 词下标——这些词
// 未获得全局锚定的保护，交给 localContextRepair 兜底。
func anchorRepair(text, orig string, vocab map[string]struct{}, st *Stats) (string, map[int]bool) {
	locs := alignWordRe.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		return text, nil
	}
	mdTok := make([]string, len(locs))
	for k, l := range locs {
		mdTok[k] = strings.ToLower(text[l[0]:l[1]])
	}
	orgTok := alignTokens(orig)

	rep := map[int]string{} // MD 词下标 → 替换词（小写）
	suspect := map[int]bool{}
	markSuspect := func(from, to int) { // [from, to) MD 词下标
		for k := from; k < to && k < len(mdTok); k++ {
			suspect[k] = true
		}
	}
	i, j := 0, 0
	for i < len(mdTok) && j < len(orgTok) {
		if mdTok[i] == orgTok[j] {
			i++
			j++
			continue
		}
		a, b := resync(mdTok, orgTok, i, j)
		if a < 0 {
			// 大幅错位（MinerU 重排/删段/图片移位）：大窗口向前找下一个
			// 对齐点。单侧跳步（a=0 或 b=0）不产生替换，安全。
			ni, nj, ok := wideSync(mdTok, orgTok, i, j)
			if !ok {
				markSuspect(i, i+1) // 真无法重同步：双侧各让一步防卡死
				i++
				j++
				continue
			}
			markSuspect(i, ni) // 跳步略过的 MD 词未获全局锚定保护
			i, j = ni, nj
			continue
		}
		if a > 1 {
			markSuspect(i+1, i+a) // 分歧块内未被候选替换逻辑覆盖的 MD 词
		}
		if a == 1 && len(mdTok[i]) >= 2 {
			w := mdTok[i]
			cand, ambiguous := "", false
			for _, c := range orgTok[j : j+b] {
				if c == w || len(c) < 2 || blockedTarget(c) {
					continue
				}
				if _, ok := vocab[c]; !ok || !distLE2(w, c) {
					continue
				}
				if cand == "" {
					cand = c
				} else if cand != c {
					ambiguous = true
					break
				}
			}
			if cand != "" && !ambiguous {
				rep[i] = cand
			}
		}
		i += a
		j += b
	}
	if len(rep) == 0 {
		return text, suspect
	}
	var out strings.Builder
	last := 0
	for k, l := range locs {
		r, ok := rep[k]
		if !ok {
			continue
		}
		word := text[l[0]:l[1]]
		out.WriteString(text[last:l[0]])
		out.WriteString(applyCase(word, r))
		last = l[1]
		st.Ctx++
		st.detail(word + " → " + applyCase(word, r))
	}
	out.WriteString(text[last:])
	return out.String(), suspect
}

// resync 在分歧处向前窗口搜索重同步点：MD 前进 a、原文前进 b 后两序列
// 再次对齐。a/b 各限 8，按总步数从小到大试；同步数内先试两侧都前进
// （真词损伤是 1:1 替换，平衡对齐优先），再试单侧跳步（公式碎片在原文
// 留下的多余 token），否则受损词会抢先对齐到原文后文的同形词上。
// 找不到返回 -1。
func resync(md, org []string, i, j int) (int, int) {
	for d := 1; d <= 16; d++ {
		for a := 1; a < d && a <= 8; a++ {
			b := d - a
			if b > 8 {
				continue
			}
			si, sj := i+a, j+b
			if si < len(md) && sj < len(org) && md[si] == org[sj] && agreed(md, org, si, sj) {
				return a, b
			}
		}
		for _, ab := range [][2]int{{0, d}, {d, 0}} {
			if ab[0] > 8 || ab[1] > 8 {
				continue
			}
			si, sj := i+ab[0], j+ab[1]
			if si < len(md) && sj < len(org) && md[si] == org[sj] && agreed(md, org, si, sj) {
				return ab[0], ab[1]
			}
		}
	}
	return -1, -1
}

// wideSync 大窗口重同步：小窗口失败后，向前 ≤300 token 找 MD 当前词或
// 原文当前词的再次出现。跳步窗口大，只接受严格的双 token 确认（或序列
// 末尾）——弱确认会把漂移区锁到同形词的错误出现位置上。
// 跳过的纯单侧 token 块不参与替换。
func wideSync(md, org []string, i, j int) (int, int, bool) {
	for jj := j + 1; jj < len(org) && jj <= j+300; jj++ {
		if org[jj] == md[i] && agreedStrict(md, org, i, jj) {
			return i, jj, true
		}
	}
	for ii := i + 1; ii < len(md) && ii <= i+300; ii++ {
		if md[ii] == org[j] && agreedStrict(md, org, ii, j) {
			return ii, j, true
		}
	}
	return i, j, false
}

// agreed 对齐确认（resync 窗口 ≤8 用）：连续命中 2 个 token；或已到双
// 序列末尾；或 ≥5 字母的强锚词可单 token 确认。
func agreed(md, org []string, i, j int) bool {
	two := i+1 < len(md) && j+1 < len(org) && md[i+1] == org[j+1]
	eof := i == len(md)-1 && j == len(org)-1
	return two || eof || len(md[i]) >= 5
}

// agreedStrict 同步确认的严格口径：连续命中 2 个 token，或已到双序列
// 末尾（供 wideSync 大跳步使用）。
func agreedStrict(md, org []string, i, j int) bool {
	two := i+1 < len(md) && j+1 < len(org) && md[i+1] == org[j+1]
	eof := i == len(md)-1 && j == len(org)-1
	return two || eof
}

// alignTokens 原文分词（小写）：先去行尾连字符（与 BuildVocab 同规则），
// 再按非字母切分，保留单字母 token 以对齐公式碎片。
func alignTokens(orig string) []string {
	var toks []string
	for _, t := range tokenSplit.Split(dehyphenate(orig), -1) {
		if t != "" {
			toks = append(toks, strings.ToLower(t))
		}
	}
	return toks
}

// distLE2 编辑距离 ≤2（两行 DP，行最小值提前退出）。
func distLE2(a, b string) bool {
	if a == b {
		return false
	}
	la, lb := len(a), len(b)
	d := la - lb
	if d < 0 {
		d = -d
	}
	if d > 2 {
		return false
	}
	prev := make([]int, lb+1)
	cur := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		cur[0] = i
		rowMin := cur[0]
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			v := prev[j] + 1
			if u := cur[j-1] + 1; u < v {
				v = u
			}
			if u := prev[j-1] + cost; u < v {
				v = u
			}
			cur[j] = v
			if v < rowMin {
				rowMin = v
			}
		}
		if rowMin > 2 {
			return false
		}
		prev, cur = cur, prev
	}
	return prev[lb] <= 2
}

// ---------------------------------------------------------------- 拼写校准

// repairProse 逐词处理。合并规则需要看前一个输出词，因此一趟扫完。
func repairProse(text string, vocab map[string]struct{}, st *Stats) string {
	locs := wordRe.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		return text
	}

	words := make([]string, len(locs))
	for i, l := range locs {
		words[i] = text[l[0]:l[1]]
	}
	lower := make([]string, len(words))
	for i, w := range words {
		lower[i] = strings.ToLower(w)
	}

	// out[i]: nil=未动；"\x00DEL\x00"=已并入前词；其他=替换文本
	out := make([]string, len(words))
	for i := 0; i < len(words); i++ {
		if _, ok := vocab[lower[i]]; ok {
			continue // 原样
		}
		// 1) 与前一输出词合并
		//   a. 两者都不在词表、拼接成词（configura+tion→configuration）
		//   b. 前词在词表、本词是常见后缀片段且拼接成词（result+ing→resulting）
		//   c. 本词 ≥3 字母且拼接成词（the+orem→theorem、of+fers→offers）
		//   d. ff 连字损伤：前词以 f 结尾、拼接差一个 f（ef+ects→ef+f+ects
		//      =effects）——MinerU 把 ﬀ 连字读成 "f " 时产生这种形态
		//   无需原文 bigram 兜底：cur 若以独立词出现在原文，必在词表、
		//   进不了本分支（合并只处理 cur ∉ 词表的碎片）
		if i > 0 {
			prev := lower[i-1]
			prevFixed := out[i-1]
			if prevFixed != "" && prevFixed != "\x00DEL\x00" {
				prev = strings.ToLower(prevFixed)
			}
			if prevFixed != "\x00DEL\x00" {
				_, prevIsWord := vocab[prev]
				_, curIsWord := vocab[lower[i]]
				if !curIsWord {
					join := prev + lower[i]
					if _, ok := vocab[join]; !ok && strings.HasSuffix(prev, "f") {
						// ff 损伤：拼接差一个 f，补上再试
						if _, ok := vocab[prev+"f"+lower[i]]; ok {
							join = prev + "f" + lower[i]
						}
					}
					if _, ok := vocab[join]; ok {
						if !prevIsWord || isCommonSuffix(lower[i]) || len(lower[i]) >= 3 {
							base := prevFixed
							if base == "" {
								base = words[i-1]
							}
							out[i-1] = applyCase(base, join)
							out[i] = "\x00DEL\x00"
							st.Merged++
							continue
						}
					}
				}
			}
		}
		// 2) 一词拆二
		if len(lower[i]) >= 5 {
			for k := 2; k <= len(lower[i])-2; k++ {
				a, b := lower[i][:k], lower[i][k:]
				if _, ok := vocab[a]; ok {
					if _, ok := vocab[b]; ok {
						out[i] = applyCase(words[i], a+" "+b)
						st.Split++
						break
					}
				}
			}
			if out[i] != "" {
				continue
			}
		}
		// 3) 编辑距离 ≤1 且候选唯一。≥3 字母才适用：两字母碎片（如
		//    "ef"）的邻词太多，替换纯属猜，交给合并规则处理整对
		if len(lower[i]) >= 3 {
			cands := edit1Candidates(lower[i], vocab)
			if len(cands) == 1 && blockedTarget(cands[0]) {
				cands = nil // 高频短词不做目标（如 off→of 的错修）
			}
			switch len(cands) {
			case 1:
				out[i] = applyCase(words[i], cands[0])
				st.Fixed++
				st.detail(words[i] + " → " + out[i])
			case 0:
				// 无候选，原样
			default:
				st.Kept = append(st.Kept, lower[i])
			}
		}
	}

	// 回写文本（从后往前替换避免位移）
	var b strings.Builder
	last := 0
	for i, l := range locs {
		seg := text[last:l[0]]
		if out[i] == "\x00DEL\x00" && i > 0 {
			// 并入前词：吃掉与前词之间的一个分隔符，避免双空格
			if s := text[locs[i-1][1]:l[0]]; len(s) > 0 && strings.TrimSpace(s) == "" {
				seg = seg[:len(seg)-1]
			}
		}
		b.WriteString(seg)
		switch rep := out[i]; {
		case rep == "\x00DEL\x00":
			// 词已并入前词
		case rep == "":
			b.WriteString(words[i]) // 未动
		default:
			b.WriteString(rep)
		}
		last = l[1]
	}
	b.WriteString(text[last:])
	return b.String()
}

// edit1Candidates 返回词表中与 w 编辑距离恰好为 1 的全部候选。
func edit1Candidates(w string, vocab map[string]struct{}) []string {
	var cands []string
	for v := range vocab {
		if editDistanceLE1(w, v) {
			cands = append(cands, v)
		}
	}
	sort.Strings(cands)
	return cands
}

// editDistanceLE1 判断 a→b 是否只需一次插入/删除/替换（O(min) 带 early-exit）。
func editDistanceLE1(a, b string) bool {
	if a == b {
		return false // 距离 0 不算
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	if len(b)-len(a) > 1 {
		return false
	}
	i, j, diff := 0, 0, 0
	for i < len(a) && j < len(b) {
		if a[i] == b[j] {
			i++
			j++
			continue
		}
		if diff++; diff > 1 {
			return false
		}
		if len(a) == len(b) {
			i++
			j++
		} else {
			j++ // 在 b 中跳过一个字符（插入）
		}
	}
	return true // 尾部剩余 ≤1 已由 diff 计入
}

// commonSuffix 常见词缀片段：MinerU 偶尔把 "resulting" 断成 "result ing"。
// 拼接成词且片段是这些后缀之一时才允许合并，防止 "the re"→"there" 类误伤。
var commonSuffix = map[string]struct{}{
	"tion": {}, "sion": {}, "ing": {}, "ed": {}, "er": {}, "ers": {}, "est": {},
	"ment": {}, "ness": {}, "ture": {}, "able": {}, "ible": {}, "ity": {},
	"ive": {}, "ous": {}, "ful": {}, "less": {}, "ally": {}, "ities": {},
}

func isCommonSuffix(w string) bool {
	_, ok := commonSuffix[w]
	return ok
}

// targetBlock 禁作替换目标的高频短词：把别的词改成这些词几乎必然是错修
// （off→of：断行拼接 offdiagonal 使 off 落词表，编辑距离 1 唯一候选 of）。
// 词表法无法分辨，只能在目标侧封死。
var targetBlock = map[string]struct{}{
	"of": {}, "to": {}, "in": {}, "on": {}, "at": {}, "by": {}, "as": {},
	"is": {}, "it": {}, "an": {}, "a": {}, "or": {}, "if": {}, "be": {},
	"we": {}, "no": {}, "so": {}, "do": {}, "up": {}, "us": {}, "am": {},
	"me": {}, "my": {}, "he": {},
}

func blockedTarget(w string) bool {
	_, ok := targetBlock[w]
	return ok
}

// applyCase 把替换词套用原词的首字母大小写。
func applyCase(orig, repl string) string {
	if repl == "" {
		return repl
	}
	if len(orig) > 0 && orig[0] >= 'A' && orig[0] <= 'Z' {
		return strings.ToUpper(repl[:1]) + repl[1:]
	}
	return repl
}
