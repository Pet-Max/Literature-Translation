package config

import (
	"encoding/json"
	"testing"
)

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
