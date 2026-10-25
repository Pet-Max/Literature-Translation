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

// MarkdownToHTMLForTest 供测试访问内部转换。
func MarkdownToHTMLForTest(md string) string {
	return markdownToHTML(md)
}
