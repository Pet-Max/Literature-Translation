// printchunks.go — 分段打印回退：整本文档在部分机器状态下整本直出会
// 稳定失败（打印生成阶段崩溃 Chromium -32000，或直出超时），而同一内容
// 切成分段各自渲染打印实测稳定（920 页书切 4 段、每段数十秒出片）。
// 本文件按段落空行边界切分译文，逐段渲染打印，最后 pdfcpu 合并为单个
// PDF；整本目录注入首段。触发条件与预算见 pipeline.export 与 pdf.go。
package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/api"

	"literature-translation/internal/config"
	"literature-translation/internal/export"
	"literature-translation/internal/pdftext"
	"literature-translation/internal/render"
)

const (
	// 文档低于此体积不值得分段（≈150 页以下，直出成功率足够高，
	// 失败多为确定性错误，分段也救不了）
	chunkMinBytes = 512 << 10
	// 单段目标体积：920 页书 ≈3.2MB 切 4 段（每段约 230 页），
	// 每段完整流程数十秒实测稳定
	chunkTargetBytes = 800 << 10
	// 段数上限（预算守护：15 分钟预算 = 直出 6 分钟 + 分段余量）
	chunkMaxCount = 12
)

// printWithRetry 浏览器启动类瞬时故障（Edge 后台更新换版、被安全软件
// 瞬时拦截、内存吃紧等）过几秒重开一次通常就能恢复，自动重试；其余
// 错误（KaTeX/打印/写盘类）是确定性的，不重试。打印本身幂等。
func (p *Pipeline) printWithRetry(ctx context.Context, dir, html, out, fontsDir string) (string, error) {
	for attempt := 1; ; attempt++ {
		got, err := export.PrintPDF(ctx, dir, html, out, fontsDir,
			func(msg string) { p.warnf("%s", msg) })
		if err == nil || attempt >= 3 || !isBrowserLaunchErr(err) {
			return got, err
		}
		p.logf("浏览器未起来（第 %d 次）：%v —— 5 秒后重试", attempt, err)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

// splitMarkdownChunks 按段落空行边界（"\n\n"）把 md 切成若干段：切点
// 不拦腰截断段落/表格/公式块；各段依序拼接等于原文（内容无损）。
// 低于单段目标体积原样返回单段。
func splitMarkdownChunks(md string, target int, maxN int) []string {
	if len(md) <= target {
		return []string{md}
	}
	n := (len(md) + target - 1) / target
	if n > maxN {
		n = maxN
	}
	if n < 2 {
		n = 2
	}
	size := (len(md) + n - 1) / n
	var chunks []string
	last := 0
	for last < len(md) {
		cut := last + size
		if cut >= len(md) {
			chunks = append(chunks, md[last:])
			break
		}
		// 优先回退到窗口内最近的段落边界；窗口起点恰为边界或窗口内
		// 无边界时向前找（避免产生空段或死循环）
		at := strings.LastIndex(md[last:cut], "\n\n")
		if at <= 0 {
			fwd := strings.Index(md[cut:], "\n\n")
			if fwd < 0 {
				chunks = append(chunks, md[last:])
				break
			}
			cut += fwd
		} else {
			cut = last + at
		}
		chunks = append(chunks, md[last:cut])
		last = cut
	}
	return chunks
}

// chunkedExport 分段打印合并：逐段渲染打印（首段注入整本目录），pdfcpu
// 合并为单个 PDF。任一段失败即整体失败——不产出残缺成品。页脚页码按
// 分段各自计数，与整本直出的连续页码无法一致，属可用的降级折中。
func (p *Pipeline) chunkedExport(ctx context.Context, mdBytes []byte, t zhTarget, outPDF string,
	cfg *config.Config, useTOC, useTwo bool, fontsDir, fullHTML string) (string, error) {
	chunks := splitMarkdownChunks(string(mdBytes), chunkTargetBytes, chunkMaxCount)
	p.logf("分段打印：%d 段（单段目标约 %d KB），逐段渲染打印…", len(chunks), chunkTargetBytes>>10)
	tmpDir, err := os.MkdirTemp("", "lit-chunks-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)

	var parts []string
	for i, part := range chunks {
		doc, _ := render.BuildWithStats(part, render.Options{
			Title:       t.Title,
			FontCSS:     fontStack(cfg),
			UserFontCSS: userFontCSS(cfg),
			TOC:         useTOC && i == 0,
			TwoColumn:   useTwo,
		})
		if i == 0 && useTOC {
			doc = injectFullTOC(fullHTML, doc)
		}
		out := filepath.Join(tmpDir, fmt.Sprintf("chunk-%03d.pdf", i))
		got, err := p.printWithRetry(ctx, t.Dir, doc, out, fontsDir)
		if err != nil {
			return "", fmt.Errorf("分段打印第 %d/%d 段失败: %w", i+1, len(chunks), err)
		}
		parts = append(parts, got)
	}
	// 合并到成品同目录的临时文件，成功后原子改名——合并中途失败不在
	// 成品路径留半成品；目标被占用（可能被阅读器打开）时另存时间戳名，
	// 对齐 PrintPDF 的占用降级行为
	tmpMerged, err := os.CreateTemp(filepath.Dir(outPDF), ".merge-*.pdf")
	if err != nil {
		return "", err
	}
	tmpPath := tmpMerged.Name()
	tmpMerged.Close()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := api.MergeCreateFile(ctx, parts, tmpPath, false, nil); err != nil {
		return "", fmt.Errorf("分段合并失败: %w", err)
	}
	if err := os.Rename(tmpPath, outPDF); err != nil {
		alt := strings.TrimSuffix(outPDF, ".pdf") + time.Now().Format("_150405") + ".pdf"
		if err2 := os.Rename(tmpPath, alt); err2 != nil {
			return "", fmt.Errorf("分段合并写入失败: %w", err)
		}
		p.warnf("原目标文件被占用（可能正被阅读器打开），已另存为新文件")
		outPDF = alt
	}
	if n, err := pdftext.PageCount(outPDF); err == nil {
		p.logf("分段打印合并完成：%d 段 → %d 页", len(parts), n)
	}
	return outPDF, nil
}

// injectFullTOC 把整本渲染的目录块（含全部章节条目）替换到分段渲染的
// 首段 HTML——首段自身的目录只覆盖首段内容。锚点跨段跳转在打印里本就
// 失效，目录条目仅作翻阅定位。
func injectFullTOC(fullHTML, chunkHTML string) string {
	full := extractTOC(fullHTML)
	if full == "" {
		return chunkHTML
	}
	cs := strings.Index(chunkHTML, `<nav class="toc">`)
	if cs < 0 {
		return chunkHTML
	}
	ce := strings.Index(chunkHTML[cs:], "</nav>")
	if ce < 0 {
		return chunkHTML
	}
	return chunkHTML[:cs] + full + chunkHTML[cs+ce+len("</nav>"):]
}

// extractTOC 抓取渲染 HTML 里的目录 nav 块（<nav class="toc">…</nav>）。
func extractTOC(html string) string {
	start := strings.Index(html, `<nav class="toc">`)
	if start < 0 {
		return ""
	}
	off := strings.Index(html[start:], "</nav>")
	if off < 0 {
		return ""
	}
	return html[start : start+off+len("</nav>")]
}
