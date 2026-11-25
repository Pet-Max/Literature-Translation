// Package config 管理Literature Translation 的持久化配置。
// 配置文件位于 %APPDATA%\Literature Translation\config.json，CLI 与 GUI 共用。
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// Term 是一条术语对照。
type Term struct {
	En string `json:"en"`
	Zh string `json:"zh"`
}

// MinerU 配置解析服务。
type MinerU struct {
	Token        string `json:"token"`              // mineru.net「API 管理」页自建的 Token
	ModelVersion string `json:"model_version"`      // "vlm"（精准，默认）或 "pipeline"（快速）
	Language     string `json:"language,omitempty"` // OCR 源文档语言代码，默认 "en"
}

// LLM 配置翻译模型，任意 OpenAI 兼容接口。
type LLM struct {
	BaseURL     string `json:"base_url"` // 如 https://open.bigmodel.cn/api/paas/v4
	APIKey      string `json:"api_key"`
	Model       string `json:"model"`       // 如 deepseek-flash
	Concurrency int    `json:"concurrency"` // 并发段数，默认 3
}

// DefaultRules 系统默认翻译规则（与内置引擎规则保持一致，
// 引擎侧现值为 engine.IronRules；一致性由 engine 包的
// TestDefaultRulesMatchesEngine 锁定）。
// 作为「物理」预设的初始文案（出厂生效），也是「物理」预设「恢复默认」
// 的还原源（其余预设与新建预设以 GeneralRules 为基线）。
// 约束：DefaultRules 逐字节参与 BuildCacheKey 任务指纹计算，变更须与
// engine.IronRules 同步，并按需将旧版摘要登记至 legacyPhysicsRuleHashes。
const DefaultRules = "你是一名物理学/学术文献翻译引擎。将用户消息中的 Markdown 学术文本完整、准确地翻译为流畅严谨的简体中文，严格遵守：\n\n" +
	"1. 占位符与公式：⟦F1⟧、⟦F2⟧ 这类 ⟦F数字⟧ 占位符（代表公式、代码块、图片）必须原样保留，不得增删、改写或解读；独立成段的占位符保持原段落位置，行内占位符可随中文语序自然调整但须保持准确对应。不要擅自增删 $ / $$ 公式定界符。\n" +
	"2. 忠实与单位：逐句完整翻译，不遗漏、不概括、不增补；按中文学术习惯合理调整长难句语序。首次出现的「全称（缩写）」保留英文缩写；数值后的国际单位与统计记号（如 nm、m/s、mg/kg、p < 0.05）保持原符号，不译成汉字单位。\n" +
	"3. 引用与结构标签：Eq./Equation、Fig./FIG./Figure、Table/TABLE、Ref.、arXiv/DOI 及附录编号（如 Fig. S1、S IV）等交叉引用标记连同编号保留原样，不翻译标记词，也不在前面重复加「如图/见表」（写作「见 Fig. 1」而非「如图 Fig. 1」）；章节与习题标签统一中译（Chapter→第 N 章、Section/Sec.→第 N 节、Exercise→习题）。\n" +
	"4. Markdown 与表格：保持标题层级（# 数量）、列表、引用块、链接、表格与原文一一对应；标题译文简洁忠实。链接仅译显示文字，URL 保持原样且 ] 与 ( 间不加空格；加粗/斜体紧贴中文引号或括号时将全角标点置于标记外侧。表格逐行保持竖线 |、列数、分隔行及单元格内 \\| 不变，单元格内不换行，不新增 <br> 等 HTML 标签；<sup>/<sub> 仅用于短角标或化学式，切勿将普通中文词句放入 <sup>/<sub> 内。\n" +
	"5. 物理记号与代码：狄拉克记号（|00⟩、⟨01|）、希腊字母（Λ、Ω、ψ）、上下标变量、行内代码（反引号 `...`），以及无法识别的乱码字符均原样保留，不翻译、不删除、不猜测含义、不改写。\n" +
	"6. 人名与专名：历史著名学者与定名概念统一采用学界通用中译（如 Schrödinger→薛定谔、von Neumann→冯·诺依曼、Bayes→贝叶斯）；正文引用的论文作者与作者栏保留原文姓氏（如 Smith et al.→Smith 等），英文姓名间保留半角逗号；无通用中译的专名保留原文。若遇参考文献条目，保留原文不译。\n" +
	"7. 术语统一：若下方提供术语表，严格按表统一译名（含单复数与大小写变体）；未列出的专业术语采用该学科规范中文译名并保持全篇前后一致，不随意混用不同译法或中途夹杂英文原词。\n" +
	"8. 只输出译文本身。不要任何解释、前言、结语、思考过程或外层代码围栏包裹。"

