// supsub.go — 散文级 HTML 上下标解包：MinerU 的 VLM 偶发把整句话包成
// 前面一个游离字符的下标（实证：摘要末句
// "Our results h<sub>demonstrate that thermal machines …</sub>"，PDF 文本层
// 证实原句并无 h）；LLM 翻译也可能把整句译文保留在下标标签内。合法上下
// 标都很短——化学式（H<sub>2</sub>O）、变量角标（ω<sub>r</sub>）、作者角
// 标（<sup>1,</sup> <sup>∗</sup>）——内容超长即判定为误包：去标签保留内
// 容，交回词级校准收尾（解包后可能粘连出的 "hdemonstrate" 与
// "demonstrate" 恰为编辑距离 1，唯一候选时由拼写规则自动修正）。
package repair

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	proseSupRe = regexp.MustCompile(`<sup>(.*?)</sup>`)
	proseSubRe = regexp.MustCompile(`<sub>(.*?)</sub>`)
)

const (
	// supsubMaxRunes 拉丁内容长度上限（rune 计）：合法角标是记号不是句子。
	supsubMaxRunes = 30
	// supsubMaxCJK 含 CJK 内容的字符数上限：中文正文没有词间空格（词数
	// 判据失效）且单字信息密度高，合法角标几乎不会超过数字或单字，超过
	// 即散文误包——判据放宽会重新放出「整句小号下标」。
	supsubMaxCJK = 4
)

// IsProseSupSub 判断 sup/sub 内容是否散文误包（通用形态判据，与文献无关）：
// 拉丁按长度/词数计，CJK 按字符数计。repair 的解包与 render 的白名单回填
// 共用本判据，保证校准层与排版层口径一致。
func IsProseSupSub(content string) bool {
	if utf8.RuneCountInString(content) > supsubMaxRunes || len(strings.Fields(content)) > 4 {
		return true
	}
	cjk := 0
	for _, r := range content {
		if unicode.Is(unicode.Han, r) {
			cjk++
			if cjk > supsubMaxCJK {
				return true
			}
		}
	}
	return false
}

// unwrapProseSupSub 解包散文级上下标，st.SupSub 计数。在 Calibrate 的
// 结构性还原阶段调用（与表格转换同类：无损、先于词级修复）。
func unwrapProseSupSub(text string, st *Stats) string {
	unwrap := func(openTag, closeTag string) func(string) string {
		return func(m string) string {
			content := strings.TrimSuffix(strings.TrimPrefix(m, openTag), closeTag)
			if !IsProseSupSub(content) {
				return m // 短内容：合法上下标，原样保留
			}
			st.SupSub++
			st.detail(m + " → " + content)
			return content
		}
	}
	text = proseSupRe.ReplaceAllStringFunc(text, unwrap("<sup>", "</sup>"))
	text = proseSubRe.ReplaceAllStringFunc(text, unwrap("<sub>", "</sub>"))
	return text
}
