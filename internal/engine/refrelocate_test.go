package engine

import (
	"strings"
	"testing"
)

// 六条样例文献（与真实归档 paper.md 同构：年份特征齐全，均 <800 字符）。
var (
	ref1 = `[1] C. Ionescu, R. Delacroix, and M. Sato, “Programmable dissipation in ion-chain simulators,” Phys. Rev. Lett. 130, 120601 (2023).`
	ref2 = `[2] A. Ferrand and K. Nakagawa, “Engineered dissipation as a resource in analog quantum simulation,” PRX Quantum 5, 010312 (2024).`
	ref3 = `[3] G. Almeida and S. Bhatt, “Multipartite entanglement certification for analog simulators,” Phys. Rev. A 109, 022415 (2024).`
	ref4 = `[4] H. Ozawa and G. Wirth, “Quantum Fisher information from reconstructed two-site states,” Phys. Rev. A 110, 023301 (2024).`
	ref5 = `[5] L. Prieto and D. Ferreira, “Subradiant edge modes in driven dissipative chains,” Quantum 8, 1221 (2024).`
	ref6 = `[6] Y. El-Amin, M. Thalberg, and W.-J. Bao, “Thalyon-16: A 16-ion simulator with programmable dissipation,” Rev. Sci. Instrum. 95, 083202 (2024).`
)