// GeneralRules 「通用学术」预设的初始文案：面向非物理领域（化学/生物/
// 医学/计算机/人文等）的通用化措辞；亦为其余预设「恢复默认」的还原源
// 与新建预设的初始底稿。与 DefaultRules 保持最小差异（仅人设与规则 5 两处，
// 其余条款逐字节一致）：规则正文是任务指纹 BuildCacheKey 的输入，切换预设即切换指纹。
const GeneralRules = "你是一名学术文献翻译引擎。将用户消息中的 Markdown 学术文本完整、准确地翻译为流畅严谨的简体中文，严格遵守：\n\n" +
	"1. 占位符与公式：⟦F1⟧、⟦F2⟧ 这类 ⟦F数字⟧ 占位符（代表公式、代码块、图片）必须原样保留，不得增删、改写或解读；独立成段的占位符保持原段落位置，行内占位符可随中文语序自然调整但须保持准确对应。不要擅自增删 $ / $$ 公式定界符。\n" +
	"2. 忠实与单位：逐句完整翻译，不遗漏、不概括、不增补；按中文学术习惯合理调整长难句语序。首次出现的「全称（缩写）」保留英文缩写；数值后的国际单位与统计记号（如 nm、m/s、mg/kg、p < 0.05）保持原符号，不译成汉字单位。\n" +
	"3. 引用与结构标签：Eq./Equation、Fig./FIG./Figure、Table/TABLE、Ref.、arXiv/DOI 及附录编号（如 Fig. S1、S IV）等交叉引用标记连同编号保留原样，不翻译标记词，也不在前面重复加「如图/见表」（写作「见 Fig. 1」而非「如图 Fig. 1」）；章节与习题标签统一中译（Chapter→第 N 章、Section/Sec.→第 N 节、Exercise→习题）。\n" +
	"4. Markdown 与表格：保持标题层级（# 数量）、列表、引用块、链接、表格与原文一一对应；标题译文简洁忠实。链接仅译显示文字，URL 保持原样且 ] 与 ( 间不加空格；加粗/斜体紧贴中文引号或括号时将全角标点置于标记外侧。表格逐行保持竖线 |、列数、分隔行及单元格内 \\| 不变，单元格内不换行，不新增 <br> 等 HTML 标签；<sup>/<sub> 仅用于短角标或化学式，切勿将普通中文词句放入 <sup>/<sub> 内。\n" +
	"5. 学科记号与代码：行内代码（反引号 `...`）、代码标识符与文件路径、数学/物理记号（|00⟩、⟨01|、希腊字母、上下标变量）、化学式（H<sub>2</sub>O）、生物拉丁学名与基因名斜体（*E. coli*、*BRCA1*），以及无法识别的乱码字符均原样保留，不翻译、不删除、不猜测含义、不改写。\n" +
	"6. 人名与专名：历史著名学者与定名概念统一采用学界通用中译（如 Schrödinger→薛定谔、von Neumann→冯·诺依曼、Bayes→贝叶斯）；正文引用的论文作者与作者栏保留原文姓氏（如 Smith et al.→Smith 等），英文姓名间保留半角逗号；无通用中译的专名保留原文。若遇参考文献条目，保留原文不译。\n" +
	"7. 术语统一：若下方提供术语表，严格按表统一译名（含单复数与大小写变体）；未列出的专业术语采用该学科规范中文译名并保持全篇前后一致，不随意混用不同译法或中途夹杂英文原词。\n" +
	"8. 只输出译文本身。不要任何解释、前言、结语、思考过程或外层代码围栏包裹。"

// stockRulesVersion 标记出厂历史原稿自动升版的已完成代数，确保一次性迁移。
const stockRulesVersion = 1

