// engine_extra_test.go — 用户可见分支补测：双语组装、收尾补翻救回、
// 预检自检、硬切防死循环边。
package engine

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// echoMock 回显 user 消息 + 标记句（复用 E2E 的 mock 语义）。
func echoMock(t *testing.T) (*httptest.Server, func() int) {
	t.Helper()
	var mu sync.Mutex
	count := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.Unmarshal(body, &req) != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		user := ""
		for _, m := range req.Messages {
			if m.Role == "user" {
				user = m.Content
			}
		}
		mu.Lock()
		count++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": user + "\n\n（机翻译文冒烟标记）"}}},
		})
	}))
	return srv, func() int { mu.Lock(); defer mu.Unlock(); return count }
}

// TestTranslateBilingual 双语组装分支：翻译段 = 译文 + 空行 + 引用块 +
// 尾换行；文献段保留原文且不加引用块；最终 \n\n 连接 + 尾 \n。
func TestTranslateBilingual(t *testing.T) {
	srv, _ := echoMock(t)
	defer srv.Close()

	md := "# 标题A\n\n第一段内容。\n\n# 标题B\n\n[1] Ref Author, Title, J. Phys. 1, 1 (2019).\n"
	out := filepath.Join(t.TempDir(), "bi_zh.md")
	res, err := Translate(TranslateOptions{
		MDText: md, OutPath: out, Bilingual: true, SkipReferences: true,
		LLM: LLMOptions{BaseURL: srv.URL, APIKey: "k", Model: "m", Concurrency: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 3 || res.Done != 3 || res.KeptReferences != 1 {
		t.Fatalf("统计异常: %+v", res)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	zh := string(data)
	// 翻译段：引用块逐行 "> " 前缀（含空行）
	if !strings.Contains(zh, "> # 标题A\n> \n> 第一段内容。") {
		t.Fatalf("翻译段缺引用块:\n%s", zh)
	}
	// 非文献段都要有标记句；文献段无 LLM 无标记
	if n := strings.Count(zh, "机翻译文冒烟标记"); n != 2 {
		t.Fatalf("标记句 %d 处，期望 2（两个翻译段）", n)
	}
	// 文献段：原文保留、不进引用块、不重复出现
	if n := strings.Count(zh, "[1] Ref Author"); n != 1 {
		t.Fatalf("文献条目出现 %d 次，期望 1（保留原文且不加引用块）", n)
	}
	if strings.Contains(zh, "> [1] Ref Author") {
		t.Fatal("文献段不应加引用块")
	}
	if !strings.HasSuffix(zh, "\n") {
		t.Fatal("最终 MD 应以换行结尾")
	}
}

// TestTranslateSweepRescue 收尾补翻"救回成功"路径：质量重试 4 次全败 →
// sweep 第 1 次成功 → 段被救回，最终 0 失败、sweep 事件 done_idx 恢复。
func TestTranslateSweepRescue(t *testing.T) {
	var mu sync.Mutex
	rescueReqs := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		// 段身份按原始 body 判定（重试请求的最后一条 user 是纠错指令，
		// 不含段标记；首条 user 才是冻结原文）。回显首条 user（冻结原文）。
		user := ""
		for _, m := range req.Messages {
			if m.Role == "user" {
				user = m.Content
				break
			}
		}
		content := user + "\n\n（机翻译文冒烟标记）"
		if strings.Contains(string(body), "RESCUEPOINT") {
			mu.Lock()
			rescueReqs++
			n := rescueReqs
			mu.Unlock()
			if n <= 4 { // 前 4 次（初译+3 质量重试）返回校验不过的内容
				content = "拒答占位符全丢"
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
		})
	}))
	defer srv.Close()

	md := "# Rescue\n\nThis section contains RESCUEPOINT and should be rescued.\n\n# Normal\n\nThis section translates fine.\n"
	var events []Event
	out := filepath.Join(t.TempDir(), "rescue_zh.md")
	res, err := Translate(TranslateOptions{
		MDText: md, OutPath: out, SkipReferences: true,
		LLM:     LLMOptions{BaseURL: srv.URL, APIKey: "k", Model: "m", Concurrency: 2},
		OnEvent: func(ev Event) { events = append(events, ev) },
		Logf:    func(format string, a ...any) { t.Logf("[engine] "+format, a...) },
	})
	if err != nil {
		t.Fatal(err)
	}
	// 救回成功：0 失败
	if res.Total != 2 || res.Done != 2 || len(res.Failed) != 0 {
		t.Fatalf("统计异常: %+v", res)
	}
	// 请求计数：正常段 1 + 救回段 5（4 质量重试 + 1 补翻成功）
	mu.Lock()
	n := rescueReqs
	mu.Unlock()
	if n != 5 {
		t.Fatalf("救回段请求数 %d，期望 5（4 质量重试 + 1 补翻成功）", n)
	}
	// 事件流：末尾 sweep 事件 done_idx 恢复完整
	if len(events) == 0 || !events[len(events)-1].Sweep {
		t.Fatalf("末事件应为 sweep: %+v", events)
	}
	last := events[len(events)-1]
	if last.Done != 2 || last.Failed != 0 || len(last.DoneIdx) != 2 || len(last.FailedIdx) != 0 {
		t.Fatalf("末 sweep 事件异常: %+v", last)
	}
	// 译文：救回段拿到了正常译文（含标记句），不再保留英文原文
	data, _ := os.ReadFile(out)
	if n := strings.Count(string(data), "机翻译文冒烟标记"); n != 2 {
		t.Fatalf("标记句 %d 处，期望 2（救回段已有译文）", n)
	}
}

// TestSelfCheck 预检自检冒烟。
func TestSelfCheck(t *testing.T) {
	if err := SelfCheck(); err != nil {
		t.Fatalf("SelfCheck 不应失败: %v", err)
	}
}

// TestHardSplitGiantFrozenBlock 防死循环哨兵：单个超限冻结区独占时
// 整段保留、绝不截断（$$ 定界符保持配对）。
func TestHardSplitGiantFrozenBlock(t *testing.T) {
	block := "$$" + strings.Repeat("x", 500) + "$$"
	got := Split(block, 100)
	if len(got) != 1 || got[0] != block {
		t.Fatalf("超限冻结区应整段保留，got %d 段", len(got))
	}
}
