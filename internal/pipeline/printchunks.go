// printchunks.go — 分段打印回退：超大文档整本直出触发 Chromium 打印规模
// 失败或超时，按段落空行边界切分译文逐段渲染打印，再合并为单个 PDF；
// 整本目录注入首段。
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
	// 分段触发阈值与单段目标体积统一为 512 KiB，避免阈值错位造成的触发死区。
	chunkTargetBytes = 512 << 10
	// 段数上限（控制整轮分段导出总时长）
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
// 避开围栏代码块内部，且不拦腰截断段落/表格/公式块；动态按剩余体积均分，
// 最后一段吸收余量（不留数 KB 孤立尾段、不超 maxN）；各段依序拼接等于原文。
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
	fences := markdownFenceRanges(md)
	inFence := func(pos int) bool {
		for _, r := range fences {
			if pos >= r[0] && pos <= r[1] {
				return true
			}
			if r[0] > pos {
				break
			}
		}
		return false
	}
	isSafeCut := func(cut int) bool {
		return cut > 0 && cut+2 <= len(md) && md[cut-1] != '\n' && !inFence(cut)
	}

	var chunks []string
	last := 0
	for rem := n; rem > 0 && last < len(md); rem-- {
		if rem == 1 {
			chunks = append(chunks, md[last:])
			break
		}
		size := (len(md) - last + rem - 1) / rem
		ideal := last + size
		if ideal >= len(md) {
			chunks = append(chunks, md[last:])
			break
		}
		chosen := -1
		// 在 (last, ideal] 内向前回退找最近的安全段落边界
		searchTo := ideal
		for searchTo > last {
			idx := strings.LastIndex(md[last:searchTo], "\n\n")
			if idx <= 0 {
				break
			}
			cand := last + idx
			if isSafeCut(cand) {
				chosen = cand
				break
			}
			searchTo = cand
		}
		// 窗口内无安全边界则向后找
		if chosen < 0 {
			pos := ideal
			for pos < len(md) {
				fwd := strings.Index(md[pos:], "\n\n")
				if fwd < 0 {
					break
				}
				cand := pos + fwd
				if cand > last && isSafeCut(cand) {
					chosen = cand
					break
				}
				pos = cand + 2
			}
		}
		if chosen < 0 || chosen <= last {
			chunks = append(chunks, md[last:])
			break
		}
		chunks = append(chunks, md[last:chosen])
		last = chosen
	}
	return chunks
}

// markdownFenceRanges 扫描 Markdown 围栏代码块（``` 或 ~~~）的起止字节区间。
func markdownFenceRanges(md string) [][2]int {
	var ranges [][2]int
	open := -1
	mark := ""
	pos := 0
	for pos <= len(md) {
		end := strings.IndexByte(md[pos:], '\n')
		lineEnd := len(md)
		next := len(md) + 1
		if end >= 0 {
			lineEnd = pos + end
			next = lineEnd + 1
		}
		trimmed := strings.TrimSpace(md[pos:lineEnd])
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			m := trimmed[:3]
			if open < 0 {
				rest := strings.TrimLeft(trimmed, m[:1])
				validOpen := !((m == "```" && strings.Contains(rest, "`")) ||
					(m == "~~~" && strings.Contains(rest, "~~~")))
				if validOpen {
					open = pos
					mark = m
				}
			} else if m == mark && strings.Trim(trimmed, m[:1]) == "" {
				ranges = append(ranges, [2]int{open, lineEnd})
				open = -1
				mark = ""
			}
		}
		pos = next
	}
	if open >= 0 {
		ranges = append(ranges, [2]int{open, len(md)})
	}
	return ranges
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
		title := ""
		if i == 0 {
			title = t.Title
		}
		doc, _ := render.BuildWithStats(part, render.Options{
			Title:        title,
			NoCoverTitle: i > 0,
			FontCSS:      fontStack(cfg),
			UserFontCSS:  userFontCSS(cfg),
			EnBg:         cfg.Export.EnBg,
			ZhBg:         cfg.Export.ZhBg,
			TOC:          useTOC && i == 0,
			TwoColumn:    useTwo,
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
// 首段 HTML；若首段因无标题未生成 <nav class="toc">，则直接插入正文前。
// 锚点跨段跳转在打印里本就失效，目录条目仅作翻阅定位。
func injectFullTOC(fullHTML, chunkHTML string) string {
	full := extractTOC(fullHTML)
	if full == "" {
		return chunkHTML
	}
	cs := strings.Index(chunkHTML, `<nav class="toc">`)
	if cs < 0 {
		const marker = `<article class="content">`
		if art := strings.Index(chunkHTML, marker); art >= 0 {
			return chunkHTML[:art] + full + "\n" + chunkHTML[art:]
		}
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
