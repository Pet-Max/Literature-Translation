package render

import (
	"regexp"
	"sort"
	"strings"
)

// texfix.go — KaTeX 渲染前的 TeX 窄清洗：损伤公式能机械修复则修复，
// 修不了的保持原样（渲染端降级红字显示），不猜测作者意图。
//
// 每条规则必须过三道准入门（缺一不可，texfix_test.go 逐条锁定）：
//  1. 恒等性：KaTeX 可渲染的公式必须逐字节原样返回；
//  2. 等价性：只做 KaTeX 支持矩阵的等价映射、LaTeX 语法有效性最小修复、
//     KaTeX 硬限制适配。目标不唯一的不修——例如生造词 \equivq 可能是
//     \equiv 或 "\equiv q" 的 OCR 粘连，映射会丢字符，只能降级；
//  3. 回归守门：通用边界与常见语法陷阱均须由单测与模糊测试锁定
//     （\rightarrow 分词、嵌套环境、\left.、\tag{…} 内嵌花括号、环境内外的 &）。
//
// 实现不变量：
//   - 命令必须整词分词：\rightarrow 若被当作 \right + 定界符会误伤合法公式；
//   - 前导反斜杠奇偶区分：\\ 为换行符，\\tag / \\normalfont 不得误判为命令；
//   - 定界符捕获按 控制词 > 控制符号 > 单字符 顺序，\left\{ 的 \{
//     要整体捕获，且未闭合 \left/\right 不得消费后续结构命令；
//   - 剥除空转或未闭合命令时，若前后紧邻 ASCII 字母须保留空格分隔防拼接成新命令；
//   - 所有修补走「编辑列表 + 倒序防重叠应用」，扫描期坐标与改写期坐标同源；
//   - 花括号失衡与复杂数组行结构损伤不在修复范围，直接交由渲染端降级显示。

// TexFixStats 记清洗规则的命中明细（键=规则名，值=命中次数；一条公式
// 可同时命中多条规则，次数与受影响公式数是两个口径）。
type TexFixStats map[string]int

// 同义映射：LaTeX 标准命令，KaTeX 未实现且目标唯一。扩展条目必须先在
// 目标 KaTeX 版本上验证为真 Undefined（与"缺参数"类报错区分），再补充对应单测锁定。
var texSynonymCmds = map[string]string{
	"hdots":         "dots",
	"displaylimits": "limits",
}

// 数学模式内语义空转的命令：不携带字形内容，剥除零视觉损失
// （KaTeX 均未实现，裸用必抛 ParseError）。
var texNullCmds = map[string]bool{
	"normalfont": true,
	"noindent":   true,
	"indent":     true,
	"medskip":    true,
	"bigskip":    true,
	"smallskip":  true,
}

// texCmdRe 整词匹配控制序列：[A-Za-z]+ 贪婪吞满字母，\hdotsline 不会
// 被误拆成 \hdots。
var texCmdRe = regexp.MustCompile(`\\([A-Za-z]+)`)

// texTokenRe 公式分词：控制词（\left、\rightarrow）> 控制符号（\&、\{）
// > 普通单字符。{ 不参与分词——环境的花括号参数由 matchBrace 现场读取。
var texTokenRe = regexp.MustCompile(`\\[A-Za-z]+|\\.|[^\\\s{]`)

// texDelimRe 定界符捕获：控制词（\langle）> 控制符号（\{）> 单字符（( | .）。
var texDelimRe = regexp.MustCompile(`\\[A-Za-z]+|\\.|.`)

// texFixer 聚合一次排版构建内全部公式的清洗命中。nil 与零值安全：
// 不关心统计的调用方传 nil 或零值结构体，清洗照常执行。
type texFixer struct {
	stats    TexFixStats // 规则命中明细（次数口径）
	affected int         // 命中过至少一条规则的公式数（公式口径）
}

func (f *texFixer) fix(tex string) string {
	out, stats := sanitizeTex(tex)
	if f != nil {
		if len(stats) > 0 {
			f.affected++
			if f.stats == nil {
				f.stats = TexFixStats{}
			}
		}
		for k, v := range stats {
			f.stats[k] += v
		}
	}
	return out
}

// sanitizeTex 清洗主入口。三步流水：命令级改写 → 多 \tag 收敛 →
// 结构配对修补（编辑列表统一应用）；迭代至不动点以消除级联暴露（如 \end&{} → \end{} → ""），保证单次调用即幂等。
func sanitizeTex(tex string) (string, TexFixStats) {
	stats := TexFixStats{}
	for pass := 0; pass < 64; pass++ {
		next := rewriteKnownCmds(tex, stats)
		next = keepFirstTag(next, stats)
		next = fixStructure(next, stats)
		if next == tex {
			break
		}
		tex = next
	}
	return tex, stats
}