// TestRelocateSampleLayout 样例结构复现：[1][2][3] 夹在正文中间、[4][5][6]
// 在文末 → 归位后文末必须是 [1]…[6] 升序（R5），正文块逐字零改动。
func TestRelocateSampleLayout(t *testing.T) {
	md := strings.Join([]string{
		"# Dissipation-Assisted Entanglement Dynamics",
		"",
		"Intro paragraph about the experiment.",
		"",
		"## III. RESULTS",
		"",
		"Neighboring-site entanglement is quantified below.",
		"",
		ref1, "", ref2, "", ref3,
		"",
		"## IV. DISCUSSION",
		"",
		"State reconstruction uses 4800 shots per setting.",
		"",
		"$$",
		"F (rho, sigma) = 1",
		"$$",
		"",
		"Conclusion sentence.",
		"",
		ref4, "", ref5, "", ref6,
	}, "\n")

	got, moved := RelocateReferences(md)
	if moved != 3 {
		t.Fatalf("moved = %d, want 3（仅统计移位条目，文末原有 3 条不计）", moved)
	}
	want := strings.Join([]string{
		"# Dissipation-Assisted Entanglement Dynamics",
		"",
		"Intro paragraph about the experiment.",
		"",
		"## III. RESULTS",
		"",
		"Neighboring-site entanglement is quantified below.",
		"",
		"## IV. DISCUSSION",
		"",
		"State reconstruction uses 4800 shots per setting.",
		"",
		"$$",
		"F (rho, sigma) = 1",
		"$$",
		"",
		"Conclusion sentence.",
		"",
		ref1, "", ref2, "", ref3, "", ref4, "", ref5, "", ref6,
	}, "\n")
	if got != want {
		t.Fatalf("relocated md mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestRelocateIdempotent 文献已在文末且升序 → 逐字节原样返回（R6，hash 不变）。
func TestRelocateIdempotent(t *testing.T) {
	md := strings.Join([]string{
		"# Paper",
		"",
		"Body text.",
		"",
		ref1, "", ref2, "", ref3, "", ref4, "", ref5, "", ref6,
	}, "\n")
	got, moved := RelocateReferences(md)
	if moved != 0 {
		t.Fatalf("moved = %d, want 0", moved)
	}
	if got != md {
		t.Fatal("idempotent case must return input byte-for-byte")
	}
}

// TestRelocateNoRefs 无 [N] 文档 → 逐字节不变。
func TestRelocateNoRefs(t *testing.T) {
	md := "Body text.\n\nMore body.\n\n$$\nx=1\n$$"
	got, moved := RelocateReferences(md)
	if moved != 0 || got != md {
		t.Fatalf("no-ref doc must be untouched (moved=%d)", moved)
	}
}

// TestRelocateGuards 防误伤组：各陷阱块一律不动。
func TestRelocateGuards(t *testing.T) {
	t.Run("区间型段首", func(t *testing.T) {
		trap := `[17, 18] bracket-range heads are inline citations, not entries.`
		md := strings.Join([]string{"Body.", "", ref1, "", ref2, "", ref3, "", trap}, "\n")
		got, moved := RelocateReferences(md)
		if moved != 3 {
			t.Fatalf("moved = %d, want 3", moved)
		}
		if !strings.Contains(got, "Body.\n\n"+trap) {
			t.Fatalf("trap block must stay in place:\n%s", got)
		}
	})
	t.Run("孤立条目低于门槛", func(t *testing.T) {
		lone := `[7] W. Smith, A lonely entry that looks like a reference (2024).`
		md := "Body.\n\n" + lone + "\n\nMore body."
		got, moved := RelocateReferences(md)
		if moved != 0 || got != md {
			t.Fatalf("fewer than 3 entries must be untouched (moved=%d)", moved)
		}
	})
	t.Run("编号倒退拒收", func(t *testing.T) {
		back := `[3] G. Almeida and S. Bhatt, out-of-order entry, Phys. Rev. A 1, 2 (2024).`
		md := strings.Join([]string{"Body.", "", ref1, "", ref2, "", ref5, "", back}, "\n")
		got, moved := RelocateReferences(md)
		if moved != 3 {
			t.Fatalf("moved = %d, want 3 ([3] rejected after [5])", moved)
		}
		if !strings.Contains(got, "Body.\n\n"+back) {
			t.Fatalf("out-of-order block must stay in place:\n%s", got)
		}
	})
	t.Run("无特征段拒收", func(t *testing.T) {
		plain := `[4] This paragraph starts like an entry but has no author initials or year marker at all.`
		md := strings.Join([]string{"Body.", "", ref1, "", ref2, "", plain, "", "More body.", "", ref3}, "\n")
		got, moved := RelocateReferences(md)
		if moved != 2 {
			t.Fatalf("moved = %d, want 2（ref3 已在文末，不计移位）", moved)
		}
		if !strings.Contains(got, "Body.\n\n"+plain+"\n\nMore body.") {
			t.Fatalf("featureless block must stay in place:\n%s", got)
		}
	})
	t.Run("含公式块不搬移", func(t *testing.T) {
		withEq := ref3 + "\n$$\nE = h nu\n$$"
		md := strings.Join([]string{"Body.", "", ref1, "", ref2, "", withEq, "", "More body.", "", ref4}, "\n")
		got, moved := RelocateReferences(md)
		if moved != 2 {
			t.Fatalf("moved = %d, want 2 (formula-bearing block rejected)", moved)
		}
		if !strings.Contains(got, "Body.\n\n"+withEq+"\n\nMore body.") {
			t.Fatalf("protected-span block must stay in place:\n%s", got)
		}
	})
}

// TestRelocateMultiEntryBlock 多条挤进一个块 → 整块搬移、内部单换行保留（R8）。
func TestRelocateMultiEntryBlock(t *testing.T) {
	twin := ref1 + "\n" + ref2
	md := strings.Join([]string{"Body.", "", twin, "", ref3, "", "More body.", "", ref4}, "\n")
	got, moved := RelocateReferences(md)
	if moved != 2 {
		t.Fatalf("moved = %d, want 2（ref4 已在文末，不计移位）", moved)
	}
	wantTail := twin + "\n\n" + ref3 + "\n\n" + ref4
	if !strings.HasSuffix(got, wantTail) {
		t.Fatalf("tail must keep the twin block verbatim:\n%s", got)
	}
	if !strings.HasPrefix(got, "Body.\n\nMore body.") {
		t.Fatalf("body must stay at head in original order:\n%s", got)
	}
}

// TestRelocateTrailingBlanks 文末空行不得破坏幂等（P1），也不得丢失原结尾。
func TestRelocateTrailingBlanks(t *testing.T) {
	tail := strings.Join([]string{ref1, ref2, ref3}, "\n\n")
	// (a) 文献已在文末 + 末尾空行 → 原样返回
	in := "Body.\n\n" + tail + "\n\n"
	if got, moved := RelocateReferences(in); moved != 0 || got != in {
		t.Fatalf("trailing blanks must stay idempotent (moved=%d)", moved)
	}
	// (b) 文献在中部 + 末尾空行 → 归位且保留原结尾
	md := "Body.\n\n" + tail + "\n\nMore body.\n\n"
	got, moved := RelocateReferences(md)
	if moved != 3 {
		t.Fatalf("moved = %d, want 3", moved)
	}
	want := "Body.\n\nMore body.\n\n" + tail + "\n\n"
	if got != want {
		t.Fatalf("suffix must be preserved:\n%q", got)
	}
}

// TestRelocateTailFeaturelessRef 尾部特征缺失的文献不触发误归位（P2）。
func TestRelocateTailFeaturelessRef(t *testing.T) {
	f4 := `[4] A featureless tail entry whose year was lost in parsing.`
	md := "Body.\n\n" + strings.Join([]string{ref1, ref2, ref3}, "\n\n") + "\n\n" + f4
	if got, moved := RelocateReferences(md); moved != 0 || got != md {
		t.Fatalf("featureless tail ref must not trigger relocation (moved=%d)", moved)
	}
}

// TestRelocateTailAfterTwinBlock L1 区分性用例：尾部续接判定必须用多行块的
// maxN（状态机游标）而非 firstN——twin 块 [3]+[5] 后跟特征缺失的 [7] 尾块，
// maxN 口径下 5<7≤8 判"已就位"（no-op）；firstN 口径下 7>3+3 会误触发归位。
func TestRelocateTailAfterTwinBlock(t *testing.T) {
	twin := ref3 + "\n" + ref5
	tail := `[7] A featureless tail entry whose number continues the sequence.`
	md := "Body.\n\n" + strings.Join([]string{ref1, ref2, twin}, "\n\n") + "\n\n" + tail
	if got, moved := RelocateReferences(md); moved != 0 || got != md {
		t.Fatalf("twin-block tail with continuing featureless [7] must be a no-op (moved=%d)", moved)
	}
}

// TestRelocateThenDetect 联动：归位后经 Split + refDetector，每条命中保留。
func TestRelocateThenDetect(t *testing.T) {
	md := strings.Join([]string{
		"Body.", "", ref1, "", ref2, "", ref3, "",
		"State reconstruction uses 4800 shots per setting.",
		"", ref4, "", ref5, "", ref6,
	}, "\n")
	relocated, _ := RelocateReferences(md)
	chunks := Split(relocated, DefaultMaxChars)
	detector := newRefDetector(true)
	hits := 0
	for _, c := range chunks {
		if detector.detect(c) {
			hits++
		}
	}
	if hits != 6 {
		t.Fatalf("detect hits = %d, want 6", hits)
	}
}
