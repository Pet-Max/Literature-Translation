package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCJKRatio(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool // IsChineseText
	}{
		{"纯英文", "Power spectral density constrains the heat current.", false},
		{"纯中文", "功率谱密度对热流的约束及其在非平衡系统中的表现。", true},
		{"英文+公式", "The entropy production $\\sigma = \\dot{Q}/T$ grows with time.", false},
		{"英文夹中文参考文献", "Smith et al. studied heat transport. 参考文献 [12] 张三, 李四. 物理学报, 2020. See also Appendix.", false},
		{"中英混排双语", "功率谱密度 The power spectral density 约束了热流。", true},
		{"只有数字符号", "123 $x^2$ 45.6 (7)", false}, // 无有效字符→0→非中文
		{"空文本", "", false},
		// 通用性及多语言/双语边界用例
		{"公式与图片密集纯中文", "见图 ![fig1](images/0123456789abcdef0123456789abcdef0123456789abcdef.jpg)，其中 $\\alpha_1 + \\beta_2 = \\gamma_{eff}$ 给出精确收敛解。参考文献 J. Smith et al.", true},
		{"双语对照译文含英文引用块", "系统满足守恒律。\n\n> The system satisfies conservation laws with precision over long interaction times in multi-qubit chains.\n\n实验验证了上述结论。", true},
		{"日文混合汉字与假名不误拒", "量子力学における基礎方程式とシュレディンガー方程式の解析解について説明する。", false},
		{"法文或德文纯拉丁", "Über die Elektrodynamik bewegter Körper und die Quantentheorie.", false},
	}
	for _, c := range cases {
		if got := IsChineseText(c.text); got != c.want {
			t.Errorf("%s: IsChineseText=%v want %v (ratio=%.2f)", c.name, got, c.want, CJKRatio(c.text))
		}
	}
}

func TestFileIsChinese(t *testing.T) {
	dir := t.TempDir()
	en := filepath.Join(dir, "en.md")
	zh := filepath.Join(dir, "zh.md")
	empty := filepath.Join(dir, "empty.md")
	os.WriteFile(en, []byte("Full counting statistics and coherences in heat transport."), 0o644)
	os.WriteFile(zh, []byte("全文计数统计与相干性：热输运中的涨落对称性。"), 0o644)
	os.WriteFile(empty, nil, 0o644)

	if FileIsChinese(en) {
		t.Error("英文 md 被误判为中文")
	}
	if !FileIsChinese(zh) {
		t.Error("中文 md 未被识别")
	}
	if FileIsChinese(empty) {
		t.Error("空文件不应判为中文")
	}
	if FileIsChinese(filepath.Join(dir, "不存在.md")) {
		t.Error("读不到的文件不应判为中文（放行原则）")
	}
}
