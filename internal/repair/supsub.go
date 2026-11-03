// supsub.go — 散文级 HTML 上下标解包：MinerU 的 VLM 偶发把整句话包成
// 前面一个游离字符的下标（实证文献的摘要末句
// "Our results h<sub>demonstrate that thermal machines …</sub>"，PDF 文本层
// 证实原句并无 h）。合法上下标都很短——化学式（H<sub>2</sub>O）、变量角标
// （ω<sub>r</sub>）、作者角标（<sup>1,</sup> <sup>∗</sup>）——内容超长或
// 词数超限即判定为解析错误：去标签保留内容，交回词级校准收尾（解包后
// 可能粘连出的 "hdemonstrate" 与 "demonstrate" 恰为编辑距离 1，唯一候选
// 时由拼写规则自动修正）。
package repair

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	proseSupRe = regexp.MustCompile(`<sup>(.*?)</sup>`)
	proseSubRe = regexp.MustCompile(`<sub>(.*?)</sub>`)
)

// supsubMaxRunes 合法上下标的内容长度上限（rune 计），超过即视为散文误包。
const supsubMaxRunes = 30

// unwrapProseSupSub 解包散文级上下标，st.SupSub 计数。在 Calibrate 的
// 结构性还原阶段调用（与表格转换同类：无损、先于词级修复）。
func unwrapProseSupSub(text string, st *Stats) string {
	unwrap := func(openTag, closeTag string) func(string) string {
		return func(m string) string {
			content := strings.TrimSuffix(strings.TrimPrefix(m, openTag), closeTag)
			if utf8.RuneCountInString(content) <= supsubMaxRunes && len(strings.Fields(content)) <= 4 {
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