// rewriteKnownCmds 应用同义映射与空转命令剥除。
func rewriteKnownCmds(tex string, stats TexFixStats) string {
	locs := texCmdRe.FindAllStringIndex(tex, -1)
	if len(locs) == 0 {
		return tex
	}
	var out strings.Builder
	out.Grow(len(tex))
	pos := 0
	needSepAfterStrip := false
	for _, l := range locs {
		if l[0] > pos {
			if needSepAfterStrip && out.Len() > 0 && isASCIILetterByte(out.String()[out.Len()-1]) && isASCIILetterByte(tex[pos]) {
				out.WriteByte(' ')
			}
			out.WriteString(tex[pos:l[0]])
			needSepAfterStrip = false
		}
		m := tex[l[0]:l[1]]
		if precedingBackslashes(tex, l[0])%2 == 1 {
			// 前紧邻 \\ 转义换行，此处为 \\ 后接普通字母而非独立 TeX 命令
			out.WriteString(m)
			needSepAfterStrip = false
			pos = l[1]
			continue
		}
		name := m[1:]
		if rep, ok := texSynonymCmds[name]; ok {
			stats["映射 \\"+name+"→\\"+rep]++
			out.WriteByte('\\')
			out.WriteString(rep)
			needSepAfterStrip = false
		} else if texNullCmds[name] {
			stats["剥除 \\"+name]++
			needSepAfterStrip = true
		} else {
			out.WriteString(m)
			needSepAfterStrip = false
		}
		pos = l[1]
	}
	if pos < len(tex) {
		if needSepAfterStrip && out.Len() > 0 && isASCIILetterByte(out.String()[out.Len()-1]) && isASCIILetterByte(tex[pos]) {
			out.WriteByte(' ')
		}
		out.WriteString(tex[pos:])
	}
	return out.String()
}

// matchBrace 返回 tex 中 i 处 '{' 开头花括号组的结束位置（'}' 之后）。
// 不平衡返回 -1。转义括号 \{ \} 原样计数——成对出现不影响平衡，与
// nestedMathDelimRe 对 \tag 参数的既有取舍同源。
func matchBrace(tex string, i int) int {
	depth := 0
	for j := i; j < len(tex); j++ {
		switch tex[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return j + 1
			}
		}
	}
	return -1
}

// keepFirstTag 收敛多 \tag：KaTeX 每个公式至多一个 \tag，MinerU 常把
// 多行编号公式组合并进一个 $$ 块，各行的 \tag{…} 叠在一起。保留首个
// 编号（确定性取舍，其余编号本已因合并丢失行列语义），其余连参数剥除。
func keepFirstTag(tex string, stats TexFixStats) string {
	// \tag 必须整词：\tagx 不是 \tag
	locs := texTagWordRe.FindAllStringSubmatchIndex(tex, -1)
	var tags []int
	for _, loc := range locs {
		if loc[2] == loc[3] && precedingBackslashes(tex, loc[0])%2 == 0 { // 后随非字母且前导反斜杠为偶数，才是真 \tag
			tags = append(tags, loc[0])
		}
	}
	if len(tags) <= 1 {
		return tex
	}
	var b strings.Builder
	prev := 0
	for n, start := range tags {
		if start < prev {
			// 首个 \tag 的花括号参数被整体保留时已覆盖此处（参数内嵌套
			// \tag 的形态）：内层 tag 随参数保留，公式仍多 tag，交由
			// 渲染降级；直接写入会切片反序 panic
			continue
		}
		j := start + len(`\tag`)
		for j < len(tex) && (tex[j] == ' ' || tex[j] == '\t') {
			j++
		}
		hasArg := j < len(tex) && tex[j] == '{'
		argEnd := -1
		if hasArg {
			argEnd = matchBrace(tex, j)
		}
		if n == 0 {
			// 首个 \tag 原样保留（含参数）；无参数形式只保留 \tag 本身
			keepTo := start + len(`\tag`)
			if hasArg && argEnd > 0 {
				keepTo = argEnd
			}
			b.WriteString(tex[prev:keepTo])
			prev = keepTo
			continue
		}
		// 其余连参数剥除；无参数形式只删 \tag 四字符
		b.WriteString(tex[prev:start])
		if hasArg && argEnd > 0 {
			prev = argEnd
		} else {
			prev = j
		}
	}
	b.WriteString(tex[prev:])
	out := b.String()
	if out != tex {
		stats["多 \\tag 只留首个"]++
	}
	return out
}

// texTagWordRe 匹配 \tag 及其后随字母（用于整词判定）；第二组捕获后随
// 字母，非空说明是 \tagx 类别的其他命令。
var texTagWordRe = regexp.MustCompile(`\\tag([A-Za-z]*)`)

// skipSpace 返回 tex 中 i 之后跳过空白的首个下标。
func skipSpace(tex string, i int) int {
	for i < len(tex) && (tex[i] == ' ' || tex[i] == '\t' || tex[i] == '\n' || tex[i] == '\r') {
		i++
	}
	return i
}

