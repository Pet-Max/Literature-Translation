package pipeline

import (
	"strings"
	"testing"
)

// TestSplitMarkdownChunks 切分三属性：内容无损（依序拼接等于原文）、
// 切点全部落在段落空行边界（不拦腰截断段落/表格）、段数上限生效。
func TestSplitMarkdownChunks(t *testing.T) {
	var b strings.Builder
	para := strings.Repeat("x", 200)
	for i := 0; i < 13000; i++ {
		b.WriteString(para)
		b.WriteString("\n\n")
	}
	md := b.String()
	chunks := splitMarkdownChunks(md, 800<<10, 12)
	if len(chunks) < 2 {
		t.Fatalf("应切出多段，got %d", len(chunks))
	}
	var joined strings.Builder
	for _, c := range chunks {
		joined.WriteString(c)
	}
	if joined.String() != md {
		t.Fatalf("切分内容有丢失或重复（%d B → %d B）", len(md), joined.Len())
	}
	for i, c := range chunks[1:] {
		if !strings.HasPrefix(c, "\n\n") {
			t.Fatalf("第 %d 段不以段落边界开头: %q…", i+1, c[:min(20, len(c))])
		}
	}
	for i, c := range chunks[:len(chunks)-1] {
		if strings.HasSuffix(c, "\n") {
			t.Fatalf("第 %d 段结尾悬在行中: %q…", i+1, c[max(0, len(c)-20):])
		}
	}

	// 小文档原样返回
	small := "a\n\nb"
	if got := splitMarkdownChunks(small, 800<<10, 12); len(got) != 1 || got[0] != small {
		t.Fatalf("小文档应原样返回单段")
	}

	// maxN 上限生效
	huge := strings.Repeat("para text here.\n\n", 200000)
	if got := splitMarkdownChunks(huge, 800<<10, 6); len(got) > 6 {
		t.Fatalf("段数超上限: %d", len(got))
	}

	// 均分无孤立尾段 + 围栏代码块内空行不切开
	var fenced strings.Builder
	for i := 0; i < 6; i++ {
		fenced.WriteString("```python\nline_a = 1\n\nline_b = 2\n```\n\n")
		fenced.WriteString(strings.Repeat("段落正文内容。", 12000))
		fenced.WriteString("\n\n")
	}
	fChunks := splitMarkdownChunks(fenced.String(), chunkTargetBytes, chunkMaxCount)
	if strings.Join(fChunks, "") != fenced.String() {
		t.Fatal("带围栏切分丢失内容")
	}
	for i, c := range fChunks {
		if strings.Count(c, "```")%2 != 0 {
			t.Fatalf("第 %d 段把围栏代码块拦腰切开", i+1)
		}
		if len(c) < 32<<10 {
			t.Fatalf("第 %d 段产生异常小尾段 (%d B)", i+1, len(c))
		}
	}

	// 行首三反引号行内代码不误判为未闭合围栏（否则后续正文全被锁死不可切分）
	if r := markdownFenceRanges("```inline()``` leads.\n\nNext paragraph."); len(r) != 0 {
		t.Fatalf("单行三反引号行内代码不应产生围栏范围: %v", r)
	}

	// 首段无标题（未生成 <nav class="toc">）时，injectFullTOC 仍须把整本目录插到正文前
	full := "<html><body><nav class=\"toc\"><ul><li><a href=\"#c2\">Ch2</a></li></ul></nav><article class=\"content\"><h2 id=\"c2\">Ch2</h2></article></body></html>"
	chunkNoTOC := "<html><body><article class=\"content\"><p>Pre</p></article></body></html>"
	injected := injectFullTOC(full, chunkNoTOC)
	if !strings.Contains(injected, `<nav class="toc">`) || !strings.Contains(injected, `href="#c2"`) {
		t.Fatalf("首段无目录节点时 injectFullTOC 未插入整本目录: %s", injected)
	}
}
