package config

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirstLoadEnvironmentAndLegacyRules(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("MINERU_TOKEN", "Bearer temporary-token")
	t.Setenv("LLM_API_KEY", "temporary-key")
	t.Setenv("LLM_BASE_URL", "https://example.invalid/v1/")
	t.Setenv("LLM_MODEL", "temporary-model")
	c, e := Load()
	if e != nil {
		t.Fatal(e)
	}
	if c.MinerU.Token != "temporary-token" || c.LLM.APIKey != "temporary-key" || c.LLM.BaseURL != "https://example.invalid/v1" || c.Export.OutputDir == "" {
		t.Fatal("first load skipped environment or normalization")
	}
	if e = c.Save(); e != nil {
		t.Fatal(e)
	}
	p, _ := Path()
	b, _ := os.ReadFile(p)
	if bytes.Contains(b, []byte("temporary-key")) || bytes.Contains(b, []byte("temporary-token")) {
		t.Fatal("environment credential persisted")
	}
	if e = os.WriteFile(p, []byte(`{"rules":{"user_global":"legacy supplement"}}`), 0600); e != nil {
		t.Fatal(e)
	}
	c, e = Load()
	if e != nil || !strings.Contains(c.Rules.ActiveText(), "legacy supplement") {
		t.Fatal("missing preset field lost old supplement", e)
	}
}

func TestDamagedConfigurationRecovery(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	_, e := Update(func(c *Config) error { c.LLM.Model = "last-valid"; return nil })
	if e != nil {
		t.Fatal(e)
	}
	_, e = Update(func(c *Config) error { c.Export.TOC = true; return nil })
	if e != nil {
		t.Fatal(e)
	}
	p, _ := Path()
	for _, bad := range []string{"broken JSON", "", `null`} {
		if e = os.WriteFile(p, []byte(bad), 0600); e != nil {
			t.Fatal(e)
		}
		if LoadingState().Ready {
			t.Fatal("damaged config reported ready")
		}
		if _, e = Update(func(c *Config) error { c.LLM.Model = "overwrite"; return nil }); e == nil {
			t.Fatal("damaged config overwritten")
		}
		if b, _ := os.ReadFile(p); string(b) != bad {
			t.Fatal("original changed")
		}
		if _, e = Recover("reload"); e == nil {
			t.Fatal("reload accepted damage")
		}
		if _, e = Recover("restore_backup"); e != nil {
			t.Fatal(e)
		}
		c, e := Load()
		if e != nil || c.LLM.Model != "last-valid" {
			t.Fatal("backup not restored", e)
		}
	}
	files, _ := filepath.Glob(p + ".damaged-*")
	if len(files) != 3 {
		t.Fatal("damaged originals not preserved", files)
	}
	if e = os.WriteFile(p, []byte("broken"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = Recover("reset"); e != nil {
		t.Fatal(e)
	}
	if !LoadingState().Ready {
		t.Fatal("reset failed")
	}
	// A read failure is also a recovery condition, not an absent configuration.
	if e = os.Remove(p); e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(p, 0700); e != nil {
		t.Fatal(e)
	}
	if _, e = Load(); e == nil {
		t.Fatal("directory accepted as config")
	}
	if _, e = Recover("reset"); e == nil {
		t.Fatal("unreadable original overwritten")
	}
}

func TestOverlongAppliedRulePreserved(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	p, _ := Path()
	os.MkdirAll(filepath.Dir(p), 0700)
	long := strings.Repeat("中", 4100)
	c := Default()
	c.Rules.Presets[0].Text = long
	// Simulate an existing file rather than applying a new invalid rule.
	b := []byte(`{"rules":{"active":"old","presets":[{"name":"old","text":"` + long + `"}]}}`)
	if e := os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
	n, e := Update(func(c *Config) error { c.Rules.Presets[0].Name = "renamed"; c.Rules.Active = "renamed"; return nil })
	if e != nil || n.Rules.ActiveText() != long {
		t.Fatal("management truncated legacy rule", e)
	}
	if _, e = Update(func(c *Config) error { c.Rules.Presets[0].Text += "x"; return nil }); e == nil {
		t.Fatal("new overlong rule accepted")
	}
	if _, e = Update(func(c *Config) error {
		duplicate := c.Rules.Presets[0]
		duplicate.Name = "duplicate"
		c.Rules.Presets = append(c.Rules.Presets, duplicate)
		return nil
	}); e == nil {
		t.Fatal("duplicating legacy overlong rule bypassed application validation")
	}
}

func TestStoreProcessWriters(t *testing.T) {
	if field := os.Getenv("LITERATURE_STORE_TEST_CHILD"); field != "" {
		_, e := Update(func(c *Config) error {
			if field == "model" {
				c.LLM.Model = "child-model"
			} else {
				c.Export.TOC = true
			}
			return nil
		})
		if e != nil {
			t.Fatal(e)
		}
		return
	}
	t.Setenv("APPDATA", t.TempDir())
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	a := exec.Command(exe, "-test.run=^TestStoreProcessWriters$")
	b := exec.Command(exe, "-test.run=^TestStoreProcessWriters$")
	a.Env = append(os.Environ(), "LITERATURE_STORE_TEST_CHILD=model")
	b.Env = append(os.Environ(), "LITERATURE_STORE_TEST_CHILD=toc")
	var outA, outB bytes.Buffer
	a.Stdout = &outA
	a.Stderr = &outA
	b.Stdout = &outB
	b.Stderr = &outB
	if e = a.Start(); e != nil {
		t.Fatal(e)
	}
	if e = b.Start(); e != nil {
		t.Fatal(e)
	}
	if e = a.Wait(); e != nil {
		t.Fatal(e, outA.String())
	}
	if e = b.Wait(); e != nil {
		t.Fatal(e, outB.String())
	}
	c, e := Load()
	if e != nil || c.LLM.Model != "child-model" || !c.Export.TOC {
		t.Fatal("cross-process field lost", e)
	}
}
