package app

import (
	"os"
	"path/filepath"
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
		a := &App{cfg: config.Default()}
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
