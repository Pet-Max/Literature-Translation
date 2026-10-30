package repair

import (
	"strings"
	"testing"
)

// both 全开：绝大多数用例验证的是完整校准链路的行为。
var both = Options{Spelling: true, Symbols: true}

func testVocab() map[string]struct{} {
	words := []string{
		"different", "diffusion", "timescales", "configuration", "short", "and",
		"long", "time", "domain", "one", "to", "the", "bounded", "behavior",
		"steady", "state", "fluctuations", "observable", "power", "spectral",
		"density", "entropy", "production", "equilibrium", "maxwell", "however",
		"research", "arch", "there", "here",
	}
	vocab := make(map[string]struct{}, len(words))
	for _, w := range words {
		vocab[w] = struct{}{}
	}
	return vocab
}

func TestMarkdown(t *testing.T) {
	vocab := testVocab()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"ff丢字母", "the diferent timescales", "the different timescales"},
		{"ff丢字母2", "difusion matrix", "diffusion matrix"},
		{"大小写保留", "Diferent results", "Different results"},
		{"粘连拆分", "the timedomain behavior", "the time domain behavior"},
		{"连字丢段", "one-toone relation", "one-to one relation"},
		{"相邻合并", "the configura tion file", "the configuration file"},
		{"正确词不动", "the diffusion process", "the diffusion process"},
		{"大写正确词不动", "Short and long", "Short and long"},
		{"公式保护", "given by $S^{z}(\\omega)$ difusion", "given by $S^{z}(\\omega)$ diffusion"},
		{"代码块保护", "```py\n\tdiferent\n``` difusion", "```py\n\tdiferent\n``` diffusion"},
		{"标签保护", "<sup>1</sup> difusion", "<sup>1</sup> diffusion"},
		{"the re 不误合并", "the re mark", "the re mark"}, // re 在词表?不在;the+re: the∈词表→不合并;re→无候选→不动
		{"误合并防护", "the arch ive", "the arch ive"},     // arch∈词表→不合并进 there
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Markdown(c.in, vocab)
			if err != nil {
				t.Fatal(err)
			}
			if got.Text != c.want {
				t.Errorf("got %q want %q (stats: %+v)", got.Text, c.want, got.Stats)
			}
		})
	}
}

func TestEditDistanceLE1(t *testing.T) {
	if !editDistanceLE1("diferent", "different") {
		t.Error("插入 f 应为距离1")
	}
	if !editDistanceLE1("diferent", "diferents") {
		t.Error("尾部插入应为距离1")
	}
	if editDistanceLE1("abc", "abc") {
		t.Error("相同词距离为0")
	}
	if editDistanceLE1("abc", "abcdx") {
		t.Error("距离2应拒绝")
	}
}

func TestHyphenJoinVocab(t *testing.T) {
	// 原文带连字符断行时，碎片不得进词表；拼接词必须进词表；
	// 悬挂连字符（两段各自成词）必须保持分离，不得制造假词
	orig := ("The thermodynamic uncer-\ntainty relation yields a proper result-\ning flow " +
		"configu-\nration. The short-\nand long-time behavior. " +
		"The short is short, and the domain is a domain.")
	vocab := BuildVocab(orig)
	for _, frag := range []string{"uncer", "tainty", "configu", "ration", "shortand", "timedomain"} {
		if _, ok := vocab[frag]; ok {
			t.Errorf("碎片/假词 %q 不应进词表", frag)
		}
	}
	for _, w := range []string{"uncertainty", "resulting", "configuration", "proper", "short", "and", "domain"} {
		if _, ok := vocab[w]; !ok {
			t.Errorf("词 %q 应在词表中", w)
		}
	}
	// 用该词表校准一段含同样损伤的 MD
	md := "the uncer tainty of the result ing flow configura tion space"
	got, err := Markdown(md, vocab)
	if err != nil {
		t.Fatal(err)
	}
	want := "the uncertainty of the resulting flow configuration space"
	if got.Text != want {
		t.Errorf("got %q want %q", got.Text, want)
	}
}

