package fonts

import (
	"fmt"
	"literature-translation/internal/config"
	"os"
)

func Add(path string) (uf config.UserFont, err error) {
	_, err = config.UpdateWithRollback(func(c *config.Config) error {
		names := []string{}
		for _, f := range c.UserFonts {
			names = append(names, f.InternalName)
		}
		name := InternalName(NextNumber(names))
		info, err := Import(path, name)
		if err != nil {
			return err
		}
		uf = config.UserFont{InternalName: name, DisplayName: info.DisplayName, FileName: info.FileName}
		c.UserFonts = append(c.UserFonts, uf)
		return nil
	}, func() {
		if uf.FileName != "" {
			_ = Remove(uf.FileName)
		}
	})
	return
}

func Select(name string) error {
	_, err := config.Update(func(c *config.Config) error {
		if name != "" && c.FindUserFont(name) == nil {
			return fmt.Errorf("未找到字体 %s", name)
		}
		c.Export.UserFont = name
		if name == "" {
			c.Export.FontPreset = "lxgw"
			c.Export.Font = config.Default().Export.Font
		} else {
			c.Export.FontPreset = "custom"
		}
		return nil
	})
	return err
}

func Unregister(name string) error {
	var file string
	_, err := config.Update(func(c *config.Config) error {
		f := c.FindUserFont(name)
		if f == nil {
			return fmt.Errorf("未找到字体 %s", name)
		}
		file = f.FileName
		kept := c.UserFonts[:0]
		for _, f := range c.UserFonts {
			if f.InternalName != name {
				kept = append(kept, f)
			}
		}
		c.UserFonts = kept
		if c.Export.UserFont == name {
			c.Export.UserFont = ""
			c.Export.FontPreset = "lxgw"
			c.Export.Font = config.Default().Export.Font
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Files use unique names; a later import cannot be deleted by this cleanup.
	if err = Remove(file); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("已解绑字体，但清理文件失败: %w", err)
	}
	return nil
}
