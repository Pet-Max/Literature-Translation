package repair

import (
	"regexp"
	"strings"
	"testing"
)

// assertProtectedSpansUnchanged 验证输入中的所有围栏代码、行内反引号与 URL 在校准前后逐字节不变。
func assertProtectedSpansUnchanged(t *testing.T, before, after string) {
	t.Helper()
	for _, re := range []*regexp.Regexp{fenceRe, codeSpanRe, urlRe} {
		want := re.FindAllString(before, -1)
		for _, span := range want {
			if !strings.Contains(after, span) {
				t.Fatalf("保护区段被篡改:\n  期望保留: %q\n  校准输出: %q", span, after)
			}
		}
	}
}

// 数学与工程不变量测试：严格幂等性、清洁恒等性、保护区隔离与零 NUL 泄漏。
func TestCalibrateInvariants(t *testing.T) {
	cases := []struct {
		name string
		md   string
		orig string
	}{
		{
			name: "含反义词对与漂移区的清洁文档保持恒等",
			md:   "The symmetric system was completely balanced. Its variance and asymmetric noise are well known.",
			orig: "Introduction notes omitted. The symmetric system was completely balanced. Its variance and asymmetric noise are well known.",
		},
		{
			name: "含混杂OCR损伤的合成文本一次收敛",
			md: "Diferent models for $n = 0, 1, 2 \\ldots$ \x00 covers continuously.\n\n" +
				"The properties ofmatrix systems and nodesK = 8 with InM = 3.14.\n\n" +
				"<table><tr><td>Z</td><td>Sym</td></tr><tr><td>1</td><td>H</td></tr></table>",
			orig: "Different models for n = 0, 1, 2 ... covers continuously.\n" +
				"The properties of matrix systems and nodes K = 8 with In M = 3.14.\n" +
				"Z Sym 1 H",
		},
		{
			name: "CS技术文档代码反引号与围栏绝对隔离",
			md: "Use `env` and `array` inside `$HOME`, or inspect https://example.org.cn/docs.\n\n" +
				"```awk\nBEGIN { env = 1; diferent = 2 }\n```\n\nThe diferent mode runs cleanly.",
			orig: "Use env and array inside HOME or inspect docs. The different mode runs cleanly.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res1, err := Calibrate(tc.md, tc.orig, both)
			if err != nil {
				t.Fatal(err)
			}
			if strings.ContainsRune(res1.Text, 0) {
				t.Fatalf("输出泄漏了 NUL 字符: %q", res1.Text)
			}
			assertProtectedSpansUnchanged(t, tc.md, res1.Text)

			// 严格幂等门禁：f(f(x)) == f(x) 且第二趟改写计数全部归零
			res2, err := Calibrate(res1.Text, tc.orig, both)
			if err != nil {
				t.Fatal(err)
			}
			if res2.Text != res1.Text {
				t.Fatalf("幂等性破坏:\n  Pass1: %q\n  Pass2: %q", res1.Text, res2.Text)
			}
			if res2.Stats.Fixed+res2.Stats.Merged+res2.Stats.Split+res2.Stats.Ctx+
				res2.Stats.Syms+res2.Stats.Tables+res2.Stats.Accents+res2.Stats.Ctrl+
				res2.Stats.Display+res2.Stats.SupSub+res2.Stats.MathFix != 0 {
				t.Fatalf("Pass2 不应再触发任何修复计数: %+v", res2.Stats)
			}
		})
	}
}