// fixStructure 结构配对修补：环境名对齐、孤立 \end、未配对 \left/\right、
// 环境外的孤立 &。全部修补进编辑列表，倒序统一应用。
func fixStructure(tex string, stats TexFixStats) string {
	type edit struct {
		s, e int
		r    string
	}
	var edits []edit
	var envStack []string
	var leftStack []int

	tokens := texTokenRe.FindAllStringIndex(tex, -1)
	// 已被 \begin/\end 花括号参数消费的区间：内部 token 不再处理。
	// 否则 \end{\right.array} 这类乱码参数里的 \right 会被当成孤立
	// \right 再修补一次，两处编辑重叠、倒序应用时坐标失效。
	consumedUntil := -1
	for _, loc := range tokens {
		if loc[0] < consumedUntil {
			continue
		}
		t := tex[loc[0]:loc[1]]
		switch {
		case t == `\begin` || t == `\end`:
			j := skipSpace(tex, loc[1])
			if j >= len(tex) || tex[j] != '{' {
				continue
			}
			e := matchBrace(tex, j)
			if e < 0 {
				continue
			}
			consumedUntil = e
			name := strings.TrimSpace(tex[j+1 : e-1])
			if t == `\begin` {
				envStack = append(envStack, name)
				continue
			}
			if len(envStack) == 0 {
				// 孤立 \end：无对应 \begin，整体剥除
				edits = append(edits, edit{loc[0], e, ""})
				stats["孤立 \\end 剥除"]++
				continue
			}
			top := envStack[len(envStack)-1]
			if name != top {
				// 环境名错配：对齐最内层打开环境（LaTeX 语法有效性），
				// 对齐后的 \end 语义上闭合该环境 → 出栈
				edits = append(edits, edit{loc[0], e, "\\end{" + top + "}"})
				envStack = envStack[:len(envStack)-1]
				stats["环境名对齐 \\end"]++
				continue
			}
			envStack = envStack[:len(envStack)-1]
		case t == `\left`:
			leftStack = append(leftStack, loc[0])
		case t == `\right`:
			j := skipSpace(tex, loc[1])
			delim := ""
			if m := texDelimRe.FindStringIndex(tex[j:]); m != nil {
				cand := tex[j+m[0] : j+m[1]]
				if !isStructuralTok(cand) {
					delim = cand
				}
			}
			if len(leftStack) > 0 {
				leftStack = leftStack[:len(leftStack)-1]
				continue
			}
			// 孤立 \right：剥命令保裸定界符；若后随另一结构命令则仅剥 \right 自身防重叠
			end := loc[1]
			if delim != "" {
				end = j + len(delim)
			}
			edits = append(edits, edit{loc[0], end, delim})
			stats["孤立 \\right 剥除"]++
		case t == `&`:
			if len(envStack) == 0 {
				// 环境外的 & 非法（KaTeX 实证抛错）：剥除。
				// \& 是控制符号，分词阶段已整体消费，不会走到这里。
				edits = append(edits, edit{loc[0], loc[1], ""})
				stats["孤立 & 剥除"]++
			}
		}
	}
	// 结束时仍未闭合的 \left：剥命令保裸定界符（保留可见括号，去掉
	// 配对语义——比补 \right. 稳，后者必须找准作用域闭合点）
	for _, p := range leftStack {
		j := skipSpace(tex, p+len(`\left`))
		delim := ""
		if m := texDelimRe.FindStringIndex(tex[j:]); m != nil {
			cand := tex[j+m[0] : j+m[1]]
			if !isStructuralTok(cand) {
				delim = cand
			}
		}
		end := p + len(`\left`)
		if delim != "" {
			end = j + len(delim)
		}
		edits = append(edits, edit{p, end, delim})
		stats["未闭合 \\left 剥除"]++
	}
	// 倒序应用（附加防重叠守卫，确保任何畸形输入均不会切片越界 panic）
	sort.Slice(edits, func(a, b int) bool {
		if edits[a].s != edits[b].s {
			return edits[a].s > edits[b].s
		}
		return edits[a].e > edits[b].e
	})
	lastStart := len(tex)
	for _, ed := range edits {
		if ed.s < 0 || ed.e < ed.s || ed.e > lastStart {
			continue
		}
		rep := ed.r
		// 防命令拼接：前面紧邻 ASCII 字母且替换为空（后随 ASCII 字母）或以 ASCII 字母开头时，保留空格分隔
		if ed.s > 0 && isASCIILetterByte(tex[ed.s-1]) {
			if (len(rep) > 0 && isASCIILetterByte(rep[0])) ||
				(len(rep) == 0 && ed.e < len(tex) && isASCIILetterByte(tex[ed.e])) {
				rep = " " + rep
			}
		}
		tex = tex[:ed.s] + rep + tex[ed.e:]
		lastStart = ed.s
	}
	return tex
}

// isASCIILetterByte 判断字节是否为 ASCII 英文字母（用于识别可能拼接为命令的边界）。
func isASCIILetterByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// precedingBackslashes 统计 s[idx] 之前紧邻连续反斜杠数量，用于区分 \\ 换行与 \cmd。
func precedingBackslashes(s string, idx int) int {
	n := 0
	for i := idx - 1; i >= 0 && s[i] == '\\'; i-- {
		n++
	}
	return n
}

// isStructuralTok 判断候选定界符是否为自身带结构语义的控制 token（避免被 \left/\right 吞并）。
func isStructuralTok(tok string) bool {
	switch tok {
	case `\left`, `\right`, `\begin`, `\end`, `&`, `{`, `}`:
		return true
	}
	return false
}
