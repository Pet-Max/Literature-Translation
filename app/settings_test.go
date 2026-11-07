package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"literature-translation/internal/config"
	"literature-translation/internal/fonts"
)

func settingsApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("APPDATA", t.TempDir())
	a := NewApp()
	a.cfg.Store(config.Default())
	a.pl, _ = a.newPipeline()
	return a
}
func patchJSON(t *testing.T, raw string) SettingsPatch {
	t.Helper()
	var p SettingsPatch
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTaskSnapshotDoesNotPublishOverNewSettings(t *testing.T) {
	a := settingsApp(t)
	if _, e := a.UpdateSettings(patchJSON(t, `{"llm":{"model":"old"}}`)); e != nil {
		t.Fatal(e)
	}
	task := a.taskPipeline()
	if e := task.PrepareTask(true); e != nil {
		t.Fatal(e)
	}
	defer task.Close()
	if _, e := a.UpdateSettings(patchJSON(t, `{"llm":{"model":"new"}}`)); e != nil {
		t.Fatal(e)
	}
	if task.Cfg.LLM.Model != "old" || a.GetConfig().LLM.Model != "new" {
		t.Fatal("snapshot overwrote application configuration")
	}
	if _, e := config.Update(func(c *config.Config) error { c.LLM.Model = "CLI-new"; return nil }); e != nil {
		t.Fatal(e)
	}
	latest, e := a.latestConfig()
	if e != nil || latest.LLM.Model != "CLI-new" || a.GetConfig().LLM.Model != "CLI-new" {
		t.Fatal("preflight used stale disk configuration", e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, e := a.latestConfig(); e != nil {
				t.Error(e)
			}
		}()
		go func() {
			defer wg.Done()
			if _, e := a.UpdateSettings(patchJSON(t, `{"llm":{"model":"concurrent-new"}}`)); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	disk, e := config.Load()
	if e != nil || disk.LLM.Model != "concurrent-new" || a.GetConfig().LLM.Model != disk.LLM.Model || task.Cfg.LLM.Model != "old" {
		t.Fatal("concurrent refresh lost settings", e)
	}
}

func TestSettingsTransactionAndTaskSnapshot(t *testing.T) {
	a := settingsApp(t)
	if a.pl.Cfg != nil {
		t.Fatal("callback template retains a stale configuration")
	}
	oldTask := a.taskPipeline()
	_, err := a.UpdateSettings(patchJSON(t, `{"llm":{"api_key":"  bearer demo-key  ","model":"new-model"},"export":{"toc":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.UpdateSettings(patchJSON(t, `{"export":{"toc":false},"llm":{"model":""}}`))
	if err != nil {
		t.Fatal(err)
	}
	c := a.GetConfig()
	if c.LLM.APIKey != "demo-key" || c.LLM.Model != "" || c.Export.TOC {
		t.Fatalf("patch semantics: %+v", c.LLM)
	}
	if oldTask.Cfg.LLM.APIKey != "" {
		t.Fatal("running task changed")
	}
	newTask := a.taskPipeline()
	if newTask.Cfg.LLM.APIKey != "demo-key" {
		t.Fatal("next task has stale settings")
	}
	c.Rules.Presets[0].Text = "outside mutation"
	if a.GetConfig().Rules.Presets[0].Text == c.Rules.Presets[0].Text {
		t.Fatal("GetConfig aliases published config")
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("APPDATA"), config.DataDirName, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var disk config.Config
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatal(err)
	}
	if disk.LLM.APIKey != newTask.Cfg.LLM.APIKey {
		t.Fatal("disk/memory normalization differ")
	}
}

func TestFontImportTransaction(t *testing.T) {
	const source = `C:\Windows\Fonts\simhei.ttf`
	if _, err := os.Stat(source); err != nil {
		t.Skip("no local CJK font fixture")
	}
	a := settingsApp(t)
	if _, err := a.UpdateSettings(patchJSON(t, `{"glossary_rows":[{"en":"unfinished","zh":""}]}`)); err != nil {
		t.Fatal(err)
	}
	oldTask := a.taskPipeline()
	p, _ := config.Path()
	_ = os.Remove(p + ".bak")
	if err := os.Mkdir(p+".bak", 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := a.FontAdd(source); err == nil {
		t.Fatal("font import accepted failed config write")
	}
	dir, _ := fonts.Dir()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 || len(a.FontList()) != 0 {
		t.Fatal("failed import left a file or published metadata", err)
	}
	if err := os.Remove(p + ".bak"); err != nil {
		t.Fatal(err)
	}
	uf, err := a.FontAdd(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetFontPreset("custom", uf.InternalName); err != nil {
		t.Fatal(err)
	}
	c := a.GetConfig()
	if len(a.FontList()) != 1 || c.Export.UserFont != uf.InternalName || len(*c.GlossaryRows) != 1 {
		t.Fatal("import/selection overwrote other settings")
	}
	if oldTask.Cfg.Export.UserFont != "" || len(oldTask.Cfg.UserFonts) != 0 {
		t.Fatal("import changed a running task")
	}
}

func TestSettingsConcurrentSectionsAndFailure(t *testing.T) {
	a := settingsApp(t)
	patches := []SettingsPatch{
		patchJSON(t, `{"mineru":{"token":"demo-token"}}`),
		patchJSON(t, `{"llm":{"model":"new-model"}}`),
		patchJSON(t, `{"export":{"bilingual":true}}`),
		patchJSON(t, `{"repair":{"symbols":false}}`),
	}
	var wg sync.WaitGroup
	for _, p := range patches {
		wg.Add(1)
		go func(p SettingsPatch) {
			defer wg.Done()
			if _, e := a.UpdateSettings(p); e != nil {
				t.Error(e)
			}
		}(p)
	}
	wg.Wait()
	c := a.GetConfig()
	if c.MinerU.Token != "demo-token" || c.LLM.Model != "new-model" || !c.Export.Bilingual || c.Repair.Symbols {
		t.Fatal("concurrent section lost")
	}
	p := filepath.Join(os.Getenv("APPDATA"), config.DataDirName, "config.json.bak")
	_ = os.Remove(p)
	if err := os.Mkdir(p, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := a.UpdateSettings(patchJSON(t, `{"llm":{"model":"must-not-publish"}}`)); err == nil {
		t.Fatal("wanted disk failure")
	}
	if a.GetConfig().LLM.Model != "new-model" {
		t.Fatal("failed write published")
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if _, err := a.UpdateSettings(patchJSON(t, `{"llm":{"model":"retry"}}`)); err != nil {
		t.Fatal(err)
	}
}

func TestRulesDraftAndIncompleteGlossaryPersist(t *testing.T) {
	a := settingsApp(t)
	p := patchJSON(t, `{"rules":{"active":"demo","presets":[{"name":"demo","text":"applied","draft_text":""}]},"glossary_rows":[{"en":"term","zh":"译名"},{"en":"unfinished","zh":""}]}`)
	c, err := a.UpdateSettings(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Rules.ActiveText() != "applied" || c.Rules.Presets[0].DraftText == nil || *c.Rules.Presets[0].DraftText != "" {
		t.Fatal("draft changed applied text")
	}
	if len(*c.GlossaryRows) != 2 || len(c.Glossary) != 1 {
		t.Fatal("incomplete row lost or sent to engine")
	}
	if err := a.SetFontPreset("songti", ""); err != nil {
		t.Fatal(err)
	}
	c = a.GetConfig()
	if len(*c.GlossaryRows) != 2 {
		t.Fatal("font overwrote glossary")
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("APPDATA"), config.DataDirName, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var disk config.Config
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatal(err)
	}
	if disk.Rules.Presets[0].DraftText == nil || len(*disk.GlossaryRows) != 2 {
		t.Fatal("drafts not persisted")
	}
	(*p.Rules.Presets)[0].Text = strings.Repeat("x", 4001)
	if _, err := a.UpdateSettings(p); err == nil {
		t.Fatal("oversize applied rule accepted")
	}
	(*p.Rules.Presets)[0].Text = "applied"
	draft := strings.Repeat("中", 4100)
	(*p.Rules.Presets)[0].DraftText = &draft
	if _, err := a.UpdateSettings(p); err != nil {
		t.Fatal("draft should not be truncated", err)
	}
	if len([]rune(*a.GetConfig().Rules.Presets[0].DraftText)) != 4100 {
		t.Fatal("draft truncated")
	}
	// Updating the active name alone preserves every applied version and draft.
	if _, err := a.UpdateSettings(patchJSON(t, `{"rules":{"active":"demo"}}`)); err != nil {
		t.Fatal(err)
	}
	if got := a.GetConfig(); got.Rules.ActiveText() != "applied" || *got.Rules.Presets[0].DraftText != draft {
		t.Fatal("partial rules patch discarded presets or applied a draft")
	}
}

func TestOutputDirectoryValidationHasNoSideEffects(t *testing.T) {
	a := settingsApp(t)
	base := t.TempDir()
	missing := filepath.Join(base, "not-created", "library")
	value := "  " + missing + "  "
	p := SettingsPatch{}
	if err := json.Unmarshal([]byte(`{"export":{}}`), &p); err != nil {
		t.Fatal(err)
	}
	p.Export.OutputDir = &value
	c, err := a.UpdateSettings(p)
	if err != nil || c.Export.OutputDir != missing {
		t.Fatal("path normalization failed", err)
	}
	if _, err := os.Stat(filepath.Dir(missing)); !os.IsNotExist(err) {
		t.Fatal("saving a path must not create it", err)
	}
	file := filepath.Join(base, "file.txt")
	if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"relative/path", filepath.Join(file, "child"), missing + "?", missing + ":stream"} {
		p.Export.OutputDir = &invalid
		if _, err := a.UpdateSettings(p); err == nil {
			t.Error("invalid directory accepted", invalid)
		}
		if a.GetConfig().Export.OutputDir != missing {
			t.Fatal("invalid directory replaced valid settings")
		}
	}
	empty := ""
	p.Export.OutputDir = &empty
	if c, err := a.UpdateSettings(p); err != nil || c.Export.OutputDir == "" {
		t.Fatal("empty path did not restore default", err)
	}
}

func TestTaskSettingsValidation(t *testing.T) {
	c := config.Default()
	if validateTaskSettings("export", c) != nil {
		t.Fatal("local export requires remote credentials")
	}
	if validateTaskSettings("md", c) == nil || validateTaskSettings("pdf", c) == nil {
		t.Fatal("translation accepted missing credentials")
	}
	c.LLM.APIKey, c.LLM.Model, c.LLM.BaseURL = "demo-key", "demo-model", "https://example.invalid/v1"
	if err := validateTaskSettings("md", c); err != nil {
		t.Fatal(err)
	}
	if validateTaskSettings("pdf", c) == nil {
		t.Fatal("PDF accepted missing MinerU token")
	}
	c.MinerU.Token = "demo-token"
	if err := validateTaskSettings("pdf", c); err != nil {
		t.Fatal(err)
	}
	c.LLM.BaseURL = "file:///local"
	if validateTaskSettings("md", c) == nil {
		t.Fatal("invalid model address accepted")
	}
}

func TestMinerUResponseMatrix(t *testing.T) {
	cases := []struct {
		status     int
		body, want string
	}{
		{200, `{"code":-60012}`, "ok"}, {200, `{"code":0}`, "ok"}, {200, `{"code":[200,0]}`, "ok"},
		{401, `{}`, "error"}, {403, `{}`, "error"}, {200, `{"msgCode":"A0202"}`, "error"}, {401, `{"msgCode":"A0211"}`, "error"},
		{429, `{}`, "error"}, {503, `{}`, "error"}, {200, `{}`, "warning"}, {200, `null`, "warning"}, {200, `{"code":null}`, "warning"},
		{200, `{"code":0,"success":false}`, "warning"}, {200, `<html>`, "warning"}, {200, `{"code":123}`, "warning"},
		{200, `{"code":[null]}`, "warning"}, {200, `{"code":[0,123]}`, "warning"},
	}
	for _, c := range cases {
		r := classifyMinerU(c.status, []byte(c.body))
		if r.Status != c.want {
			t.Errorf("%d %s: %+v", c.status, c.body, r)
		}
	}
}

func TestMinerUProbeMessages(t *testing.T) {
	cases := []struct {
		status     int
		body, want string
	}{
		{200, `{"code":-60012}`, "Token 有效，服务连通 · 解析功能待验证"},
		{200, `{"code":0}`, "Token 有效，服务连通 · 解析功能待验证"},
		{401, `{}`, "Token 认证失败，请检查凭据或账号权限"},
		{200, `{"msgCode":"A0202"}`, "Token 认证失败，请检查凭据或账号权限"},
		{401, `{"msgCode":"A0211"}`, "Token 已过期，请更新"},
		{200, `{}`, "已收到响应，暂时无法确认服务状态"},
		{200, `<html>`, "已收到响应，暂时无法确认服务状态"},
		{404, `{}`, "已收到响应，暂时无法确认服务状态"},
	}
	for _, c := range cases {
		if r := classifyMinerU(c.status, []byte(c.body)); r.Message != c.want {
			t.Errorf("%d %s: message %q, want %q", c.status, c.body, r.Message, c.want)
		}
	}
}

func TestMinerUProbeStatuses(t *testing.T) {
	a := settingsApp(t)
	old := http.DefaultClient
	defer func() { http.DefaultClient = old }()
	for _, status := range []int{200, 401} {
		http.DefaultClient = &http.Client{Transport: settingsTransport(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"code":-60012}`))}, nil
		})}
		r := a.TestMinerUWithConfig("demo-token")
		if status == 200 && (r.Status != "ok" || r.Message != "Token 有效，服务连通 · 解析功能待验证") {
			t.Fatal("successful probe not reported as ok", r)
		}
		if status == 401 && r.Status != "error" {
			t.Fatal("probe accepted authentication rejection", r)
		}
	}
}

