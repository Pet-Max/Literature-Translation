package pipeline

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	dirEnPDF   = "01_en_pdf"
	dirEnMD    = "02_en_md"
	dirZhMD    = "03_zh_md"
	dirZhPDF   = "04_zh_pdf"
	dirProcess = "_process"

	archiveFolderMax = 100 // 档案夹名长度上限（防超长路径）
)

// ArchivePlan 一篇文献的归档路径方案（目录均已创建）。
type ArchivePlan struct {
	Dir          string // 文献档案夹（打开文件夹直达这里）
	EnPDFDir     string // 01_en_pdf
	EnMDDir      string // 02_en_md
	ZhMDDir      string // 03_zh_md
	ZhPDFDir     string // 04_zh_pdf
	ProcessDir   string // _process
	MineruDir    string // _process/mineru：MinerU 解包目录 + 渲染 HTTP root
	EnMDPath     string // 02_en_md/<原名>.md
	EnPDFPath    string // 01_en_pdf/<原名>.pdf（仅 PDF 上传时有）
	ZhMDPath     string // 03_zh_md/<原名>_zh.md
	ZhPDFPath    string // 04_zh_pdf/<原名>_zh.pdf
	ProgressPath string // _process/<原名>_zh.md.progress.jsonl
	SourcePath   string // 原始绝对路径
}

// winReserved Windows 保留设备名（CON.md 这类带扩展形式同样保留）。
var winReserved = map[string]struct{}{
	"CON": {}, "PRN": {}, "AUX": {}, "NUL": {},
	"COM1": {}, "COM2": {}, "COM3": {}, "COM4": {}, "COM5": {},
	"COM6": {}, "COM7": {}, "COM8": {}, "COM9": {},
	"LPT1": {}, "LPT2": {}, "LPT3": {}, "LPT4": {}, "LPT5": {},
	"LPT6": {}, "LPT7": {}, "LPT8": {}, "LPT9": {},
}

// isBadArchiveName 目录名不可用：空、全点（. / .. 是相对路径分量，
// 会让档案夹逃出归档根）、或主名是 Windows 保留设备名（目录创建必败）。
func isBadArchiveName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return true
	}
	head := strings.ToUpper(name)
	if i := strings.IndexByte(head, '.'); i >= 0 {
		head = head[:i]
	}
	_, bad := winReserved[head]
	return bad
}

// NewArchivePlan 构建并创建档案夹。root 为归档根（调用方保证非空）。
// 档案夹名 = <原文件名去扩展名>；同名文件夹若来源不同则加 -2/-3 后缀，
// 同源重跑复用同一个档案夹（文件覆盖，幂等）。
func NewArchivePlan(root, sourcePath string) (*ArchivePlan, error) {
	sourcePath, _ = filepath.Abs(sourcePath)
	stem := strings.TrimSuffix(filepath.Base(sourcePath), filepath.Ext(sourcePath))
	stem = sanitizeName(stem) // 归档文件名同样清洗（Windows 保留名/结尾点）
	base := sanitizeName(stem)
	if isBadArchiveName(base) {
		base = "未命名" // 文件名 ...md / CON.md 等极端名落固定档案夹
	}
	if r := []rune(base); len(r) > archiveFolderMax {
		base = string(r[:archiveFolderMax])
	}
	dir := filepath.Join(root, base)
	for i := 2; ; i++ {
		data, err := os.ReadFile(filepath.Join(dir, dirProcess, "source.txt"))
		if err != nil || strings.TrimSpace(string(data)) == sourcePath {
			break // 新文件夹，或同源重跑
		}
		dir = filepath.Join(root, fmt.Sprintf("%s-%d", base, i))
	}

	plan := &ArchivePlan{
		Dir:          dir,
		EnPDFDir:     filepath.Join(dir, dirEnPDF),
		EnMDDir:      filepath.Join(dir, dirEnMD),
		ZhMDDir:      filepath.Join(dir, dirZhMD),
		ZhPDFDir:     filepath.Join(dir, dirZhPDF),
		ProcessDir:   filepath.Join(dir, dirProcess),
		MineruDir:    filepath.Join(dir, dirProcess, "mineru"),
		SourcePath:   sourcePath,
		EnMDPath:     filepath.Join(dir, dirEnMD, stem+".md"),
		ZhMDPath:     filepath.Join(dir, dirZhMD, stem+"_zh.md"),
		ZhPDFPath:    filepath.Join(dir, dirZhPDF, stem+"_zh.pdf"),
		ProgressPath: filepath.Join(dir, dirProcess, stem+"_zh.md.progress.jsonl"),
	}
	if strings.HasSuffix(strings.ToLower(sourcePath), ".pdf") {
		plan.EnPDFPath = filepath.Join(plan.EnPDFDir, filepath.Base(sourcePath))
	}
	for _, d := range []string{plan.Dir, plan.EnPDFDir, plan.EnMDDir, plan.ZhMDDir, plan.ZhPDFDir, plan.ProcessDir, plan.MineruDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("创建归档目录失败: %w", err)
		}
	}
	if err := os.WriteFile(filepath.Join(plan.ProcessDir, "source.txt"),
		[]byte(sourcePath), 0o644); err != nil {
		return nil, fmt.Errorf("写入归档来源失败: %w", err)
	}
	return plan, nil
}

// sanitizeName 归档命名用：替换 Windows 非法字符（\/:*?"<>| 与 0x00-0x1F
// 控制字符）、去掉结尾的点与空格（Windows 目录名禁止），空格保留。
func sanitizeName(s string) string {
	mapped := strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case r < 0x20:
			return '_'
		case strings.ContainsRune(`\/:*?"<>|`, r):
			return '_'
		}
		return r
	}, s)
	return strings.TrimRight(strings.TrimSpace(mapped), ". ")
}

// copyFile 单文件拷贝（覆盖同名）。
func copyFile(src, dst string) error {
	return copyFileContext(context.Background(), src, dst)
}

func copyFileContext(ctx context.Context, src, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sourceInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	if targetInfo, e := os.Stat(dst); e == nil && os.SameFile(sourceInfo, targetInfo) {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.CreateTemp(filepath.Dir(dst), ".copy-*")
	if err != nil {
		return err
	}
	tmp := out.Name()
	defer os.Remove(tmp)
	if _, err = io.Copy(out, contextReader{ctx, in}); err != nil {
		out.Close()
		return err
	}
	if err = out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// contextReader prevents a large local copy from continuing after cancellation.
type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
