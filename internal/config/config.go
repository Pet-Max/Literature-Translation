// Package config 管理Literature Translation 的持久化配置。
// 配置文件位于 %APPDATA%\Literature Translation\config.json，CLI 与 GUI 共用。
package config

import (
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
	Token        string `json:"token"`         // mineru.net「API 管理」页自建的 Token
	ModelVersion string `json:"model_version"` // "vlm"（精准，默认）或 "pipeline"（快速）
}

// LLM 配置翻译模型，任意 OpenAI 兼容接口。
type LLM struct {
	BaseURL     string `json:"base_url"` // 如 https://open.bigmodel.cn/api/paas/v4
	APIKey      string `json:"api_key"`
	Model       string `json:"model"`       // 如 deepseek-flash
	Concurrency int    `json:"concurrency"` // 并发段数，默认 3
}

// DefaultRules 系统默认翻译规则（与 engine/prompt.py 的 IRON_RULES 保持一致，
// 一致性由 config_test 的 TestDefaultRulesMatchesPython 锁定）。
// 作为「默认」预设的初始文案，也是「恢复默认」按钮的还原源。
const DefaultRules = "你是一名物理学/学术文献翻译引擎。把用户消息中的 Markdown 学术文本翻译成简体中文，严格遵守：\n\n" +
	"1. 只翻译自然语言文字；⟦F1⟧、⟦F2⟧ 这类 ⟦F数字⟧ 占位符（代表公式、代码、图片）必须原样保留：一个不能少、一个不能改、位置不能移动，也不要试图翻译或解读占位符内容。\n" +
	"2. Eq. (5)、Fig. 3、Table 1、Ref. [12]、arXiv:2306.00417、附录编号 S IV 等引用编号保留原样，不要翻译 Eq./Fig. 等标记。\n" +
	"3. Markdown 结构保持不变：标题层级（# 数量）、列表、引用块、加粗斜体、链接、表格与原文一一对应；标题译文务必简短。表格逐行保持竖线 | 与列数不变，只翻译单元格内的文字，单元格内的占位符原样保留。\n" +
	"4. 物理记号原样保留：狄拉克记号（|00⟩、⟨01|）、希腊字母（Λ、Ω、ψ）、上下标变量，以及无法识别的乱码字符——一律不翻译、不删除、不猜测含义、不改写。\n" +
	"5. 专有名词、人名按学界惯例处理：有通用中译的用中译（如 Langevin→朗之万），没有的保留原文。\n" +
	"6. 术语严格按下方术语表统一译名。\n" +
	"7. 只输出译文本身。不要任何解释、前言、结语、代码围栏包裹。"

