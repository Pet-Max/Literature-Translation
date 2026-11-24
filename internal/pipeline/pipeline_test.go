package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"literature-translation/internal/config"
	"literature-translation/internal/fonts"
	"literature-translation/internal/mineru"
	"literature-translation/internal/repair"
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
	if _, attempted, _, e := p.ResumeExport(ctx, plan); attempted || e != nil {
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
	const source = `C:\Windows\Fonts\simhei.ttf`
	// 与 fonts_test 同款守卫：CI runner（英文版 Windows）没有 CJK 字体，
	// 且 Validate 拒收纯拉丁字体，无法用其他系统字体重写该测试。
	if _, err := os.Stat(source); err != nil {
		t.Skip("系统无 simhei.ttf，跳过")
	}
	t.Setenv("APPDATA", t.TempDir())
	uf, e := fonts.Add(source)
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

// reusableArchiveParse：解析复用守卫——归档英文 MD 与源文件/指纹一致时
// 判定可续用；翻译未完成（暂停接续场景）不阻拦，内容/指纹/解析器变化
// 与元数据缺失则拒绝。
func TestReusableArchiveParse(t *testing.T) {
	d := t.TempDir()
	source := filepath.Join(d, "doc.pdf")
	os.WriteFile(source, []byte("# Source\n\n![figure](same.png)"), 0600)
	os.WriteFile(filepath.Join(d, "same.png"), []byte("resource"), 0600)
	plan, e := NewArchivePlan(t.TempDir(), source)
	if e != nil {
		t.Fatal(e)
	}
	p, _ := New(config.Default(), Callbacks{})
	ctx := context.Background()
	if _, e = p.beginRun(ctx, source, source, plan.ZhMDPath, plan); e != nil {
		t.Fatal(e)
	}
	copyFile(source, plan.EnMDPath)
	archiveResources(context.Background(), source, plan.EnMDDir)
	if !p.reusableArchiveParse(plan) {
		t.Fatal("一致的归档英文 MD 应判定可复用")
	}
	// 翻译未完成（beginRun 后未 finishRun）不阻拦——暂停接续正是此场景
	if m, e := readRunMeta(metadataPath(plan.ZhMDPath, plan)); e != nil || m.Complete {
		t.Fatalf("前置状态异常: complete=%v err=%v", m.Complete, e)
	}
	if !p.reusableArchiveParse(plan) {
		t.Fatal("翻译未完成也应复用（暂停接续场景）")
	}
	// 源文件变化 → 拒绝
	os.WriteFile(source, []byte("changed"), 0600)
	if p.reusableArchiveParse(plan) {
		t.Fatal("源文件变化不应复用")
	}
	os.WriteFile(source, []byte("# Source\n\n![figure](same.png)"), 0600)
	// 归档英文 MD 变化（如人工编辑）→ 拒绝
	os.WriteFile(plan.EnMDPath, []byte("edited"), 0600)
	if p.reusableArchiveParse(plan) {
		t.Fatal("归档内容变化不应复用")
	}
	copyFile(source, plan.EnMDPath)
	// 解析器版本变化 → 拒绝（指纹含 Parser）
	original := p.Cfg.Clone()
	p.Cfg.MinerU.ModelVersion = "other"
	if p.reusableArchiveParse(plan) {
		t.Fatal("解析器版本变化不应复用")
	}
	p.Cfg = original.Clone()
	// 归档图片缺失 → 拒绝
	os.Remove(filepath.Join(plan.EnMDDir, "same.png"))
	if p.reusableArchiveParse(plan) {
		t.Fatal("归档图片缺失不应复用")
	}
	// 元数据缺失 → 拒绝
	os.Remove(metadataPath(plan.ZhMDPath, plan))
	if p.reusableArchiveParse(plan) {
		t.Fatal("缺少元数据不应复用")
	}
}

// 端到端阶段交互契约：翻译途中取消并接续时复用归档解析，绝不重调 MinerU；
// 无文本层扫描件亦须在归档前完成 HTML 表格还原。
func TestRunPDFPauseResumeAndScannedTablePreserved(t *testing.T) {
	dir := t.TempDir()
	fakePDF := filepath.Join(dir, "scanned.pdf")
	if err := os.WriteFile(fakePDF, []byte("%PDF-1.4\n%not-extractable"), 0o644); err != nil {
		t.Fatal(err)
	}

	var parseCalls atomic.Int32
	var activeCancel atomic.Pointer[context.CancelFunc]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fn := activeCancel.Load(); fn != nil {
			(*fn)()
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.MinerU.Token = "tok"
	cfg.LLM.BaseURL = srv.URL
	cfg.LLM.APIKey = "k"
	cfg.LLM.Model = "m"
	cfg.LLM.Concurrency = 1

	pl, _ := New(cfg, Callbacks{})
	pl.parseOneFn = func(ctx context.Context, pdfPath, workDir string, onState func(string)) (*mineru.Result, error) {
		parseCalls.Add(1)
		outDir := filepath.Join(workDir, "result-mock")
		_ = os.MkdirAll(outDir, 0o755)
		md := filepath.Join(outDir, "full.md")
		content := "# Scanned Doc\n\n<table><tr><td>Param</td><td>Value</td></tr><tr><td>Ion</td><td>16</td></tr></table>\n"
		if err := os.WriteFile(md, []byte(content), 0o644); err != nil {
			return nil, err
		}
		return &mineru.Result{MDPath: md}, nil
	}

	archiveRoot := t.TempDir()
	plan, err := NewArchivePlan(archiveRoot, fakePDF)
	if err != nil {
		t.Fatal(err)
	}

	// 第 1 次运行：在翻译阶段取消，验证解析结果已还原表格并归档至 02_en_md
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	activeCancel.Store(&cancel1)

	_, err = pl.RunPDF(ctx1, fakePDF, "", plan)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("期望返回 Canceled，got %v", err)
	}
	if parseCalls.Load() != 1 {
		t.Fatalf("第 1 次应调用解析 1 次，got %d", parseCalls.Load())
	}

	// 验证扫描件在无文本层情况下已完成 HTML→Markdown 表格还原且落盘至 02_en_md
	archived, rerr := os.ReadFile(plan.EnMDPath)
	if rerr != nil {
		t.Fatalf("翻译前必须已归档 02_en_md: %v", rerr)
	}
	if !strings.Contains(string(archived), "| Param | Value |") {
		t.Fatalf("扫描件 HTML 表格未还原:\n%s", string(archived))
	}
	if !pl.reusableArchiveParse(plan) {
		t.Fatal("翻译暂停后归档英文 MD 应判定为可复用，不得重烧 MinerU")
	}

	// 第 2 次运行：保持同一配置直接接续，在翻译阶段取消退出，断言不再重复调用 MinerU 解析
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	activeCancel.Store(&cancel2)
	_, err = pl.RunPDF(ctx2, fakePDF, "", plan)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("接续运行期望在翻译取消阶段返回 Canceled，got %v", err)
	}
	if parseCalls.Load() != 1 {
		t.Fatalf("接续重跑不应再次调用 MinerU 解析，parseCalls = %d（期望 1）", parseCalls.Load())
	}

	// 另验证：遗留含 HTML 表格的中文 MD 在进入渲染前经 NormalizeStructure 不会静默丢表
	normZH, nst := repair.NormalizeStructure("正文\n\n<table><tr><td>Col</td></tr><tr><td>Val</td></tr></table>")
	if nst.Tables != 1 || !strings.Contains(normZH, "| Col |") {
		t.Fatalf("结构还原未转化 <table>: %q %+v", normZH, nst)
	}
}

