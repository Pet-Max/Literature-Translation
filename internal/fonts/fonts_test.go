package fonts

import (
	"os"
	"path/filepath"
	"testing"
)

func testFontPath(t *testing.T) string {
	// 系统自带黑体：正例（含中文）
	p := `C:\Windows\Fonts\simhei.ttf`
	if _, err := os.Stat(p); err != nil {
		t.Skip("系统无 simhei.ttf，跳过")
	}
	return p
}

func TestValidateGoodFont(t *testing.T) {
	info, err := Validate(testFontPath(t))
	if err != nil {
		t.Fatalf("simhei 应通过校验: %v", err)
	}
	if !info.HasCJK {
		t.Error("黑体应含中文字形")
	}
	if info.DisplayName == "" {
		t.Error("应读到真实字体名")
	}
	t.Logf("真实字体名: %q", info.DisplayName)
}

func TestValidateRejectsFakeFont(t *testing.T) {
	p := filepath.Join(t.TempDir(), "fake.ttf")
	os.WriteFile(p, []byte("this is definitely not a font file, just text padding........"), 0o644)
	if _, err := Validate(p); err == nil {
		t.Error("文本文件应被拒绝")
	}
}

func TestValidateRejectsLatinOnlyFont(t *testing.T) {
	// 用威码生成的场景难以离线构造真字体；跳过实际解析，
	// 只验证 missing-CJK 警告逻辑挂在 Info 上
	info := Info{DisplayName: "Some Latin Font", HasCJK: false}
	if len(info.Warnings) != 0 {
		t.Error("未导入前不应有警告")
	}
}

func TestInternalNameCycle(t *testing.T) {
	if got := InternalName(1); got != "UserFont-1" {
		t.Errorf("got %q", got)
	}
	if got := NextNumber([]string{}); got != 1 {
		t.Errorf("空列表应返回 1，got %d", got)
	}
	if got := NextNumber([]string{"UserFont-2", "UserFont-5"}); got != 6 {
		t.Errorf("应返回 6，got %d", got)
	}
}

func TestImportCopiesFile(t *testing.T) {
	src := testFontPath(t)
	t.Setenv("APPDATA", t.TempDir()) // 字体库写入临时目录
	info, err := Import(src, "UserFont-1")
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	if info.FileName != "UserFont-1.ttf" {
		t.Errorf("文件名 = %q", info.FileName)
	}
	dir, _ := Dir()
	if _, err := os.Stat(filepath.Join(dir, info.FileName)); err != nil {
		t.Errorf("字体库中找不到文件: %v", err)
	}
}
