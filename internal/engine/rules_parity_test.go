// rules_parity_test.go — 规则一致性锚点：GUI 侧默认规则全文
// （config.DefaultRules）与引擎侧兜底文案（IronRules）逐字一致，
// 曾出现措辞漂移，改任一侧必须同步另一侧。
// 放在 engine 包：config 是低层包，其测试不应反向依赖本包。
// 历史规则正文已由 migration_test 的黄金对拍
// 逐字节锁死，此处传递锚定。
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
