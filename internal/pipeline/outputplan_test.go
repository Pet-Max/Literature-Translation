package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewArchivePlan_Basic(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(t.TempDir(), "L001_Full counting statistics_2212.11307v1.md")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := NewArchivePlan(root, src)
	if err != nil {
		t.Fatal(err)
	}
	wantDir := filepath.Join(root, "L001_Full counting statistics_2212.11307v1")
	if plan.Dir != wantDir {
		t.Fatalf("档案夹名不符: %s", plan.Dir)
	}
	// 五类子目录齐备
	for _, d := range []string{plan.EnPDFDir, plan.EnMDDir, plan.ZhMDDir, plan.ZhPDFDir, plan.ProcessDir, plan.MineruDir} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Fatalf("子目录未创建: %s", d)
		}
	}
	// 命名位：英文原名 / 中文 _zh
	if got := filepath.Base(plan.EnMDPath); got != "L001_Full counting statistics_2212.11307v1.md" {
		t.Fatalf("英文md名: %s", got)
	}
	if got := filepath.Base(plan.ZhMDPath); got != "L001_Full counting statistics_2212.11307v1_zh.md" {
		t.Fatalf("中文md名: %s", got)
	}
	if got := filepath.Base(plan.ZhPDFPath); got != "L001_Full counting statistics_2212.11307v1_zh.pdf" {
		t.Fatalf("中文pdf名: %s", got)
	}
	// md 直传：无 EnPDFPath
	if plan.EnPDFPath != "" {
		t.Fatalf("md 上传不应有 EnPDFPath: %s", plan.EnPDFPath)
	}
	// source.txt 记录原始路径
	data, _ := os.ReadFile(filepath.Join(plan.ProcessDir, "source.txt"))
	if !strings.Contains(string(data), "冒烟") == false && strings.TrimSpace(string(data)) != src {
		t.Fatalf("source.txt: %s", string(data))
	}
	if strings.TrimSpace(string(data)) != src {
		t.Fatalf("source.txt 应为原始绝对路径: %s", string(data))
	}
}

func TestNewArchivePlan_PDFHasEnPDF(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(t.TempDir(), "L002_paper.pdf")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := NewArchivePlan(root, src)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(plan.EnPDFPath) != "L002_paper.pdf" {
		t.Fatalf("PDF 归档位: %s", plan.EnPDFPath)
	}
	// PDF 上传：英文 md 归档位必须是解析产物 .md（不能带 .pdf 扩展）
	if filepath.Base(plan.EnMDPath) != "L002_paper.md" {
		t.Fatalf("英文md归档位: %s", plan.EnMDPath)
	}
	if filepath.Base(plan.ZhMDPath) != "L002_paper_zh.md" {
		t.Fatalf("zh md: %s", plan.ZhMDPath)
	}
}

func TestNewArchivePlan_ReuseSameSource(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(t.TempDir(), "L003_note.md")
	os.WriteFile(src, []byte("x"), 0o644)
	p1, err := NewArchivePlan(root, src)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := NewArchivePlan(root, src)
	if err != nil {
		t.Fatal(err)
	}
	if p1.Dir != p2.Dir {
		t.Fatalf("同源重跑应复用档案夹: %s vs %s", p1.Dir, p2.Dir)
	}
}

func TestNewArchivePlan_CollisionDifferentSource(t *testing.T) {
	root := t.TempDir()
	dirA := t.TempDir()
	dirB := t.TempDir()
	srcA := filepath.Join(dirA, "paper.md") // 同名不同目录
	srcB := filepath.Join(dirB, "paper.md")
	os.WriteFile(srcA, []byte("a"), 0o644)
	os.WriteFile(srcB, []byte("b"), 0o644)
	p1, err := NewArchivePlan(root, srcA)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := NewArchivePlan(root, srcB)
	if err != nil {
		t.Fatal(err)
	}
	if p1.Dir == p2.Dir {
		t.Fatalf("同名不同源必须分档案夹")
	}
	if filepath.Base(p2.Dir) != "paper-2" {
		t.Fatalf("冲突后缀: %s", filepath.Base(p2.Dir))
	}
	// p2 的source.txt 应指向 srcB
	data, _ := os.ReadFile(filepath.Join(p2.ProcessDir, "source.txt"))
	if strings.TrimSpace(string(data)) != srcB {
		t.Fatalf("p2 来源: %s", string(data))
	}
}

func TestNewArchivePlan_LongNameTruncated(t *testing.T) {
	root := t.TempDir()
	long := strings.Repeat("长", 150)
	src := filepath.Join(t.TempDir(), long+".md")
	os.WriteFile(src, []byte("x"), 0o644)
	plan, err := NewArchivePlan(root, src)
	if err != nil {
		t.Fatal(err)
	}
	if got := len([]rune(filepath.Base(plan.Dir))); got > archiveFolderMax {
		t.Fatalf("档案夹名未截断: %d", got)
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		`a:b*c?d"e<f>g|h`: "a_b_c_d_e_f_g_h",
		"普通 名称.md":        "普通 名称.md",
		`path\to\file`:    "path_to_file",
	}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Fatalf("sanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

// fuzz 抓出的路径逃逸（stem=".."）与保留设备名的防回归。
func TestNewArchivePlan_BadNames(t *testing.T) {
	root := t.TempDir()
	for _, stem := range []string{"..", ".", "", "CON", "nul.txt", "...md 的 stem 来源"} {
		src := filepath.Join(t.TempDir(), stem+".md")
		plan, err := NewArchivePlan(root, src)
		if err != nil {
			t.Fatalf("stem=%q: %v", stem, err)
		}
		rel, err := filepath.Rel(root, plan.Dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, `..\`) {
			t.Fatalf("stem=%q 档案夹逃出归档根: %s", stem, plan.Dir)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "未命名")); err != nil {
		t.Fatalf("极端名应落「未命名」档案夹: %v", err)
	}
}