// RulePreset 一套可切换的翻译规则预设（正文为发给模型的完整规则）。
type RulePreset struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// Rules 翻译规则。多套预设可切换，正文整体可编辑；旧版 user_global 仅用于迁移。
type Rules struct {
	Active     string       `json:"active"`      // 当前生效预设名
	Presets    []RulePreset `json:"presets"`     // 预设列表，至少一套
	UserGlobal string       `json:"user_global"` // 旧版补充规则，Normalize 迁移后清空
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

// UserFont 用户导入的字体（文件存于 %APPDATA%\Literature Translation\fonts）。
type UserFont struct {
	InternalName string `json:"internal_name"` // CSS 引用名，如 UserFont-1
	DisplayName  string `json:"display_name"`  // 字体 name 表里的真实名字
	FileName     string `json:"file_name"`     // 字体库目录内的文件名
}

// Export 导出选项。
type Export struct {
	Bilingual  bool   `json:"bilingual"`   // 译文段后附原文（引用块）
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
	SkipReferences bool `json:"skip_references"` // 文献条目（段首 [N]）保留原文不翻译，学界惯例
}

// Config 根配置。
type Config struct {
	MinerU    MinerU     `json:"mineru"`
	LLM       LLM        `json:"llm"`
	Glossary  []Term     `json:"glossary"`
	Rules     Rules      `json:"rules"`
	Export    Export     `json:"export"`
	Repair    Repair     `json:"repair"`
	Translate Translate  `json:"translate"`
	UserFonts []UserFont `json:"user_fonts"`
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

// legacyDataDirNames 历史数据目录名（按迁移优先级）；首次运行新版时整体搬移一次。
var legacyDataDirNames = []string{"Literature-Translation", "fanyi"}

// DataDir 返回 %APPDATA%\Literature Translation。若存在历史目录（上一版
// 的连字符名或最初的 fanyi）且新目录尚未创建，则整体改名搬移（一次性
// 迁移，幂等；失败不阻塞——之后按全新配置走，用户仍可手动搬回）。
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

// LegacyWebviewDataDir 返回 v1.0.1 及以前 WebView2 默认数据目录
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
		Glossary: []Term{{En: "Langevin equation", Zh: "朗之万方程"}},
		Rules:    Rules{Active: "默认", Presets: []RulePreset{{Name: "默认", Text: DefaultRules}}},
		Export: Export{
			// 出厂默认关闭：纯译文为主，需要对照的用户在设置里自行打开
			Bilingual:  false,
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
// 文件里留空的凭据字段会依次尝试环境变量（便于不落盘的密钥管理）：
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
	if os.IsNotExist(err) {
		return Default(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}
	cfg := Default()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("配置文件损坏（%s）: %w", p, err)
	}
	applyEnv(cfg)
	cfg.Normalize()
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
	} else if v := regUserEnv("MINERU_TOKEN", "MINERU-TOKEN", "MINERU_API_TOKEN"); v != "" {
		cfg.MinerU.Token = v
	}
	if v := envAny("LLM_API_KEY", "OPENAI_API_KEY"); v != "" {
		cfg.LLM.APIKey = v
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

// MustLoad 读取配置，失败时回退默认值（GUI 启动不应崩）。
func MustLoad() *Config {
	cfg, err := Load()
	if err != nil {
		return Default()
	}
	return cfg
}

// Save 写回配置文件（自动创建目录）。
func (c *Config) Save() error {
	c.Normalize()
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

// normalize 修正越界与空值。
func (c *Config) Normalize() {
	// 旧版默认字体栈迁移到中西文配对版（自定义栈不动）
	if c.Export.Font == `"LXGW WenKai", "Microsoft YaHei", sans-serif` {
		c.Export.Font = Default().Export.Font
	}
	c.MinerU.Token = strings.TrimSpace(c.MinerU.Token)
	c.MinerU.Token = strings.Trim(c.MinerU.Token, `"`)
	c.MinerU.Token = strings.TrimSpace(c.MinerU.Token)
	// 容错：误粘贴了 "Bearer xxx" 前缀
	c.MinerU.Token = strings.TrimPrefix(c.MinerU.Token, "Bearer ")
	c.MinerU.Token = strings.TrimPrefix(c.MinerU.Token, "bearer ")
	c.MinerU.Token = strings.TrimSpace(c.MinerU.Token)
	c.LLM.APIKey = strings.TrimSpace(c.LLM.APIKey)
	c.LLM.APIKey = strings.TrimPrefix(c.LLM.APIKey, "Bearer ")
	c.LLM.APIKey = strings.TrimSpace(c.LLM.APIKey)
	c.MinerU.ModelVersion = strings.ToLower(strings.TrimSpace(c.MinerU.ModelVersion))
	if c.MinerU.ModelVersion != "pipeline" {
		c.MinerU.ModelVersion = "vlm"
	}
	c.LLM.BaseURL = strings.TrimRight(strings.TrimSpace(c.LLM.BaseURL), "/")
	if c.LLM.Concurrency < 1 {
		c.LLM.Concurrency = 1
	}
	if c.LLM.Concurrency > 8 {
		c.LLM.Concurrency = 8
	}
	// 规则预设：迁移旧版 user_global → 默认预设；保证至少一套、激活项有效、长度钳制
	if len(c.Rules.Presets) == 0 {
		text := DefaultRules
		if ug := strings.TrimSpace(c.Rules.UserGlobal); ug != "" {
			text += "\n\n用户补充规则（与上述规则同等效力，须一并遵守）：\n" + ug
		}
		c.Rules.Presets = []RulePreset{{Name: "默认", Text: text}}
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
		if len([]rune(p.Text)) > 4000 {
			p.Text = string([]rune(p.Text)[:4000])
		}
		if strings.TrimSpace(p.Text) == "" {
			p.Text = DefaultRules
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
	if strings.TrimSpace(c.Export.OutputDir) == "" {
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
	if strings.TrimSpace(c.LLM.BaseURL) == "" || strings.TrimSpace(c.LLM.APIKey) == "" || strings.TrimSpace(c.LLM.Model) == "" {
		missing = append(missing, "模型 API 地址 / API Key / 模型名未配置完整（设置 → 服务连接）")
	}
	return missing
}
