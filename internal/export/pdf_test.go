package export

import (
	"context"
	"literature-translation/internal/render"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrintUsesOneAdaptedPage(t *testing.T) {
	d := t.TempDir()
	html := render.Build(`A valid equation $E=mc^2$.`, render.Options{Title: "single page"})
	// window.name survives navigation; a second load would discard the adapted DOM.
	html = strings.Replace(html, "<head>", `<head><script>window.name=String(Number(window.name||0)+1);</script>`, 1)
	html = strings.Replace(html, "</body>", `<script>const originalFit=fitWideFormulas;fitWideFormulas=function(){originalFit();if(Number(window.name)>1){window.__renderStats={errors:['page reloaded']};}};</script></body>`, 1)
	if _, e := PrintPDF(context.Background(), d, html, filepath.Join(d, "single.pdf"), ""); e != nil {
		t.Fatal(e)
	}
}

func TestInvalidTeXIsNotExported(t *testing.T) {
	if _, e := FindBrowser(); e != nil {
		t.Skip("本机无 Edge，无法执行打印验证")
	}
	d := t.TempDir()
	out := filepath.Join(d, "invalid.pdf")
	html := render.Build(`Invalid $\notARealCommand{x}$ formula.`, render.Options{Title: "invalid"})
	_, e := PrintPDF(context.Background(), d, html, out, "")
	if e == nil || !strings.Contains(e.Error(), "公式渲染失败") {
		t.Fatal("invalid TeX silently exported", e)
	}
	if _, e = os.Stat(out); !os.IsNotExist(e) {
		t.Fatal("invalid PDF created")
	}
}
