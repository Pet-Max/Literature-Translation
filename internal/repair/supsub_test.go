// supsub_test.go — 散文级上下标解包：MinerU 误包整句的真实实例回归 +
// 合法短上下标不受影响 + 词数阈值 + 解包后词级校准自动收尾
// （hdemonstrate → demonstrate，编辑距离 1 唯一候选）。
package repair

import (
	"strings"
	"testing"
)

func TestUnwrapProseSupSub(t *testing.T) {
	st := Stats{}
	in := "Our results h<sub>demonstrate that thermal machines can be reduced to the ultimate limit of single atoms.</sub>"
	out := unwrapProseSupSub(in, &st)
	if want := "Our results hdemonstrate that thermal machines can be reduced to the ultimate limit of single atoms."; out != want {
		t.Fatalf("解包结果不符:\n got: %s\nwant: %s", out, want)
	}
	if st.SupSub != 1 {
		t.Fatalf("SupSub = %d, 期望 1", st.SupSub)
	}
	if len(st.Details) == 0 {
		t.Fatal("解包应留下 detail 记录供人工抽查")
	}
}

func TestUnwrapProseSupSub_KeepsLegitShort(t *testing.T) {
	in := "H<sub>2</sub>O 与 ω<sub>r</sub>，Obinna Abah,<sup>2</sup>、Kilian Singer<sup>1,</sup> <sup>∗</sup>"
	st := Stats{}
	if out := unwrapProseSupSub(in, &st); out != in {
		t.Fatalf("合法短上下标被误伤:\n got: %s\nwant: %s", out, in)
	}
	if st.SupSub != 0 {
		t.Fatalf("SupSub = %d, 期望 0", st.SupSub)
	}
}

func TestUnwrapProseSupSub_WordCountThreshold(t *testing.T) {
	// 长度未超限但词数超限（>4 词）同样解包：短而散的正文片段不是角标
	st := Stats{}
	if out := unwrapProseSupSub("x<sub>a b c d e f</sub>", &st); out != "xa b c d e f" {
		t.Fatalf("词数超限未解包: %q", out)
	}
	if st.SupSub != 1 {
		t.Fatalf("SupSub = %d, 期望 1", st.SupSub)
	}
}

func TestUnwrapProseSupSub_CJKThreshold(t *testing.T) {
	// 中文译文没有词间空格（词数判据失效），长度 16 rune 也远未到拉丁
	// 上限——必须按 CJK 字符数判散文，否则整句重新变成小号下标
	// （实证形态：LLM 把下标内容也翻译了，h<sub>表明，……极限。</sub>）。
	st := Stats{}
	in := "我们的结果 h<sub>表明，热机可以缩减到单原子的极限。</sub>"
	// 标签外的游离 h 不属解包职责（与英文用例 "h"+"demonstrate" 粘连同构，
	// 英文由词级校准收尾；中文侧的孤立拉丁字母留给人工）。
	want := "我们的结果 h表明，热机可以缩减到单原子的极限。"
	if out := unwrapProseSupSub(in, &st); out != want {
		t.Fatalf("CJK 散文下标未解包:\n got: %s\nwant: %s", out, want)
	}
	if st.SupSub != 1 {
		t.Fatalf("SupSub = %d, 期望 1", st.SupSub)
	}
	// 合法短 CJK 角标（≤4 字）不受影响
	st2 := Stats{}
	keep := "临界温度记作 T<sub>特</sub>，见注<sup>注 1</sup>"
	if out := unwrapProseSupSub(keep, &st2); out != keep {
		t.Fatalf("合法短 CJK 角标被误伤:\n got: %s", out)
	}
	if st2.SupSub != 0 {
		t.Fatalf("SupSub = %d, 期望 0", st2.SupSub)
	}
}

// 用户实例端到端：结构解包后，词级校准把粘连出的 "hdemonstrate"
// 修回 "demonstrate"（编辑距离 1、词表内唯一候选）。
func TestCalibrateUnwrapsProseSubscript(t *testing.T) {
	orig := "We report the experimental realization. Our results demonstrate that thermal machines can be reduced to the ultimate limit of single atoms."
	in := "Our results h<sub>demonstrate that thermal machines can be reduced to the ultimate limit of single atoms.</sub>"
	res, err := Calibrate(in, orig, Options{Spelling: true, Symbols: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Text, "<sub>") {
		t.Fatalf("下标标签未解包: %s", res.Text)
	}
	if !strings.Contains(res.Text, "Our results demonstrate that") {
		t.Fatalf("游离基词未被词级校准收尾: %s", res.Text)
	}
	if res.Stats.SupSub != 1 {
		t.Fatalf("SupSub = %d, 期望 1", res.Stats.SupSub)
	}
}
