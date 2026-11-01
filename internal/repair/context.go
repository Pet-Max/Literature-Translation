// 本文件自 repair.go（v1.1.1 引擎校准域）按既有分段接缝切分而来，
// 逻辑与切分前逐行一致（同包纯移动，零语义变化）。

// 上下文锚定：全局 token 对齐替换（真词损伤）与局部夹缝兜底。
package repair

import (
	"regexp"
	"strings"
)

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
