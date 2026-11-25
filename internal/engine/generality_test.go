package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGeneralityCRLFAndCodeFences(t *testing.T) {
	md := "# Title\r\n\r\nIntro prose.\r\n\r\n```python\r\n# python comment heading trap\r\nx = 1\r\n```\r\n\r\nEnd prose."
	chunks := Split(md, DefaultMaxChars)
	if len(chunks) != 1 {
		t.Fatalf("代码块内 # 注释不应被切分，got %d 段: %q", len(chunks), chunks)
	}
	fr := Freeze(md, 1)
	if len(fr.Mapping) != 1 {
		t.Fatalf("CRLF 代码围栏未整体冻结，got mapping=%v frozen=%q", fr.Mapping, fr.Text)
	}
	if back := Unfreeze(fr.Text, fr.Mapping); back != md {
		t.Fatalf("CRLF 冻结回填不一致")
	}
	t.Run("围栏代码块含内部空行不被拆分", func(t *testing.T) {
		md := "# Section\n\n" +
			"```python\ndef f():\n    a = 1\n\n    return a\n```\n\n" +
			strings.Repeat("Trailing paragraph text. ", 20)
		chunks := Split(md, 90)
		for i, c := range chunks {
			if strings.Count(c, "```")%2 != 0 {
				t.Fatalf("第 %d 段将含空行的围栏代码块劈成两半: %q", i, c)
			}
		}
	})
	t.Run("行首三反引号行内代码不误开围栏", func(t *testing.T) {
		md := "# Intro\n\n```inline()``` leads this paragraph.\n\n# Next Heading\n\nFollowing body text."
		chunks := Split(md, 200)
		if len(chunks) != 2 {
			t.Fatalf("单行三反引号行内代码不应阻断后续标题切节，got %d 段: %q", len(chunks), chunks)
		}
	})
}

func TestCurrencyDollarNotFrozenAsMath(t *testing.T) {
	text := "The cost rose from $50 to $100 and then $200, or $50-$100 ($1,000-$2,000), or $50 ($x+1$) and $5-$10 ($y+2$), while $1.50, $2.50, and $3.50 apply; formula $1-2$ and $1 \\le x \\le 2$ stay math."
	fr := Freeze(text, 1)
	if len(fr.Mapping) != 4 {
		t.Fatalf("期望只冻结 4 条真公式（x+1, y+2, 1-2, 1<=x<=2），实际冻结 %d 条: %v", len(fr.Mapping), fr.Mapping)
	}
	for _, v := range fr.Mapping {
		if strings.Contains(v, "50") || strings.Contains(v, "100") || strings.Contains(v, "200") || strings.Contains(v, "1,000") || strings.Contains(v, "10") {
			t.Fatalf("货币金额被误冻结成公式: %q", v)
		}
	}
	if spans := ProtectedSpans(text); len(spans) != 4 {
		t.Fatalf("ProtectedSpans 应只含 4 条真公式，got %d", len(spans))
	}
}

func TestRefDetectorMultiEntryAndSectionExit(t *testing.T) {
	t.Run("多条目文献块同步推进maxN", func(t *testing.T) {
		d := newRefDetector(true)
		multi := strings.Join([]string{
			"[1] A. Alpha, Paper one, J. Test 1, 1 (2020).",
			"[2] B. Beta, Paper two, J. Test 2, 2 (2021).",
			"[3] C. Gamma, Paper three, J. Test 3, 3 (2022).",
			"[4] D. Delta, Paper four, J. Test 4, 4 (2023).",
			"[5] E. Epsilon, Paper five, J. Test 5, 5 (2024).",
		}, "\n")
		if !d.detect(multi) {
			t.Fatal("多条目参考文献块未被识别")
		}
		ref6 := "[6] F. Zeta, Paper six, J. Test 6, 6 (2024)."
		if !d.detect(ref6) {
			t.Fatalf("多条目块后 [6] 断链（游标未推进到 maxN=5，last=%d）", d.last)
		}
	})

	t.Run("APA作者年份文献节识别与跨节退出", func(t *testing.T) {
		d := newRefDetector(true)
		if d.detect("## References") {
			t.Fatal("纯参考文献节标题自身不应被跳过")
		}
		apa := "Acemoglu, D., Johnson, S., & Robinson, J. A. (2001). The colonial origins of comparative development.\n\n" +
			"Autor, D. H., Dorn, D., & Hanson, G. H. (2013). The China syndrome."
		if !d.detect(apa) {
			t.Fatal("References 节下的 APA 文献段未被识别")
		}
		// 遇到不高于参考节层级的新章节标题（# Chapter 2 或 ## Supplementary Material）时须立刻退出参考节
		if d.detect("## Supplementary Material\n\nThe panel survey from (2021) is analyzed in detail below.") {
			t.Fatal("同级非参考文献节 ## Supplementary Material 下正文误判为文献")
		}
		if d.detect("## References\n\nSmith, J. (2019). Annual survey review.") != true {
			t.Fatal("重入 ## References 失败")
		}
		if d.detect("# Chapter 2") {
			t.Fatal("章标题不应跳过")
		}
		if d.detect("Historical observations reported in (2018) motivate our second chapter.") {
			t.Fatal("离开 # Chapter 2 后正文仍被误判为 APA 文献")
		}
		if d.detect("## Appendix\n\nDetailed proofs without citation format follow here.") {
			t.Fatal("Appendix 节误判为参考文献")
		}
	})
}