func TestRealWorldSamples(t *testing.T) {
	// 模拟 MinerU 真实输出的句子
	vocab := testVocab()
	vocab["reveal"] = struct{}{}
	vocab["influence"] = struct{}{}
	in := "can reveal the influence of diferent timescales"
	got, _ := Markdown(in, vocab)
	if !strings.Contains(got.Text, "different") {
		t.Errorf("真实句样例未修复: %q", got.Text)
	}
}

// ---------------------------------------------------------------- Calibrate

// symOrig 模拟物理文献的原文文本层：⟨⟩/→/“” 为真实 Unicode（PyMuPDF
// 抽取形态），供符号修复与上下文锚定做锚点。
const symOrig = "with eigenstates |00⟩, |01⟩, |10⟩, |11⟩ and the basis {|0⟩, |1⟩}. " +
	"Denote projectors as operators in the joint basis. " +
	"the role of the generating function pq(i → f) and a transition i → f follows. " +
	"where “+c.c.” is shorthand. the standard fluctuation theorem. " +
	"quasiprobability approaches offer a natural framework. " +
	"engineered via controlled driving fields. This yields the matrix-element identity. " +
	"A nonzero correction quantities the deviation from the classical symmetry relation. " +
	"The off-diagonal elements are parametrized. " +
	"the proposed scheme offers a realistic route."

func TestBuildVocabLigatures(t *testing.T) {
	// CM 字体连字码点必须还原后再建词表，否则词表被词碎片毒化
	orig := "the eﬀects and diﬀerence of ﬁelds are suﬃcient"
	vocab := BuildVocab(orig)
	for _, w := range []string{"effects", "difference", "fields", "sufficient"} {
		if _, ok := vocab[w]; !ok {
			t.Errorf("词 %q 应在词表", w)
		}
	}
	for _, frag := range []string{"ects", "erence", "elds", "ficient"} {
		if _, ok := vocab[frag]; ok {
			t.Errorf("碎片 %q 不应在词表", frag)
		}
	}
}

func TestCalibrateSymbols(t *testing.T) {
	md := "with eigenstates |00i, |01i and the basis {|0i, |1i}. " +
		"Denote projectors as $\\Pi _ { i } = \\left| i \\right. \\left. i \\right|$ in the joint basis. " +
		"the generating function $p _ { q } ( i  f )$ and a transition $i  f$ follows. " +
		"where $^ { 6 6 } + \\mathrm { c . c . } ^ { , 9 }$ is shorthand. the standard fluctuation the orem."
	got, err := Calibrate(md, symOrig, both)
	if err != nil {
		t.Fatal(err)
	}
	want := "with eigenstates |00⟩, |01⟩ and the basis {|0⟩, |1⟩}. " +
		"Denote projectors as $\\Pi _ { i } = \\left| i \\rangle\\langle i \\right|$ in the joint basis. " +
		"the generating function $p _ { q } ( i \\to f )$ and a transition $i \\to f$ follows. " +
		"where “+c.c.” is shorthand. the standard fluctuation theorem."
	if got.Text != want {
		t.Errorf("got:\n%s\nwant:\n%s", got.Text, want)
	}
	if got.Stats.Syms != 8 { // 4 ket + 1 rightleft + 2 箭头 + 1 引号
		t.Errorf("Syms = %d, want 8 (details: %v)", got.Stats.Syms, got.Stats.Details)
	}
	if got.Stats.Ctx != 0 {
		t.Errorf("Ctx = %d, want 0", got.Stats.Ctx)
	}
}

func TestCalibrateContext(t *testing.T) {
	// 真词损伤（over/yields/of）由上下文锚定修复；合法 "This yields" 与
	// 原文自身的 "quantities"（原稿笔误）不得改动；"of fers" 走合并规则
	md := "quasiprobability approaches over a natural framework. " +
		"engineered via controlled driving yields. " +
		"This yields the matrix-element identity. " +
		"A nonzero correction quantities the deviation from the classical symmetry relation. " +
		"The of-diagonal elements are parametrized. " +
		"the proposed scheme of fers a realistic route."
	got, err := Calibrate(md, symOrig, both)
	if err != nil {
		t.Fatal(err)
	}
	want := "quasiprobability approaches offer a natural framework. " +
		"engineered via controlled driving fields. " +
		"This yields the matrix-element identity. " +
		"A nonzero correction quantities the deviation from the classical symmetry relation. " +
		"The off-diagonal elements are parametrized. " +
		"the proposed scheme offers a realistic route."
	if got.Text != want {
		t.Errorf("got:\n%s\nwant:\n%s", got.Text, want)
	}
	if got.Stats.Ctx != 3 || got.Stats.Merged != 1 {
		t.Errorf("Ctx = %d, Merged = %d, want 3/1 (details: %v)", got.Stats.Ctx, got.Stats.Merged, got.Stats.Details)
	}
}

