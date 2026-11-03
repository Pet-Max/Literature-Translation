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