func TestResetGlossaryToDefault(t *testing.T) {
	a := settingsApp(t)
	def := a.DefaultGlossary()
	if len(def) != 1 || def[0].En != "Schrödinger equation" || def[0].Zh != "薛定谔方程" {
		t.Fatal("unexpected factory glossary", def)
	}
	p := patchJSON(t, `{"glossary_rows":[{"en":"custom-a","zh":"自定义甲"},{"en":"custom-b","zh":"自定义乙"}]}`)
	if _, err := a.UpdateSettings(p); err != nil {
		t.Fatal(err)
	}
	if c := a.GetConfig(); len(*c.GlossaryRows) != 2 {
		t.Fatal("custom rows not applied")
	}
	c, err := a.ResetGlossaryToDefault()
	if err != nil {
		t.Fatal(err)
	}
	if len(*c.GlossaryRows) != 1 || (*c.GlossaryRows)[0] != def[0] || len(c.Glossary) != 1 {
		t.Fatal("reset did not restore factory glossary", *c.GlossaryRows)
	}
	if c = a.GetConfig(); len(*c.GlossaryRows) != 1 || (*c.GlossaryRows)[0] != def[0] || len(c.Glossary) != 1 {
		t.Fatal("reset not persisted to config", *c.GlossaryRows)
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("APPDATA"), config.DataDirName, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var disk config.Config
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatal(err)
	}
	if len(*disk.GlossaryRows) != 1 || (*disk.GlossaryRows)[0] != def[0] {
		t.Fatal("reset not persisted to disk")
	}
}

