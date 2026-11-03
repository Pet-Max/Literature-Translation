package mineru

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestActualAPIResponses(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body, want string
		retry      bool
	}{
		{"success", 200, `{"code":0,"data":{"batch_id":"batch"}}`, "", false},
		{"missing task", 200, `{"code":-60012}`, "code=-60012", false},
		{"unauthorized", 401, `{"code":0}`, "认证失败", false},
		{"forbidden", 403, `not JSON`, "认证失败", false},
		{"invalid token", 200, `{"msgCode":"A0202","success":false}`, "认证失败", false},
		{"expired", 401, `{"msgCode":"A0211","success":false}`, "已过期", false},
		{"rate limit", 429, `{}`, "限流", true},
		{"server", 503, `not JSON`, "服务故障", true},
		{"unknown", 200, `{"other":1}`, "缺少 code", false},
		{"null code", 200, `{"code":null}`, "缺少 code", false},
		{"null object", 200, `null`, "缺少 code", false},
		{"non JSON", 200, `html`, "不是有效 JSON", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer srv.Close()
			var got apiResp
			e := getJSON(context.Background(), "temporary-token", srv.URL, &got)
			if tc.want == "" {
				if e != nil {
					t.Fatal(e)
				}
				return
			}
			if e == nil || !strings.Contains(e.Error(), tc.want) || retryable(e) != tc.retry {
				t.Fatalf("err=%v retry=%v", e, retryable(e))
			}
		})
	}
}

func TestParsePermanentErrorStopsPolling(t *testing.T) {
	var polls atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file-urls/batch":
			fmt.Fprintf(w, `{"code":0,"data":{"batch_id":"b","file_urls":[%q]}}`, srv.URL+"/upload")
		case "/upload":
			w.WriteHeader(200)
		default:
			polls.Add(1)
			w.WriteHeader(403)
			fmt.Fprint(w, `{}`)
		}
	}))
	defer srv.Close()
	file := filepath.Join(t.TempDir(), "source.pdf")
	os.WriteFile(file, []byte("mock PDF"), 0600)
	_, e := parsePDF(context.Background(), "temporary-token", file, "vlm", t.TempDir(), nil, srv.URL, time.Millisecond, time.Second)
	if e == nil || !strings.Contains(e.Error(), "认证失败") || polls.Load() != 1 {
		t.Fatal("permanent error retried", e, polls.Load())
	}
}

func TestParseCancellationDuringPollWait(t *testing.T) {
	var polls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file-urls/batch":
			fmt.Fprintf(w, `{"code":0,"data":{"batch_id":"b","file_urls":[%q]}}`, srv.URL+"/upload")
		case "/upload":
			w.WriteHeader(200)
		default:
			polls.Add(1)
			fmt.Fprint(w, `{"code":0,"data":{"extract_result":[{"state":"running"}]}}`)
			cancel()
		}
	}))
	defer srv.Close()
	file := filepath.Join(t.TempDir(), "source.pdf")
	os.WriteFile(file, []byte("mock"), 0600)
	start := time.Now()
	_, e := parsePDF(ctx, "temporary-token", file, "vlm", t.TempDir(), nil, srv.URL, time.Minute, 2*time.Minute)
	if !errors.Is(e, context.Canceled) || time.Since(start) > time.Second || polls.Load() != 1 {
		t.Fatal(e, polls.Load())
	}
}

func TestRequestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	var out apiResp
	e := getJSON(ctx, "temporary-token", srv.URL, &out)
	if !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
}
