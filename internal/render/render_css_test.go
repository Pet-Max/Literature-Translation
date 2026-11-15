// render_css_test.go — 关键 CSS 片段（公式横线、英文块配色、中文纸墨）必须随 Build 输出。
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

// enQuoteCSS 各预设键必须随 Build 注入覆盖规则；未知键回落默认（无覆盖）。
func TestBuildEnQuoteCSS(t *testing.T) {
	body := "> English quote"
	cases := map[string]string{
		"cool": "blockquote{background:#f0f4f7;color:#3e4c54}",
		"teal": "blockquote{background:#eef3f1;color:#2E5A57}",
		"warm": "blockquote{background:#f5f3ee;color:#4a4640}",
		"none": "blockquote{background:none}",
	}
	for key, want := range cases {
		html := Build(body, Options{Title: "t", EnBg: key})
		if !strings.Contains(html, want) {
			t.Errorf("EnBg=%s 时 Build 输出缺少 %q", key, want)
		}
	}
	for _, key := range []string{"", "paper", "未知键"} {
		html := Build(body, Options{Title: "t", EnBg: key})
		for _, banned := range cases {
			if strings.Contains(html, banned) {
				t.Errorf("EnBg=%s 时不应注入覆盖 %q", key, banned)
			}
		}
		if !strings.Contains(html, "blockquote {") || !strings.Contains(html, "#fafaf7") {
			t.Errorf("EnBg=%s 时默认引用块样式缺失", key)
		}
	}
}

// zhBodyCSS 各预设键必须随 Build 注入整页覆盖；未知键回落默认（无覆盖）。
func TestBuildZhBodyCSS(t *testing.T) {
	cases := map[string]string{
		"cream": "body{background:#fafaf7;color:#4a4436}",
		"cool":  "body{background:#f0f4f7;color:#33414e}",
		"teal":  "body{background:#eef3f1;color:#1f4a44}",
		"warm":  "body{background:#f5f3ee;color:#4e453a}",
	}
	for key, want := range cases {
		html := Build("正文", Options{Title: "t", ZhBg: key})
		if !strings.Contains(html, want) {
			t.Errorf("ZhBg=%s 时 Build 输出缺少 %q", key, want)
		}
	}
	for _, key := range []string{"", "paper", "未知键"} {
		html := Build("正文", Options{Title: "t", ZhBg: key})
		for _, banned := range cases {
			if strings.Contains(html, banned) {
				t.Errorf("ZhBg=%s 时不应注入覆盖 %q", key, banned)
			}
		}
		if !strings.Contains(html, "blockquote {") || !strings.Contains(html, "#fafaf7") {
			t.Errorf("ZhBg=%s 时默认引用块样式缺失", key)
		}
	}
}
