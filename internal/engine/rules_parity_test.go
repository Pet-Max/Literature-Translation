// rules_parity_test.go — 默认规则一致性回归测试：锁定配置默认规则
// （config.DefaultRules）与引擎侧兜底文案（IronRules）逐字节一致，
// 并与 prompt_golden.json 协同确保任一侧修改时同步更新另一侧与提示词基线。
// 放在 engine 包：config 是基础配置包，其测试不反向依赖本包。
package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"literature-translation/internal/config"
)

func TestDefaultRulesMatchesEngine(t *testing.T) {
	rules := strings.ReplaceAll(config.DefaultRules, "\r\n", "\n")
	if strings.TrimSpace(rules) != strings.TrimSpace(IronRules) {
		t.Errorf("config.DefaultRules 与 engine.IronRules 不一致（改一侧须同步另一侧）")
	}
}

func TestMigratedRulesRealFingerprint(t *testing.T) {
	c := config.Default()
	b, _ := json.Marshal(config.Rules{Active: "默认", Presets: []config.RulePreset{{Name: "默认", Text: config.DefaultRules}}})
	if err := json.Unmarshal(b, &c.Rules); err != nil {
		t.Fatal(err)
	}
	key := func() string { return BuildCacheKey(BuildSystem(nil, c.Rules.ActiveText(), ""), false, true, "model") }
	before := key()
	c.Normalize()
	if key() != before {
		t.Fatal("migration changed actual fingerprint")
	}
	c.Rules.Presets[0].Text += "x"
	if key() == before {
		t.Fatal("changed rule reused fingerprint")
	}
}
