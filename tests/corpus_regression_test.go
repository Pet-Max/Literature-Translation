//go:build corpus

package tests

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"literature-translation/internal/pdftext"
	"literature-translation/internal/pipeline"
	"literature-translation/internal/render"
	"literature-translation/internal/repair"
)

func externalCorpusRoot(t *testing.T) string {
	t.Helper()
	root := strings.TrimSpace(os.Getenv("LIT_CORPUS_DIR"))
	if root == "" {
		t.Skip("未设置 LIT_CORPUS_DIR，跳过外部只读样本回归")
	}
	if _, err := os.Stat(root); err != nil {
		t.Skipf("外部样本目录不可访问 (%v)，跳过", err)
	}
	return root
}

// TestExternalCorpusLangDetect 遍历 LIT_CORPUS_DIR 下的归档目录结构，验证
// 02_en_md/*.md 与 03_zh_md/*_zh.md 的语言自动判定结果。全程只读。
func TestExternalCorpusLangDetect(t *testing.T) {
	root := externalCorpusRoot(t)
	checked := 0
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, werr error) error {
		if werr != nil || d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return werr
		}
		parent := filepath.Base(filepath.Dir(p))
		lower := strings.ToLower(d.Name())
		switch {
		case parent == "02_en_md" && !strings.HasSuffix(lower, "_zh.md"):
			checked++
			if got := pipeline.FileIsChinese(p); got {
				t.Errorf("%s: FileIsChinese=true want false", p)
			}
		case parent == "03_zh_md" && strings.HasSuffix(lower, "_zh.md"):
			checked++
			if got := pipeline.FileIsChinese(p); !got {
				t.Errorf("%s: FileIsChinese=false want true", p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历样本目录失败: %v", err)
	}
	if checked == 0 {
		t.Skip("未发现按 02_en_md / 03_zh_md 组织的 Markdown 样本")
	}
}

// TestExternalCorpusInvariants 对外部归档目录中的所有文档执行全流程只读不变量检查：
//   - Calibrate 严格幂等且零 NUL 泄漏；
//   - NormalizeStructure + render.BuildWithStats 无原始 HTML 静默丢失、无内部占位符泄漏、渲染无平方级耗时退化。
func TestExternalCorpusInvariants(t *testing.T) {
	root := externalCorpusRoot(t)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("读取样本根目录失败: %v", err)
	}
	both := repair.Options{Spelling: true, Symbols: true}

	assertIdempotentNoNUL := func(label, md, orig string) {
		t.Helper()
		res1, err := repair.Calibrate(md, orig, both)
		if err != nil {
			t.Fatalf("%s Calibrate Pass1 err: %v", label, err)
		}
		if strings.ContainsRune(res1.Text, 0) {
			t.Fatalf("%s Calibrate Pass1 泄漏 NUL (U+0000)", label)
		}
		res2, err := repair.Calibrate(res1.Text, orig, both)
		if err != nil {
			t.Fatalf("%s Calibrate Pass2 err: %v", label, err)
		}
		if res2.Text != res1.Text {
			t.Fatalf("%s Calibrate 非幂等（Pass2 != Pass1）: Pass2 stats=%+v", label, res2.Stats)
		}
	}

	tested := 0
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		docDir := filepath.Join(root, ent.Name())
		pdfs, _ := filepath.Glob(filepath.Join(docDir, "01_en_pdf", "*.pdf"))
		if len(pdfs) > 0 {
			orig, _ := pdftext.ExtractText(pdfs[0])
			var mdFiles []string
			enMDs, _ := filepath.Glob(filepath.Join(docDir, "02_en_md", "*.md"))
			mdFiles = append(mdFiles, enMDs...)
			rawMDs, _ := filepath.Glob(filepath.Join(docDir, "_process", "mineru", "result-*", "full.raw.md"))
			mdFiles = append(mdFiles, rawMDs...)
			sort.Strings(mdFiles)
			for _, m := range mdFiles {
				if strings.HasSuffix(strings.ToLower(m), ".bak") {
					continue
				}
				mdB, rerr := os.ReadFile(m)
				if rerr != nil {
					continue
				}
				tested++
				assertIdempotentNoNUL(m, string(mdB), orig)
			}
			// 若存在分片原始产物，按序拼接测一次（优先取 full.raw.md，避免对每片重复解析整书文本层）
			partDirs, _ := filepath.Glob(filepath.Join(docDir, "_process", "mineru", "parts", "part-*", "result-*"))
			sort.Strings(partDirs)
			if len(partDirs) > 0 {
				var sb strings.Builder
				for _, pd := range partDirs {
					p := filepath.Join(pd, "full.raw.md")
					if _, serr := os.Stat(p); serr != nil {
						p = filepath.Join(pd, "full.md")
					}
					b, rerr := os.ReadFile(p)
					if rerr != nil {
						continue
					}
					if sb.Len() > 0 {
						sb.WriteString("\n\n")
					}
					sb.WriteString(strings.TrimSpace(string(b)))
				}
				if sb.Len() > 0 {
					tested++
					assertIdempotentNoNUL(filepath.Join(docDir, "_process", "mineru", "parts"), sb.String(), orig)
				}
			}
		}

		zhMDs, _ := filepath.Glob(filepath.Join(docDir, "03_zh_md", "*_zh.md"))
		for _, zm := range zhMDs {
			zhB, rerr := os.ReadFile(zm)
			if rerr != nil {
				continue
			}
			tested++
			normZH, _ := repair.NormalizeStructure(string(zhB))
			start := time.Now()
			htmlDoc, st := render.BuildWithStats(normZH, render.Options{Title: ent.Name(), TOC: true})
			elapsed := time.Since(start)
			t.Logf("%s BuildWithStats 耗时: %v (texfix=%d, rawOmitted=%d)", ent.Name(), elapsed.Round(time.Millisecond), st.Affected, st.RawHTMLOmitted)
			if elapsed > 6*time.Second {
				t.Errorf("%s BuildWithStats 耗时 %v 超过 6s 性能门禁", zm, elapsed)
			}
			if st.RawHTMLOmitted != 0 {
				t.Errorf("%s RawHTMLOmitted = %d, want 0", zm, st.RawHTMLOmitted)
			}
			if strings.Contains(htmlDoc, "⟦M") {
				t.Errorf("%s 输出泄漏内部占位符", zm)
			}
		}
	}
	if tested == 0 {
		t.Skip("样本目录中未发现可测文档")
	}
}
