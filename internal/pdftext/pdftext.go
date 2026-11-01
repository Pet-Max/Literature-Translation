// Package pdftext 抽取 PDF 文本层（PDFium，webassembly 内嵌运行时）。
//
// Python 边车（v1.1.1 引擎）的 op_pdf_text 文本抽取由本包进程内直调替代。
// 用于语言预检与拼写校准：原文本层是干净参照。
//
// 与 v1.1.1 引擎 op_pdf_text 的行为对齐：
//   - 逐页抽取后以 "\n" 连接；
//   - 连字码点还原（ﬀ→ff 等 7 条；pdfium 通常直接输出展开字形，此处
//     为其它字体兜底）；
//   - "组合删除符+等号"（U+0338=）归一为 ≠；
//   - pdfium 特有差异归一：行尾 \r\n → \n；行尾断词被 pdfium 合并并以
//     U+FFFE 标记，还原为 "-\n"（与 v1.1.1 引擎输出同形，供 repair 包的
//     dehyphenate 三分法按母本逻辑工作）。
package pdftext

import (
	"os"
	"strings"
	"sync"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
)

var (
	poolOnce sync.Once
	pool     pdfium.Pool
	poolErr  error
)

func ensurePool() error {
	poolOnce.Do(func() {
		pool, poolErr = webassembly.Init(webassembly.Config{
			MinIdle:  1, // 常驻一个 worker，首次调用免冷启动
			MaxIdle:  1,
			MaxTotal: 2,
		})
	})
	return poolErr
}

// ligatures LaTeX CM 字体的连字码点：文本层里 "effects" 常以 ﬀ (U+FB00)
// 出现，按 ASCII 字母分词会把词切成 "e"+"ects" 毒化校准词表，先还原为
// 普通字母序列。
var ligatures = strings.NewReplacer(
	"\ufb00", "ff", "\ufb01", "fi", "\ufb02", "fl",
	"\ufb03", "ffi", "\ufb04", "ffl", "\ufb05", "st", "\ufb06", "st",
)

// ExtractText 抽取 PDF 文本层全文。
func ExtractText(pdfPath string) (string, error) {
	if err := ensurePool(); err != nil {
		return "", err
	}
	pdfBytes, err := os.ReadFile(pdfPath)
	if err != nil {
		return "", err
	}

	instance, err := pool.GetInstance(2 * time.Minute)
	if err != nil {
		return "", err
	}
	defer instance.Close()

	doc, err := instance.OpenDocument(&requests.OpenDocument{File: &pdfBytes})
	if err != nil {
		return "", err
	}
	defer instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})

	count, err := instance.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
	if err != nil {
		return "", err
	}

	pages := make([]string, count.PageCount)
	for i := 0; i < count.PageCount; i++ {
		page, err := instance.FPDF_LoadPage(&requests.FPDF_LoadPage{Document: doc.Document, Index: i})
		if err != nil {
			return "", err
		}
		textPage, err := instance.FPDFText_LoadPage(&requests.FPDFText_LoadPage{
			Page: requests.Page{ByReference: &page.Page},
		})
		if err != nil {
			return "", err
		}
		nChars, err := instance.FPDFText_CountChars(&requests.FPDFText_CountChars{TextPage: textPage.TextPage})
		if err != nil {
			return "", err
		}
		if nChars.Count > 0 {
			got, err := instance.FPDFText_GetText(&requests.FPDFText_GetText{
				TextPage: textPage.TextPage, StartIndex: 0, Count: nChars.Count,
			})
			if err != nil {
				return "", err
			}
			pages[i] = got.Text
		}
		instance.FPDFText_ClosePage(&requests.FPDFText_ClosePage{TextPage: textPage.TextPage})
		instance.FPDF_ClosePage(&requests.FPDF_ClosePage{Page: page.Page})
	}

	// pdfium 行尾为 \r\n；统一为 v1.1.1 引擎抽取口径的 \n
	text := strings.ReplaceAll(strings.Join(pages, "\n"), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	// 连字码点还原；CM 字体还会把 ≠ 映射为"组合删除符+等号"，一并归一
	text = ligatures.Replace(text)
	text = strings.ReplaceAll(text, "\u0338=", "\u2260")
	// pdfium 的行尾断词合并标记还原为 v1.1.1 引擎输出同形的 "-\n"
	text = strings.ReplaceAll(text, "\uFFFE", "-\n")
	return text, nil
}
