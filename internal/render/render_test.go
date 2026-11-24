package render

import (
	"strings"
	"testing"
)

// 回归：向量粗体命令必须改写为 \boldsymbol。
// KaTeX 内建 \pmb 是 text-shadow 假粗体，打印后几乎不可见，
// 而向量粗体承载语义（向量 vs 标量），必须换成真粗体字形。
func TestPmbRewrittenToBoldsymbol(t *testing.T) {
	md := "向量 $\\pmb { x } (t)$ 与漂移 $\\pmb { f } ( \\pmb { x} )$，以及 $$\\dot {\\boldsymbol {x}}(t) = \\pmb {G} \\cdot \\tilde{\\pmb {\\xi}}(t).$$"
	out := MarkdownToHTMLForTest(md)
	if strings.Contains(out, `\pmb`) {
		t.Errorf("输出仍含 \\pmb：%s", out[:min(len(out), 300)])
	}
	if !strings.Contains(out, `\boldsymbol`) {
		t.Errorf("输出缺少 \\boldsymbol：%s", out[:min(len(out), 300)])
	}
	// 中文与结构不受影响
	if !strings.Contains(out, "向量") || !strings.Contains(out, "ktx-disp") {
		t.Error("正文或行间公式结构丢失")
	}
}

func TestBmAlsoRewritten(t *testing.T) {
	out := MarkdownToHTMLForTest("见 $\\bm {B}$ 矩阵。")
	if strings.Contains(out, `\bm `) || strings.Contains(out, `\bm{`) {
		t.Errorf("\\bm 未改写")
	}
	if !strings.Contains(out, `\boldsymbol`) {
		t.Error("缺少 \\boldsymbol")
	}
}

// TestUnsafeHTMLescaped 回归：源 Markdown 里的原始 HTML 必须被转义成
// 纯文本（goldmark 默认行为，禁用 WithUnsafe）。译文/中文稿若混入
// <script> 或事件属性，导出页加载时不得执行——headless 浏览器里跑脚本
// 等于把工作目录读走（本地文件服务同源）。
func TestUnsafeHTMLescaped(t *testing.T) {
	md := "正文段落。\n\n<script>alert(1)</script>\n\n<img src=x onerror=alert(2)>\n\n[a](javascript:alert(3))"
	out := Build(md, Options{Title: "安全冒烟"})
	if strings.Contains(strings.ToLower(out), "<script>alert") {
		t.Errorf("script 标签未被转义：%s", out[:min(len(out), 300)])
	}
	if strings.Contains(strings.ToLower(out), "onerror=") {
		t.Errorf("事件属性未被转义")
	}
	if strings.Contains(strings.ToLower(out), `href="javascript:`) {
		t.Errorf("javascript: 链接未被清洗")
	}
	// 自身的 KaTeX 挂载脚本必须仍为外链（CSP script-src 'self' 允许）
	if !strings.Contains(out, `<script src="/assets/katex/katex.min.js"></script>`) ||
		!strings.Contains(out, `<script src="/assets/katex/render.js"></script>`) {
		t.Error("KaTeX 外链脚本缺失")
	}
	if strings.Contains(out, "<script>\n") {
		t.Error("不应再有内联 <script>（CSP 禁止内联脚本）")
	}
	// CSP 头必须存在且收紧脚本源
	if !strings.Contains(out, `http-equiv="Content-Security-Policy"`) ||
		!strings.Contains(out, "script-src 'self'") {
		t.Error("缺少 CSP meta 或 script-src 未收紧")
	}
	// 数学公式回填不受影响（占位符 → data-tex span）
	if !strings.Contains(out, "ktx-disp") || !strings.Contains(out, "正文段落") {
		t.Error("正文或公式结构丢失")
	}
}

// MarkdownToHTMLForTest 供测试访问内部转换。
func MarkdownToHTMLForTest(md string) string {
	return markdownToHTML(md, nil)
}

