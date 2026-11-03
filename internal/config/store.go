package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/windows"
)

var storeMu sync.Mutex

// WithSnapshot holds the same process and OS lock as writers. Resource snapshots
// can therefore copy a selected font before another process unregisters it.
func WithSnapshot(use func(*Config) error) error {
	return locked(func(_ string) error {
		c, err := Load()
		if err != nil {
			return err
		}
		return use(c.Clone())
	})
}

func locked(fn func(string) error) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	p, err := Path()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(p+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	var ov windows.Overlapped
	if err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &ov); err != nil {
		return err
	}
	defer windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ov)
	return fn(p)
}

func Update(edit func(*Config) error) (*Config, error) {
	return UpdateWithRollback(edit, nil)
}

// UpdateWithRollback re-reads disk under the cross-process lock; rollback runs
// before releasing that lock so another import cannot reuse its file name.
func UpdateWithRollback(edit func(*Config) error, rollback func()) (result *Config, err error) {
	err = locked(func(p string) (err error) {
		defer func() {
			if err != nil && rollback != nil {
				rollback()
			}
		}()
		old, err := Load()
		if err != nil {
			return err
		}
		next := old.Clone()
		if err = edit(next); err != nil {
			return err
		}
		if err = validateAppliedRules(old, next); err != nil {
			return err
		}
		next = next.Clone()
		next.Normalize()
		disk := next.Clone()
		previous, readErr := os.ReadFile(p)
		if readErr != nil && !os.IsNotExist(readErr) {
			return readErr
		}
		var saved Config
		if len(previous) != 0 {
			if err = json.Unmarshal(previous, &saved); err != nil {
				return err
			}
		}
		if disk.envToken != "" && disk.MinerU.Token == cleanCredential(disk.envToken) {
			disk.MinerU.Token = saved.MinerU.Token
		}
		if disk.envKey != "" && disk.LLM.APIKey == cleanCredential(disk.envKey) {
			disk.LLM.APIKey = saved.LLM.APIKey
		}
		data, err := json.MarshalIndent(disk, "", "  ")
		if err != nil {
			return err
		}
		if len(previous) != 0 {
			if err = atomicWrite(p+".bak", previous); err != nil {
				return fmt.Errorf("备份有效配置失败，未保存: %w", err)
			}
		}
		if err = atomicWrite(p, data); err != nil {
			return err
		}
		result = next.Clone()
		return nil
	})
	return
}

func validateAppliedRules(old, next *Config) error {
	remaining := map[string]int{}
	for _, p := range old.Rules.Presets {
		remaining[p.Text]++
	}
	for _, p := range next.Rules.Presets {
		if strings.TrimSpace(p.Text) == "" || utf8.RuneCountInString(p.Text) > 4000 {
			if remaining[p.Text] == 0 {
				return fmt.Errorf("新应用的规则应为 1–4000 个字符；原有超长正文可以保留")
			}
		}
		if remaining[p.Text] > 0 {
			remaining[p.Text]--
		}
	}
	return nil
}

func atomicWrite(p string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(p), ".settings-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	from, err := windows.UTF16PtrFromString(tmp)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

type LoadState struct {
	Ready     bool   `json:"ready"`
	Message   string `json:"message"`
	HasBackup bool   `json:"has_backup"`
}

func LoadingState() LoadState {
	_, err := Load()
	s := LoadState{Ready: err == nil}
	if err != nil {
		s.Message = "配置读取失败，自动保存和任务启动已暂停。请重新读取或选择恢复方式。"
	}
	p, e := Path()
	if e == nil {
		b, e := os.ReadFile(p + ".bak")
		if e == nil {
			_, e = decodeConfig(b)
			s.HasBackup = e == nil && len(b) > 0
		}
	}
	return s
}

// Recover never discards the damaged original. Destructive rebuilding is an
// explicit UI choice, and the exclusive backup must succeed before replacement.
func Recover(action string) (*Config, error) {
	err := locked(func(p string) error {
		if action == "reload" {
			_, err := Load()
			return err
		}
		var replacement []byte
		switch action {
		case "restore_backup":
			b, err := os.ReadFile(p + ".bak")
			if err != nil {
				return err
			}
			if len(b) == 0 {
				return fmt.Errorf("有效备份为空")
			}
			if _, err = decodeConfig(b); err != nil {
				return fmt.Errorf("备份无效: %w", err)
			}
			replacement = b
		case "reset":
			replacement, _ = json.MarshalIndent(Default(), "", "  ")
		default:
			return fmt.Errorf("未知恢复操作")
		}
		b, err := os.ReadFile(p)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil {
			backup, err := os.OpenFile(p+".damaged-"+time.Now().Format("20060102-150405.000000000"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			_, err = backup.Write(b)
			if err == nil {
				err = backup.Sync()
			}
			closeErr := backup.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
		}
		return atomicWrite(p, replacement)
	})
	if err != nil {
		return nil, err
	}
	return Load()
}
