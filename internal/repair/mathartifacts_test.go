// mathartifacts_test.go — 数学区伪影归一：全库语料真实实例回归 + 误伤
// 边界锁定（同位素 ^{40}Ca⁺、尾注 ^{,9}/^{9}、math 外伪影、行间公式 R4 豁免）。
package repair

import (
	"strings"
	"testing"
)

// TestMathArtifacts_CorpusCases 语料实例回归：每条规则对应真实文献中
// 出现过的原始形态。
func TestMathArtifacts_CorpusCases(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		// R1：间距宏裂开（"3.4 : mK" 冒号伪影的实证形态）
		{"R1 间距宏", "$T _ { C } = 3 . 4 \\ : \\mathrm { mK }$", "$T _ { C } = 3 . 4 \\: \\mathrm { mK }$"},
		// R3：引用撕裂（点在 math 内/外两种实证变体）
		{"R3 带内点", "from $\\operatorname { E q . }$ . (3) accounting", "from Eq. (3) accounting"},
		{"R3 点在外", "of $\\operatorname { E q } .$ . (2) does", "of Eq. (2) does"},
		{"R3 无编号", "per $\\operatorname { E q . }$ below", "per Eq. below"},
		// R4：伪上标（某图注 "4 V," 被整体上标）
		{"R4 伪上标", "amplitude of $^ { 4 \\mathrm { V } , }$ we", "amplitude of 4 V, we"},
		{"R4 分号收尾", "noise of $^ { 2 \\mathrm { mW } ; }$ then", "noise of 2 mW; then"},
		// R4 双句读去重：MinerU 把逗号既包进伪上标又落在 math 外（实证形态）
		{"R4 双逗号去重", "amplitude of $^ { 4 \\mathrm { V } , }$ , we calculate", "amplitude of 4 V, we calculate"},
		// R5：公式后冗余下标残片（\bar{n}_r 后跟 <sub>r</sub> 的实证形态）
		{"R5 冗余残片", "$\\bar { n } _ { \\mathrm { r } } = k _ { B } T / \\hbar \\omega _ { i }$ <sub>r</sub> 随",
			"$\\bar { n } _ { \\mathrm { r } } = k _ { B } T / \\hbar \\omega _ { i }$ 随"},
		{"R5 裸下标", "where $E _ { kin }$ <sub>kin</sub> is", "where $E _ { kin }$ is"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := Stats{}
			if got := fixMathArtifacts(c.in, &st); got != c.want {
				t.Fatalf("\n got: %s\nwant: %s", got, c.want)
			}
			if st.MathFix == 0 {
				t.Fatal("命中应计入 MathFix")
			}
		})
	}
}

// TestMathArtifacts_Boundaries 误伤边界：合法结构必须原样通过。
func TestMathArtifacts_Boundaries(t *testing.T) {
	st := Stats{}
	keep := []string{
		"$2^{66}$ and $x^{99}$",
		"$2^ { 6 6 }$ and $x^ { 9 9 }$",
		// 同位素记法：^{40}Ca⁺ 是真实上标（内容非句读结尾，R4 不触发）
		"a single $^ { 4 0 } \\mathrm { { \\bar { C } a } ^ { + } }$ ion",
		// 尾注号：逗号在头（^{,9}）与单数字（^{9}）都不是引号形态
		"$\\eta ^ { , 9 }$ and referenced$^ { 9 }$ here",
		// math 外的伪影字符不处理，真实次幂也不能仅凭数字形态改成引号。
		"plain \\ : text and 之外的 ^ { 6 6 } 不动",
		// R4 仅行内：行间公式豁免
		"$$^ { 4 \\mathrm { V } , }$$",
		// R5 非重复下标：前式没有该下标时残片保留
		"$\\omega _ { i }$ <sub>r</sub> 保持",
	}
	for _, s := range keep {
		if got := fixMathArtifacts(s, &st); got != s {
			t.Fatalf("误伤:\n in: %s\ngot: %s", s, got)
		}
	}
	if st.MathFix != 0 {
		t.Fatalf("边界用例不应计数，MathFix = %d", st.MathFix)
	}
}

// TestCalibrateMathArtifacts 端到端：伪影归一在符号层后、词级修复前生效，
// 词级修复不得破坏其结果（公式在词级阶段冻结）。
func TestCalibrateMathArtifacts(t *testing.T) {
	orig := "The temperature of the ion is 3.4 mK. See the equation above."
	in := "The temperature is $T _ { C } = 3 . 4 \\ : \\mathrm { mK }$ and from $\\operatorname { E q . }$ . (3) we get it."
	res, err := Calibrate(in, orig, Options{Spelling: true, Symbols: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, `3 . 4 \: \mathrm { mK }`) {
		t.Fatalf("R1 未生效: %s", res.Text)
	}
	if !strings.Contains(res.Text, "Eq. (3)") {
		t.Fatalf("R3 未生效: %s", res.Text)
	}
}

func TestSymbolsOffPreservesMathWithSpellingOn(t *testing.T) {
	in := "Different $T = 3 \\ : 4$ and $2^{66}$ and $x^{99}$ here."
	got, err := Calibrate(in, "Different T and here.", Options{Spelling: true, Symbols: false})
	if err != nil {
		t.Fatal(err)
	}
	for _, formula := range []string{"$T = 3 \\ : 4$", "$2^{66}$", "$x^{99}$"} {
		if !strings.Contains(got.Text, formula) {
			t.Fatalf("math changed with symbols off: %s", got.Text)
		}
	}
	if got.Stats.MathFix != 0 || got.Stats.Display != 0 {
		t.Fatal(got.Stats)
	}
}

func TestSymbolsOnPreservesLegitimatePowers(t *testing.T) {
	in := "The powers $2^{66}$ and $x^{99}$ are exact."
	got, err := Calibrate(in, in, Options{Symbols: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, formula := range []string{"$2^{66}$", "$x^{99}$"} {
		if !strings.Contains(got.Text, formula) {
			t.Fatalf("legitimate power changed: %s", got.Text)
		}
	}
}
