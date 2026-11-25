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

// SettingsPatch 仅包含本次编辑显式改动的叶子字段；nil 表示保留原值。
type SettingsPatch struct {
	MinerU *struct {
		Token        *string `json:"token"`
		ModelVersion *string `json:"model_version"`
		Language     *string `json:"language"`
	} `json:"mineru,omitempty"`
	LLM *struct {
		BaseURL     *string `json:"base_url"`
		APIKey      *string `json:"api_key"`
		Model       *string `json:"model"`
		Concurrency *int    `json:"concurrency"`
	} `json:"llm,omitempty"`
	Export *struct {
		Bilingual *bool   `json:"bilingual"`
		EnBg      *string `json:"enbg"`
		ZhBg      *string `json:"zhbg"`
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

// updateConfig 为所有 GUI 设置写入串行执行读取、修改、落盘与快照发布。
func (a *App) updateConfig(edit func(*config.Config) error) (*config.Config, error) {
	return a.updateConfigWithRollback(edit, nil)
}

// latestConfig 刷新预检配置，同时避免把运行中任务的旧快照写回全局状态。
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

// DefaultGlossary 返回出厂示例术语，供「恢复默认」按钮的置灰判断。
func (a *App) DefaultGlossary() []config.Term { return config.Default().Glossary }

// ResetGlossaryToDefault 将术语表整体还原为出厂示例，复用术语行保存通道。
func (a *App) ResetGlossaryToDefault() (*config.Config, error) {
	rows := config.Default().Glossary
	return a.UpdateSettings(SettingsPatch{GlossaryRows: &rows})
}

func (a *App) UpdateSettings(patch SettingsPatch) (*config.Config, error) {
	return a.updateConfig(func(c *config.Config) error {
		// 先按强类型补丁序列化，确保 false 与空串得以保留而未改动指针字段被忽略。
		b, err := json.Marshal(patch)
		if err != nil {
			return err
		}
		var fields map[string]json.RawMessage
		if err = json.Unmarshal(b, &fields); err != nil {
			return err
		}
		// 嵌套指针字段缺省时序列化为 null，合并前予以剔除，防止覆盖既有配置。
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
			// OPEN_EXISTING 在不创建探测文件或目录的前提下校验目录写入权限；
			// 对目录句柄而言，这两项权限对应创建文件（FILE_ADD_FILE）与建子目录（FILE_ADD_SUBDIRECTORY）。
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

// FrontendReady 在前端完成事件监听注册后才允许执行退出保存握手。
func (a *App) FrontendReady() { a.frontendReady.Store(true) }
func (a *App) FinishSettingsClose() {
	a.stopQueue()
	a.closeAllowed.Store(true)
	runtime.Quit(a.ctx)
}
