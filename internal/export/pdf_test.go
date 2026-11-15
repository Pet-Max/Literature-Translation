package export

import (
	"context"
	"fmt"
	"literature-translation/internal/pdftext"
	"literature-translation/internal/render"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

func TestPrintUsesOneAdaptedPage(t *testing.T) {
	d := t.TempDir()
	html := render.Build(`A valid equation $E=mc^2$.`, render.Options{Title: "single page"})
	// window.name survives navigation; a second load would discard the adapted DOM.
	html = strings.Replace(html, "<head>", `<head><script>window.name=String(Number(window.name||0)+1);</script>`, 1)
	html = strings.Replace(html, "</body>", `<script>const originalFit=fitWideFormulas;fitWideFormulas=function(){originalFit();if(Number(window.name)>1){window.__renderStats={errors:['page reloaded']};}};</script></body>`, 1)
	if _, e := PrintPDF(context.Background(), d, html, filepath.Join(d, "single.pdf"), "", nil); e != nil {
		t.Fatal(e)
	}
}

// 旧版行为是「任一公式失败即整任务失败且无产物」；texfix 窄清洗 +
// 降级放行后，解析失败的公式以红字源码保留在 PDF 中，任务照常完成，
// 降级报告经 logf 送日志。
func TestInvalidTeXDegradesButExports(t *testing.T) {
	if _, e := FindBrowser(); e != nil {
		t.Skip("本机无 Edge，无法执行打印验证")
	}
	d := t.TempDir()
	out := filepath.Join(d, "invalid.pdf")
	// \notARealCommand 既不在清洗字典也不存在对应物，必然走降级路径
	html := render.Build(`Invalid $\notARealCommand{x}$ formula.`, render.Options{Title: "invalid"})
	var logs []string
	_, e := PrintPDF(context.Background(), d, html, out, "", func(msg string) { logs = append(logs, msg) })
	if e != nil {
		t.Fatal("降级放行后不应失败:", e)
	}
	if _, e = os.Stat(out); e != nil {
		t.Fatal("PDF 未产出:", e)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "公式渲染降级") || !strings.Contains(joined, "notARealCommand") {
		t.Fatal("降级报告缺失或不含错误样例:", joined)
	}
}

// 宽表与长代码行的打印裁剪回归：修复前超宽表格右端列被页边物理裁掉
// （A4 竖版可打印宽 ~542px，30 列表格溢出后被切），打印前实测适配必须
// 让最右列文字与代码行尾出现在 PDF 提取文本里。30 列用例在层1（缩字号）
// 即收敛；层2（fixed 定宽保底）路径由真机验收目检覆盖（如 19 列周期表
// 的英文原表）。
func TestWideTableAndCodeNotClipped(t *testing.T) {
	if _, e := FindBrowser(); e != nil {
		t.Skip("本机无 Edge，无法执行打印验证")
	}
	d := t.TempDir()
	out := filepath.Join(d, "wide.pdf")
	var b strings.Builder
	b.WriteString("## 宽表\n\n")
	b.WriteString("|")
	for c := 1; c <= 30; c++ {
		b.WriteString(fmt.Sprintf(" 列%d |", c))
	}
	b.WriteString("\n|")
	for c := 1; c <= 30; c++ {
		b.WriteString(" --- |")
	}
	b.WriteString("\n|")
	for c := 1; c <= 29; c++ {
		b.WriteString(fmt.Sprintf(" 值%d |", c))
	}
	b.WriteString(" ZZENDMARKZZ |\n\n")
	b.WriteString("## 长代码\n\n```text\n")
	b.WriteString(strings.Repeat("x", 110) + "ZZCODEENDZZ\n")
	b.WriteString("```\n\n普通窄表 | A | B |\n| --- | --- |\n| 左 | 右 |\n")
	html := render.Build(b.String(), render.Options{Title: "wide"})
	if _, e := PrintPDF(context.Background(), d, html, out, "", nil); e != nil {
		t.Fatal(e)
	}
	txt, e := pdftext.ExtractText(out)
	if e != nil {
		t.Fatalf("提取失败: %v", e)
	}
	// 窄列适配后单元格内文字会折行，提取文本在断行处带空白——剥离全部
	// 空白后再比对标记，避免把「已渲染」误判成「被裁」
	compact := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, txt)
	for _, marker := range []string{"ZZENDMARKZZ", "ZZCODEENDZZ", "列30", "右"} {
		if !strings.Contains(compact, marker) {
			t.Fatalf("PDF 文本缺 %q —— 宽内容仍被页边裁剪", marker)
		}
	}
}
