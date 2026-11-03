package mineru

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseUsesOnlyCurrentZip(t *testing.T) {
	for _, entries := range []map[string]string{
		{"a-new/full.md": "new parse"},
		{"result.json": "{}"},
		{"a/full.md": "one", "b/full.md": "two"},
		{"one.md": "one", "two.md": "two"},
	} {
		var body bytes.Buffer
		zw := zip.NewWriter(&body)
		for name, content := range entries {
			f, _ := zw.Create(name)
			fmt.Fprint(f, content)
		}
		zw.Close()
		var srv *httptest.Server
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/file-urls/batch":
				fmt.Fprintf(w, `{"code":0,"data":{"batch_id":"b","file_urls":[%q]}}`, srv.URL+"/upload")
			case "/upload":
				w.WriteHeader(200)
			case "/zip":
				w.Write(body.Bytes())
			default:
				fmt.Fprintf(w, `{"code":0,"data":{"extract_result":[{"state":"done","full_zip_url":%q}]}}`, srv.URL+"/zip")
			}
		}))
		d := t.TempDir()
		old := filepath.Join(d, "z-old", "full.md")
		os.MkdirAll(filepath.Dir(old), 0700)
		os.WriteFile(old, []byte("old parse"), 0600)
		pdf := filepath.Join(t.TempDir(), "source.pdf")
		os.WriteFile(pdf, []byte("mock"), 0600)
		result, err := parsePDF(context.Background(), "test", pdf, "vlm", d, nil, srv.URL, time.Millisecond, time.Second)
		srv.Close()
		if len(entries) == 1 && entries["a-new/full.md"] != "" {
			if err != nil {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(result.MDPath)
			if string(b) != "new parse" {
				t.Fatal("stale parse selected", result.MDPath)
			}
		} else if err == nil {
			t.Fatal("invalid or ambiguous result accepted", entries)
		}
		files, _ := os.ReadDir(d)
		for _, f := range files {
			if strings.HasPrefix(f.Name(), ".parse-") {
				t.Fatal("temporary extraction leaked")
			}
		}
	}
}

// mockParseServer 起一个 MinerU 兼容 mock：申请/上传/轮询/下载四步，
// state 决定轮询返回的任务状态，zipBody 为结果包内容（失败态可为 nil）。
func mockParseServer(t *testing.T, zipBody []byte, state string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file-urls/batch":
			fmt.Fprintf(w, `{"code":0,"data":{"batch_id":"b","file_urls":[%q]}}`, srv.URL+"/upload")
		case "/upload":
			w.WriteHeader(200)
		case "/zip":
			w.Write(zipBody)
		default:
			fmt.Fprintf(w, `{"code":0,"data":{"extract_result":[{"state":%q,"full_zip_url":%q}]}}`, state, srv.URL+"/zip")
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// zipWith 打包 entries 为内存 zip。
func zipWith(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var body bytes.Buffer
	zw := zip.NewWriter(&body)
	for name, content := range entries {
		f, _ := zw.Create(name)
		fmt.Fprint(f, content)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}

// TestParsePublishPurgesStaleResults 发布成功后：历史 result-* 与崩溃残留
// .parse-* 必须被清理，仅保留本次发布的产物且内容完好。
func TestParsePublishPurgesStaleResults(t *testing.T) {
	srv := mockParseServer(t, zipWith(t, map[string]string{"full.md": "new parse"}), "done")
	d := t.TempDir()
	for _, stale := range []string{"result-old", ".parse-crashed"} {
		if err := os.MkdirAll(filepath.Join(d, stale), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(d, "result-old", "full.md"), []byte("old parse"), 0600); err != nil {
		t.Fatal(err)
	}
	pdf := filepath.Join(t.TempDir(), "source.pdf")
	if err := os.WriteFile(pdf, []byte("mock"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := parsePDF(context.Background(), "test", pdf, "vlm", d, nil, srv.URL, time.Millisecond, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(result.MDPath); err != nil || string(b) != "new parse" {
		t.Fatalf("当前产物缺失或内容不符: %v", err)
	}
	entries, _ := os.ReadDir(d)
	results := 0
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".parse-") {
			t.Fatal("残留 .parse-* 临时目录:", name)
		}
		if strings.HasPrefix(name, "result-") {
			results++
		}
	}
	if results != 1 {
		t.Fatalf("应仅保留本次发布的 result-*，实际 %d 个", results)
	}
}

// TestParseFailureKeepsPreviousResult 解析失败时不得清理：旧 result-* 是
// 最后一次成功解析的产物，必须原样保留。
func TestParseFailureKeepsPreviousResult(t *testing.T) {
	srv := mockParseServer(t, nil, "failed")
	d := t.TempDir()
	prev := filepath.Join(d, "result-prev")
	if err := os.MkdirAll(prev, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prev, "full.md"), []byte("previous parse"), 0600); err != nil {
		t.Fatal(err)
	}
	pdf := filepath.Join(t.TempDir(), "source.pdf")
	if err := os.WriteFile(pdf, []byte("mock"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := parsePDF(context.Background(), "test", pdf, "vlm", d, nil, srv.URL, time.Millisecond, time.Second); err == nil {
		t.Fatal("解析失败应返回错误")
	}
	if b, err := os.ReadFile(filepath.Join(prev, "full.md")); err != nil || string(b) != "previous parse" {
		t.Fatalf("失败时旧产物应原样保留: %v", err)
	}
}
