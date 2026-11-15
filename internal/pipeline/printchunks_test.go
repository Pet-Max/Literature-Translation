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
}
