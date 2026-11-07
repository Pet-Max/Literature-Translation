// extract_smoke_test.go — 抽取质量冒烟门：对仓库自带的合成示例 PDF 跑
// pdfium 文本层抽取，断言一组稳定的内容不变量（标题锚点、节标题、文献
// 条目、公式编号）与输出卫生（无 U+FFFE 标记、无连字码点）。
//
// 前身 tests/parity_test.go 是 v1.2.0 引擎 Go 化时对 Python 引擎的逐字
// 对拍——迁移完成后其使命即告终结，且基线锚定的旧样例与 Python 引擎
// 均已移除，测试随之退役（历史版本可从旧 tag 检索）。本文件以不依赖
// 任何外部基线的方式接替其"抽取质量有护栏"的职责：换样例时同步更新
// 断言即可。
package tests

import (
	"fmt"
	"strings"
	"testing"

	"literature-translation/internal/pdftext"
)

const smokeSamplePath = "../samples/example-document-en.pdf"

func TestExtractSyntheticSampleSmoke(t *testing.T) {
	goText, err := pdftext.ExtractText(smokeSamplePath)
	if err != nil {
		t.Fatalf("pdfium 抽取失败: %v", err)
	}

	// 抽取非空转：文本量在合理区间（当前样例约 4.3k 字符）
	if n := len([]rune(goText)); n < 3000 {
		t.Fatalf("抽取文本过短（%d 字符），疑似抽取失败", n)
	}

	// 输出卫生：残留码点与连字码点（承自旧对拍测试的有效检查）
	if strings.ContainsRune(goText, '\ufffe') {
		t.Fatalf("抽取输出仍含 U+FFFE 标记")
	}
	for _, l := range "ﬀﬁﬂﬃﬄﬅﬆ" {
		if strings.ContainsRune(goText, l) {
			t.Fatalf("抽取输出仍含连字码点 %U", l)
		}
	}

	// 内容不变量：标题、节标题、期刊锚点
	for _, anchor := range []string{
		"Thalyon-16", // 标题与设备名
		"I. INTRODUCTION",
		"IV. CONCLUSION",
		"Phys. Rev. Lett.", // 文献条目未残损
	} {
		if !strings.Contains(goText, anchor) {
			t.Fatalf("抽取文本缺少锚点 %q", anchor)
		}
	}

	// 文献条目 [1]–[6] 段首齐全
	for n := 1; n <= 6; n++ {
		if !strings.Contains(goText, fmt.Sprintf("[%d] ", n)) {
			t.Fatalf("抽取文本缺少文献条目 [%d]", n)
		}
	}

	// 公式编号 (1)–(10) 齐全
	for n := 1; n <= 10; n++ {
		if !strings.Contains(goText, fmt.Sprintf("(%d)", n)) {
			t.Fatalf("抽取文本缺少公式编号 (%d)", n)
		}
	}
}