func TestCalibrateNoAnchor(t *testing.T) {
	// 原文无锚的符号与无法对齐的词一律不动
	md := "with eigenstates |99i and |xXi. totally unrelated words here"
	got, err := Calibrate(md, "no anchors in this text at all", both)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != md {
		t.Errorf("原文无锚不得改动: %q", got.Text)
	}
}

func TestArrowContextAnchor(t *testing.T) {
	// 箭头回填需要散文上下文锚：短 token 的 "1→2" 在原文里太常见，
	// 公式两侧的词必须在原文命中位置附近按序出现才回填 \to
	orig := "in table rows 1 → 2 mapping shifts down. nothing else matters here at all"
	// 正例：上下文词在原文命中位置附近按序出现 → 回填
	md1 := "in table rows $x _ { 1  2 }$ mapping shifts down. nothing else matters here at all"
	got1, err := Calibrate(md1, orig, both)
	if err != nil {
		t.Fatal(err)
	}
	want1 := "in table rows $x _ { 1 \\to 2 }$ mapping shifts down. nothing else matters here at all"
	if got1.Text != want1 {
		t.Errorf("正例未回填: got %q want %q (details: %v)", got1.Text, want1, got1.Stats.Details)
	}
	// 负例：原文虽有 1→2，但公式的上下文词不在其附近 → 不动
	md2 := "we inspect $y _ { 1  2 }$ today only"
	got2, err := Calibrate(md2, orig, both)
	if err != nil {
		t.Fatal(err)
	}
	if got2.Text != md2 {
		t.Errorf("负例误回填: %q (details: %v)", got2.Text, got2.Stats.Details)
	}
}

