package pipeline

// langdetect.go — 源文件语言判定（英文→翻译流程 / 中文→仅排版流程）。
// 剥离公式/代码块/URL/双语引文噪声后计算汉字占总字母文字的比例，
// 剔除显著日语假名文档，并采用 30% 阈值区分中文与非中文文献。

import (
	"os"
	"regexp"
	"strings"
	"unicode"
)

// cjkThreshold 汉字占比超过此值判定为中文文档。
const cjkThreshold = 0.30

// readLimit 语言采样上限：文档前 64KB 足以稳定判定。
const readLimit = 64 * 1024

var (
	ldFenceRe = regexp.MustCompile("(?ms)^(?:```|~~~)[^\n]*\n.*?^(?:```|~~~)")
	ldCodeRe  = regexp.MustCompile("``[^`\n]+``|`[^`\n]+`")
	ldDispRe  = regexp.MustCompile(`(?s)\$\$.+?\$\$`)
	ldMathRe  = regexp.MustCompile(`(?s)\$[^$\n]+?\$`)
	ldLinkRe  = regexp.MustCompile(`!?\[[^\]]*\]\([^)]*\)`)
	ldTagRe   = regexp.MustCompile(`<[^>]+>`)
	ldURLRe   = regexp.MustCompile(`https?://[^\s)<>"']+`)
)

func isHan(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || // CJK 统一表意文字
		(r >= 0x3400 && r <= 0x4DBF) || // 扩展 A
		(r >= 0xF900 && r <= 0xFAFF) // 兼容表意文字
}

func isKana(r rune) bool {
	return (r >= 0x3040 && r <= 0x309F) || // 平假名
		(r >= 0x30A0 && r <= 0x30FF) // 片假名
}

// stripMarkdownNoise 剥离公式/代码块/链接 URL/标签，防止英文 LaTeX 命令与
// 图片哈希拉低中文占比；对双语对照 Markdown（同时含非引用正文与 `> ` 引用行），
// 仅保留非引用正文行以准确反映目标译文语言。
func stripMarkdownNoise(text string) string {
	s := ldFenceRe.ReplaceAllString(text, " ")
	s = ldCodeRe.ReplaceAllString(s, " ")
	s = ldDispRe.ReplaceAllString(s, " ")
	s = ldMathRe.ReplaceAllString(s, " ")
	s = ldLinkRe.ReplaceAllString(s, " ")
	s = ldTagRe.ReplaceAllString(s, " ")
	s = ldURLRe.ReplaceAllString(s, " ")

	lines := strings.Split(s, "\n")
	var nonQuote, quote []string
	nonQuoteLetters := 0
	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if strings.HasPrefix(trimmed, ">") {
			quote = append(quote, ln)
			continue
		}
		nonQuote = append(nonQuote, ln)
		for _, r := range trimmed {
			if unicode.IsLetter(r) {
				nonQuoteLetters++
			}
		}
	}
	if len(quote) > 0 && nonQuoteLetters >= 10 {
		return strings.Join(nonQuote, "\n")
	}
	return s
}

// CJKRatio 返回剥离公式/代码/URL/双语引用噪声后，汉字占总字母文字的比例。
// 若检测到显著比例的日语假名，直接返回 0 以防把日文文献误判为中文拒译。
func CJKRatio(text string) float64 {
	cleaned := stripMarkdownNoise(text)
	han, kana, otherLetters := 0, 0, 0
	for _, r := range cleaned {
		switch {
		case isHan(r):
			han++
		case isKana(r):
			kana++
		case unicode.IsLetter(r):
			otherLetters++
		}
	}
	if kana > 0 && float64(kana)/float64(han+kana) > 0.15 {
		return 0
	}
	if han+otherLetters == 0 {
		return 0
	}
	return float64(han) / float64(han+otherLetters)
}

// IsChineseText 按阈值判定文本是否为中文。
func IsChineseText(text string) bool {
	return CJKRatio(text) > cjkThreshold
}

// FileIsChinese 读取文件前 64KB 判定语言。读取失败返回 false（放行走翻译，
// 由后续流程自然报错，不做语言拦路）。
func FileIsChinese(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	if len(data) > readLimit {
		data = data[:readLimit]
	}
	// 容错 BOM 与非法字节：被 RuneError 替换，不影响两类计数
	return IsChineseText(string(data))
}
