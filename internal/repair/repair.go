// Package repair 用原 PDF 文本层词表校准 MinerU 解析 MD 的 OCR 拼写损伤。
//
// 背景：MinerU 云端（vlm 与 pipeline 皆然）对页面重新做文字识别，
// 衬线体 "ff" 常被误读为单个 f（different→diferent、diffusion→difusion），
// 行尾连字符偶尔合并不当（timedomain、one-toone、configura tion）。
// 原 PDF 的文本层是干净的，因此以它为词表：
//   - 词在词表中 → 保留；
//   - 词不在词表，但与相邻非词拼接后成词（configura+tion）→ 合并；
//   - 词不在词表，但可拆成两个词表词（timedomain→time domain）→ 拆分；
//   - 词不在词表，但词表中恰有一个编辑距离 ≤1 的候选（diferent→different）→ 替换；
//   - 其余不动并记录（避免误伤）。
//
// 公式 $…$、代码块、<img>/<table> 标签受保护，不做任何改写。
package repair

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Stats 是一次校准的统计。
type Stats struct {
	Fixed  int      // 编辑距离替换
	Merged int      // 相邻合并
	Split  int      // 一词拆二
	Kept   []string // 存疑未动的词（去重，供日志）
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

// functionWords 常见功能词：行尾连字符后跟功能词，几乎总是悬挂连字符
// （"short-\nand" 即 "short- and"），应保持两词分离；跟其他内容则是断词
// （"configura-\ntion"→configuration、"differ-\nent"→different），应接回。
var functionWords = map[string]struct{}{
	"and": {}, "or": {}, "the": {}, "of": {}, "to": {}, "in": {}, "a": {},
	"an": {}, "with": {}, "for": {}, "on": {}, "at": {}, "by": {}, "from": {},
	"is": {}, "are": {}, "was": {}, "were": {}, "not": {}, "but": {}, "as": {},
	"its": {}, "this": {}, "that": {}, "these": {}, "those": {},
}

// BuildVocab 从原文文本构建词表（小写，长度 ≥2）。
//
// 行尾连字符三分法：
//   - A 或 B 是功能词 → 悬挂连字符（"short-\nand"、"to-\none"），保持两词；
//   - 原文存在固体连字符形式 "A-B"（如 "time-domain"）→ 保持两词（可拆分修复）；
//   - 其余 → 断词拼接进词表（"multi-\ntude"→multitude，供合并修复）。
func BuildVocab(origText string) map[string]struct{} {
	processed := hyphenBreakRe.ReplaceAllStringFunc(origText, func(m string) string {
		p := hyphenBreakRe.FindStringSubmatch(m)
		_, aFunc := functionWords[p[1]]
		_, bFunc := functionWords[p[2]]
		if aFunc || bFunc {
			return p[1] + "- " + p[2]
		}
		if strings.Contains(origText, p[1]+"-"+p[2]) {
			return p[1] + "- " + p[2]
		}
		return p[1] + p[2]
	})
	vocab := make(map[string]struct{})
	for _, w := range tokenSplit.Split(strings.ToLower(processed), -1) {
		if len(w) >= 2 {
			vocab[w] = struct{}{}
		}
	}
	return vocab
}

// Apply 校准 mdPath 指向的 Markdown 文件（原地写回），origText 为原 PDF
// 文本层内容。返回统计。
func Apply(mdPath, origText string) (Stats, error) {
	md, err := os.ReadFile(mdPath)
	if err != nil {
		return Stats{}, fmt.Errorf("读取解析 MD 失败: %w", err)
	}
	res, err2 := Markdown(string(md), BuildVocab(origText))
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

// Markdown 校准 Markdown 文本并返回结果（供测试与 Apply 复用）。
func Markdown(md string, vocab map[string]struct{}) (Result, error) {
	// 保护公式/代码/标签
	protect := map[string]string{}
	n := 0
	keep := func(s string) string {
		n++
		key := fmt.Sprintf("\x00P%d\x00", n)
		protect[key] = s
		return key
	}
	text := fenceRe.ReplaceAllStringFunc(md, keep)
	text = dispRe.ReplaceAllStringFunc(text, keep)
	text = inlineRe.ReplaceAllStringFunc(text, keep)
	text = tagRe.ReplaceAllStringFunc(text, keep)
	// HTML 表格是多行整体，先于单标签保护（防止 <table> 内部被单独处理）
	// 注：表格里的英文单词同样需要校准，故表格不整体冻结，仅保护标签本身。

	var st Stats
	text = repairProse(text, vocab, &st)
	for key, orig := range protect {
		text = strings.ReplaceAll(text, key, orig)
	}
	return Result{Text: text, Stats: st}, nil
}

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
					if _, ok := vocab[prev+lower[i]]; ok {
						if !prevIsWord || isCommonSuffix(lower[i]) {
							base := prevFixed
							if base == "" {
								base = words[i-1]
							}
							out[i-1] = applyCase(base, prev+lower[i])
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
		// 3) 编辑距离 ≤1 且候选唯一
		cands := edit1Candidates(lower[i], vocab)
		switch len(cands) {
		case 1:
			out[i] = applyCase(words[i], cands[0])
			st.Fixed++
		case 0:
			// 无候选，原样
		default:
			st.Kept = append(st.Kept, lower[i])
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