// 历史各发行版未编辑原稿的 SHA-256 摘要（CRLF 归一为 LF 后计算），
// 用于一次性自动升级未自定义的旧预设正文，避免覆盖用户自编辑规则。
var (
	legacyPhysicsRuleHashes = map[string]bool{
		"5e890899100a410986cd8639786897db01c00a474c9604df228011b90fe60321": true, // v1.0.0–v1.0.5
		"9b1f974c8d9f83f4190ac325bac091b7c4ba81ac802d6a844aa7ea97c4b8ab68": true, // v1.1.0–v1.2.4
		"eda43fff16754df4233f391b45f0f5a7c3ba2626f21caa59149650f65935dff3": true, // v1.2.5–v1.4.1
	}
	legacyGeneralRuleHashes = map[string]bool{
		"6a93c334014c8fca0ba79a7ecb7fe9d169ee6f4a8840d4e555df4d148781da15": true, // v1.2.1–v1.2.4
		"5320532a65d39c03f3d5847ad2cdf53578e389b13a291a856933c0a6fd69b7e1": true, // v1.2.5–v1.4.1
	}
)

// ruleTextHash 将换行归一为 LF 后计算 SHA-256 十六进制摘要，消除平台换行差异。
func ruleTextHash(text string) string {
	norm := strings.ReplaceAll(text, "\r\n", "\n")
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:])
}

// isLegacyPhysicsStock 判断正文是否严格匹配历史发行版内置物理原稿。
func isLegacyPhysicsStock(text string) bool {
	return legacyPhysicsRuleHashes[ruleTextHash(text)]
}

// isLegacyGeneralStock 判断正文是否严格匹配历史发行版内置通用学术原稿。
func isLegacyGeneralStock(text string) bool {
	return legacyGeneralRuleHashes[ruleTextHash(text)]
}

// RulePreset 一套可切换的翻译规则预设（正文为发给模型的完整规则）。
type RulePreset struct {
	Name      string  `json:"name"`
	Text      string  `json:"text"`
	DraftText *string `json:"draft_text,omitempty"`
}

// Rules 翻译规则。多套预设可切换，正文整体可编辑；旧版 user_global 仅用于迁移。
type Rules struct {
	Active       string       `json:"active"`                  // 当前生效预设名
	Presets      []RulePreset `json:"presets"`                 // 预设列表，至少一套
	UserGlobal   string       `json:"user_global"`             // 旧版补充规则，Normalize 迁移后清空
	StockVersion int          `json:"stock_version,omitempty"` // 出厂规则自动升版标记（一次性迁移）
}

// ActiveText 返回当前生效预设的规则正文（异常时兜底系统默认）。
func (r Rules) ActiveText() string {
	for _, p := range r.Presets {
		if p.Name == r.Active && strings.TrimSpace(p.Text) != "" {
			return p.Text
		}
	}
	if len(r.Presets) > 0 && strings.TrimSpace(r.Presets[0].Text) != "" {
		return r.Presets[0].Text
	}
	return DefaultRules
}

// migrateLegacyDefaultPreset 双预设升级迁移（Load/Save 均经 Normalize 触发，
// 按「默认」预设签名触发、天然幂等、不复活已删除的出厂预设）：
//   - 旧版「默认」预设且正文与历史或当前内置物理原稿一致（CRLF 容差）→ 改名
//     「物理」，Active 随迁，并补挂「通用学术」（随后由 migrateStockRulePresets 统一升版）；
//   - 「默认」预设被用户编辑过 → 原名原样保留、Active 不变，仅补挂
//     「通用学术」（自定义文本不得误标为物理原版）；
//   - 无「默认」预设（已迁移或用户自管/删除过）→ 整体不动。
func migrateLegacyDefaultPreset(r *Rules) {
	hasPhysics, hasGeneral, hasDefault, legacyStock := false, false, false, false
	for _, p := range r.Presets {
		switch p.Name {
		case "物理":
			hasPhysics = true
		case "通用学术":
			hasGeneral = true
		case "默认":
			hasDefault = true
			norm := strings.ReplaceAll(p.Text, "\r\n", "\n")
			if norm == DefaultRules || isLegacyPhysicsStock(norm) {
				legacyStock = true
			}
		}
	}
	if hasGeneral || !hasDefault {
		return // 已迁移/用户自管形态：无签名即不动，删除过的出厂预设不复活
	}
	if legacyStock && !hasPhysics {
		for i := range r.Presets {
			if r.Presets[i].Name == "默认" {
				r.Presets[i].Name = "物理"
				break
			}
		}
		if r.Active == "默认" {
			r.Active = "物理"
		}
	}
	r.Presets = append(r.Presets, RulePreset{Name: "通用学术", Text: GeneralRules})
}

