// rules_migration_test.go — 双预设升级（物理/通用学术）的种子与存量迁移：
// 现行原稿改名迁移不改正文（指纹零影响）、历史未编辑原稿一次性自动升版、
// 老自定义预设或自命名副本不被误覆盖、迁移幂等、已删除的出厂预设不复活。
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

// TestMigrateLegacyStockPreset 老原版用户：单「默认」(现行内置原文) → 改名
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

// TestPhysicsRulesFingerprintStable 现行原稿改名迁移前后生效正文与任务指纹一致性：
// 规则正文经 ActiveText 进入 BuildCacheKey，字面不变即断点有效；
// 历史旧发行版原稿的一次性自动升版由 TestMigrateStockPresetsAutoUpgrade 覆盖。
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

// 以下为 v1.4.1 内置原稿快照夹具，供存量一次性升版与自定义保留测试复用。
const (
	v141PhysicsRules = "你是一名物理学/学术文献翻译引擎。把用户消息中的 Markdown 学术文本翻译成简体中文，严格遵守：\n\n" +
		"1. 只翻译自然语言文字；⟦F1⟧、⟦F2⟧ 这类 ⟦F数字⟧ 占位符（代表公式、代码、图片）必须原样保留：一个不能少、一个不能改、位置不能移动，也不要试图翻译或解读占位符内容。\n" +
		"2. Eq. (5)、Fig. 3、Table 1、Ref. [12]、arXiv:2306.00417、附录编号 S IV 等引用编号保留原样，不要翻译 Eq./Fig. 等标记。\n" +
		"3. Markdown 结构保持不变：标题层级（# 数量）、列表、引用块、加粗斜体、链接、表格与原文一一对应；标题译文务必简短。表格逐行保持竖线 | 与列数不变，只翻译单元格内的文字，单元格内的占位符原样保留。\n" +
		"4. 物理记号原样保留：狄拉克记号（|00⟩、⟨01|）、希腊字母（Λ、Ω、ψ）、上下标变量，以及无法识别的乱码字符——一律不翻译、不删除、不猜测含义、不改写。\n" +
		"5. 专有名词、人名按学界惯例处理：有通用中译的用中译（如 Schrödinger→薛定谔），没有的保留原文。\n" +
		"6. 术语严格按下方术语表统一译名。\n" +
		"7. 只输出译文本身。不要任何解释、前言、结语、代码围栏包裹。"
	v141GeneralRules = "你是一名学术文献翻译引擎。把用户消息中的 Markdown 学术文本翻译成简体中文，严格遵守：\n\n" +
		"1. 只翻译自然语言文字；⟦F1⟧、⟦F2⟧ 这类 ⟦F数字⟧ 占位符（代表公式、代码、图片）必须原样保留：一个不能少、一个不能改、位置不能移动，也不要试图翻译或解读占位符内容。\n" +
		"2. Eq. (5)、Fig. 3、Table 1、Ref. [12]、arXiv:2306.00417、附录编号 S IV 等引用编号保留原样，不要翻译 Eq./Fig. 等标记。\n" +
		"3. Markdown 结构保持不变：标题层级（# 数量）、列表、引用块、加粗斜体、链接、表格与原文一一对应；标题译文务必简短。表格逐行保持竖线 | 与列数不变，只翻译单元格内的文字，单元格内的占位符原样保留。\n" +
		"4. 学科记号原样保留：数学/物理记号（狄拉克记号 |00⟩、⟨01|、希腊字母 Λ、上下标变量）、化学式与上下标（H<sub>2</sub>O 一类）、生物命名法（斜体的拉丁学名 *E. coli*、基因名 *BRCA1* 保持斜体与原文、不译成俗名）、代码标识符（驼峰/下划线命名、API 与文件路径），以及无法识别的乱码字符——一律不翻译、不删除、不猜测含义、不改写。\n" +
		"5. 专有名词、人名按学界惯例处理：有通用中译的用中译（如 Schrödinger→薛定谔），没有的保留原文。\n" +
		"6. 术语严格按下方术语表统一译名。\n" +
		"7. 只输出译文本身。不要任何解释、前言、结语、代码围栏包裹。"
)

