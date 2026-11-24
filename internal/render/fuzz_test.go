package render

import (
	"strings"
	"testing"
)

// texfix 与 render 模糊测试：验证畸形 TeX/Markdown 不 panic、texfix 幂等、无内部占位符泄漏。
func FuzzSanitizeTex(f *testing.F) {
	f.Add(`\left \left ( x \right \end{foo}`)
	f.Add(`\begin{array}{c} \left & \tag{1} \tag{2}`)
	f.Add(`\tag{\tag{a}} \hdots \displaylimits \normalfont`)
	f.Fuzz(func(t *testing.T, tex string) {
		out1, _ := sanitizeTex(tex)
		out2, _ := sanitizeTex(out1)
		if out2 != out1 {
			t.Fatalf("sanitizeTex 破坏幂等性:\n  in:   %q\n  out1: %q\n  out2: %q", tex, out1, out2)
		}
	})
}

func FuzzBuildWithStats(f *testing.F) {
	f.Add("# 标题\n\n正文 $E=mc^2$ 与 $50 to $100。\n\n<table><tr><td>1</td></tr></table>")
	f.Add("```py\n$$\n```\n<sup>$a$</sup> <sub>$b$</sub> \\$5")
	f.Fuzz(func(t *testing.T, md string) {
		// 避开用户输入恰好自带字面量 ⟦M / \x00ESC\x00 的伪构造，专注检查正常输入绝不泄漏占位符
		if strings.Contains(md, "⟦M") || strings.Contains(md, "\x00ESC\x00") {
			return
		}
		htmlDoc, _ := BuildWithStats(md, Options{Title: "Fuzz", TOC: true})
		if strings.Contains(htmlDoc, "⟦M") || strings.Contains(htmlDoc, "\x00ESC\x00") {
			t.Fatalf("BuildWithStats 泄漏了内部占位符")
		}
	})
}
