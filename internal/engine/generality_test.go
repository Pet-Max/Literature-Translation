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
