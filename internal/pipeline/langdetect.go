package pipeline

// langdetect.go — 源文件语言判定（英文→翻译流程 / 中文→仅排版流程）。
// 朴素占比法：汉字数 /（汉字 + 拉丁字母数）。英文论文的参考文献里
// 出现中文作者名占比极小，不受影响；阈值 30% 取纯中文与纯英文的中带。

import (
	"os"
	"unicode"
)

// cjkThreshold 汉字占比超过此值判定为中文文档。
const cjkThreshold = 0.30

// readLimit 语言采样上限：文档前 64KB 足以稳定判定。
const readLimit = 64 * 1024

func isHan(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || // CJK 统一表意文字
		(r >= 0x3400 && r <= 0x4DBF) || // 扩展 A
		(r >= 0xF900 && r <= 0xFAFF) // 兼容表意文字
}

// CJKRatio 返回汉字占（汉字+拉丁字母）的比例。无有效字符返回 0。
func CJKRatio(text string) float64 {
	han, latin := 0, 0
	for _, r := range text {
		switch {
		case isHan(r):
			han++
		case unicode.Is(unicode.Latin, r):
			latin++
		}
	}
	if han+latin == 0 {
		return 0
	}
	return float64(han) / float64(han+latin)
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
