package repair

import (
	"strings"
	"testing"
)

func testVocab() map[string]struct{} {
	words := []string{
		"different", "diffusion", "timescales", "configuration", "short", "and",
		"long", "time", "domain", "one", "to", "the", "bounded", "behavior",
		"steady", "state", "fluctuations", "observable", "power", "spectral",
		"density", "entropy", "production", "equilibrium", "maxwell", "however",
		"research", "arch", "there", "here",
	}
	vocab := make(map[string]struct{}, len(words))
	for _, w := range words {
		vocab[w] = struct{}{}
	}
	return vocab
}

func TestMarkdown(t *testing.T) {
	vocab := testVocab()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"ff丢字母", "the diferent timescales", "the different timescales"},
		{"ff丢字母2", "difusion matrix", "diffusion matrix"},
		{"大小写保留", "Diferent results", "Different results"},
		{"粘连拆分", "the timedomain behavior", "the time domain behavior"},
		{"连字丢段", "one-toone relation", "one-to one relation"},
		{"相邻合并", "the configura tion file", "the configuration file"},
		{"正确词不动", "the diffusion process", "the diffusion process"},
		{"大写正确词不动", "Short and long", "Short and long"},
		{"公式保护", "given by $S^{z}(\\omega)$ difusion", "given by $S^{z}(\\omega)$ diffusion"},
		{"代码块保护", "```py\n\tdiferent\n``` difusion", "```py\n\tdiferent\n``` diffusion"},
		{"标签保护", "<sup>1</sup> difusion", "<sup>1</sup> diffusion"},
		{"the re 不误合并", "the re mark", "the re mark"}, // re 在词表?不在;the+re: the∈词表→不合并;re→无候选→不动
		{"误合并防护", "the arch ive", "the arch ive"},     // arch∈词表→不合并进 there
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Markdown(c.in, vocab)
			if err != nil {
				t.Fatal(err)
			}
			if got.Text != c.want {
				t.Errorf("got %q want %q (stats: %+v)", got.Text, c.want, got.Stats)
			}
		})
	}
}

func TestEditDistanceLE1(t *testing.T) {
	if !editDistanceLE1("diferent", "different") {
		t.Error("插入 f 应为距离1")
	}
	if !editDistanceLE1("diferent", "diferents") {
		t.Error("尾部插入应为距离1")
	}
	if editDistanceLE1("abc", "abc") {
		t.Error("相同词距离为0")
	}
	if editDistanceLE1("abc", "abcdx") {
		t.Error("距离2应拒绝")
	}
}

func TestHyphenJoinVocab(t *testing.T) {
	// 原文带连字符断行时，碎片不得进词表；拼接词必须进词表；
	// 悬挂连字符（两段各自成词）必须保持分离，不得制造假词
	orig := ("The thermodynamic uncer-\ntainty relation yields a proper result-\ning flow " +
		"configu-\nration. The short-\nand long-time behavior. " +
		"The short is short, and the domain is a domain.")
	vocab := BuildVocab(orig)
	for _, frag := range []string{"uncer", "tainty", "configu", "ration", "shortand", "timedomain"} {
		if _, ok := vocab[frag]; ok {
			t.Errorf("碎片/假词 %q 不应进词表", frag)
		}
	}
	for _, w := range []string{"uncertainty", "resulting", "configuration", "proper", "short", "and", "domain"} {
		if _, ok := vocab[w]; !ok {
			t.Errorf("词 %q 应在词表中", w)
		}
	}
	// 用该词表校准一段含同样损伤的 MD
	md := "the uncer tainty of the result ing flow configura tion space"
	got, err := Markdown(md, vocab)
	if err != nil {
		t.Fatal(err)
	}
	want := "the uncertainty of the resulting flow configuration space"
	if got.Text != want {
		t.Errorf("got %q want %q", got.Text, want)
	}
}

func TestRealWorldSamples(t *testing.T) {
	// 模拟 MinerU 真实输出的句子
	vocab := testVocab()
	vocab["reveal"] = struct{}{}
	vocab["influence"] = struct{}{}
	in := "can reveal the influence of diferent timescales"
	got, _ := Markdown(in, vocab)
	if !strings.Contains(got.Text, "different") {
		t.Errorf("真实句样例未修复: %q", got.Text)
	}
}