type settingsTransport func(*http.Request) (*http.Response, error)

func (f settingsTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestExplicitConnectionsAndTimeout(t *testing.T) {
	a := settingsApp(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer current-key" {
			t.Error("not current normalized inputs")
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"model-one"}]}`)
	}))
	defer srv.Close()
	r := a.TestLLMWithConfig(" "+srv.URL+"/v1/ ", "bearer current-key")
	if r.Status != "ok" || r.Message != "模型列表接口连通 · 1 个模型" {
		t.Fatal(r)
	}
	old := http.DefaultClient
	defer func() { http.DefaultClient = old }()
	http.DefaultClient = &http.Client{Transport: settingsTransport(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "Bearer current-token" {
			t.Error("wrong probe token")
		}
		if _, ok := req.Context().Deadline(); !ok {
			t.Error("missing timeout")
		}
		return nil, context.DeadlineExceeded
	})}
	r = a.TestMinerUWithConfig("\"Bearer current-token\"")
	if r.Status != "error" || r.Message != "暂时无法连接，请检查网络后重试" || strings.Contains(r.Message, "current-token") {
		t.Fatal("timeout leaked or misclassified", r)
	}
}

func TestRecoveryBlocksBackendAndFontResources(t *testing.T) {
	a := settingsApp(t)
	if _, err := a.UpdateSettings(patchJSON(t, `{"llm":{"model":"valid"}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.UpdateSettings(patchJSON(t, `{"llm":{"model":"newer"}}`)); err != nil {
		t.Fatal(err)
	}
	p, _ := config.Path()
	if err := os.WriteFile(p, []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if a.GetConfigLoadState().Ready {
		t.Fatal("corrupt config accepted")
	}
	if _, err := a.UpdateSettings(patchJSON(t, `{"export":{"toc":true}}`)); err == nil {
		t.Fatal("autosave bypassed recovery")
	}
	if err := a.QueueStart(); err == nil {
		t.Fatal("queue start bypassed recovery")
	}
	if _, err := a.RecoverConfig("restore_backup"); err != nil {
		t.Fatal(err)
	}
	if !a.GetConfigLoadState().Ready || a.GetConfig().LLM.Model != "valid" {
		t.Fatal("backup not published")
	}

	dir, _ := fonts.Dir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "registered.ttf"), []byte("font-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Update(func(c *config.Config) error {
		c.UserFonts = append(c.UserFonts, config.UserFont{InternalName: "UserFont-1", FileName: "registered.ttf", DisplayName: "fixture"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h := AssetHandler(a)
	for path, want := range map[string]int{"/userfonts/registered.ttf": 200, "/userfonts/unregistered.ttf": 404, "/userfonts/../config.json": 404, "/userfonts/a%5Cb.ttf": 404} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != want {
			t.Errorf("%s: %d, want %d", path, w.Code, want)
		}
		if want == 200 && (w.Body.String() != "font-fixture" || w.Header().Get("Content-Type") != "font/ttf") {
			t.Fatal("font response corrupted")
		}
	}
}
