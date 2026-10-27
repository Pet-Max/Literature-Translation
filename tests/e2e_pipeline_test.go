// e2e_pipeline_test.go — 管线集成测试（第三层测试体系）。
//
// 用 Go 内嵌 httptest 起 mock LLM（OpenAI 兼容 /chat/completions），
// 驱动真实 Python 边车 + Go 排版 + Edge 打印，跑通 TranslateMD 全流程，
// 断言归档体系四类产物落盘。不需要真 LLM、不烧 MinerU 配额。
// 两个用例：全链路归档（TestE2ETranslateMDArchive）、断点续跑（TestE2ECheckpointResume）。
//
// 运行：go test ./tests/ -v -run TestE2E   （-short 跳过）
// 依赖：PATH 上的 python（引擎边车依赖）、本机 Edge（PDF 打印）；
//
//	缺任一则跳过，不算失败。
package tests

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"literature-translation/internal/config"
	"literature-translation/internal/export"
	"literature-translation/internal/pipeline"
)

// repoRoot 以本文件位置定位仓库根（go test 的 cwd 是包目录，不能依赖相对路径）。
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位源文件路径")
	}
	return filepath.Dir(filepath.Dir(file))
}

const sampleEN = `# Sample Paper

## Introduction

This paper studies the mock translation pipeline end to end.

The second paragraph mentions energy $E=mc^2$ and momentum conservation.

## Methods

The method section references the main result ⟦F1⟧ and the auxiliary plot ⟦F2⟧.

## References

[1] J. Doe, A. Smith, and B. Lee, Mock citation for testing purposes, Phys. Rev. A 100, 012345 (2019).
`