func TestRenderGeneralityAndStats(t *testing.T) {
	// 1. 空标题不生成空 <h1 class="doc-title">；无标题不生成空 <nav class="toc">
	//    $50 to $100、$50-$100 与 $50 ($x+1$) / $5-$10 ($y+2$) 中金额部分不被误识别为公式，而 $1-2$、$x+1$、$y+2$ 保留公式属性
	noTitleOut, noTitleSt := BuildWithStats("Only body paragraph.\n\nCosts $50 to $100, $50-$100, $50 ($x+1$) and $5-$10 ($y+2$), math $1-2$.", Options{Title: "", TOC: true})
	if strings.Contains(noTitleOut, `class="doc-title"`) {
		t.Fatalf("空标题不应输出 doc-title: %s", noTitleOut)
	}
	if strings.Contains(noTitleOut, `class="toc"`) {
		t.Fatalf("无标题文档不应输出空目录框: %s", noTitleOut)
	}
	if strings.Contains(noTitleOut, `data-tex="50`) || strings.Contains(noTitleOut, `data-tex="10`) {
		t.Fatalf("货币金额不应被渲染为公式: %s", noTitleOut)
	}
	for _, wantTex := range []string{`data-tex="x+1"`, `data-tex="y+2"`, `data-tex="1-2"`} {
		if !strings.Contains(noTitleOut, wantTex) {
			t.Fatalf("真公式 %s 应保留渲染: %s", wantTex, noTitleOut)
		}
	}
	if noTitleSt.RawHTMLOmitted != 0 {
		t.Fatalf("无原始 HTML 不应计数: %+v", noTitleSt)
	}

	// 2. NoCoverTitle=true 时保留后续分段开头的 # 章节标题在正文区，不抽离为居中封面标题
	contOut := Build("# Chapter 3\n\nContinuation body.", Options{Title: "", NoCoverTitle: true})
	if strings.Contains(contOut, `class="doc-title"`) {
		t.Fatalf("NoCoverTitle=true 不应生成 doc-title: %s", contOut)
	}
	if !strings.Contains(contOut, `<article class="content">`+"\n"+`<h1 id="chapter-3">Chapter 3</h1>`) {
		t.Fatalf("续段 H1 应保留在 content 内: %s", contOut)
	}

	// 3. 原始 HTML 被安全过滤时，RawHTMLOmitted 准确告警计数
	rawHTMLDoc, rawSt := BuildWithStats("<div>raw block</div>\n\n<table><tr><td>x</td></tr></table>", Options{Title: "T"})
	if !strings.Contains(rawHTMLDoc, "raw HTML omitted") || rawSt.RawHTMLOmitted == 0 {
		t.Fatalf("未统计原始 HTML 过滤数: stats=%+v doc=%s", rawSt, rawHTMLDoc)
	}
}

func TestNestedFreezeRestore(t *testing.T) {
	// 代码围栏夹在 $$…$$ 之间：含占位符的匹配不再冻结成"公式"，
	// 围栏按代码块还原，$$ 落为普通文本，不得出现 ⟦M⟧ 字面量泄漏。
	md := "before\n\n$$\n```\nfenced $$ inner\n```\n$$\n\nafter"
	out := Build(md, Options{Title: "T"})
	if strings.Contains(out, "⟦M") || strings.Contains(out, "[M1]") {
		t.Errorf("占位符泄漏: %q", out)
	}
	if !strings.Contains(out, "<pre><code>fenced $$ inner</code></pre>") {
		t.Errorf("代码块丢失: %q", out)
	}
	if strings.Contains(out, `data-tex`) {
		t.Errorf("夹住围栏的 $$ 不应变成公式: %q", out)
	}
}

// 回归：双语对照把原文逐行加「> 」前缀拼成引用块（engine 组装），行间
// 公式的 $$…$$ 跨行捕获会把行首引用标记一起抓进 TeX 源码，KaTeX 将其
// 当关系符号画进公式（双语导出公式两端多出「>」）。冻结前须剥掉。
func TestQuotedDisplayMathStripped(t *testing.T) {
	out := MarkdownToHTMLForTest("> $$\n> i \\hbar x\n> $$")
	if strings.Contains(out, "&gt;") {
		t.Errorf("引用标记混进公式源码: %q", out)
	}
	if !strings.Contains(out, "<blockquote>") || !strings.Contains(out, "ktx-disp") {
		t.Errorf("引用块或公式结构丢失: %q", out)
	}
	if !strings.Contains(out, "i \\hbar x") {
		t.Errorf("公式内容丢失: %q", out)
	}
	if strings.Contains(out, "⟦M") {
		t.Errorf("占位符泄漏: %q", out)
	}
}

// 混合型 TeX（部分行首恰为 >，如单独成行的不等式写法）：不满足「全段
// 均为引用行」的剥除条件，必须原样保留。data-tex 中的 &gt; 是
// html.EscapeString(>) 的产物，存在即证明未被误剥。
func TestMixedQuoteTeXNotStripped(t *testing.T) {
	out := MarkdownToHTMLForTest("$$\n> x\ny\n$$")
	if !strings.Contains(out, "&gt;") {
		t.Errorf("混合型 TeX 被误剥: %q", out)
	}
}

// 单行引用公式（> $$x$$）：捕获不含内部行，剥除为空操作，内容不变。
func TestSingleLineQuotedMath(t *testing.T) {
	out := MarkdownToHTMLForTest("> $$x$$")
	if !strings.Contains(out, `data-tex="x"`) {
		t.Errorf("单行引用公式内容不符: %q", out)
	}
	if strings.Contains(out, "&gt;") {
		t.Errorf("引用标记混进公式源码: %q", out)
	}
}
