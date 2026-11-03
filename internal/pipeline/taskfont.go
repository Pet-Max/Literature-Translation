package pipeline

import (
	"fmt"
	"literature-translation/internal/config"
	"literature-translation/internal/fonts"
	"os"
	"path/filepath"
)

// PrepareTask snapshots config and the selected font while writers are locked.
// CLI flags are retained when refresh is false. Close releases only this task's
// private temporary directory, never the shared font library.
func (p *Pipeline) PrepareTask(refresh bool) error {
	return config.WithSnapshot(func(latest *config.Config) error {
		if refresh {
			p.Cfg = latest.Clone()
		} else {
			p.Cfg = p.Cfg.Clone()
		}
		if p.Cfg.Export.UserFont == "" {
			return nil
		}
		uf := p.Cfg.FindUserFont(p.Cfg.Export.UserFont)
		if uf == nil || uf.FileName != filepath.Base(uf.FileName) || uf.FileName == "" {
			return fmt.Errorf("所选字体未注册或文件名无效")
		}
		dir, err := fonts.Dir()
		if err != nil {
			return err
		}
		tmp, err := os.MkdirTemp("", "literature-task-font-*")
		if err != nil {
			return err
		}
		if err = copyFile(filepath.Join(dir, uf.FileName), filepath.Join(tmp, uf.FileName)); err != nil {
			os.RemoveAll(tmp)
			return fmt.Errorf("快照字体失败: %w", err)
		}
		p.fontsDir, p.ownsFonts = tmp, true
		return nil
	})
}

func (p *Pipeline) Close() {
	if p.ownsFonts {
		_ = os.RemoveAll(p.fontsDir)
		p.fontsDir = ""
		p.ownsFonts = false
	}
}
