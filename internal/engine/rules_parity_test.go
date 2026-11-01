// rules_parity_test.go — 规则一致性锚点：GUI 侧默认规则全文
// （config.DefaultRules）与引擎侧兜底文案（IronRules）逐字一致，
// 曾出现措辞漂移，改任一侧必须同步另一侧。
// 放在 engine 包：config 是低层包，其测试不应反向依赖本包。
// 历史母本（v1.1.1 引擎 IRON_RULES）已由 migration_test 的黄金对拍
// 逐字节锁死，此处传递锚定。
package engine

import (
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