// migrateStockRulePresets 将出厂原名（「物理」「默认」「通用学术」）且内容严格匹配
// 历史内置原稿的预设一次性自动升级至当前新版文案；之后置位 StockVersion，避免后续
// 用户主动保存历史文案时被静默覆盖。自定义命名预设、修改过任意字符的预设及未应用
// 草稿（DraftText）均原样保留；天然幂等。
func migrateStockRulePresets(r *Rules) {
	if r.StockVersion >= stockRulesVersion {
		return
	}
	for i := range r.Presets {
		switch {
		case (r.Presets[i].Name == "物理" || r.Presets[i].Name == "默认") && isLegacyPhysicsStock(r.Presets[i].Text):
			r.Presets[i].Text = DefaultRules
		case r.Presets[i].Name == "通用学术" && isLegacyGeneralStock(r.Presets[i].Text):
			r.Presets[i].Text = GeneralRules
		}
	}
	r.StockVersion = stockRulesVersion
}

// UserFont 用户导入的字体（文件存于 %APPDATA%\Literature Translation\fonts）。
type UserFont struct {
	InternalName string `json:"internal_name"` // CSS 引用名，如 UserFont-1
	DisplayName  string `json:"display_name"`  // 字体 name 表里的真实名字
	FileName     string `json:"file_name"`     // 字体库目录内的文件名
}

// Export 导出选项。
type Export struct {
	Bilingual  bool   `json:"bilingual"`   // 译文段后附原文（引用块）
	EnBg       string `json:"enbg"`        // 英文引用块配色预设键：paper|cool|teal|warm|none，空=paper（现状米白）
	ZhBg       string `json:"zhbg"`        // 中文正文纸色/墨色预设键：paper|cream|cool|teal|warm，空=paper（白纸黑墨现状）
	TOC        bool   `json:"toc"`         // 按标题层级生成目录
	TwoColumn  bool   `json:"two_column"`  // 双栏排版（公式多时建议关）
	FontPreset string `json:"font_preset"` // lxgw | songti | yahei | custom（导入字体）
	Font       string `json:"font"`        // 正文字体栈（中西文配对，含跨平台回退）
	UserFont   string `json:"user_font"`   // 选中的用户字体内部名，空=不用
	OutputDir  string `json:"output_dir"`  // 成品输出目录，空则用 PDF 同目录
}

// Repair 解析后处理选项。
type Repair struct {
	Spelling bool `json:"spelling"` // 用原 PDF 词表校准 OCR 拼写损伤（推荐开启）
	Symbols  bool `json:"symbols"`  // 修复 |00⟩、公式箭头等符号级损伤（推荐开启）
}

// Translate 翻译策略。
type Translate struct {
	SkipReferences bool `json:"skip_references"` // 参考文献条目保留原文不翻译（含编号条目与参考节引文列表）
}

// Config 根配置。
type Config struct {
	envToken     string
	envKey       string
	MinerU       MinerU     `json:"mineru"`
	LLM          LLM        `json:"llm"`
	Glossary     []Term     `json:"glossary"`
	GlossaryRows *[]Term    `json:"glossary_rows,omitempty"`
	Rules        Rules      `json:"rules"`
	Export       Export     `json:"export"`
	Repair       Repair     `json:"repair"`
	Translate    Translate  `json:"translate"`
	UserFonts    []UserFont `json:"user_fonts"`
}

// FindUserFont 按内部名查找用户字体。
func (c *Config) FindUserFont(internal string) *UserFont {
	for i := range c.UserFonts {
		if c.UserFonts[i].InternalName == internal {
			return &c.UserFonts[i]
		}
	}
	return nil
}

// Path 返回配置文件绝对路径。
// DataDirName 本应用的用户数据目录名（%APPDATA% 下）。
const DataDirName = "Literature Translation"