func TestLLMRetryOn524And408WithRetryAfter(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		switch n {
		case 1:
			w.WriteHeader(524) // 上游网关超时状态码（非标准 5xx）
			_, _ = w.Write([]byte("error code: 524"))
		case 2:
			w.WriteHeader(http.StatusRequestTimeout) // 408
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
		}
	}))
	defer srv.Close()

	c := NewLLMClient(srv.URL, "k", "m", 5.0, 4)
	defer c.Close()
	got, err := c.CompleteContext(context.Background(), "sys", "usr", 0.1, nil)
	if err != nil || got != "ok" || attempts.Load() != 3 {
		t.Fatalf("got=%q err=%v attempts=%d", got, err, attempts.Load())
	}
}

func TestBilingualFailedSegmentNotDuplicated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized) // 不可重试失败
		_, _ = w.Write([]byte("bad key"))
	}))
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "out.md")
	res, err := TranslateContext(context.Background(), TranslateOptions{
		MDText:    "# Heading\n\nUnique english failure marker.",
		OutPath:   out,
		Bilingual: true,
		LLM:       LLMOptions{BaseURL: srv.URL, APIKey: "k", Model: "m", Concurrency: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Failed) != 1 {
		t.Fatalf("期望 1 个失败段，got %+v", res.Failed)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), "Unique english failure marker."); n != 1 {
		t.Fatalf("双语模式下失败段英文重复出现 %d 次（期望 1）:\n%s", n, string(data))
	}
}

