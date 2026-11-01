// render_css_test.go — 公式横线减细 CSS（v1.2.0 顺带项）必须随 Build 输出。
package render

import (
	"strings"
	"testing"
)

func TestBuildFormulaLineCSS(t *testing.T) {
	html := Build("正文 $E=mc^2$ 与 $$\\frac{a}{b}$$。", Options{Title: "t"})
	for _, want := range []string{
		".mfrac .frac-line", ".overline .overline-line",
		"scaleY(0.6)", "transform-origin:bottom",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("Build 输出缺少公式横线 CSS 片段 %q", want)
		}
	}
}
