package mineru

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// buildResultZip 构造与 MinerU 结果 zip 同构的内存包（full.md + 图片）。
func buildResultZip(tag string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, _ := zw.Create("full.md")
	f.Write([]byte("WINDOW-" + tag + "\n"))
	img, _ := zw.Create("images/x.jpg")
	img.Write([]byte("jpg-" + tag))
	zw.Close()
	return buf.Bytes()
}

// TestParseFilesBatch 多条目批次：全部上传、轮询两轮至全 done、逐条目
// 解包发布。校验条目顺序、上传次数与结果内容。
func TestParseFilesBatch(t *testing.T) {
	zips := [][]byte{buildResultZip("A"), buildResultZip("B")}
	var putCount, pollCount int32
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/file-urls/batch", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fmt.Sprintf(`{"code":0,"data":{"batch_id":"b1","file_urls":["%s/put/0","%s/put/1"]}}`, srv.URL, srv.URL)))
	})
	mux.HandleFunc("/put/", func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		atomic.AddInt32(&putCount, 1)
		w.WriteHeader(200)
	})
	mux.HandleFunc("/extract-results/batch/b1", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&pollCount, 1)
		if n < 2 {
			w.Write([]byte(`{"code":0,"data":{"extract_result":[{"state":"running"},{"state":"pending"}]}}`))
			return
		}
		w.Write([]byte(fmt.Sprintf(`{"code":0,"data":{"extract_result":[`+
			`{"state":"done","full_zip_url":"%s/zip/0"},`+
			`{"state":"done","full_zip_url":"%s/zip/1"}]}}`, srv.URL, srv.URL)))
	})
	for i, z := range zips {
		zb := z
		mux.HandleFunc(fmt.Sprintf("/zip/%d", i), func(w http.ResponseWriter, r *http.Request) {
			w.Write(zb)
		})
	}
	srv = httptest.NewServer(mux)
	defer srv.Close()

	pdf1 := filepath.Join(t.TempDir(), "probe-a.pdf")
	pdf2 := filepath.Join(t.TempDir(), "probe-b.pdf")
	os.WriteFile(pdf1, []byte("fake-pdf-a"), 0o644)
	os.WriteFile(pdf2, []byte("fake-pdf-b"), 0o644)
	workDir := t.TempDir()

	var states []string
	results, itemErrs, err := parseFilesBatch(context.Background(), "tok",
		[]string{pdf1, pdf2}, "vlm", "en", workDir,
		func(idx int, state string) { states = append(states, fmt.Sprintf("%d:%s", idx, state)) },
		srv.URL, 10*time.Millisecond, 20*time.Minute, false)
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range itemErrs {
		if e != nil {
			t.Fatalf("条目 %d 意外失败: %v", i+1, e)
		}
	}
	if len(results) != 2 {
		t.Fatalf("结果数 %d，期望 2", len(results))
	}
	for i, res := range results {
		data, err := os.ReadFile(res.MDPath)
		if err != nil {
			t.Fatalf("条目 %d 的 full.md 缺失: %v", i, err)
		}
		if !strings.Contains(string(data), fmt.Sprintf("WINDOW-%c", 'A'+i)) {
			t.Fatalf("条目 %d 内容不符: %q", i, data)
		}
	}
	if atomic.LoadInt32(&putCount) != 2 {
		t.Fatalf("上传次数 %d，期望 2", putCount)
	}
	if atomic.LoadInt32(&pollCount) != 2 {
		t.Fatalf("轮询次数 %d，期望 2", pollCount)
	}
	_ = states
}

// TestParseFilesBatchItemFailed 任一条目 failed 即整体失败（调用方降级）。
func TestParseFilesBatchItemFailed(t *testing.T) {
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/file-urls/batch", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fmt.Sprintf(`{"code":0,"data":{"batch_id":"b2","file_urls":["%s/put/0","%s/put/1"]}}`, srv.URL, srv.URL)))
	})
	mux.HandleFunc("/put/", func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(200)
	})
	mux.HandleFunc("/extract-results/batch/b2", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"data":{"extract_result":[` +
			`{"state":"failed","err_msg":"条目内模拟失败"},` +
			`{"state":"running"}]}}`))
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	pdf := filepath.Join(t.TempDir(), "probe.pdf")
	os.WriteFile(pdf, []byte("fake-pdf"), 0o644)
	_, _, err := parseFilesBatch(context.Background(), "tok",
		[]string{pdf, pdf}, "vlm", "en", t.TempDir(), nil,
		srv.URL, 10*time.Millisecond, 20*time.Minute, false)
	if err == nil || !strings.Contains(err.Error(), "条目 1") {
		t.Fatalf("应报条目 1 失败，实际: %v", err)
	}
}

// TestParseFilesBatchTolerant 宽容模式（片解析语义）：条目 0 失败不中止
// 批次，条目 1 照常解析发布；逐条目回报结果与错误。
func TestParseFilesBatchTolerant(t *testing.T) {
	zips := [][]byte{buildResultZip("B")}
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/file-urls/batch", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fmt.Sprintf(`{"code":0,"data":{"batch_id":"b3","file_urls":["%s/put/0","%s/put/1"]}}`, srv.URL, srv.URL)))
	})
	mux.HandleFunc("/put/", func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(200)
	})
	mux.HandleFunc("/extract-results/batch/b3", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fmt.Sprintf(`{"code":0,"data":{"extract_result":[`+
			`{"state":"failed","err_msg":"条目内模拟失败"},`+
			`{"state":"done","full_zip_url":"%s/zip/0"}]}}`, srv.URL)))
	})
	zb := zips[0]
	mux.HandleFunc("/zip/0", func(w http.ResponseWriter, r *http.Request) {
		w.Write(zb)
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	pdf := filepath.Join(t.TempDir(), "probe.pdf")
	os.WriteFile(pdf, []byte("fake-pdf"), 0o644)
	results, itemErrs, err := parseFilesBatch(context.Background(), "tok",
		[]string{pdf, pdf}, "vlm", "en", t.TempDir(), nil,
		srv.URL, 10*time.Millisecond, 20*time.Minute, true)
	if err != nil {
		t.Fatalf("宽容模式不应整批失败: %v", err)
	}
	if itemErrs[0] == nil || !strings.Contains(itemErrs[0].Error(), "条目 1") {
		t.Fatalf("条目 0 的失败未逐条目回报: %v", itemErrs[0])
	}
	if results[0] != nil {
		t.Fatal("失败条目不应有结果")
	}
	if results[1] == nil {
		t.Fatal("成功条目应正常发布")
	}
	data, err := os.ReadFile(results[1].MDPath)
	if err != nil || !strings.Contains(string(data), "WINDOW-B") {
		t.Fatalf("成功条目内容不符: %v", err)
	}
}
