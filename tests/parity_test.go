// parity_test.go — M3 对拍（集成层）：抽取换血（PyMuPDF→pdfium wasm）的
// 词表级硬门槛与校准端到端等价。黄金基准为 PyMuPDF 1.26.7 对同样本抽取
// 的全文（由 _v1.2.0_baseline/baseline_extract.py 逐字复刻 op_pdf_text
// 生成，落 tests/testdata/*_py.txt）。放在 tests/ 集成包：跨 pdftext 与
// repair 两域，且避免低层包测试反向依赖高层包。
package tests

import (
	"os"
	"sort"
	"strings"
	"testing"

	"literature-translation/internal/pdftext"
	"literature-translation/internal/repair"
)

// benignVocabDiff 两引擎在公式/脚注区的噪声 token 差异白名单（已逐个
// 取证，见各注释）。词表差异必须完全落在白名单内，出现任何白名单之外
// 的差异即失败——门槛对未来的真实回归仍然有效。
//
// 例证（example）：
//   - offbetween：PyMuPDF 吞空格产物（原文 "trade-off between"，Py 输出
//     "trade-offbetween"；Go 侧为正确分词，Go 更干净）；
//   - pe：公式区 "(7) β∆= ln |1−pe pe|" 碎片，两引擎对公式行距处理不同；
//   - dhei/dpe/hei：公式区重音符组合与碎片的派生 token。
//
// 例证（1510，积分公式 ∫tds 区）：tds/ts/ds 均为公式 spacing 碎片。
var benignVocabDiff = map[string]map[string]struct{}{
	"example": {
		"offbetween": {}, "pe": {}, "dhei": {}, "dpe": {}, "hei": {}, "off": {},
	},
	"1510": {
		"tds": {}, "ts": {}, "ds": {},
	},
}

func TestVocabParity(t *testing.T) {
	cases := []struct{ name, pdf, py string }{
		{"example", "testdata/example.pdf", "testdata/example_py.txt"},
		{"1510", "../samples/example-document-en.pdf", "testdata/1510_py.txt"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pyData, err := os.ReadFile(c.py)
			if err != nil {
				t.Fatalf("读 Python 基准: %v", err)
			}
			goText, err := pdftext.ExtractText(c.pdf)
			if err != nil {
				t.Fatalf("pdfium 抽取失败: %v", err)
			}

			// 残留码点检查
			if strings.ContainsRune(goText, '\ufffe') {
				t.Fatalf("Go 输出仍含 U+FFFE 标记")
			}
			for _, l := range "ﬀﬁﬂﬃﬄﬅﬆ" {
				if strings.ContainsRune(goText, l) {
					t.Fatalf("Go 输出仍含连字码点 %U", l)
				}
			}

			vGo := repair.BuildVocab(goText)
			vPy := repair.BuildVocab(string(pyData))
			allow := benignVocabDiff[c.name]
			var unexpected []string
			for w := range vGo {
				if _, ok := vPy[w]; !ok {
					if _, benign := allow[w]; !benign {
						unexpected = append(unexpected, "+go:"+w)
					}
				}
			}
			for w := range vPy {
				if _, ok := vGo[w]; !ok {
					if _, benign := allow[w]; !benign {
						unexpected = append(unexpected, "+py:"+w)
					}
				}
			}
			if len(unexpected) > 0 {
				sort.Strings(unexpected)
				t.Fatalf("词表出现白名单外差异 (%d): %v", len(unexpected), unexpected)
			}
		})
	}
}

// TestCalibrateParityE2E 端到端消费等价：同一份带典型 MinerU 损伤的 MD，
// 分别以 PyMuPDF 基准与 pdfium 抽取为原文参照跑完整 Calibrate，结果
// 逐字节一致，且校准确实发生了（非空转）。
func TestCalibrateParityE2E(t *testing.T) {
	md := `# Section

The finite-time thermodynamic process transforms a given initial state into a
final state while producing entropy. The effciency of a heat engine is bounded
by the second law. The uncer tainty re lation yields a proper result ing flow,
and the configua tion of the heat eng ine follows.

| state | occupation |
| --- | --- |
| ground | 2 |
| excited | 3 |

As shown in Eq. (7), the β∆ relation holds for $T_1 < T_2$ with work $W$.
`
	for _, c := range []struct{ name, pdf, py string }{
		{"example", "testdata/example.pdf", "testdata/example_py.txt"},
		{"1510", "../samples/example-document-en.pdf", "testdata/1510_py.txt"},
	} {
		t.Run(c.name, func(t *testing.T) {
			pyData, err := os.ReadFile(c.py)
			if err != nil {
				t.Fatal(err)
			}
			goText, err := pdftext.ExtractText(c.pdf)
			if err != nil {
				t.Fatal(err)
			}
			resPy, err := repair.Calibrate(md, string(pyData), repair.Options{Spelling: true, Symbols: true})
			if err != nil {
				t.Fatal(err)
			}
			resGo, err := repair.Calibrate(md, goText, repair.Options{Spelling: true, Symbols: true})
			if err != nil {
				t.Fatal(err)
			}
			if resPy.Text != resGo.Text {
				t.Fatalf("校准结果不一致\n Py  : %q\n Go  : %q", resPy.Text, resGo.Text)
			}
			// 校准非空转：修正确实发生了
			if resPy.Stats.Fixed == 0 {
				t.Fatalf("校准未产生任何修正（用例失效），stats=%+v", resPy.Stats)
			}
		})
	}
}
