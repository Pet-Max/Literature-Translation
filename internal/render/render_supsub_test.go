// render_supsub_test.go — 上下标白名单通道：<sup>/<sub> 必须原样排版进
// 导出 HTML。v1.2.1 及以前 goldmark 默认剥离原始 HTML，"Abah,<sup>2</sup>"
// 打印成 "Abah,2"（真实案例：某论文的作者行与机构行）。
package render

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBuildPreservesSupSub(t *testing.T) {
	md := "Obinna Abah,<sup>2</sup> Eric Lutz,<sup>1,</sup> <sup>∗</sup>\n\n" +
		"<sup>1</sup>Quantum, Universit¨at Mainz, Germany <sub>2</sub> 部门"
	out := Build(md, Options{Title: "t"})
	for _, want := range []string{
		"<sup>2</sup>", "<sup>1,</sup>", "<sup>∗</sup>",
		"<sup>1</sup>Quantum", "<sub>2</sub> 部门",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("上下标丢失，输出缺 %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "raw HTML omitted") {
		t.Fatalf("sup/sub 被当作原始 HTML 剥离:\n%s", out)
	}
}

func TestBuildSupSubContentEscaped(t *testing.T) {
	// 安全锁：白名单只放行标签名，内容必须整体转义——混入源 MD 的
	// 恶意/畸形标签不得以 HTML 形态进入导出文档
	out := Build("<sup><script>alert(1)</script></sup>", Options{Title: "t"})
	if strings.Contains(out, "<script>") {
		t.Fatalf("script 标签透传:\n%s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatalf("内容未转义:\n%s", out)
	}
}

func TestBuildSupSubWithInnerMath(t *testing.T) {
	// 内层公式先冻结、后还原：<sub>$x_1$</sub> 应同时保住下标与 KaTeX span
	out := Build("<sub>$x_1$</sub>", Options{Title: "t"})
	if !strings.Contains(out, "<sub>") || !strings.Contains(out, `class="ktx"`) {
		t.Fatalf("内含公式的下标丢失:\n%s", out)
	}
	if strings.Contains(out, "raw HTML omitted") {
		t.Fatalf("下标被剥离:\n%s", out)
	}
}

func TestLiteralPlaceholderAndNestedSup(t *testing.T) {
	for _, md := range []string{"<sup>⟦M1⟧</sup>", "<sub>$x_1$</sub> ⟦M2⟧", "```text\n⟦M1⟧\n```"} {
		got := Build(md, Options{})
		if !utf8.ValidString(got) {
			t.Fatal("invalid UTF-8")
		}
		if strings.Contains(md, "⟦M1⟧") && !strings.Contains(got, "⟦M1⟧") {
			t.Fatal("literal changed")
		}
		if strings.Contains(md, "$x_1$") && (!strings.Contains(got, `<sub><span class="ktx" data-tex="x_1"></span></sub>`) || strings.Contains(got, "⟦M1⟧")) {
			t.Fatal("nested formula not restored", got)
		}
	}
}