func TestRefDetectorBookAndNumberedLists(t *testing.T) {
	t.Run("Bibliography与章内编号及单首字母出版年份", func(t *testing.T) {
		md := strings.Join([]string{
			"# Chapter 1",
			"",
			"Intro paragraph.",
			"",
			"## Bibliography",
			"",
			"[1.1] C. Alpha and L. Beta, “Diffraction study,” J. Sample 12, 345 (1927).",
			"",
			"[1.2] H. Gamma, “Sample Treatise,” 2nd ed. (Example Press, 1981).",
			"",
			"[1.3] L. de Delta, “Historical treatise,” arXiv:2301.00001 (1897).",
		}, "\n")
		chunks := Split(md, DefaultMaxChars)
		d := newRefDetector(true)
		var skipped int
		for _, c := range chunks {
			if d.detect(c) {
				skipped++
			}
		}
		if skipped != 3 {
			t.Fatalf("章内编号文献跳过数 = %d，期望 3；chunks=%q", skipped, chunks)
		}
	})

	t.Run("参考节内整型[N]回落与多章重启及超长[N]块", func(t *testing.T) {
		md := strings.Join([]string{
			"# Chapter 1",
			"",
			"## Bibliography",
			"",
			"[1] H. Author, “Sample Treatise,” 2nd ed. (Example Press, 1981).",
			"",
			"[2] L. Researcher, “Historical note,” arXiv:2301.00001 (1897).",
			"",
			"[3] A. One and B. Two, “Third study,” J. Sample 3, 30 (2020).",
			"",
			"[4] C. Three and D. Four, “Fourth study,” J. Sample 4, 40 (2021).",
			"",
			"[5] E. Five and F. Six, “Fifth study,” J. Sample 5, 50 (2022).",
			"",
			"# Chapter 2",
			"",
			"Second chapter body text.",
			"",
			"## Bibliography",
			"",
			"[1] G. Next and H. Author, “Restarted entry one,” J. Sample 6, 60 (2023).",
			"",
			"[2] I. Reader, “Restarted monograph,” (Example Press, 2024).",
		}, "\n")
		chunks := Split(md, DefaultMaxChars)
		d := newRefDetector(true)
		var skipped int
		for _, c := range chunks {
			if d.detect(c) {
				skipped++
			}
		}
		if skipped != 7 {
			t.Fatalf("多章参考文献跳过数 = %d，期望 7；chunks=%q", skipped, chunks)
		}

		dSing := newRefDetector(true)
		if dSing.detect("## Reference") {
			t.Fatal("单数参考节标题自身不应跳过")
		}
		longBracketed := strings.Join([]string{
			"[1] A. Author, “Entry one with detailed subtitle and institutional notes,” J. Test 1, 10 (2020).",
			"[2] B. Author, “Entry two with detailed subtitle and institutional notes,” J. Test 2, 20 (2021).",
			"[3] C. Author, “Entry three with detailed subtitle and institutional notes,” J. Test 3, 30 (2022).",
			"[4] D. Author, “Entry four with detailed subtitle and institutional notes,” J. Test 4, 40 (2023).",
			"[5] E. Author, “Entry five with detailed subtitle and institutional notes,” J. Test 5, 50 (2024).",
			"[6] F. Author, “Entry six with detailed subtitle and institutional notes,” J. Test 6, 60 (2024).",
			"[7] G. Author, “Entry seven with detailed subtitle and institutional notes,” J. Test 7, 70 (2024).",
			"[8] H. Author, “Entry eight with detailed subtitle and institutional notes,” J. Test 8, 80 (2024).",
			"[9] I. Author, “Entry nine with detailed subtitle and institutional notes,” J. Test 9, 90 (2024).",
		}, "\n")
		if runeLen(longBracketed) <= refMaxRunes {
			t.Fatalf("测试夹具需超过 %d 字符", refMaxRunes)
		}
		if !dSing.detect(longBracketed) {
			t.Fatal("单数参考节下超长多行 [N] 文献块应回落并逐行核验后保留")
		}
	})

	t.Run("带节号参考文献与1.编号列表及缩写与索引豁免", func(t *testing.T) {
		d := newRefDetector(true)
		if d.detect("## 5. References") {
			t.Fatal("纯参考文献节标题自身不应跳过")
		}
		numbered := "1. C. Ionescu, “Programmable study,” J. Sample 130, 120601 (2023).\n\n" +
			"2. A. Ferrand, “Analog benchmark,” Example University Press (2024)."
		if !d.detect(numbered) {
			t.Fatal("参考节内 1./2. 编号列表文献未识别")
		}
		longMulti := strings.Join([]string{
			"1. A. Author, “Entry one with detailed subtitle and institutional notes,” J. Test 1, 10 (2020).",
			"2. B. Author, “Entry two with detailed subtitle and institutional notes,” J. Test 2, 20 (2021).",
			"3. C. Author, “Entry three with detailed subtitle and institutional notes,” J. Test 3, 30 (2022).",
			"4. D. Author, “Entry four with detailed subtitle and institutional notes,” J. Test 4, 40 (2023).",
			"5. E. Author, “Entry five with detailed subtitle and institutional notes,” J. Test 5, 50 (2024).",
			"6. F. Author, “Entry six with detailed subtitle and institutional notes,” J. Test 6, 60 (2024).",
			"7. G. Author, “Entry seven with detailed subtitle and institutional notes,” J. Test 7, 70 (2024).",
			"8. H. Author, “Entry eight with detailed subtitle and institutional notes,” J. Test 8, 80 (2024).",
			"9. I. Author, “Entry nine with detailed subtitle and institutional notes,” J. Test 9, 90 (2024).",
		}, "\n")
		if runeLen(longMulti) <= refMaxRunes {
			t.Fatalf("测试夹具需超过 %d 字符", refMaxRunes)
		}
		if !d.detect(longMulti) {
			t.Fatal("超长多行 1. 编号列表文献块应逐行核验后保留")
		}
		if d.detect("sample topic term, 120, 145, 188") {
			t.Fatal("非引文索引条目误判为文献")
		}
		if d.detect("We analyzed the DNA. The results confirm our model.") {
			t.Fatal("参考节内含句末全大写缩写的普通英文句误判为文献")
		}
		if d.detect("In the U.S. government survey, no citation appears.") {
			t.Fatal("参考节内含 U.S. 后接小写词的普通英文句误判为文献")
		}
		if !d.detect("Ångström, A., “Optical wave study,” J. Sample 1, 10.") {
			t.Fatal("参考节内非 ASCII 大写字母开头的姓前置引文未识别")
		}
		if !d.detect("H. van der Waals, “Equation of state study,” Example Press.") {
			t.Fatal("参考节内含双介词姓氏的引文未识别")
		}
		// 重置并验证正文区即使含 (1981) 与 H. Author 也不误判
		d2 := newRefDetector(true)
		if d2.detect("H. Author discussed methods in (Example Press, 1981) during the lecture.") {
			t.Fatal("未进入参考节的正文误判为文献")
		}
	})
}

