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
	return markdownToHTML(md)
}

func TestNestedFreezeRestore(t *testing.T) {
	// 代码围栏夹在 $$…$$ 之间：含占位符的匹配不再冻结成"公式"，
	// 围栏按代码块还原，$$ 落为普通文本，不得出现 ⟦M⟧ 字面量泄漏
	// （repair/render 两处同源 bug，V4 全库验证泄漏 862 处）
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