func TestTranslateMDWithHTMLTableAndResume(t *testing.T) {
	srcDir := t.TempDir()
	srcMD := filepath.Join(srcDir, "doc.md")
	origContent := "# Markdown Table Test\n\n<table><tr><td>Param</td><td>Val</td></tr><tr><td>A</td><td>1</td></tr></table>\n\nEnd paragraph."
	if err := os.WriteFile(srcMD, []byte(origContent), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"# 表格测试\n\n| 参数 | 数值 |\n| --- | --- |\n| A | 1 |\n\n结束段落。"}}]}`))
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.LLM.BaseURL = srv.URL
	cfg.LLM.APIKey = "k"
	cfg.LLM.Model = "m"
	cfg.LLM.Concurrency = 1

	pl, _ := New(cfg, Callbacks{})
	plan, err := NewArchivePlan(t.TempDir(), srcMD)
	if err != nil {
		t.Fatal(err)
	}

	_, err = pl.TranslateMD(context.Background(), srcMD, "", plan)
	if err != nil && !strings.Contains(err.Error(), "Microsoft Edge 或 Chrome") {
		t.Fatalf("TranslateMD 失败（应不因结构规整触发哈希不匹配）: %v", err)
	}
	enBytes, rerr := os.ReadFile(plan.EnMDPath)
	if rerr != nil {
		t.Fatalf("归档英文 MD 缺失: %v", rerr)
	}
	if !strings.Contains(string(enBytes), "| Param | Val |") {
		t.Fatalf("归档英文 MD 未完成 HTML 表格规整:\n%s", string(enBytes))
	}
	if _, rerr := pl.resumableRun(plan); rerr != nil {
		t.Fatalf("结构规整后的 TranslateMD 完成后应可断点续导出: %v", rerr)
	}
}

func TestRunPDFParseReuseSyncsMissingZhImages(t *testing.T) {
	dir := t.TempDir()
	fakePDF := filepath.Join(dir, "with_img.pdf")
	if err := os.WriteFile(fakePDF, []byte("%PDF-1.4\n%mock"), 0o644); err != nil {
		t.Fatal(err)
	}

	var parseCalls atomic.Int32
	var activeCancel atomic.Pointer[context.CancelFunc]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fn := activeCancel.Load(); fn != nil {
			(*fn)()
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"# 图表测试\n\n见图 ![图](images/a.png)。"}}]}`))
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.MinerU.Token = "tok"
	cfg.LLM.BaseURL = srv.URL
	cfg.LLM.APIKey = "k"
	cfg.LLM.Model = "m"
	cfg.LLM.Concurrency = 1

	pl, _ := New(cfg, Callbacks{})
	pl.parseOneFn = func(ctx context.Context, pdfPath, workDir string, onState func(string)) (*mineru.Result, error) {
		parseCalls.Add(1)
		outDir := filepath.Join(workDir, "result-mock")
		imgDir := filepath.Join(outDir, "images")
		_ = os.MkdirAll(imgDir, 0o755)
		_ = os.WriteFile(filepath.Join(imgDir, "a.png"), []byte("png-bytes"), 0o644)
		md := filepath.Join(outDir, "full.md")
		_ = os.WriteFile(md, []byte("# Chart Test\n\nSee ![fig](images/a.png).\n"), 0o644)
		return &mineru.Result{MDPath: md}, nil
	}

	plan, err := NewArchivePlan(t.TempDir(), fakePDF)
	if err != nil {
		t.Fatal(err)
	}

	// 第 1 次：翻译取消，02_en_md 与 03_zh_md/images 已归档
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	activeCancel.Store(&cancel1)
	if _, err := pl.RunPDF(ctx1, fakePDF, "", plan); !errors.Is(err, context.Canceled) {
		t.Fatalf("期望 Canceled，got %v", err)
	}

	// 模拟用户删除 03_zh_md 整个目录以强制重译：02_en_md 仍可复用，须重新同步 images 到 03_zh_md
	if err := os.RemoveAll(plan.ZhMDDir); err != nil {
		t.Fatal(err)
	}
	activeCancel.Store(nil)
	_, err = pl.RunPDF(context.Background(), fakePDF, "", plan)
	if err != nil && !strings.Contains(err.Error(), "Microsoft Edge 或 Chrome") {
		t.Fatalf("删掉 03_zh_md 后解析复用重译不应因缺图失败: %v", err)
	}
	if parseCalls.Load() != 1 {
		t.Fatalf("应复用归档解析结果，parseCalls = %d（期望 1）", parseCalls.Load())
	}
	if _, serr := os.Stat(filepath.Join(plan.ZhMDDir, "images", "a.png")); serr != nil {
		t.Fatalf("解析复用时未同步补齐 03_zh_md/images/a.png: %v", serr)
	}
}
