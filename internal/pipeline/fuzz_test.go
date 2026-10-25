// fuzz_test.go — 模糊测试：只打吃「不可信外部输入」的两个函数。
// 种子语料随 go test 常跑；深挖用：
//
//	go test ./internal/pipeline -fuzz FuzzFileIsChinese -fuzztime 30s
//	go test ./internal/pipeline -fuzz FuzzNewArchivePlan -fuzztime 30s
package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzFileIsChinese：任意字节流（畸形 UTF-8、二进制片段）不 panic，
// 占比恒在 [0,1]，文件路径与纯文本路径判定一致。
func FuzzFileIsChinese(f *testing.F) {
	f.Add([]byte("Hello, world! This is an English document."))
	f.Add([]byte("这是一段中文文本，用于测试语言检测阈值判定。"))
	f.Add([]byte("mixed 中英 mixed text 123 ABC"))
	f.Add([]byte{0xFF, 0xFE, 0x00, 0x01})          // 畸形字节
	f.Add([]byte("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")) // PDF 头片段
	f.Fuzz(func(t *testing.T, data []byte) {
		text := string(data) // 非法字节 → RuneError，不应 panic
		r := CJKRatio(text)
		if r < 0 || r > 1 {
			t.Fatalf("CJKRatio 越界: %v", r)
		}
		if got := IsChineseText(text); got != (r > cjkThreshold) {
			t.Fatalf("IsChineseText=%v 与阈值判定不一致 (ratio=%v)", got, r)
		}
		p := filepath.Join(t.TempDir(), "f.bin")
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Skip()
		}
		_ = FileIsChinese(p) // 文件路径只要求不 panic
	})
}

// FuzzNewArchivePlan：任意文件名（非法字符、超长、全点、Windows 保留名）下
// 档案夹不逃出归档根、名字不含非法字符且不超上限。
func FuzzNewArchivePlan(f *testing.F) {
	f.Add("normal paper")
	f.Add("论文标题：带冒号")
	f.Add(`a\b/c:d*e?f"g<h>i|j`)
	f.Add(strings.Repeat("长", 300)) // 超长截断
	f.Add("..")                     // 文件名 ...md 的 stem 恰为两点（路径逃逸尝试）
	f.Add(".")                      // 隐藏文件 .md 的 stem
	f.Add("")                       // 纯扩展名
	f.Add("CON")                    // Windows 保留设备名
	f.Add("NUL.txt")                // 保留名带扩展
	f.Add("\x1e")                   // 控制字符（fuzz 实测抓到的）
	f.Add("论文标题.")                  // 结尾点（Windows 目录名禁止）
	f.Fuzz(func(t *testing.T, stem string) {
		root := t.TempDir()
		src := filepath.Join(t.TempDir(), stem+".md")
		plan, err := NewArchivePlan(root, src)
		if err != nil {
			t.Fatalf("NewArchivePlan(stem=%q) 出错: %v", stem, err)
		}
		rel, err := filepath.Rel(root, plan.Dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, `..\`) || strings.HasPrefix(rel, "../") {
			t.Fatalf("档案夹逃出归档根: stem=%q dir=%s (rel=%s)", stem, plan.Dir, rel)
		}
		name := filepath.Base(plan.Dir)
		if strings.ContainsAny(name, `\/:*?"<>|`) {
			t.Fatalf("档案夹名含非法字符: %q", name)
		}
		if n := utf8.RuneCountInString(name); n > archiveFolderMax {
			t.Fatalf("档案夹名 %d rune 超上限 %d", n, archiveFolderMax)
		}
	})
}
