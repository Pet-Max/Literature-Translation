package repair

import (
	"strings"
	"testing"
)

// TestAnchorFunctionWordGate 上下文锚定的功能词闸：PDF 文本层里的两字母
// 物理符号（BN/Ni/Hf…）都在词表里，无闸时漂移区会把 and/of/for 类功能词
// 改成符号碎片（一本书实测约 70 处）。闸后：碎片目标与功能词源词
// （编辑距离>1）一律不替换；真词损伤（Cornwal→Cornwall）不受影响。
func TestAnchorFunctionWordGate(t *testing.T) {
	orig := "the signal and the noise differ markedly in scale while bn the phase stays stable. " +
		"Cornwall hosted the workshop on mesoscopic transport. " +
		"dependence of the form factor is evaluated for the lowest band. "
	md := "the signal and the noise differ markedly in scale while and the phase stays stable. " +
		"Cornwal hosted the workshop on mesoscopic transport. " +
		"dependence ofy the form factor is evaluated for the lowest band. "
	got, err := Calibrate(md, orig, both)
	if err != nil {
		t.Fatal(err)
	}
	// and→bn（目标碎片，len<3）、of→ofy（功能词源、距离 2）不得替换；
	// Cornwal→Cornwall 照常修复
	want := "the signal and the noise differ markedly in scale while and the phase stays stable. " +
		"Cornwall hosted the workshop on mesoscopic transport. " +
		"dependence ofy the form factor is evaluated for the lowest band. "
	if got.Text != want {
		t.Errorf("got:\n%s\nwant:\n%s\ndetails: %v", got.Text, want, got.Stats.Details)
	}
	if got.Stats.Ctx != 1 {
		t.Errorf("Ctx = %d, want 1（仅 Cornwall）(details: %v)", got.Stats.Ctx, got.Stats.Details)
	}
}

// TestCalibrateDeterministic 校准必须确定性：同一输入多次运行输出逐字节
// 一致（对称互改对互斥曾依赖 map 遍历顺序，同一输入两次运行修正集不同）。
func TestCalibrateDeterministic(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 12; i++ {
		sb.WriteString("quasiprobability approaches over a natural framework for the driven system. ")
		sb.WriteString("engineered via controlled driving yields sharp resonances. ")
		sb.WriteString("A nonzero correction quantities the deviation from the classical symmetry relation. ")
		sb.WriteString("The of-diagonal elements are parametrized by the coupling strength. ")
		sb.WriteString("Cornwal reports the transition rates while bn the phase stays stable. ")
	}
	md := sb.String()
	orig := strings.NewReplacer(
		"over a natural", "offer a natural",
		"driving yields", "driving fields",
		"of-diagonal", "off-diagonal",
		"Cornwal reports", "Cornwall reports",
		"while bn the", "while and the",
	).Replace(md)
	var first string
	for run := 0; run < 3; run++ {
		got, err := Calibrate(md, orig, both)
		if err != nil {
			t.Fatal(err)
		}
		if run == 0 {
			first = got.Text
			continue
		}
		if got.Text != first {
			t.Fatalf("第 %d 次校准输出与第 1 次不一致（非确定性）:\n%s\n---\n%s", run+1, first, got.Text)
		}
	}
}
