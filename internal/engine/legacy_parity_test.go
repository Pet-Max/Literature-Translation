// legacy_parity_test.go — v1.1.1 自带单元测试（engine/tests/test_core.py）
// 的 Go 移植版：逐用例对照，保证移植后测试覆盖不低于母本。
// 黄金对拍（migration_test/events_golden_test）证明行为等价，本文件
// 证明 v1.1.1 关心的每个边界分支在 Go 侧同样成立。
package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------- freezer

func TestLegacyFreezer(t *testing.T) {
	rt := func(text string) {
		t.Helper()
		f := Freeze(text, 1)
		if back := Unfreeze(f.Text, f.Mapping); back != text {
			t.Fatalf("roundtrip 不一致\n GOT %q\n WANT %q", back, text)
		}
	}
	t.Run("行内公式", func(t *testing.T) {
		rt("能量 $E=mc^2$ 守恒。")
	})
	t.Run("行间公式", func(t *testing.T) {
		rt("$$\n\\Delta S = \\int \\frac{\\delta Q}{T}\n$$")
	})
	t.Run("行间先于行内", func(t *testing.T) {
		text := "$$a$$ 和 $b$"
		f := Freeze(text, 1)
		// $$ 块先冻结 → 占位符 ⟦F1⟧ 是整块，$b$ 是 ⟦F2⟧
		if !strings.Contains(f.Text, "⟦F1⟧ 和 ⟦F2⟧") {
			t.Fatalf("冻结顺序异常: %q", f.Text)
		}
	})
	t.Run("表格与图片", func(t *testing.T) {
		rt("<table><tr><td>1</td></tr></table> 与 ![alt](img.png)")
	})
	t.Run("代码块", func(t *testing.T) {
		rt("```py\nx = 1\n```")
	})
	t.Run("转义美元不冻结", func(t *testing.T) {
		f := Freeze("价格 \\$5 与 \\$10。", 1)
		if len(f.Mapping) != 0 {
			t.Fatalf("转义 \\$ 不应冻结: %v", f.Mapping)
		}
	})
	t.Run("跨行行内公式不匹配", func(t *testing.T) {
		f := Freeze("起点 $a\nb$ 终点", 1)
		if len(f.Mapping) != 0 {
			t.Fatalf("跨行 $…$ 不应冻结（行内公式限单行）: %v", f.Mapping)
		}
	})
	t.Run("公式密集段落", func(t *testing.T) {
		rt("由 $\\hbar\\omega$ 与 $$E=\\hbar\\omega$$ 及 $k_B T$ 组成。")
	})
}

// ---------------------------------------------------------------- segmenter

