// 本文件自 repair.go（v1.1.1 引擎校准域）按既有分段接缝切分而来，
// 逻辑与切分前逐行一致（同包纯移动，零语义变化）。

// 词级拼写校准：相邻合并/一词拆二/编辑距离三规则。
package repair

import (
	"sort"
	"strings"
)

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