// 跨学科正反例契约：确保既定格式/符号纠偏保留，同时不误伤跨学科专名与合法缩写。
func TestCrossDisciplineContracts(t *testing.T) {
	t.Run("符号_粘连介词_变量_HTML表_ef连接正例保留", func(t *testing.T) {
		orig := "Mirror reflection maps (u, v) → (u, -v) and transitions k(n → n') dominate. " +
			"Structure of matrix states satisfy nodes K = 8 in M = 3.14 with these effects and Midtown records."
		md := "Mirror reflection maps $( u , v )  ( u , - v )$ and transitions $k _ { n  n ^ { \\prime } }$ dominate. " +
			"Structure ofmatrix states satisfy nodesK = 8 InM = 3.14 with these ef ects and Midtow records.\n\n" +
			"<table><tr><th>Param</th><th>Val</th></tr><tr><td>A</td><td>1</td></tr></table>"
		got, err := Calibrate(md, orig, both)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			`\to ( u , - v )`,
			`k _ { n \to n`,
			`of matrix states`,
			`nodes K = 8`,
			`In M = 3.14`,
			`effects`,
			`Midtown`,
			`| Param | Val |`,
		} {
			if !strings.Contains(got.Text, want) {
				t.Fatalf("真修复退化，缺少 %q\ngot:\n%s", want, got.Text)
			}
		}
	})

	t.Run("跨学科反例禁区_CS与图形专名_多行空定界符_生医大写缩写_合法反义词", func(t *testing.T) {
		orig := "Compare PyTorch, TensorFlow, DirectX, SpaceX, ActiveX, SystemV, LaTeX late direct space active system with mRNA and CRISPR assays. " +
			"The symmetric phase was completely balanced and showed variance under TLS. " +
			"Consider piecewise linear forms."
		md := "Compare PyTorch, TensorFlow, DirectX, SpaceX, ActiveX, SystemV, LaTeX late direct space active system with `env` and `array` for mRNA and CRISPR assays.\n\n" +
			"```awk\n`each $x$\n```\n\n" +
			"The symmetric phase was completely balanced and showed variance under TLS.\n\n" +
			"Consider piecewise linear forms $\\left( x \\right. \\left. + y \\right)$ here."
		got, err := Calibrate(md, orig, both)
		if err != nil {
			t.Fatal(err)
		}
		if got.Text != md {
			t.Fatalf("跨学科合法文本被误改:\n  want: %q\n  got:  %q\n  details: %v", md, got.Text, got.Stats.Details)
		}
	})

	t.Run("NormalizeStructure 无损结构还原在空文本层下有效且对干净文本恒等", func(t *testing.T) {
		clean := "# Title\n\nNormal markdown | a | b |\n\n```html\n<table><tr><td>skip</td></tr></table>\n```"
		gotClean, stClean := NormalizeStructure(clean)
		if gotClean != clean || stClean.Tables != 0 {
			t.Fatalf("干净文档或代码块内 HTML 被误改: %q %+v", gotClean, stClean)
		}

		withTable := "Intro\n\n<table><tr><td>Ion</td><td><eq>\\Delta</eq></td></tr></table>\n\nEnd"
		gotTable, stTable := NormalizeStructure(withTable)
		if stTable.Tables != 1 || !strings.Contains(gotTable, "| Ion | $\\Delta$ |") {
			t.Fatalf("无文本层 HTML 表格未还原: %q %+v", gotTable, stTable)
		}
	})

	t.Run("repairProse 合并分隔与 CRLF 与 Kept 去重", func(t *testing.T) {
		vocab := BuildVocab("The time-domain theorem results.")
		// 不得跨连字符合并
		res, err := Markdown("the-orem behavior", vocab)
		if err != nil {
			t.Fatal(err)
		}
		if res.Stats.Merged != 0 {
			t.Fatalf("不应跨连字符合并: %q (%+v)", res.Text, res.Stats)
		}
		// 不得跨越空行（段落边界）合并
		paraRes, err := Markdown("the\n\norem results", vocab)
		if err != nil {
			t.Fatal(err)
		}
		if paraRes.Stats.Merged != 0 || paraRes.Text != "the\n\norem results" {
			t.Fatalf("不应跨段落合并: %q (%+v)", paraRes.Text, paraRes.Stats)
		}
		// CRLF 断行合并不应残留孤立 \r
		crlfRes, err := Markdown("the\r\norem results", vocab)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(crlfRes.Text, "\r") || crlfRes.Text != "theorem results" {
			t.Fatalf("CRLF 合并残留 \\r 或结果异常: %q", crlfRes.Text)
		}
		// 多趟 Calibrate 的 Kept 应去重
		kRes, err := Calibrate("Differenc qxyz unknownw and differenc", "Different aabc knownw", both)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]int{}
		for _, w := range kRes.Stats.Kept {
			seen[w]++
			if seen[w] > 1 {
				t.Fatalf("Stats.Kept 含重复词 %q: %v", w, kRes.Stats.Kept)
			}
		}
	})
}
