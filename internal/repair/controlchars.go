// 本文件自 repair.go（v1.1.1 引擎校准域）按既有分段接缝切分而来，
// 逻辑与切分前逐行一致（同包纯移动，零语义变化）。

// 控制码还原：按 PDF 文本层锚定回填希腊字母等。
package repair

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

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
