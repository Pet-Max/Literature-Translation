package app

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"literature-translation/internal/config"
)

func writeTemp(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestQueueAddRouting 验证三档模式 × 文件语言的路由表。
func TestQueueAddRouting(t *testing.T) {
	dir := t.TempDir()
	enMD := writeTemp(t, dir, "en.md", "Power spectral density constrains the heat current.")
	zhMD := writeTemp(t, dir, "zh.md", "功率谱密度对热流的约束。")
	pdf := writeTemp(t, dir, "any.pdf", "%PDF-1.4 fake")

	newApp := func() *App {
		a := &App{}
		a.cfg.Store(config.Default())
		a.queue = NewTaskQueue(func(*Task) error { return nil })
		return a
	}

	t.Run("auto_en_md_to_translate", func(t *testing.T) {
		a := newApp()
		ts, err := a.QueueAddMD([]string{enMD}, "auto")
		if err != nil || len(ts) != 1 || ts[0].Kind != "md" || ts[0].Mode != "auto" {
			t.Fatalf("want kind=md auto, got %+v err=%v", ts, err)
		}
	})
	t.Run("auto_zh_md_to_export", func(t *testing.T) {
		a := newApp()
		ts, err := a.QueueAddMD([]string{zhMD}, "auto")
		if err != nil || len(ts) != 1 || ts[0].Kind != "export" {
			t.Fatalf("want kind=export, got %+v err=%v", ts, err)
		}
	})
	t.Run("translate_zh_md_rejected", func(t *testing.T) {
		a := newApp()
		if _, err := a.QueueAddMD([]string{zhMD}, "translate"); err == nil {
			t.Fatal("翻译档 + 中文 MD 应返回错误")
		}
	})
	t.Run("export_en_md_forced_export", func(t *testing.T) {
		a := newApp()
		ts, err := a.QueueAddMD([]string{enMD}, "export")
		if err != nil || len(ts) != 1 || ts[0].Kind != "export" {
			t.Fatalf("want kind=export (forced), got %+v err=%v", ts, err)
		}
	})
	t.Run("export_pdf_rejected", func(t *testing.T) {
		a := newApp()
		if _, err := a.QueueAddPDF([]string{pdf}, "export"); err == nil {
			t.Fatal("导出档 + PDF 应返回错误")
		}
	})
	t.Run("translate_pdf_rejected", func(t *testing.T) {
		a := newApp()
		if _, err := a.QueueAddPDF([]string{pdf}, "translate"); err == nil {
			t.Fatal("翻译档 + PDF 应返回错误（跳过解析，仅收 MD）")
		}
	})
}

func TestQueueClaimsAndCancellation(t *testing.T) {
	var calls atomic.Int32
	q := NewTaskQueue(func(*Task) error { calls.Add(1); return nil })
	task, _ := q.Add("md", "test.md", "translate")
	q.Start()
	var claimed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if q.claimPending() != nil {
				claimed.Add(1)
			}
		}()
	}
	wg.Wait()
	if claimed.Load() != 1 {
		t.Fatal("task was claimed more than once", claimed.Load())
	}
	if err := q.Remove(task.ID); err == nil {
		t.Fatal("claimed task removed")
	}
	if _, err := q.Retry(task.ID); err == nil {
		t.Fatal("claimed task retried")
	}
	q.Mutate(func(t *Task) { t.Done = 7 })
	snapshot, _ := q.Add("md", "test.md", "translate")
	q.Mutate(func(t *Task) { t.Done = 8 })
	if snapshot.Done != 7 {
		t.Fatal("Add did not return independent snapshot")
	}
	q2 := NewTaskQueue(func(*Task) error { calls.Add(1); return nil })
	q2.Add("md", "queued.md", "translate")
	q2.Start()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	q2.Worker(ctx)
	if calls.Load() != 0 || q2.PendingCount() != 1 {
		t.Fatal("cancelled worker started a task")
	}
}

// Retry 的「仅重导出」标记捕获：失败阶段为「排版中」
// 的任务重试时置位 ResumeExport，其余阶段不置位。
func TestRetryCapturesExportStage(t *testing.T) {
	cases := []struct {
		name  string
		stage string
		want  bool
	}{
		{"失败于排版中", "排版中", true},
		{"失败于翻译中", "翻译中", false},
		{"失败于解析中", "解析中（running）", false},
		{"完成的任务重跑", "完成", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := NewTaskQueue(func(*Task) error { return nil })
			tk, err := q.Add("pdf", `D:\tmp\paper.pdf`, "auto")
			if err != nil {
				t.Fatal(err)
			}
			q.mu.Lock()
			q.tasks[0].Stage = c.stage
			q.mu.Unlock()
			if _, err := q.Retry(tk.ID); err != nil {
				t.Fatal(err)
			}
			q.mu.Lock()
			defer q.mu.Unlock()
			if got := q.tasks[0].ResumeExport; got != c.want {
				t.Fatalf("ResumeExport = %v, 期望 %v", got, c.want)
			}
			// 标记置位不影响常规重置语义
			if q.tasks[0].Status != StatusPending || q.tasks[0].Stage != "重新排队" {
				t.Fatalf("重置语义被破坏: status=%q stage=%q", q.tasks[0].Status, q.tasks[0].Stage)
			}
		})
	}
}

