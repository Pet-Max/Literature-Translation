package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenPathArgsHandlesDotNamedDirs(t *testing.T) {
	root := t.TempDir()
	dotDir := filepath.Join(root, "Sample Book (v1.0, ed.2)")
	if err := os.MkdirAll(dotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "sample_doc_zh.pdf")
	if err := os.WriteFile(file, []byte("pdf"), 0o644); err != nil {
		t.Fatal(err)
	}

	dirArgs, err := openPathArgs(dotDir)
	if err != nil || len(dirArgs) != 1 || dirArgs[0] != dotDir {
		t.Fatalf("带点号目录应直接打开文件夹而非 /select,: got=%v err=%v", dirArgs, err)
	}
	fileArgs, err := openPathArgs(file)
	if err != nil || len(fileArgs) != 2 || fileArgs[0] != "/select," || fileArgs[1] != file {
		t.Fatalf("文件应使用 /select,: got=%v err=%v", fileArgs, err)
	}
}
