// rules_migration_test.go — 双预设升级（物理/通用学术）的种子与存量迁移：
// 物理正文逐字节不变（任务指纹不受影响）、老自定义预设不被误标、
// 迁移幂等、已删除的出厂预设不复活。
package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDefaultSeedTwoPresets 出厂种子：双预设齐备、物理第一且默认生效。
func TestDefaultSeedTwoPresets(t *testing.T) {
	c := Default()
	if len(c.Rules.Presets) != 2 {
		t.Fatalf("出厂预设数 = %d, 期望 2", len(c.Rules.Presets))
	}
	if c.Rules.Presets[0].Name != "物理" || c.Rules.Presets[0].Text != DefaultRules {
		t.Fatalf("第一套预设应为物理/内置原文: %q", c.Rules.Presets[0].Name)
	}
	if c.Rules.Presets[1].Name != "通用学术" || c.Rules.Presets[1].Text != GeneralRules {
		t.Fatalf("第二套预设应为通用学术/GeneralRules: %q", c.Rules.Presets[1].Name)
	}
	if c.Rules.Active != "物理" {
		t.Fatalf("出厂 Active = %q, 期望 物理", c.Rules.Active)
	}
	if got := c.Rules.ActiveText(); got != DefaultRules {
		t.Fatalf("出厂生效正文不是内置物理原文")
	}
	if GeneralRules == DefaultRules || strings.TrimSpace(GeneralRules) == "" {
		t.Fatal("GeneralRules 应为非空的差异化文本")
	}
}

// TestMigrateLegacyStockPreset 老原版用户：单「默认」(内置原文) → 改名
// 「物理」且正文逐字节不变（指纹零影响），追加「通用学术」，Active 随迁。
func TestMigrateLegacyStockPreset(t *testing.T) {
	c := Default()
	if err := json.Unmarshal([]byte(`{"rules":{"active":"默认","presets":[{"name":"默认","text":`+quote(DefaultRules)+`}]}}`), c); err != nil {
		t.Fatal(err)
	}
	c.Normalize()
	if len(c.Rules.Presets) != 2 {
		t.Fatalf("迁移后预设数 = %d, 期望 2", len(c.Rules.Presets))
	}
	if c.Rules.Presets[0].Name != "物理" || c.Rules.Presets[0].Text != DefaultRules {
		t.Fatal("老原版预设应改名物理、正文逐字节不变")
	}
	if c.Rules.Presets[1].Name != "通用学术" || c.Rules.Presets[1].Text != GeneralRules {
		t.Fatal("应补挂通用学术")
	}
	if c.Rules.Active != "物理" {
		t.Fatalf("Active = %q, 期望随迁为 物理", c.Rules.Active)
	}
	// 幂等：再次 Normalize 不得产生重复预设或改动
	c.Normalize()
	if len(c.Rules.Presets) != 2 || c.Rules.Presets[0].Text != DefaultRules {
		t.Fatal("二次 Normalize 破坏了迁移结果")
	}
}

// TestMigrateLegacyEditedPreset 老自定义用户：「默认」正文被编辑过 →
// 原名原样保留、Active 不变，仅补挂「通用学术」。
func TestMigrateLegacyEditedPreset(t *testing.T) {
	c := Default()
	if err := json.Unmarshal([]byte(`{"rules":{"active":"默认","presets":[{"name":"默认","text":"我的自定义规则"}]}}`), c); err != nil {
		t.Fatal(err)
	}
	c.Normalize()
	if len(c.Rules.Presets) != 2 {
		t.Fatalf("迁移后预设数 = %d, 期望 2", len(c.Rules.Presets))
	}
	if c.Rules.Presets[0].Name != "默认" || c.Rules.Presets[0].Text != "我的自定义规则" {
		t.Fatal("自定义预设被误动")
	}
	if c.Rules.Presets[1].Name != "通用学术" {
		t.Fatal("应补挂通用学术")
	}
	if c.Rules.Active != "默认" || c.Rules.ActiveText() != "我的自定义规则" {
		t.Fatal("Active 应保持不动")
	}
}

// TestMigrateNoResurrectDeletedPreset 用户已删除「通用学术」的形态（升级
// 后只剩物理、无「默认」签名）：迁移不得复活它——按触发签名而非
// 「确保存在」设计。若未来误改为「确保两套存在」逻辑，此用例会失败。
func TestMigrateNoResurrectDeletedPreset(t *testing.T) {
	c := Default()
	if err := json.Unmarshal([]byte(`{"rules":{"active":"物理","presets":[{"name":"物理","text":`+quote(DefaultRules)+`}]}}`), c); err != nil {
		t.Fatal(err)
	}
	c.Normalize()
	if len(c.Rules.Presets) != 1 {
		t.Fatalf("已删除的出厂预设被复活：预设数 = %d, 期望 1", len(c.Rules.Presets))
	}
	if c.Rules.Active != "物理" || c.Rules.ActiveText() != DefaultRules {
		t.Fatal("物理预设应保持生效")
	}
}

// TestMigrateUserGlobalFold 用户补充规则折叠落入「物理」预设（沿用历史
// 行为），通用学术随后。
func TestMigrateUserGlobalFold(t *testing.T) {
	c := Default()
	if err := json.Unmarshal([]byte(`{"rules":{"user_global":"补充一条","active":"","presets":[]}}`), c); err != nil {
		t.Fatal(err)
	}
	c.Normalize()
	if len(c.Rules.Presets) != 2 {
		t.Fatalf("空 presets 种子数 = %d, 期望 2", len(c.Rules.Presets))
	}
	if c.Rules.Presets[0].Name != "物理" ||
		!strings.Contains(c.Rules.Presets[0].Text, "补充一条") ||
		!strings.HasPrefix(c.Rules.Presets[0].Text, "你是一名物理学") {
		t.Fatal("user_global 应折叠进物理预设")
	}
	if c.Rules.Presets[1].Name != "通用学术" {
		t.Fatal("第二套应为通用学术")
	}
	if c.Rules.Active != "物理" {
		t.Fatalf("Active = %q, 期望 物理", c.Rules.Active)
	}
}

// TestPhysicsRulesFingerprintStable 迁移前后物理正文的任务指纹一致性：
// 规则正文经 ActiveText 进入 BuildCacheKey，字面不变即断点有效。
// 比较完整正文；真实任务指纹另由 engine 包回归测试覆盖。
func TestPhysicsRulesFingerprintStable(t *testing.T) {
	legacy := Default()
	if err := json.Unmarshal([]byte(`{"rules":{"active":"默认","presets":[{"name":"默认","text":`+quote(DefaultRules)+`}]}}`), legacy); err != nil {
		t.Fatal(err)
	}
	before := legacy.Rules.ActiveText()
	legacy.Normalize()
	after := legacy.Rules.ActiveText()
	if before != after {
		t.Fatalf("迁移改变了生效正文（指纹将作废）: %x → %x", before, after)
	}
}

// quote JSON 字符串安全编码（内置原文含引号/换行，直接拼 JSON 不可靠）。
func quote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}