// TestQueuePersistRoundTrip 状态节点落盘 + 启动恢复：Add/跑完触发回调，
// 恢复时非终态重置为等待、终态连同「排版中」Stage 原样保留（重试仍可
// 走仅重导出短路）。
func TestQueuePersistRoundTrip(t *testing.T) {
	q := NewTaskQueue(func(*Task) error { return nil })
	var saved [][]Task
	var mu sync.Mutex
	q.onPersist = func(tasks []Task) {
		mu.Lock()
		saved = append(saved, tasks)
		mu.Unlock()
	}
	if _, err := q.Add("pdf", "D:/books/en.pdf", "auto"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	n := len(saved)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("入队应落盘一次，got %d", n)
	}

	// 终态任务：失败于「排版中」；进行中任务：翻译中
	q.mu.Lock()
	q.tasks[0].Status = StatusFailed
	q.tasks[0].Stage = string(StatusRendering)
	failed := *q.tasks[0]
	q.tasks = append(q.tasks, &Task{ID: "T2", Code: "B2", Kind: "pdf",
		Status: StatusTranslating, Stage: "翻译中", SourcePath: "D:/books/other.pdf"})
	q.mu.Unlock()

	q.loadTasks([]Task{failed,
		Task{ID: "T2", Code: "B2", Kind: "pdf", Status: StatusTranslating,
			Stage: "翻译中", SourcePath: "D:/books/other.pdf"}})
	got := q.List()
	if len(got) != 2 {
		t.Fatalf("应恢复 2 个任务，got %d", len(got))
	}
	if got[0].Status != StatusFailed || got[0].Stage != string(StatusRendering) {
		t.Fatalf("终态任务应原样保留: %+v", got[0])
	}
	if got[1].Status != StatusPending || got[1].Err != "" {
		t.Fatalf("非终态任务应重置为等待: %+v", got[1])
	}
	if got[1].Stage != "翻译中" {
		t.Fatalf("非终态任务的 Stage 应保留: %+v", got[1])
	}

	// Retry 重算仅重导出标记：失败于「排版中」→ 短路
	if _, err := q.Retry(got[0].ID); err != nil {
		t.Fatal(err)
	}
	if !q.List()[0].ResumeExport {
		t.Fatal("失败于排版中的任务重试应置位 ResumeExport")
	}
}

// PauseNow：置位暂停标志与当前任务的 PauseRequested 并触发任务级取消；
// 无进行中任务时仅暂停队列，不触发取消。
func TestPauseNowCancelsCurrent(t *testing.T) {
	q := NewTaskQueue(func(*Task) error { return nil })
	if _, err := q.Add("pdf", `D:\tmp\paper.pdf`, "auto"); err != nil {
		t.Fatal(err)
	}
	q.mu.Lock()
	q.tasks[0].Status = StatusTranslating
	q.current = q.tasks[0]
	q.mu.Unlock()
	canceled := false
	q.setCancelCurrent(func() { canceled = true })

	q.PauseNow()

	if !canceled {
		t.Fatal("PauseNow 未触发任务级取消")
	}
	q.mu.Lock()
	paused, requested := q.paused, q.tasks[0].PauseRequested
	q.mu.Unlock()
	if !paused || !requested {
		t.Fatalf("paused=%v PauseRequested=%v, 期望均为 true", paused, requested)
	}
	// 翻译阶段暂停不记录 PauseStage（仅排版阶段需要仅重导出判定）
	if q.tasks[0].PauseStage != "" {
		t.Fatalf("翻译阶段暂停不应记录 PauseStage, got %q", q.tasks[0].PauseStage)
	}

	// 排版阶段暂停：PauseStage 记录来源阶段，重试仍走仅重导出短路
	q3 := NewTaskQueue(func(*Task) error { return nil })
	tk3, err := q3.Add("pdf", `D:\tmp\paper.pdf`, "auto")
	if err != nil {
		t.Fatal(err)
	}
	q3.mu.Lock()
	q3.tasks[0].Status = StatusRendering
	q3.current = q3.tasks[0]
	q3.mu.Unlock()
	q3.setCancelCurrent(func() {})
	q3.PauseNow()
	q3.Mutate(func(x *Task) { x.PauseStage = string(StatusRendering) }) // runner 侧记录（与 app.go 一致）
	q3.mu.Lock()
	q3.current = nil
	q3.mu.Unlock()
	if _, err := q3.Retry(tk3.ID); err != nil {
		t.Fatal(err)
	}
	q3.mu.Lock()
	defer q3.mu.Unlock()
	if !q3.tasks[0].ResumeExport {
		t.Fatal("排版阶段暂停后的重试应走仅重导出短路")
	}

	q2 := NewTaskQueue(func(*Task) error { return nil })
	q2.PauseNow()
	if !q2.IsPaused() {
		t.Fatal("无进行中任务时 PauseNow 应暂停队列")
	}

	q4 := NewTaskQueue(func(*Task) error { return nil })
	tk4, err := q4.Add("pdf", `D:\tmp\big.pdf`, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if updated, ok := q4.MutateByID(tk4.ID, func(x *Task) { x.Pages = 320; x.Parts = 2 }); !ok || updated.Pages != 320 || updated.Parts != 2 {
		t.Fatalf("MutateByID failed: ok=%v updated=%+v", ok, updated)
	}
	if _, ok := q4.MutateByID("missing", func(x *Task) {}); ok {
		t.Fatal("MutateByID should return false for unknown id")
	}
}