// legacyDataDirNames 旧版数据目录名（按迁移优先级）；首次运行新版时整体搬移一次。
var legacyDataDirNames = []string{"Literature-Translation", "fanyi"}

// DataDir 返回 %APPDATA%\Literature Translation。若存在历史数据目录且新目录
// 尚未创建，则按优先级整体改名搬移（一次性迁移，幂等；失败不阻塞）。
func DataDir() (string, error) {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return "", fmt.Errorf("环境变量 APPDATA 不存在")
	}
	dir := filepath.Join(appData, DataDirName)
	if _, err := os.Stat(dir); err != nil {
		for _, name := range legacyDataDirNames {
			legacy := filepath.Join(appData, name)
			if _, err := os.Stat(legacy); err == nil {
				if os.Rename(legacy, dir) == nil {
					break
				}
			}
		}
	}
	return dir, nil
}

func Path() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// WebviewDataDir 返回 WebView2 内核数据目录：%LOCALAPPDATA%\Literature
// Translation\WebView2。不设置时 go-webview2 会回退到 %APPDATA%\{exe 完整
// 文件名}（生成带 .exe 后缀的迷惑性文件夹，且与安装版/便携版共享）。
// 内核缓存属机器本地数据，放 LOCALAPPDATA 使 Roaming 只保留 config.json。
// 环境变量缺失时返回空串，走库默认行为，不致启动失败。
func WebviewDataDir() string {
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		return ""
	}
	return filepath.Join(localAppData, DataDirName, "WebView2")
}

// LegacyWebviewDataDir 返回早期版本遗留的 WebView2 默认数据目录
// （%APPDATA%\Literature-Translation.exe），供启动时一次性清理（纯缓存）。
func LegacyWebviewDataDir() string {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return ""
	}
	return filepath.Join(appData, "Literature-Translation.exe")
}

// Default 返回带默认值的空配置（首次使用）。
func Default() *Config {
	return &Config{
		MinerU: MinerU{ModelVersion: "vlm"},
		LLM:    LLM{Concurrency: 3},
		// 出厂自带一条示例，让新用户明白「原文术语 → 统一译名」的填法；
		// 物理领域的标准译名，短语形态正是术语表要解决的场景
		Glossary: []Term{{En: "Schrödinger equation", Zh: "薛定谔方程"}},
		// 双预设出厂：「物理」排第一并默认生效（与 ActiveText 的
		// Presets[0] 兜底链一致）；「通用学术」供非物理领域切换。
		// StockVersion 刻意保持零值（不设为 stockRulesVersion），确保 decodeConfig
		// 载入不含 stock_version 字段的历史配置后可由 Normalize 触发一次性升版并置位。
		Rules: Rules{Active: "物理", Presets: []RulePreset{
			{Name: "物理", Text: DefaultRules},
			{Name: "通用学术", Text: GeneralRules},
		}},
		Export: Export{
			// 出厂默认关闭：纯译文为主，需要对照的用户在设置里自行打开
			Bilingual:  false,
			EnBg:       "paper",
			ZhBg:       "paper",
			TOC:        false,
			TwoColumn:  false,
			FontPreset: "lxgw",
			// 中西文配对：西文/数字/希腊字母统一 Times，中文落霞鹜文楷
			Font: `"Times New Roman", "LXGW WenKai", "Microsoft YaHei", serif`,
		},
		Repair:    Repair{Spelling: true, Symbols: true},
		Translate: Translate{SkipReferences: true},
		// 空切片而非 nil：nil 经 JSON 序列化成 null，前端拿到会崩
		UserFonts: []UserFont{},
	}
}

// Load 读取配置；文件不存在时返回默认配置（不落盘，首次 Save 时才创建）。
// 凭据由环境变量优先提供（注入值未改变时不落盘），支持以下变量：
//
//	MinerU Token: MINERU_TOKEN / MINERU-TOKEN / MINERU_API_TOKEN
//	LLM Key:      LLM_API_KEY / OPENAI_API_KEY
//	LLM 地址:      LLM_BASE_URL / OPENAI_BASE_URL
//	LLM 模型:      LLM_MODEL / OPENAI_MODEL
func Load() (*Config, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}
	if err == nil && len(data) == 0 {
		return nil, fmt.Errorf("配置文件为空，请先恢复")
	}
	cfg, err := decodeConfig(data)
	if err != nil {
		return nil, fmt.Errorf("配置文件损坏，请先恢复配置: %w", err)
	}
	applyEnv(cfg)
	cfg.Normalize()
	return cfg, nil
}