func TestValidatorHardeningAndCorrection(t *testing.T) {
	t.Run("标题层级与表格出错行定位", func(t *testing.T) {
		if r := Validate("# Main Title\n\nBody.", "## 主标题\n\n正文。"); !contains(r, "标题层级不一致") {
			t.Fatalf("期望报标题层级不一致，got %q", r)
		}
		srcTbl := strings.Join([]string{
			"| c1 | c2 | c3 |", "| --- | --- | --- |",
			"| a | 1 | x |", "| b | 2 | y |", "| c | 3 | z |",
			"| d | 4 | w |", "| e | 5 | u |", "| f | 6 | v |",
		}, "\n")
		dstBadRow8 := strings.Join([]string{
			"| 甲 | 乙 | 丙 |", "| --- | --- | --- |",
			"| a | 1 | x |", "| b | 2 | y |", "| c | 3 | z |",
			"| d | 4 | w |", "| e | 5 | u |", "| f 6 | v |",
		}, "\n")
		if r := Validate(srcTbl, dstBadRow8); !contains(r, "第 8 行竖线数不一致：原文 4 个 |，译文 3 个 |") {
			t.Fatalf("未精确报告第 8 行竖线数差异，got %q", r)
		}
		dstRowDropped := strings.Join(strings.Split(srcTbl, "\n")[:6], "\n")
		if r := Validate(srcTbl, dstRowDropped); !contains(r, "原文 8 行，译文 6 行") {
			t.Fatalf("未报告表格行数差异，got %q", r)
		}
	})

	t.Run("外层代码围栏与think标签拦截", func(t *testing.T) {
		src := "# Title\n\nNormal prose paragraph."
		if r := Validate(src, "```markdown\n# 标题\n\n正常译文。\n```"); !contains(r, "外层代码围栏") {
			t.Fatalf("未拦截 markdown 代码围栏包裹，got %q", r)
		}
		if r := Validate(src, "~~~\n# 标题\n\n正常译文。\n~~~"); !contains(r, "外层代码围栏") {
			t.Fatalf("未拦截波浪线代码围栏包裹，got %q", r)
		}
		if r := Validate("Use ```inline()``` here.", "此处使用 ```inline()``` 函数。"); len(r) != 0 {
			t.Fatalf("译文行内代码误判为外层围栏: %q", r)
		}
		if r := Validate(src, "<think>internal chain</think>\n# 标题\n\n译文内容。"); !contains(r, "<think>") {
			t.Fatalf("未拦截思考标签泄漏，got %q", r)
		}
		if r := Validate("Literal <think> tag in source.", "原文自带 <think> 标签。"); len(r) != 0 {
			t.Fatalf("原文自带 <think> 时误拦截: %q", r)
		}
	})

	t.Run("长英文零汉字回显检测且不误伤符号表或精炼译文", func(t *testing.T) {
		longEng := strings.Repeat("The experimental observation demonstrates consistent agreement with theoretical predictions across multiple regimes. ", 3)
		if r := Validate(longEng, longEng); !contains(r, "未包含任何中文内容") {
			t.Fatalf("未拦截长英文纯回显，got %q", r)
		}
		// 含少量中文或正常精炼译文（>=0.2 阈值）不得误报
		if r := Validate(longEng, strings.Repeat("实验观测结果与多个区间内的理论预测保持高度一致且具备良好的可重复性。", 3)); len(r) != 0 {
			t.Fatalf("正常精炼译文误报: %q", r)
		}
		// 元素周期表符号形态（缺少 25 个小写英文词）不误报
		symbolRow := "| 1H | 2He | 3Li | 4Be | 5B | 6C | 7N | 8O | 9F | 10Ne | 11Na | 12Mg |\n" + strings.Repeat("| X | Y | Z | W | U | V | A | B | C | D | E | F |\n", 4)
		if r := Validate(symbolRow, symbolRow); len(r) != 0 {
			t.Fatalf("符号表不应被未翻译正文检查误伤: %q", r)
		}
		// 按规则 6 原样保留的参考文献条目段（>200 字符且含 >=25 个英文小写词）不得误报
		refBlock := "[1] A. Author, B. Author, and C. Author, “Long experimental study on sample dynamical equations and generalized transport behavior across multiple regimes,” J. Sample 12, 100 (2021).\n\n" +
			"[2] D. Writer and E. Scholar, “Supplementary theoretical framework for coupled systems and asymptotic convergence analysis,” J. Sample 14, 210 (2023)."
		if r := Validate(refBlock, refBlock); len(r) != 0 {
			t.Fatalf("按规则保留原文的参考文献段落不应被误判为未翻译回显: %q", r)
		}
		apaRefBlock := "Author, A., & Writer, B. (2021). Long experimental study on sample dynamical equations and generalized transport behavior across multiple regimes in complex materials and laboratory setups.\n\n" +
			"Scholar, E., & Researcher, F. (2023). Supplementary theoretical framework for coupled systems and asymptotic convergence analysis across parameter boundaries."
		if r := Validate(apaRefBlock, apaRefBlock); len(r) != 0 {
			t.Fatalf("独立作者年份制参考文献段落不应被误判为未翻译回显: %q", r)
		}
		// 含 (2020)、J. Smith、arXiv:... 的正常英文长正文原样回显仍应被拦截，不能因引文线索被整段豁免
		proseWithCitation := "Several recent empirical investigations introduced by J. Smith (2020) and later extended in arXiv:2301.00001 examine how coherent boundary driving alters macroscopic transport coefficients and thermodynamic consistency across a broad range of experimental parameters and numerical benchmarks."
		if r := Validate(proseWithCitation, proseWithCitation); !contains(r, "未包含任何中文内容") {
			t.Fatalf("含引文线索的普通英文正文回显应被拦截，got %q", r)
		}
		// 行内代码、Markdown 链接 URL 与裸 URL 中的大量英文路径/标识符不计入散文词数，不得误报
		codeAndLinks := strings.Repeat("`compute_long_sample_trajectory_identifier_alpha` [Ref.](https://example.org/archive/papers/sample/long-identifier-slug-one) ", 4)
		if r := Validate(codeAndLinks, codeAndLinks); len(r) != 0 {
			t.Fatalf("行内代码与链接 URL 段落不应被误判为未翻译回显: %q", r)
		}
		bareURLList := strings.Repeat("https://example.org/archive/dataset/measurements/experimental-run-summary-report-alpha ", 3)
		if r := Validate(bareURLList, bareURLList); len(r) != 0 {
			t.Fatalf("裸 URL 列表段落不应被误判为未翻译回显: %q", r)
		}
	})

	t.Run("BuildCorrection聚焦提示", func(t *testing.T) {
		tblCorr := BuildCorrection([]string{"表格结构被改动"})
		if strings.HasPrefix(tblCorr, "\n") || strings.Contains(tblCorr, "——") || strings.Contains(tblCorr, "⟦F数字⟧") {
			t.Fatalf("表格纠错提示应直指问题且不强制占位符提示: %q", tblCorr)
		}
		phCorr := BuildCorrection([]string{"占位符缺失 ⟦F2⟧"})
		if !strings.Contains(phCorr, "⟦F数字⟧") {
			t.Fatalf("占位符纠错提示应强调 ⟦F数字⟧: %q", phCorr)
		}
	})
}