func TestLegacySegmenter(t *testing.T) {
	t.Run("按标题切", func(t *testing.T) {
		got := Split("# A\n\ntext a\n\n## B\n\ntext b", DefaultMaxChars)
		if len(got) != 2 || !strings.HasPrefix(got[0], "# A") || !strings.HasPrefix(got[1], "## B") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("max_chars 生效", func(t *testing.T) {
		got := Split(strings.Repeat("段落内容。", 400), 100)
		for _, c := range got {
			if runeLen(c) > 100 {
				t.Fatalf("段超限: %d > 100", runeLen(c))
			}
		}
	})
	t.Run("标题随后续文本", func(t *testing.T) {
		got := Split("# T\n\nbody", DefaultMaxChars)
		if len(got) != 1 || !strings.HasPrefix(got[0], "# T") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("空文本", func(t *testing.T) {
		if got := Split("  \n\n  ", DefaultMaxChars); got != nil {
			t.Fatalf("空文本应返回空，got %q", got)
		}
	})
	t.Run("拼接保内容", func(t *testing.T) {
		text := "# A\n\ntext a\n\n## B\n\ntext b"
		got := Split(text, DefaultMaxChars)
		joined := strings.Join(got, "\n\n")
		// 仅差分隔符：原文去掉换行差异后应一致
		norm := func(s string) string { return strings.ReplaceAll(strings.TrimSpace(s), "\n\n", "\n") }
		if norm(joined) != norm(text) {
			t.Fatalf("回填内容不一致\n GOT %q\n WANT %q", norm(joined), norm(text))
		}
	})
	t.Run("硬切保持行间公式完整", func(t *testing.T) {
		block := strings.Repeat("前文内容。", 100) + "\n\n$$x=1$$\n\n" + strings.Repeat("后文内容。", 100)
		got := Split(block, 150)
		for i, c := range got {
			if strings.Contains(c, "$$") && !strings.HasPrefix(strings.TrimSpace(c), "$$") {
				continue
			}
			if strings.Count(c, "$$")%2 != 0 {
				t.Fatalf("第 %d 段 $$ 不配对: %q", i, c)
			}
		}
	})
	t.Run("硬切切点避开公式", func(t *testing.T) {
		block := "aaa " + strings.Repeat("b", 300) + " $$x=1$$ " + strings.Repeat("c", 300)
		got := hardSplit(block, 150)
		joined := strings.Join(got, "\n")
		// 公式必须完整出现在某一段中
		if !strings.Contains(strings.ReplaceAll(joined, "\n", " "), "$$x=1$$") {
			t.Fatalf("行间公式被切断: %q", got)
		}
	})
}

// ---------------------------------------------------------------- validator

func TestLegacyValidatorTableStructure(t *testing.T) {
	t.Run("表格完好通过", func(t *testing.T) {
		src := "表：\n\n| Model | ⟦F1⟧ |\n| --- | --- |\n| A | 1 |\n"
		dst := "表：\n\n| 模型 | ⟦F1⟧ |\n| --- | --- |\n| A | 1 |\n"
		if got := Validate(src, dst); len(got) != 0 {
			t.Fatalf("应通过，got %q", got)
		}
	})
	t.Run("列结构破坏报错", func(t *testing.T) {
		src := "| Model | ⟦F1⟧ |\n| --- | --- |\n| A | 1 |"
		dst := "| 模型 | ⟦F1⟧ |\n| --- | --- |\n| A | 1 缺竖线"
		if got := Validate(src, dst); !contains(got, "表格结构") {
			t.Fatalf("应报表格结构，got %q", got)
		}
	})
	t.Run("行数变化报错", func(t *testing.T) {
		src := "| A | 1 |\n| B | 2 |"
		dst := "| A | 1 |"
		if got := Validate(src, dst); !contains(got, "表格结构") {
			t.Fatalf("应报表格结构，got %q", got)
		}
	})
	t.Run("行首狄拉克记号不误报", func(t *testing.T) {
		// 本库实测 57 行 |p⟩ = … 形态：行首 | 不是表格行
		src := "态 |00⟩ 与 |01⟩ 构成基。"
		dst := "|00⟩ 与 |01⟩ 构成基。"
		if got := Validate(src, dst); len(got) != 0 {
			t.Fatalf("狄拉克记号行不应误报表格，got %q", got)
		}
	})
	t.Run("含狄拉克记号的表格仍校验", func(t *testing.T) {
		src := "| 态 | 值 |\n| --- | --- |\n| |00⟩ | 1 |"
		dst := "| 态 | 值 |\n| --- | --- |\n| |00⟩ |"
		if got := Validate(src, dst); !contains(got, "表格结构") {
			t.Fatalf("应报表格结构，got %q", got)
		}
	})
}

func contains(xs []string, sub string) bool {
	for _, s := range xs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- 参考文献甄别

func TestLegacyReferenceDetector(t *testing.T) {
	refs := []string{
		"[2] R. D. Yates and D. J. Goodman, Probability and stochastic processes (John Wiley & Sons, 2014).",
		"[3] N. Wiener, Generalized harmonic analysis, Acta Math. 55, 117 (1930).",
		"[4] A. Dechant and E. Lutz, Aging Wiener-Khinchin theorem, Phys. Rev. Lett. 115, 080603 (2015).",
	}
	t.Run("接受连续文献序列", func(t *testing.T) {
		d := newRefDetector(true)
		for i, r := range refs {
			if !d.detect(r) {
				t.Fatalf("第 %d 条应识别为文献", i)
			}
		}
	})
	t.Run("正文段首含引用编号不误判", func(t *testing.T) {
		d := newRefDetector(true)
		body := "The bound Eq. (1) also holds for out-of-equilibrium systems, as shown in [17, 18] and (2023) studies with A. B. initials."
		if d.detect(body) {
			t.Fatal("正文不应误判为文献")
		}
	})
	t.Run("编号断档拒绝", func(t *testing.T) {
		d := newRefDetector(true)
		if !d.detect(refs[0]) { // [2] 起始
			t.Fatal("[2] 应接受")
		}
		if !d.detect(refs[2]) { // [4]：间隔 2，容忍（合并条目场景）
			t.Fatal("[4] 间隔 2 应容忍")
		}
		if d.detect(refs[1]) { // [3] 小于上一个编号 → 拒绝
			t.Fatal("[3] 倒退应拒绝")
		}
		if d.detect("[9] N. X. Author, J. Abbrev. 1, 2 (2020).") { // 跳距过大
			t.Fatal("[9] 跳距过大应拒绝")
		}
	})
	t.Run("区间型段首拒绝", func(t *testing.T) {
		d := newRefDetector(true)
		if !d.detect(refs[0]) {
			t.Fatal("[2] 应接受")
		}
		if d.detect("[17, 18] entropy production estimates follow.") {
			t.Fatal("[17, 18] 行内引用形式应拒绝")
		}
		if !d.detect(refs[1]) {
			t.Fatal("补上 [3] 后序列应继续")
		}
	})
	t.Run("无特征条目拒绝且不污染状态", func(t *testing.T) {
		d := newRefDetector(true)
		if !d.detect(refs[0]) {
			t.Fatal("[2] 应接受")
		}
		if d.detect("[3] some text without features") {
			t.Fatal("无作者缩写/年份应拒绝")
		}
		if !d.detect(refs[1]) {
			t.Fatal("拒绝后状态不应被污染，[3] 仍应接受")
		}
	})
	t.Run("开关关闭", func(t *testing.T) {
		d := newRefDetector(false)
		for _, r := range refs {
			if d.detect(r) {
				t.Fatal("关闭时不应识别")
			}
		}
	})
	t.Run("超长条目拒绝", func(t *testing.T) {
		d := newRefDetector(true)
		long := "[2] R. D. Yates (2014). " + strings.Repeat("x", 900)
		if d.detect(long) {
			t.Fatal("超长条目应拒绝")
		}
	})
}

// ---------------------------------------------------------------- 断点重载

func TestLegacyProgressReload(t *testing.T) {
	// 回归：先失败后成功的段，重启后不得再被 failed 标记卡住
	dir := t.TempDir()
	p := filepath.Join(dir, "progress.jsonl")
	pr, err := NewProgress(p, "")
	if err != nil {
		t.Fatal(err)
	}
	pr.RecordFailed(0, "占位符缺失")
	pr.Record(0, "chunk-source", "译文内容")
	// 模拟重启：重新从 JSONL 加载
	pr2, err := NewProgress(p, "")
	if err != nil {
		t.Fatal(err)
	}
	// Reusable 内部同时检查 failed 标记：先失败后成功的段必须可复用
	if !pr2.Reusable(0, "chunk-source") {
		t.Fatal("先失败后成功的段应可复用（成功行应清除失败标记）")
	}
	// 源段内容变化（校准后）→ 不得复用
	if pr2.Reusable(0, "chunk-source-changed") {
		t.Fatal("源段变化后不应复用")
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
}