func TestCalibrateGating(t *testing.T) {
	orig := "the temperature difference matters. with eigenstates |00⟩ here."
	md := "the temperature diference matters. with eigenstates |00i here."

	// 仅符号层：ket 修复、词级损伤不动
	got, err := Calibrate(md, orig, Options{Symbols: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Stats.Syms != 1 || got.Stats.Fixed+got.Stats.Ctx+got.Stats.Merged != 0 {
		t.Errorf("仅符号层统计错误: %+v", got.Stats)
	}
	if !strings.Contains(got.Text, "|00⟩") || !strings.Contains(got.Text, "diference") {
		t.Errorf("仅符号层文本错误: %q", got.Text)
	}

	// 仅词级层：拼写修复、符号损伤不动
	got, err = Calibrate(md, orig, Options{Spelling: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Stats.Syms != 0 || got.Stats.Fixed+got.Stats.Ctx+got.Stats.Merged != 1 {
		t.Errorf("仅词级层统计错误: %+v", got.Stats)
	}
	if !strings.Contains(got.Text, "difference") || !strings.Contains(got.Text, "|00i") {
		t.Errorf("仅词级层文本错误: %q", got.Text)
	}

	// 全关：原样返回
	got, err = Calibrate(md, orig, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != md || got.Stats.Syms+got.Stats.Fixed+got.Stats.Ctx+got.Stats.Merged != 0 {
		t.Errorf("全关应原样返回: %q %+v", got.Text, got.Stats)
	}
}

func TestDehyphenateFunctionWordFragments(t *testing.T) {
	// 断行切在功能词前缀后（the-\norem、of-\nfers、in-\nterference）：
	// 后段是碎片必须接回，否则碎片进词表，轻则校准失效、重则把
	// 正确的 interference 对齐成 terference
	orig := "the fluctuation the-\norem. the scheme of-\nfers a route. " +
		"used to control in-\nterference patterns. the theorem again."
	vocab := BuildVocab(orig)
	for _, w := range []string{"theorem", "offers", "interference"} {
		if _, ok := vocab[w]; !ok {
			t.Errorf("词 %q 应在词表", w)
		}
	}
	for _, frag := range []string{"orem", "fers", "terference"} {
		if _, ok := vocab[frag]; ok {
			t.Errorf("碎片 %q 不应在词表", frag)
		}
	}
}

func TestMergeDoubleF(t *testing.T) {
	// ﬀ 连字被 MinerU 读成 "f " 的形态：ef+ects 拼接差一个 f，
	// 合并规则补上再拼（ef+f+ects=effects）
	orig := "these effects remain unclear. the effects are real. effects matter"
	md := "these ef ects remain unclear. the ef ects are real. effects matter"
	got, err := Calibrate(md, orig, both)
	if err != nil {
		t.Fatal(err)
	}
	want := "these effects remain unclear. the effects are real. effects matter"
	if got.Text != want {
		t.Errorf("got %q want %q (details: %v)", got.Text, want, got.Stats.Details)
	}
}

func TestUnfreezeNestedPlaceholder(t *testing.T) {
	// 代码围栏嵌在 $$…$$ 内：冻结嵌套，恢复必须按外层→内层完成，
	// 不得把 \x00N\x00 以字面量留在正文（V4 全库 862 处泄漏的根因）
	orig := "some prose text for the vocabulary here different"
	md := "$$\n```\nfenced $$ content\n```\n$$\n\nthe diferent text"
	vocab := BuildVocab(orig)
	got, err := Markdown(md, vocab)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Text, "\x00") {
		t.Errorf("占位符泄漏: %q", got.Text)
	}
	if !strings.Contains(got.Text, "different") || !strings.Contains(got.Text, "fenced $$ content") {
		t.Errorf("内容丢失: %q", got.Text)
	}
}

func TestURLProtected(t *testing.T) {
	// 图片/裸链接里的域名片段不得进词级修复（V4 验证 org→or ×399）
	orig := "download the dataset mirrors and offer a natural framework for it"
	md := "fetch ![img](https://cdn.openxlab.org.cn/a.png) and see openxlab.org.cn mirrors and over a natural framework"
	got, err := Calibrate(md, orig, both)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Text, "openxlab.org.cn") {
		t.Errorf("URL 被改坏: %q", got.Text)
	}
	if !strings.Contains(got.Text, "offer a natural") {
		t.Errorf("真词损伤未修复: %q", got.Text)
	}
}

func TestControlCharAnchor(t *testing.T) {
	// OT1 控制码 → 希腊字母，须有 PDF 文本层锚。原文里同位置是 bell 的
	// 控制码按"原文该处无字符"删除（sep 不吃字母，但控制码非字母）。
	orig := "a three-level Λ system under damping. plus stray \x07 bytes here"
	md := "a three-level \x03 system under damping. plus stray \x07 bytes here"
	got, err := Calibrate(md, orig, both)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Text, "three-level Λ system") {
		t.Errorf("控制码未还原: %q", got.Text)
	}
	if strings.ContainsRune(got.Text, 7) {
		t.Errorf("孤立控制码应删除: %q", got.Text)
	}
	if got.Stats.Ctrl != 2 {
		t.Errorf("Ctrl = %d, want 2", got.Stats.Ctrl)
	}
}

func TestAccentCompose(t *testing.T) {
	md := "H¨anggi and Schr¨odinger and W´ojcik and A. Ac´ın"
	got, err := Calibrate(md, "Hänggi and Schrödinger and Wójcik and A. Acín", both)
	if err != nil {
		t.Fatal(err)
	}
	want := "Hänggi and Schrödinger and Wójcik and A. Acín"
	if got.Text != want {
		t.Errorf("got %q want %q", got.Text, want)
	}
	if got.Stats.Accents != 4 {
		t.Errorf("Accents = %d, want 4", got.Stats.Accents)
	}
}

func TestTableConvert(t *testing.T) {
	md := "caption here\n\n<table><tr><td>Model</td><td><eq>\\Delta</eq></td></tr>" +
		"<tr><td rowspan=\"2\">A</td><td>1</td></tr><tr><td>2</td></tr></table>\n\nafter text"
	got, err := Calibrate(md, "caption here after text", both)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Text, "| Model | $\\Delta$ |") {
		t.Errorf("表格未转换: %q", got.Text)
	}
	if !strings.Contains(got.Text, "| A | 1 |") || !strings.Contains(got.Text, "| A | 2 |") {
		t.Errorf("rowspan 未展开: %q", got.Text)
	}
	if got.Stats.Tables != 1 {
		t.Errorf("Tables = %d, want 1", got.Stats.Tables)
	}
}

func TestLocalContextRepair(t *testing.T) {
	// 局部锚定只作用于漂移区（suspect 集合）：suspect 内的 of→off 修复，
	// 健康区的 of/合法词一律不动（首版全文放开误伤 1.9 万处）
	orig := "the behavior of the off-diagonal elements matters here"
	text := "the behavior of the of-diagonal elements matters here"
	vocab := BuildVocab(orig)
	suspect := map[int]bool{}
	for k, l := range alignWordRe.FindAllStringIndex(text, -1) {
		if text[l[0]:l[1]] == "of" {
			suspect[k] = true
		}
	}
	var st Stats
	got := localContextRepair(text, orig, vocab, &st, suspect)
	if !strings.Contains(got, "off-diagonal") {
		t.Errorf("漂移区未修复: %q", got)
	}
	if st.Ctx != 1 {
		t.Errorf("Ctx = %d, want 1 (details: %v)", st.Ctx, st.Details)
	}
	var st2 Stats
	got2 := localContextRepair(text, orig, vocab, &st2, nil)
	if got2 != text {
		t.Errorf("非漂移区不得替换: %q", got2)
	}
}

func TestPromoteDisplayMath(t *testing.T) {
	var st Stats
	got := promoteDisplayMath(strings.Join([]string{
		"prose here",
		"$a = b$, and $c = d$",      // 一行两个公式：不动
		"$\\frac {N!} {n! (N-n)!}$", // 孤行长公式 → 升级
		"$x$",                       // 紧随其后的短公式并入同一块
		"$$",                        // 既有 $$ 块
		"$y _ { 1 }$",               // 块内不受影响
		"$$",
		"```",
		"$fenced$", // 围栏内不动
		"```",
		"$p =$",
		"$q + r = s _ { 2 }$", // 连续两行 → 合并成一个 $$ 块
		"tail text",
		"$x$", // 落单短公式：原样保留（含 $ 定界符）
	}, "\n"), &st)
	if !strings.Contains(got, "$$\n\\frac {N!} {n! (N-n)!}\nx\n$$") {
		t.Errorf("孤行长公式未升级: %q", got)
	}
	if !strings.Contains(got, "$a = b$, and $c = d$") {
		t.Errorf("一行两公式被破坏: %q", got)
	}
	if !strings.Contains(got, "$$\n$y _ { 1 }$\n$$") {
		t.Errorf("既有块被破坏: %q", got)
	}
	if !strings.Contains(got, "$fenced$") {
		t.Errorf("围栏被破坏: %q", got)
	}
	if !strings.Contains(got, "$$\np =\nq + r = s _ { 2 }\n$$") {
		t.Errorf("连续行未合并: %q", got)
	}
	if !strings.HasSuffix(got, "tail text\n$x$") {
		t.Errorf("落单短公式被破坏: %q", got)
	}
	if st.Display != 2 { // 孤行升级 1 + 连续合并 1
		t.Errorf("Display = %d, want 2", st.Display)
	}
}

func TestPromoteAfterSymbolRepair(t *testing.T) {
	// 独占一行的行内公式带 ⟩⟨ 损伤：升级必须发生在符号修复之后，
	// 否则 $$ 块逃过符号规则（顺序缺口的回归锁）
	orig := `Denote projectors as |i> strictly in the joint basis of this theory.`
	md := "Denote projectors as\n$\\Pi = \\left| i \\right. \\left. i \\right|$\nin the joint basis here."
	got, err := Calibrate(md, orig, both)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Text, `\rangle\langle`) {
		t.Errorf("升级后 $$ 块内符号损伤未修复: %q", got.Text)
	}
	if !strings.Contains(got.Text, "$$\n\\Pi") {
		t.Errorf("未升级为行间块: %q", got.Text)
	}
	if got.Stats.Syms != 1 {
		t.Errorf("Syms = %d, want 1", got.Stats.Syms)
	}
}