func TestE2ETranslateMDArchive(t *testing.T) {
	if testing.Short() {
		t.Skip("集成测试，-short 跳过")
	}
	if _, err := exec.LookPath("python"); err != nil {
		t.Skip("PATH 上没有 python，跳过管线集成测试")
	}
	if _, err := export.FindEdge(); err != nil {
		t.Skip("本机没有 Edge，跳过管线集成测试")
	}
	t.Setenv("LITERATURE_ENGINE", filepath.Join(repoRoot(t), "engine"))

	// ---- mock LLM：回显 user 内容 + 追加中文标记句 ----
	// 占位符/标题数/长度校验因此全部必过；标记句可断言「LLM 响应确实进入译文」。
	var mu sync.Mutex
	llmCalls := 0
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		user := ""
		for _, m := range req.Messages {
			if m.Role == "user" {
				user = m.Content
			}
		}
		resp := map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{"content": user + "\n\n（机翻译文冒烟标记）"},
			}},
		}
		mu.Lock()
		llmCalls++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer mock.Close()

	// ---- 样本文献 + 旁侧图片 ----
	srcDir := t.TempDir()
	mdPath := filepath.Join(srcDir, "sample_paper.md")
	if err := os.WriteFile(mdPath, []byte(sampleEN), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(srcDir, "images"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "images", "fig1.svg"),
		[]byte("<svg xmlns='http://www.w3.org/2000/svg'/>"), 0o644); err != nil {
		t.Fatal(err)
	}

	// ---- 配置与管线 ----
	cfg := config.Default()
	cfg.LLM.BaseURL = mock.URL
	cfg.LLM.APIKey = "test-key"
	cfg.LLM.Model = "mock-model"
	cfg.LLM.Concurrency = 2
	cfg.Export.Bilingual = false

	var logMu sync.Mutex
	var logs []string
	lastDone := -1
	pl, err := pipeline.New(cfg, pipeline.Callbacks{
		Log: func(level, msg string) {
			logMu.Lock()
			logs = append(logs, level+"|"+msg)
			logMu.Unlock()
		},
		Progress: func(done, total, failed int, doneIdx, failedIdx []int, sweep bool) {
			lastDone = done
		},
		Stage: func(string) {},
	})
	if err != nil {
		t.Fatalf("创建管线失败: %v", err)
	}

	archiveRoot := t.TempDir()
	plan, err := pipeline.NewArchivePlan(archiveRoot, mdPath)
	if err != nil {
		t.Fatalf("构建归档方案失败: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	res, err := pl.TranslateMD(ctx, mdPath, "", plan)
	if err != nil {
		logMu.Lock()
		defer logMu.Unlock()
		t.Fatalf("TranslateMD 失败: %v\n日志尾部:\n%s", err, strings.Join(tail(logs, 15), "\n"))
	}

	// ---- 翻译结果 ----
	if res.Total < 4 {
		t.Errorf("段数过少: %d（样本应有 ≥4 段）", res.Total)
	}
	if res.Done != res.Total || len(res.Failed) != 0 {
		t.Errorf("有段落失败: done=%d total=%d failed=%v", res.Done, res.Total, res.Failed)
	}
	if res.KeptReferences != 1 {
		t.Errorf("参考文献保留数 = %d，应为 1", res.KeptReferences)
	}
	mu.Lock()
	calls := llmCalls
	mu.Unlock()
	if calls < 3 {
		t.Errorf("mock LLM 只被调用 %d 次，应 ≥3（非文献段都该调 LLM）", calls)
	}
	if lastDone != res.Total {
		t.Errorf("进度事件最后 done=%d，应为 total=%d", lastDone, res.Total)
	}

	// ---- 归档结构 ----
	mustExist := func(p string, minSize int64, desc string) {
		t.Helper()
		fi, err := os.Stat(p)
		if err != nil {
			logMu.Lock()
			t.Errorf("%s 不存在: %v\n日志尾部:\n%s", desc, err, strings.Join(tail(logs, 15), "\n"))
			logMu.Unlock()
			return
		}
		if fi.Size() < minSize {
			t.Errorf("%s 大小 %d < %d", desc, fi.Size(), minSize)
		}
	}

	// 01_en_pdf：MD 入队不产生
	if _, err := os.Stat(plan.EnPDFPath); !os.IsNotExist(err) {
		t.Errorf("MD 入队不应产生 01_en_pdf 产物")
	}
	// 02_en_md：原件 + 图片
	mustExist(plan.EnMDPath, 10, "02_en_md 归档")
	if orig, _ := os.ReadFile(mdPath); true {
		arch, err := os.ReadFile(plan.EnMDPath)
		if err != nil || string(arch) != string(orig) {
			t.Errorf("02_en_md 与原件不一致")
		}
	}
	mustExist(filepath.Join(plan.EnMDDir, "images", "fig1.svg"), 5, "02 英文图片归档")
	// 03_zh_md：译文 + 图片 + 标记句
	mustExist(plan.ZhMDPath, 10, "03_zh_md 译文")
	zh, err := os.ReadFile(plan.ZhMDPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(zh), "机翻译文冒烟标记") {
		t.Errorf("译文中找不到 mock 标记句，LLM 响应可能未进入译文")
	}
	if !strings.Contains(string(zh), "E=mc^2") {
		t.Errorf("译文中行内公式丢失")
	}
	if !strings.Contains(string(zh), "Mock citation for testing purposes") {
		t.Errorf("参考文献条目未按原文保留")
	}
	mustExist(filepath.Join(plan.ZhMDDir, "images", "fig1.svg"), 5, "03 译文图片归档")
	// 04_zh_pdf：%PDF 头
	mustExist(plan.ZhPDFPath, 500, "04_zh_pdf 成品")
	if head, err := os.ReadFile(plan.ZhPDFPath); err == nil && len(head) >= 5 {
		if string(head[:5]) != "%PDF-" {
			t.Errorf("04_zh_pdf 不是 PDF（文件头 %q）", head[:5])
		}
	}
	// _process：进度缓存 + source.txt
	mustExist(plan.ProgressPath, 10, "_process 进度缓存")
	srcNote, err := os.ReadFile(filepath.Join(plan.ProcessDir, "source.txt"))
	if err != nil || strings.TrimSpace(string(srcNote)) != plan.SourcePath {
		t.Errorf("_process/source.txt 记录与源路径不符")
	}

	// 同源重跑：复用同一档案夹（不产生 -2 后缀）
	plan2, err := pipeline.NewArchivePlan(archiveRoot, mdPath)
	if err != nil {
		t.Fatal(err)
	}
	if plan2.Dir != plan.Dir {
		t.Errorf("同源重跑应复用档案夹：首次=%s 二次=%s", plan.Dir, plan2.Dir)
	}
}

func tail(s []string, n int) []string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// ---------------------------------------------------------------- 断点续跑

// failMock 在回显式 mock 之上加段级故障注入：user 消息含 failSub → 回 400。
// 4xx 不触发网络层重试，段在首次尝试后立即转失败，保证请求计数确定。
type failMock struct {
	mu       sync.Mutex
	failSub  string
	requests []string
}

func (m *failMock) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		user := ""
		for _, msg := range req.Messages {
			if msg.Role == "user" {
				user = msg.Content
			}
		}
		m.mu.Lock()
		m.requests = append(m.requests, user)
		fail := m.failSub != "" && strings.Contains(user, m.failSub)
		m.mu.Unlock()
		if fail {
			http.Error(w, `{"error":{"message":"mock 注入的段级失败"}}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{"content": user + "\n\n（机翻译文冒烟标记）"},
			}},
		})
	}
}

func (m *failMock) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

func (m *failMock) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = nil
}

// 三节样本：中间节含故障标记。标题边界各收束一段 → 恰 3 段，中间段下标 1。
const resumeSample = `# Alpha

The first section translates normally in both runs.

## Beta

The middle section contains ZZFAILZZ and should fail on the first run only.

# Gamma

The last section also translates normally in both runs.
`

// 用例二：断点续跑。首轮注入中间段失败（其余两段落断点），
// 二轮修好 mock——断言只有失败段重新请求 LLM，其余命中缓存直取译文。
func TestE2ECheckpointResume(t *testing.T) {
	if testing.Short() {
		t.Skip("集成测试，-short 跳过")
	}
	if _, err := exec.LookPath("python"); err != nil {
		t.Skip("PATH 上没有 python，跳过管线集成测试")
	}
	t.Setenv("LITERATURE_ENGINE", filepath.Join(repoRoot(t), "engine"))

	mock := &failMock{failSub: "ZZFAILZZ"}
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()

	srcDir := t.TempDir()
	mdPath := filepath.Join(srcDir, "resume_paper.md")
	if err := os.WriteFile(mdPath, []byte(resumeSample), 0o644); err != nil {
		t.Fatal(err)
	}

	// 两次调用共用同一 cfg——缓存指纹（规则/双语/跳文献/模型）必须一致才会续跑
	cfg := config.Default()
	cfg.LLM.BaseURL = srv.URL
	cfg.LLM.APIKey = "test-key"
	cfg.LLM.Model = "mock-model"
	cfg.LLM.Concurrency = 2
	cfg.Export.Bilingual = false

	pl, err := pipeline.New(cfg, pipeline.Callbacks{
		Log:      func(string, string) {},
		Progress: func(int, int, int, []int, []int, bool) {}, // 非 nil：持续抽干引擎事件通道
		Stage:    func(string) {},
	})
	if err != nil {
		t.Fatal(err)
	}

	plan, err := pipeline.NewArchivePlan(t.TempDir(), mdPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	// ---- 第一轮：中间段 400 → 该段失败保留原文，其余两段完成并落断点
	res1, err := pl.TranslateMD(ctx, mdPath, "", plan)
	if err != nil {
		t.Fatalf("首轮 TranslateMD（段失败不应让任务报错）: %v", err)
	}
	if res1.Total != 3 || res1.Done != 2 || len(res1.Failed) != 1 || res1.Failed[0].I != 1 {
		t.Fatalf("首轮统计异常: total=%d done=%d failed=%+v", res1.Total, res1.Done, res1.Failed)
	}
	// 首轮请求数 = 每段一次（3）+ 收尾补翻对失败段的重试（1）。
	// mock 的 400 是确定性的，补翻救不回——正好验证补翻失败不改统计。
	if n := mock.count(); n != 4 {
		t.Errorf("首轮 mock 收到 %d 次请求，期望 4（每段一次 + 失败段收尾补翻一次）", n)
	}
	zh1, err := os.ReadFile(plan.ZhMDPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(zh1), "机翻译文冒烟标记"); got != 2 {
		t.Errorf("首轮译文标记 %d 处，期望 2（失败段保留英文）", got)
	}
	if !strings.Contains(string(zh1), "ZZFAILZZ") {
		t.Errorf("失败段应保留英文原文（ZZFAILZZ）")
	}

	// ---- 第二轮：修好 mock。前两段命中断点，只有失败段重新请求
	mock.failSub = ""
	mock.reset()
	res2, err := pl.TranslateMD(ctx, mdPath, "", plan)
	if err != nil {
		t.Fatalf("二轮 TranslateMD: %v", err)
	}
	if res2.Total != 3 || res2.Done != 3 || len(res2.Failed) != 0 {
		t.Fatalf("二轮统计异常: total=%d done=%d failed=%+v", res2.Total, res2.Done, res2.Failed)
	}
	if n := mock.count(); n != 1 {
		t.Errorf("二轮 mock 收到 %d 次请求，期望 1（断点命中，仅补译失败段）", n)
	}
	zh2, err := os.ReadFile(plan.ZhMDPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(zh2), "机翻译文冒烟标记"); got != 3 {
		t.Errorf("二轮译文标记 %d 处，期望 3（失败段补齐）", got)
	}

	// 断点文件：meta 指纹行 + 3 条译文记录（失败标记被成功行覆盖后仍留档）
	data, err := os.ReadFile(plan.ProgressPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"meta"`) || strings.Count(string(data), `"zh"`) != 3 {
		t.Errorf("断点文件内容异常（应有 meta 行 + 3 条 zh 记录）：\n%s", data)
	}
}
