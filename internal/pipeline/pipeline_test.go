package pipeline

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"literature-translation/internal/config"
	"literature-translation/internal/fonts"
)

func TestCopyFileIdentity(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "File.md")
	os.WriteFile(src, []byte("keep"), 0600)
	alias := filepath.Join(d, "hardlink.md")
	if e := os.Link(src, alias); e != nil {
		t.Fatal(e)
	}
	for _, dst := range []string{alias, strings.ToUpper(src)} {
		if e := copyFile(src, dst); e != nil {
			t.Fatal(e)
		}
		if b, _ := os.ReadFile(src); string(b) != "keep" {
			t.Fatal("identity copy truncated source")
		}
	}

}

// 自拷贝守卫：自拷贝必须原样保留源文件，正常拷贝行为不回归。
func TestCopyFileSelfCopyAndNormal(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "paper_zh.md")
	content := "# 译文\n\n不应被清空。"
	if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// 自拷贝：内容必须原样保留（旧实现 os.Create 先截断会把源清成空文件）
	if err := copyFile(src, src); err != nil {
		t.Fatalf("自拷贝报错: %v", err)
	}
	if data, err := os.ReadFile(src); err != nil || string(data) != content {
		t.Fatalf("自拷贝后内容被破坏: err=%v data=%q", err, data)
	}
	// 正常拷贝不回归（含自动建父目录）
	dst := filepath.Join(dir, "nested", "copy.md")
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("正常拷贝报错: %v", err)
	}
	if data, err := os.ReadFile(dst); err != nil || string(data) != content {
		t.Fatalf("正常拷贝内容不符: err=%v data=%q", err, data)
	}
}

func TestLocalResourceArchivesAndBoundaries(t *testing.T) {
	d := t.TempDir()
	md := filepath.Join(d, "full.md")
	dest := t.TempDir()
	for _, rel := range []string{"images/a.png", "figs/b.svg", "same.png"} {
		p := filepath.Join(d, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0700)
		os.WriteFile(p, []byte(rel), 0600)
	}
	os.WriteFile(md, []byte("![a](images/a.png)\n![b](figs/b.svg)\n![same](same.png)"), 0600)
	if e := archiveResources(context.Background(), md, dest); e != nil {
		t.Fatal(e)
	}
	for _, rel := range []string{"images/a.png", "figs/b.svg", "same.png"} {
		if b, e := os.ReadFile(filepath.Join(dest, rel)); e != nil || string(b) != rel {
			t.Fatal("resource missing", rel, e)
		}
	}
	for _, ref := range []string{"missing.png", "../outside.png", "/absolute.png", "file:///C:/outside.png"} {
		os.WriteFile(md, []byte("![bad]("+ref+")"), 0600)
		if e := archiveResources(context.Background(), md, dest); e == nil {
			t.Fatal("invalid resource accepted", ref)
		}
	}
}

// Only referenced files are copied now; a target below the source cannot
// trigger the old recursive directory-copy behavior.
func TestNestedResourceArchiveIsFinite(t *testing.T) {
	d := t.TempDir()
	md := filepath.Join(d, "source.md")
	os.WriteFile(md, []byte("![figure](figure.png)"), 0600)
	os.WriteFile(filepath.Join(d, "figure.png"), []byte("figure"), 0600)
	dest := filepath.Join(d, "archive")
	if err := archiveResources(context.Background(), md, dest); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dest)
	if err != nil || len(entries) != 1 || entries[0].Name() != "figure.png" {
		t.Fatal("recursive or unrelated copy", entries, err)
	}
}