// TestMigrateStockPresetsAutoUpgrade 精确匹配出厂原名与历史内置原稿的预设一次性自动升级，
// 自定义命名预设、用户修改过任意字符的预设及未应用草稿（DraftText）完整保留；升级置位后
// 即使主动填回历史原稿也不再被静默覆盖，且迁移幂等。
func TestMigrateStockPresetsAutoUpgrade(t *testing.T) {
	c := Default()
	draft := "用户尚未应用的草稿"
	custom := v141PhysicsRules + " "
	crlfPhys := strings.ReplaceAll(v141PhysicsRules, "\n", "\r\n")
	raw := `{"rules":{"active":"通用学术","presets":[` +
		`{"name":"物理","text":` + quote(crlfPhys) + `,"draft_text":` + quote(draft) + `},` +
		`{"name":"通用学术","text":` + quote(v141GeneralRules) + `},` +
		`{"name":"物理旧版备份","text":` + quote(v141PhysicsRules) + `},` +
		`{"name":"物理副本","text":` + quote(custom) + `}` +
		`]}}`
	if err := json.Unmarshal([]byte(raw), c); err != nil {
		t.Fatal(err)
	}
	c.Normalize()
	if len(c.Rules.Presets) != 4 {
		t.Fatalf("预设数 = %d, 期望 4", len(c.Rules.Presets))
	}
	if c.Rules.Presets[0].Text != DefaultRules {
		t.Fatal("未编辑物理原稿（CRLF）应自动升级为新版 DefaultRules")
	}
	if c.Rules.Presets[0].DraftText == nil || *c.Rules.Presets[0].DraftText != draft {
		t.Fatalf("迁移丢失或改动了未应用草稿: %v", c.Rules.Presets[0].DraftText)
	}
	if c.Rules.Presets[1].Text != GeneralRules {
		t.Fatal("未编辑通用学术原稿应自动升级为新版 GeneralRules")
	}
	if c.Rules.Presets[2].Text != v141PhysicsRules {
		t.Fatal("自定义命名预设即使正文匹配历史原稿也不应被自动升级覆盖")
	}
	if c.Rules.Presets[3].Text != custom {
		t.Fatal("用户改动 1 字符的自定义预设不应被覆盖")
	}
	if c.Rules.Active != "通用学术" || c.Rules.ActiveText() != GeneralRules {
		t.Fatalf("Active = %q, 期望 通用学术", c.Rules.Active)
	}
	if c.Rules.StockVersion != stockRulesVersion {
		t.Fatalf("StockVersion = %d, 期望 %d", c.Rules.StockVersion, stockRulesVersion)
	}
	c.Normalize()
	if c.Rules.Presets[0].Text != DefaultRules || c.Rules.Presets[1].Text != GeneralRules ||
		c.Rules.Presets[2].Text != v141PhysicsRules || c.Rules.Presets[3].Text != custom {
		t.Fatal("二次 Normalize 破坏了升级迁移结果")
	}

	// 一次性迁移完成后，用户若主动保存历史文案回「物理」并经 JSON 往返，不得被静默覆盖
	c.Rules.Presets[0].Text = v141PhysicsRules
	saved, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	cReloaded := Default()
	if err := json.Unmarshal(saved, cReloaded); err != nil {
		t.Fatal(err)
	}
	cReloaded.Normalize()
	if cReloaded.Rules.Presets[0].Text != v141PhysicsRules {
		t.Fatal("已完成一次性迁移后，用户主动写回的历史规则不应被再次覆盖")
	}

	// 预设名含首尾空格（如 " 物理 "）时应先规整名称再执行一次性自动升版
	cSpace := Default()
	if err := json.Unmarshal([]byte(`{"rules":{"active":" 物理 ","presets":[{"name":" 物理 ","text":`+quote(v141PhysicsRules)+`}]}}`), cSpace); err != nil {
		t.Fatal(err)
	}
	cSpace.Normalize()
	if cSpace.Rules.Presets[0].Name != "物理" || cSpace.Rules.Presets[0].Text != DefaultRules {
		t.Fatalf("含首尾空格的旧版出厂预设应先规整名称并完成升版: name=%q", cSpace.Rules.Presets[0].Name)
	}
}

// TestMigrateLegacyDefaultOldStockAndNoResurrect 老版单「默认」(v1.4.1 原稿)
// 自动改名「物理」并升版，补挂新版「通用学术」；已删除「通用学术」的形态不复活。
func TestMigrateLegacyDefaultOldStockAndNoResurrect(t *testing.T) {
	c := Default()
	if err := json.Unmarshal([]byte(`{"rules":{"active":"默认","presets":[{"name":"默认","text":`+quote(v141PhysicsRules)+`}]}}`), c); err != nil {
		t.Fatal(err)
	}
	c.Normalize()
	if len(c.Rules.Presets) != 2 {
		t.Fatalf("迁移后预设数 = %d, 期望 2", len(c.Rules.Presets))
	}
	if c.Rules.Presets[0].Name != "物理" || c.Rules.Presets[0].Text != DefaultRules {
		t.Fatalf("旧原版默认预设应改名物理并升版: name=%q", c.Rules.Presets[0].Name)
	}
	if c.Rules.Presets[1].Name != "通用学术" || c.Rules.Presets[1].Text != GeneralRules {
		t.Fatal("应补挂新版通用学术")
	}
	if c.Rules.Active != "物理" {
		t.Fatalf("Active = %q, 期望 物理", c.Rules.Active)
	}

	// 已删除「通用学术」、仅剩旧原版「物理」：只升级正文，绝不复活已删预设
	c2 := Default()
	if err := json.Unmarshal([]byte(`{"rules":{"active":"物理","presets":[{"name":"物理","text":`+quote(v141PhysicsRules)+`}]}}`), c2); err != nil {
		t.Fatal(err)
	}
	c2.Normalize()
	if len(c2.Rules.Presets) != 1 || c2.Rules.Presets[0].Text != DefaultRules {
		t.Fatalf("已删预设不应复活，且物理正文应升级: presets=%d", len(c2.Rules.Presets))
	}
}

// TestEmptyPresetTextFallbackByName 空正文按预设名区分兜底源：物理→DefaultRules，其余→GeneralRules。
func TestEmptyPresetTextFallbackByName(t *testing.T) {
	c := Default()
	raw := `{"rules":{"active":"物理","presets":[{"name":"物理","text":"  "},{"name":"通用学术","text":""},{"name":"自定义","text":"\n"}]}}`
	if err := json.Unmarshal([]byte(raw), c); err != nil {
		t.Fatal(err)
	}
	c.Normalize()
	if c.Rules.Presets[0].Text != DefaultRules {
		t.Fatal("空正文物理预设应兜底 DefaultRules")
	}
	if c.Rules.Presets[1].Text != GeneralRules || c.Rules.Presets[2].Text != GeneralRules {
		t.Fatal("空正文非物理预设应兜底 GeneralRules")
	}
}
