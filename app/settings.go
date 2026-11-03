package app

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sys/windows"
	"literature-translation/internal/config"
)

// SettingsPatch contains only explicitly edited leaves; nil preserves the old value.
type SettingsPatch struct {
	MinerU *struct {
		Token        *string `json:"token"`
		ModelVersion *string `json:"model_version"`
	} `json:"mineru,omitempty"`
	LLM *struct {
		BaseURL     *string `json:"base_url"`
		APIKey      *string `json:"api_key"`
		Model       *string `json:"model"`
		Concurrency *int    `json:"concurrency"`
	} `json:"llm,omitempty"`
	Export *struct {
		Bilingual *bool   `json:"bilingual"`
		TOC       *bool   `json:"toc"`
		TwoColumn *bool   `json:"two_column"`
		OutputDir *string `json:"output_dir"`
	} `json:"export,omitempty"`
	Translate *struct {
		SkipReferences *bool `json:"skip_references"`
	} `json:"translate,omitempty"`
	Repair *struct {
		Spelling *bool `json:"spelling"`
		Symbols  *bool `json:"symbols"`
	} `json:"repair,omitempty"`
	Rules        *RulesPatch    `json:"rules,omitempty"`
	GlossaryRows *[]config.Term `json:"glossary_rows,omitempty"`
}

type RulesPatch struct {
	Active  *string              `json:"active,omitempty"`
	Presets *[]config.RulePreset `json:"presets,omitempty"`
}

// updateConfig serializes read/modify/persist/publish for every GUI writer.
func (a *App) updateConfig(edit func(*config.Config) error) (*config.Config, error) {
	return a.updateConfigWithRollback(edit, nil)
}

// latestConfig refreshes preflight state without publishing a task's old snapshot.
func (a *App) latestConfig() (*config.Config, error) {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	if a.configBlocked.Load() {
		return nil, fmt.Errorf("请先恢复配置")
	}
	var latest *config.Config
	err := config.WithSnapshot(func(c *config.Config) error {
		latest = c.Clone()
		a.cfg.Store(latest)
		return nil
	})
	if err != nil {
		a.configBlocked.Store(true)
		return nil, err
	}
	return latest.Clone(), nil
}

func (a *App) updateConfigWithRollback(edit func(*config.Config) error, rollback func()) (*config.Config, error) {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	if a.configBlocked.Load() {
		return nil, fmt.Errorf("请先恢复配置，再保存设置")
	}
	next, err := config.UpdateWithRollback(edit, rollback)
	if err != nil {
		return nil, err
	}
	a.cfg.Store(next)
	return next.Clone(), nil
}

func (a *App) UpdateSettings(patch SettingsPatch) (*config.Config, error) {
	return a.updateConfig(func(c *config.Config) error {
		// Marshal the typed patch so false and empty strings survive, while absent leaves do not.
		b, err := json.Marshal(patch)
		if err != nil {
			return err
		}
		var fields map[string]json.RawMessage
		if err = json.Unmarshal(b, &fields); err != nil {
			return err
		}
		// Nested pointer fields marshal as null when absent. Remove them before merging.
		for key, raw := range fields {
			if key == "rules" || key == "glossary_rows" {
				continue
			}
			var leaves map[string]json.RawMessage
			if json.Unmarshal(raw, &leaves) != nil {
				continue
			}
			for k, value := range leaves {
				if string(value) == "null" {
					delete(leaves, k)
				}
			}
			fields[key], _ = json.Marshal(leaves)
		}
		b, _ = json.Marshal(fields)
		if err = json.Unmarshal(b, c); err != nil {
			return err
		}
		if patch.Rules != nil {
			if len(c.Rules.Presets) == 0 {
				return fmt.Errorf("至少保留一套规则预设")
			}
			seen := map[string]bool{}
			for _, p := range c.Rules.Presets {
				name := strings.TrimSpace(strings.ReplaceAll(p.Name, "\n", " "))
				if name == "" || utf8.RuneCountInString(name) > 20 || seen[name] {
					return fmt.Errorf("预设名称应唯一，且为 1–20 个字符")
				}
				seen[name] = true
			}
		}
		if patch.Export != nil && patch.Export.OutputDir != nil {
			return validateOutputDir(*patch.Export.OutputDir)
		}
		return nil
	})
}

func validateOutputDir(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if strings.ContainsAny(value, "\x00\r\n<>\"|?*") {
		return fmt.Errorf("归档目录含有不允许的字符")
	}
	if !filepath.IsAbs(value) {
		return fmt.Errorf("归档目录请填写绝对路径，或留空使用默认目录")
	}
	if strings.Contains(strings.TrimPrefix(value, filepath.VolumeName(value)), ":") {
		return fmt.Errorf("归档目录含有无效的冒号")
	}
	for p := filepath.Clean(value); ; p = filepath.Dir(p) {
		info, err := os.Stat(p)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("归档路径或其父级是文件")
			}
			name, err := windows.UTF16PtrFromString(p)
			if err != nil {
				return fmt.Errorf("归档目录格式无效")
			}
			// OPEN_EXISTING checks directory write rights without creating a probe file or directory.
			// For directory handles these bits mean FILE_ADD_FILE and FILE_ADD_SUBDIRECTORY.
			h, err := windows.CreateFile(name, windows.FILE_WRITE_DATA|windows.FILE_APPEND_DATA,
				windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
				nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
			if err != nil {
				return fmt.Errorf("归档目录或其父级不可写，请检查路径和权限")
			}
			windows.CloseHandle(h)
			return nil
		}
		if !os.IsNotExist(err) {
			return fmt.Errorf("无法访问归档目录，请检查路径和权限")
		}
		if filepath.Dir(p) == p {
			return fmt.Errorf("归档目录的磁盘不可访问")
		}
	}
}

func validateTaskSettings(kind string, c *config.Config) error {
	if kind == "export" {
		return nil
	}
	if kind == "pdf" && c.MinerU.Token == "" {
		return fmt.Errorf("请先在设置中填写 MinerU Token")
	}
	if c.LLM.APIKey == "" || strings.TrimSpace(c.LLM.Model) == "" {
		return fmt.Errorf("请先在设置中填写模型 API Key 和模型名")
	}
	u, err := url.Parse(c.LLM.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("请在设置中填写完整的 HTTP 或 HTTPS 模型 API 地址")
	}
	return nil
}

// FrontendReady enables the close handshake only once the frontend can receive events.
func (a *App) FrontendReady() { a.frontendReady.Store(true) }
func (a *App) FinishSettingsClose() {
	a.stopQueue()
	a.closeAllowed.Store(true)
	runtime.Quit(a.ctx)
}
