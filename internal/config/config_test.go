package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDefaultRulesMatchesPython 默认规则在 Go（DefaultRules，GUI 预设/
// 恢复默认）与 Python（prompt.py IRON_RULES，引擎兜底）各存一份，
// 曾出现措辞漂移。逐字比对锁死，改任一侧必须同步另一侧。
func TestDefaultRulesMatchesPython(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "engine", "prompt.py"))
	if err != nil {
		t.Fatal(err)
	}
	const marker = `IRON_RULES = """`
	i := strings.Index(string(data), marker)
	if i < 0 {
		t.Fatal("prompt.py 中未找到 IRON_RULES")
	}
	rest := string(data)[i+len(marker):]
	j := strings.Index(rest, `"""`)
	if j < 0 {
		t.Fatal("IRON_RULES 未闭合")
	}
	pyRules := rest[:j]
	// 检出换行随平台（.gitattributes text=auto，Windows CI 为 CRLF）：
	// 比对的是内容而非字节，先归一化行尾再比，否则 Go 常量的 \n 对不上
	pyRules = strings.ReplaceAll(pyRules, "\r\n", "\n")
	if strings.TrimSpace(pyRules) != strings.TrimSpace(DefaultRules) {
		t.Errorf("Go DefaultRules 与 Python IRON_RULES 不一致（改一侧须同步另一侧）")
	}
}

// TestRepairSymbolsLegacyConfig 旧版 config.json 没有 symbols 键：
// Load 的口径是先填 Default 再 Unmarshal，缺省键必须保持默认 true，
// 显式 false 才生效——否则存量用户升级后符号修复会被静默关闭。
func TestRepairSymbolsLegacyConfig(t *testing.T) {
	legacy := []byte(`{"repair": {"spelling": false}}`)
	cfg := Default()
	if err := json.Unmarshal(legacy, cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Repair.Symbols {
		t.Error("旧配置缺 symbols 键应保持默认 true")
	}
	if cfg.Repair.Spelling {
		t.Error("显式 spelling=false 应生效")
	}

	// 新版键显式写 false 同样生效
	explicit := []byte(`{"repair": {"symbols": false}}`)
	cfg = Default()
	if err := json.Unmarshal(explicit, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Repair.Symbols {
		t.Error("显式 symbols=false 应生效")
	}
}
