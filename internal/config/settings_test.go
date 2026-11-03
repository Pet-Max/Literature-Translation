package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDraftMigrationAndCloneIsolation(t *testing.T) {
	c := Default()
	if err := json.Unmarshal([]byte(`{"rules":{"active":"old","presets":[{"name":"old","text":"old rule"}]},"glossary":[{"en":"a","zh":"甲"}]}`), c); err != nil {
		t.Fatal(err)
	}
	c.Normalize()
	if c.Rules.ActiveText() != "old rule" || c.Rules.Presets[0].DraftText != nil || c.Glossary[0].Zh != "甲" {
		t.Fatal("legacy migration changed content")
	}
	draft := ""
	c.Rules.Presets[0].DraftText = &draft
	rows := []Term{{En: "incomplete"}}
	c.GlossaryRows = &rows
	n := c.Clone()
	*n.Rules.Presets[0].DraftText = "new draft"
	(*n.GlossaryRows)[0].En = "changed"
	n.Normalize()
	if *c.Rules.Presets[0].DraftText != "" || (*c.GlossaryRows)[0].En != "incomplete" {
		t.Fatal("clone aliases drafts")
	}
}

func TestEnvironmentCredentialSaveUsesNormalizedSnapshot(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	c := Default()
	c.envToken, c.envKey = "Bearer env-token", "bearer env-key"
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	c.MinerU.Token = c.envToken
	c.LLM.APIKey = c.envKey
	c.Normalize()
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	read := func() *Config {
		t.Helper()
		b, e := os.ReadFile(filepath.Join(os.Getenv("APPDATA"), DataDirName, "config.json"))
		if e != nil {
			t.Fatal(e)
		}
		var d Config
		if e = json.Unmarshal(b, &d); e != nil {
			t.Fatal(e)
		}
		return &d
	}
	d := read()
	if d.MinerU.Token != "" || d.LLM.APIKey != "" {
		t.Fatal("unchanged environment credential persisted")
	}
	if c.MinerU.Token != "env-token" || c.LLM.APIKey != "env-key" {
		t.Fatal("save changed effective config")
	}
	c.MinerU.Token = "explicit-new-token"
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if read().MinerU.Token != "explicit-new-token" {
		t.Fatal("explicit credential edit lost")
	}
}

func TestEnvironmentCredentialsWithMissingOrInvalidDisk(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	c := Default()
	c.envToken, c.envKey = "Bearer env-token", "env-key"
	c.MinerU.Token, c.LLM.APIKey = c.envToken, c.envKey
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	p, _ := Path()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var disk Config
	if err := json.Unmarshal(b, &disk); err != nil {
		t.Fatal(err)
	}
	if disk.MinerU.Token != "" || disk.LLM.APIKey != "" {
		t.Fatal("first save wrote environment credentials")
	}
	if err := os.WriteFile(p, []byte("invalid json"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.Save(); err == nil {
		t.Fatal("cannot preserve disk credentials from invalid JSON")
	}
	b, _ = os.ReadFile(p)
	if string(b) != "invalid json" {
		t.Fatal("failed save replaced original configuration")
	}
}