func decodeConfig(data []byte) (*Config, error) {
	cfg := Default()
	if len(data) != 0 {
		if !strings.HasPrefix(strings.TrimSpace(string(data)), "{") {
			return nil, fmt.Errorf("配置应为 JSON 对象")
		}
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, err
		}
		var raw struct {
			Rules map[string]json.RawMessage `json:"rules"`
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, err
		}
		if _, has := raw.Rules["presets"]; !has && strings.TrimSpace(cfg.Rules.UserGlobal) != "" {
			cfg.Rules.Presets = nil
		}
	}
	return cfg, nil
}

func envAny(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

// applyEnv 凭据字段：环境变量/注册表优先于配置文件（12-factor 惯例），
// 这样 setx 之后无需清理配置文件残留值即可生效。
func applyEnv(cfg *Config) {
	if v := envAny("MINERU_TOKEN", "MINERU-TOKEN", "MINERU_API_TOKEN"); v != "" {
		cfg.MinerU.Token = v
		cfg.envToken = v
	} else if v := regUserEnv("MINERU_TOKEN", "MINERU-TOKEN", "MINERU_API_TOKEN"); v != "" {
		cfg.MinerU.Token = v
		cfg.envToken = v
	}
	if v := envAny("LLM_API_KEY", "OPENAI_API_KEY"); v != "" {
		cfg.LLM.APIKey = v
		cfg.envKey = v
	}
	if v := envAny("LLM_BASE_URL", "OPENAI_BASE_URL"); v != "" {
		cfg.LLM.BaseURL = v
	}
	if v := envAny("LLM_MODEL", "OPENAI_MODEL"); v != "" {
		cfg.LLM.Model = v
	}
}

// regUserEnv 从 HKCU\Environment 读取用户级持久化环境变量（仅 Windows）。
func regUserEnv(names ...string) string {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	for _, n := range names {
		if v, _, err := k.GetStringValue(n); err == nil && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// Save 写回配置文件（自动创建目录）。
func (c *Config) Save() error {
	_, err := Update(func(next *Config) error { *next = *c.Clone(); return nil })
	return err
}

// Normalize 规范化、修剪并修正越界配置项及遗留字段迁移。
func (c *Config) Normalize() {
	if c.GlossaryRows != nil {
		c.Glossary = []Term{}
		for _, row := range *c.GlossaryRows {
			if strings.TrimSpace(row.En) != "" && strings.TrimSpace(row.Zh) != "" {
				c.Glossary = append(c.Glossary, Term{En: strings.TrimSpace(row.En), Zh: strings.TrimSpace(row.Zh)})
			}
		}
	}
	if c.Glossary == nil {
		c.Glossary = []Term{}
	}
	// 旧版默认字体栈迁移到中西文配对版（自定义栈不动）
	if c.Export.Font == `"LXGW WenKai", "Microsoft YaHei", sans-serif` {
		c.Export.Font = Default().Export.Font
	}
	c.MinerU.Token = cleanCredential(c.MinerU.Token)
	c.LLM.APIKey = cleanCredential(c.LLM.APIKey)
	c.MinerU.ModelVersion = strings.ToLower(strings.TrimSpace(c.MinerU.ModelVersion))
	if c.MinerU.ModelVersion != "pipeline" {
		c.MinerU.ModelVersion = "vlm"
	}
	c.MinerU.Language = strings.TrimSpace(c.MinerU.Language)
	c.LLM.Model = strings.TrimSpace(c.LLM.Model)
	c.LLM.BaseURL = strings.TrimRight(strings.TrimSpace(c.LLM.BaseURL), "/")
	if c.LLM.Concurrency < 1 {
		c.LLM.Concurrency = 1
	}
	if c.LLM.Concurrency > 8 {
		c.LLM.Concurrency = 8
	}
	// 规则预设：迁移旧版 user_global → 出厂预设；双预设升级迁移；保证
	// 至少一套、激活项有效、长度钳制
	if len(c.Rules.Presets) == 0 {
		text := DefaultRules
		if ug := strings.TrimSpace(c.Rules.UserGlobal); ug != "" {
			text += "\n\n用户补充规则（与上述规则同等效力，须一并遵守）：\n" + ug
		}
		c.Rules.Presets = []RulePreset{
			{Name: "物理", Text: text},
			{Name: "通用学术", Text: GeneralRules},
		}
	}
	c.Rules.UserGlobal = ""
	for i := range c.Rules.Presets {
		p := &c.Rules.Presets[i]
		p.Name = strings.TrimSpace(strings.ReplaceAll(p.Name, "\n", " "))
		if len([]rune(p.Name)) > 20 {
			p.Name = string([]rune(p.Name)[:20])
		}
		if p.Name == "" {
			p.Name = "预设"
		}
	}
	migrateLegacyDefaultPreset(&c.Rules)
	migrateStockRulePresets(&c.Rules)
	for i := range c.Rules.Presets {
		p := &c.Rules.Presets[i]
		if strings.TrimSpace(p.Text) == "" {
			if p.Name == "物理" {
				p.Text = DefaultRules
			} else {
				p.Text = GeneralRules
			}
		}
	}
	activeOK := false
	for _, p := range c.Rules.Presets {
		if p.Name == c.Rules.Active {
			activeOK = true
			break
		}
	}
	if !activeOK {
		c.Rules.Active = c.Rules.Presets[0].Name
	}
	if strings.TrimSpace(c.Export.Font) == "" {
		c.Export.Font = Default().Export.Font
	}
	// 归档根目录：空则默认 = 程序本体目录\Literature Library（便携式：程序与产出同在）
	c.Export.OutputDir = strings.TrimSpace(c.Export.OutputDir)
	if c.Export.OutputDir == "" {
		if exe, err := os.Executable(); err == nil {
			c.Export.OutputDir = filepath.Join(filepath.Dir(exe), "Literature Library")
		}
	}
	// 字体预设：与字体栈/导入字体联动。旧配置无此字段时按栈特征推断迁移；
	// 选了导入字体则一律视为 custom（渲染时 fontStack 会把内部名前置）。
	switch c.Export.FontPreset {
	case "lxgw", "songti", "yahei", "custom":
	default:
		switch {
		case strings.Contains(c.Export.Font, "SimSun"):
			c.Export.FontPreset = "songti"
		case c.Export.UserFont != "":
			c.Export.FontPreset = "custom"
		default:
			c.Export.FontPreset = "lxgw"
		}
	}
	if c.Export.UserFont != "" {
		c.Export.FontPreset = "custom"
	}
	// 旧版落盘的 "user_fonts": null 载入后是 nil，归一化为空切片：
	// null 发到前端会让 FontList 的 list.map 崩溃，整条初始化链中断
	if c.UserFonts == nil {
		c.UserFonts = []UserFont{}
	}
}

// ValidateAndReport 返回可直接展示给用户的缺失项列表（用于启动自检）。
func (c *Config) ValidateAndReport() []string {
	var missing []string
	if strings.TrimSpace(c.MinerU.Token) == "" {
		missing = append(missing, "MinerU Token 未配置（设置 → 服务连接）")
	}
	return append(missing, c.ValidateLLMAndReport()...)
}

// ValidateLLMAndReport 仅检查翻译模型 API 是否配置完整（供不依赖 MinerU 的纯 MD 翻译使用）。
func (c *Config) ValidateLLMAndReport() []string {
	if strings.TrimSpace(c.LLM.BaseURL) == "" || strings.TrimSpace(c.LLM.APIKey) == "" || strings.TrimSpace(c.LLM.Model) == "" {
		return []string{"模型 API 地址 / API Key / 模型名未配置完整（设置 → 服务连接）"}
	}
	return nil
}

func cleanCredential(s string) string {
	s = strings.TrimSpace(strings.Trim(strings.TrimSpace(s), `"`))
	if len(s) >= 7 && strings.EqualFold(s[:7], "Bearer ") {
		s = s[7:]
	}
	return strings.TrimSpace(s)
}
