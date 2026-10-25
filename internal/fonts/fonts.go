// Package fonts 实现用户字体导入：三连检（格式魔数 → 字形覆盖 → 真实字体名）
// 与字体库管理（存于 %APPDATA%\Literature Translation\fonts\，注册进 config）。
//
// 导入的字体文件在导出时由本地 HTTP 服务器挂载到 /userfonts/ 路由，
// 渲染层用 @font-face 以内部名（UserFont-1）引用——不解析字体表里的
// 商业字体名做 CSS family，避免命名冲突与编码坑。
package fonts

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/image/font/sfnt"

	"literature-translation/internal/config"
)

// Info 是一次成功导入的结果。
type Info struct {
	InternalName string // CSS 引用名，如 "UserFont-1"
	DisplayName  string // 从字体 name 表读出的真实名字，如 "思源宋体 CN"
	FileName     string // 字体库目录内的文件名
	SizeBytes    int64
	HasCJK       bool     // 是否覆盖中文字形
	Warnings     []string // 非致命提示（如不含中文）
}

var magicCheck = func(b []byte) error {
	if len(b) < 4 {
		return fmt.Errorf("文件太小，不是有效的字体文件")
	}
	switch {
	case b[0] == 0x00 && b[1] == 0x01 && b[2] == 0x00 && b[3] == 0x00,
		string(b[0:4]) == "true", // 旧版 Mac TrueType
		string(b[0:4]) == "OTTO":
		return nil
	}
	return fmt.Errorf("不是 ttf/otf 字体文件（请勿导入 woff2 或改名的其他文件）")
}

// 探测字形覆盖用的测试字符：覆盖常用汉字、ASCII、希腊字母
var probeText = []rune("功率谱密度热力学约束Thermodynamic123ωα")

// parse 字体并提取真实名与覆盖信息。
func parse(path string) (displayName string, hasCJK bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	if err := magicCheck(data); err != nil {
		return "", false, err
	}
	sf, err := sfnt.Parse(data)
	if err != nil {
		return "", false, fmt.Errorf("字体文件损坏或不受支持: %w", err)
	}
	var buf sfnt.Buffer
	// 真实字体名：优先完整名，退化用家族名
	name, nerr := sf.Name(&buf, sfnt.NameIDFull)
	if nerr != nil || strings.TrimSpace(name) == "" {
		name, _ = sf.Name(&buf, sfnt.NameIDFamily)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "未命名字体"
	}

	hasCJK = true
	missing := 0
	for _, r := range probeText {
		idx, lerr := sf.GlyphIndex(&buf, r)
		if lerr != nil || idx == 0 {
			if r >= 0x4E00 { // 只统计汉字缺失
				missing++
			}
		}
	}
	if missing > 0 {
		hasCJK = false
	}
	return name, hasCJK, nil
}

// Dir 返回字体库目录（%APPDATA%\Literature Translation\fonts）。
func Dir() (string, error) {
	dir, err := config.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "fonts"), nil
}

// InternalName 由序号生成内部引用名。
func InternalName(n int) string { return fmt.Sprintf("UserFont-%d", n) }

var internalNameRe = regexp.MustCompile(`^UserFont-(\d+)$`)

// NextNumber 扫描已注册的内部名，返回下一个可用序号。
func NextNumber(registered []string) int {
	max := 0
	for _, name := range registered {
		if m := internalNameRe.FindStringSubmatch(name); m != nil {
			if n := atoi(m[1]); n > max {
				max = n
			}
		}
	}
	return max + 1
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

// Validate 对外部字体文件做三连检（不落库），供 GUI/CLI 预检与导入共用。
func Validate(path string) (Info, error) {
	displayName, hasCJK, err := parse(path)
	if err != nil {
		return Info{}, err
	}
	info := Info{
		DisplayName: displayName,
		HasCJK:      hasCJK,
	}
	if st, err := os.Stat(path); err == nil {
		info.SizeBytes = st.Size()
	}
	if !hasCJK {
		info.Warnings = append(info.Warnings,
			"该字体不含中文字形：中文将回退到默认字体，仅西文/数字生效")
	}
	return info, nil
}

// Import 三连检通过后把字体复制进字体库，返回注册所需信息。
// fileName 由调用方决定（通常 UserFont-N.ttf/otf），避免并发冲突。
func Import(path, destFileName string) (Info, error) {
	info, err := Validate(path)
	if err != nil {
		return info, err
	}
	dir, err := Dir()
	if err != nil {
		return info, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return info, fmt.Errorf("创建字体目录失败: %w", err)
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".ttf" && ext != ".otf" {
		// 魔数已验过是字体，扩展名不规范时按内容补齐
		if magicIsOTF(magicPeek(path)) {
			ext = ".otf"
		} else {
			ext = ".ttf"
		}
	}
	dest := filepath.Join(dir, destFileName+ext)
	data, err := os.ReadFile(path)
	if err != nil {
		return info, err
	}
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return info, fmt.Errorf("写入字体库失败: %w", err)
	}
	info.FileName = filepath.Base(dest)
	return info, nil
}

// Remove 从字体库删除文件（config 里的注册由调用方清理）。
func Remove(fileName string) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	return os.Remove(filepath.Join(dir, fileName))
}

func magicPeek(path string) []byte {
	b := make([]byte, 4)
	f, err := os.Open(path)
	if err != nil {
		return b
	}
	defer f.Close()
	f.Read(b)
	return b
}

func magicIsOTF(b []byte) bool {
	return len(b) >= 4 && string(b[0:4]) == "OTTO"
}