func TestCancelledCopyDoesNotPublish(t *testing.T) {
	d := t.TempDir()
	src, dst := filepath.Join(d, "source.md"), filepath.Join(d, "target.md")
	os.WriteFile(src, []byte("source"), 0600)
	os.WriteFile(dst, []byte("keep"), 0600)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := copyFileContext(ctx, src, dst); err != context.Canceled {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "keep" {
		t.Fatal("cancelled copy published output")
	}
}

func TestRunMetadataGuardsAndFingerprints(t *testing.T) {
	d := t.TempDir()
	source := filepath.Join(d, "source.md")
	os.WriteFile(source, []byte("# Source\n\n![figure](same.png)"), 0600)
	os.WriteFile(filepath.Join(d, "same.png"), []byte("resource"), 0600)
	plan, e := NewArchivePlan(t.TempDir(), source)
	if e != nil {
		t.Fatal(e)
	}
	p, _ := New(config.Default(), Callbacks{})
	p.Cfg.LLM.BaseURL = "https://example.invalid/v1"
	p.Cfg.LLM.APIKey = "secret-should-not-appear"
	p.Cfg.MinerU.Token = "other-secret"
	ctx := context.Background()
	if _, e = p.beginRun(ctx, source, source, plan.ZhMDPath, plan); e != nil {
		t.Fatal(e)
	}
	copyFile(source, plan.EnMDPath)
	copyFile(source, plan.ZhMDPath)
	archiveResources(context.Background(), source, plan.EnMDDir, plan.ZhMDDir)
	tr := &translateOutcome{Total: 3, Done: 3, KeptReferences: 1}
	if e = p.finishRun(ctx, source, source, plan.ZhMDPath, plan, tr); e != nil {
		t.Fatal(e)
	}
	if _, e = p.resumableRun(plan); e != nil {
		t.Fatal("valid metadata refused", e)
	}
	b, e := os.ReadFile(metadataPath(plan.ZhMDPath, plan))
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(b), "secret") || strings.Contains(string(b), "api_key") || strings.Contains(string(b), "token") {
		t.Fatal("metadata contains credentials")
	}
	p.Cfg.Export.TOC = true
	p.Cfg.Export.TwoColumn = true
	p.Cfg.Export.Font = "different font"
	if _, e = p.resumableRun(plan); e != nil {
		t.Fatal("presentation-only change invalidated translation", e)
	}
	original := p.Cfg.Clone()
	for _, change := range []func(){
		func() { p.Cfg.LLM.BaseURL = "https://other.invalid/v1" },
		func() { p.Cfg.Rules.Presets[0].Text += "changed" },
		func() { p.Cfg.MinerU.ModelVersion = "pipeline" },
		func() { p.Cfg.Repair.Symbols = !p.Cfg.Repair.Symbols },
	} {
		change()
		if _, e = p.resumableRun(plan); e == nil {
			t.Fatal("incompatible config reused")
		}
		p.Cfg = original.Clone()
	}
	// Source content, resource bytes, translation bytes and completion all matter.
	for _, file := range []string{source, filepath.Join(d, "same.png"), plan.ZhMDPath, filepath.Join(plan.ZhMDDir, "same.png")} {
		old, _ := os.ReadFile(file)
		os.WriteFile(file, []byte("changed"), 0600)
		if _, e = p.resumableRun(plan); e == nil {
			t.Fatal("changed content reused", file)
		}
		os.WriteFile(file, old, 0600)
	}
	var m runMeta
	json.Unmarshal(b, &m)
	legacy := m
	legacy.Schema = 1
	writeRunMeta(ctx, metadataPath(plan.ZhMDPath, plan), legacy)
	if _, e = p.resumableRun(plan); e == nil {
		t.Fatal("Schema 1 directly re-exported")
	}
	progress, e := p.beginRun(ctx, source, source, plan.ZhMDPath, plan)
	if e != nil || filepath.Base(progress) != legacy.ProgressFile {
		t.Fatal("compatible legacy checkpoint path lost", e)
	}
	upgraded, e := readRunMeta(metadataPath(plan.ZhMDPath, plan))
	if e != nil || upgraded.Schema != 2 || upgraded.Complete {
		t.Fatal("legacy run not safely upgraded", e)
	}
	for _, mutate := range []func(*runMeta){func(m *runMeta) { m.Complete = false }, func(m *runMeta) { m.Done = 2 }, func(m *runMeta) { m.Total = 0 }} {
		bad := m
		mutate(&bad)
		writeRunMeta(ctx, metadataPath(plan.ZhMDPath, plan), bad)
		if _, e = p.resumableRun(plan); e == nil {
			t.Fatal("incomplete run reused")
		}
	}
	os.Remove(metadataPath(plan.ZhMDPath, plan))
	if _, attempted, e := p.ResumeExport(ctx, plan); attempted || e != nil {
		t.Fatal("legacy archive directly exported", e)
	}
	// A known provider change selects independent checkpoint storage.
	writeRunMeta(ctx, metadataPath(plan.ZhMDPath, plan), m)
	p.Cfg.LLM.BaseURL = "https://changed.invalid/v1"
	path, e := p.beginRun(ctx, source, source, plan.ZhMDPath, plan)
	if e != nil || path == plan.ProgressPath {
		t.Fatal("known provider change reused checkpoint", e)
	}
}

func TestTaskFontSurvivesUnregister(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	uf, e := fonts.Add(`C:\Windows\Fonts\simhei.ttf`)
	if e != nil {
		t.Fatal(e)
	}
	if e = fonts.Select(uf.InternalName); e != nil {
		t.Fatal(e)
	}
	p, _ := New(config.Default(), Callbacks{})
	if e = p.PrepareTask(true); e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	snapshot := filepath.Join(p.fontsDir, uf.FileName)
	before, e := fileHash(snapshot)
	if e != nil {
		t.Fatal(e)
	}
	if e = fonts.Unregister(uf.InternalName); e != nil {
		t.Fatal(e)
	}
	if after, e := fileHash(snapshot); e != nil || after != before {
		t.Fatal("running task font changed", e)
	}
	latest, e := config.Load()
	if e != nil || latest.Export.UserFont != "" {
		t.Fatal("next task retains deleted font", e)
	}
}
