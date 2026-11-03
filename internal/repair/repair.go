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
	SupSub  int      // 散文级 HTML 上下标解包（MinerU 误包整句为下标）
	MathFix int      // 数学区伪影归一（间距宏/排版引号/引用撕裂/伪上标/冗余残片）
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

// urlRe Markdown 链接/图片 与裸 URL。URL 里合法的词片段（如域名
// openxlab.org.cn 的 "org"）绝不能进词级修复——V4 验证中 399 处图片
// CDN 的 org 被上下文锚定改成 "or"，链接全坏。
var urlRe = regexp.MustCompile(`!?\[[^\]]*\]\([^)]*\)|https?://[^\s)<>"']+`)

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
	//    分离重音符合成；散文级上下标解包
	text := tablesToMarkdown(md, &st)
	text = eqToMath(text, &st)
	text = composeAccents(text, &st)
	text = unwrapProseSupSub(text, &st)
	// 1) 符号修复（行内公式内容在此阶段单独处理）
	if opt.Symbols {
		text = fixControlChars(text, orig, &st)
		text = normalizeSymbols(text, orig, &st)
	}
	// 数学伪影修复也遵循符号开关；引号仅由上方带原文锚点的规则处理。
	if opt.Symbols {
		text = fixMathArtifacts(text, &st)
	}
	// 1.5) 伪行间公式升级：必须在符号修复之后——升级成的 $$ 块会被
	//      词级冻结且符号规则只处理行内 $…$，先升级会让独立成行的
	//      $…$ 里的 |00i/⟩⟨ 损伤逃过修复（探针实证的顺序缺口）
	if opt.Symbols {
		text = promoteDisplayMath(text, &st)
	}
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

// unfreeze 按键号从大到小恢复（后冻结的是外层），保证外层先还原，
// 内层键随后处理，避免 map 的随机遍历顺序残留占位符。
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
