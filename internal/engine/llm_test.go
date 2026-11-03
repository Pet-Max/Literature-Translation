// llm_test.go — LLM 客户端语义锁定：200-但-异常体三分支立即失败不重试；
// 429/网络错误走重试并聚合最终错误。用小 maxAttempts 控制退避测试时长。
package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type countingMock struct {
	mu    sync.Mutex
	count int
	body  func() (int, string)
}

func (m *countingMock) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.count++
		m.mu.Unlock()
		code, body := m.body()
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
}

func (m *countingMock) requests() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.count
}

func newTestClient(url string, attempts int) *LLMClient {
	return NewLLMClient(url, "test-key", "test-model", 10.0, attempts)
}

// 200-但-异常体三分支：立即失败、不重试（请求计数 == 1）。
func TestLLMBad200Bodies(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantMsg string
	}{
		{"非 JSON（如打到了网页）", "<html>hello</html>", "返回了非 JSON 内容（检查模型 API 地址是否正确）: <html>hello</html>"},
		{"合法 JSON 但非对象", "[1,2,3]", "返回了非预期格式（应为 JSON 对象）: [1,2,3]"},
		{"空 choices", `{"choices": []}`, "模型返回了空内容"},
		{"空内容", `{"choices":[{"message":{"content":"   "}}]}`, "模型返回了空内容"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := &countingMock{body: func() (int, string) { return 200, c.body }}
			srv := httptest.NewServer(m.handler())
			defer srv.Close()
			_, err := newTestClient(srv.URL, 4).CompleteContext(context.Background(), "sys", "user", 0.1, nil)
			if err == nil {
				t.Fatal("应报错")
			}
			if !strings.Contains(err.Error(), c.wantMsg) {
				t.Fatalf("文案不符\n GOT %q\n WANT 含 %q", err.Error(), c.wantMsg)
			}
			if m.requests() != 1 {
				t.Fatalf("异常体应立即失败不重试，实际请求 %d 次", m.requests())
			}
		})
	}
}

// 429 → 重试集合内：maxAttempts=2 即两次请求 + 一次退避，最终聚合报错。
func TestLLMRetryOn429(t *testing.T) {
	m := &countingMock{body: func() (int, string) { return http.StatusTooManyRequests, `rate limited` }}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()
	_, err := newTestClient(srv.URL, 2).CompleteContext(context.Background(), "sys", "user", 0.1, nil)
	if err == nil {
		t.Fatal("应报错")
	}
	if !strings.Contains(err.Error(), "重试 2 次后仍失败") || !strings.Contains(err.Error(), "HTTP 429") {
		t.Fatalf("聚合文案不符: %q", err.Error())
	}
	if m.requests() != 2 {
		t.Fatalf("429 应重试，实际请求 %d 次（期望 2）", m.requests())
	}
}

// 网络错误 → 可重试分类；最终错误聚合含「网络错误」。
func TestLLMRetryOnNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // 端口已关 → 连接拒绝
	_, err := newTestClient(url, 2).CompleteContext(context.Background(), "sys", "user", 0.1, nil)
	if err == nil {
		t.Fatal("应报错")
	}
	if !strings.Contains(err.Error(), "网络错误") || !strings.Contains(err.Error(), "重试 2 次后仍失败") {
		t.Fatalf("网络错误文案不符: %q", err.Error())
	}
}

// 成功路径回归：正常 200 抽取 content 并 strip。
func TestLLMSuccessStripsContent(t *testing.T) {
	m := &countingMock{body: func() (int, string) {
		b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"content": "  译文内容  "},
		}}})
		return 200, string(b)
	}}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()
	got, err := newTestClient(srv.URL, 1).CompleteContext(context.Background(), "sys", "user", 0.1, nil)
	if err != nil || got != "译文内容" {
		t.Fatalf("got %q, err %v", got, err)
	}
}

// 响应体中途断开 → 按网络错误处理走重试（不判为「非 JSON」）。
func TestLLMResponseBodyReadError(t *testing.T) {
	var mu sync.Mutex
	count := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		count++
		mu.Unlock()
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("short"))
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				conn.Close() // 声明 1000 字节只写 5 字节即断开
			}
		}
	}))
	defer srv.Close()
	_, err := newTestClient(srv.URL, 2).CompleteContext(context.Background(), "sys", "user", 0.1, nil)
	if err == nil {
		t.Fatal("应报错")
	}
	if !strings.Contains(err.Error(), "网络错误") || !strings.Contains(err.Error(), "重试 2 次后仍失败") {
		t.Fatalf("读中断应按网络错误聚合: %q", err.Error())
	}
	mu.Lock()
	n := count
	mu.Unlock()
	if n != 2 {
		t.Fatalf("读中断应走重试，实际请求 %d 次（期望 2）", n)
	}
}
