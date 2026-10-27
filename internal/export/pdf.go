// Package export 用系统 Edge（无头）把渲染好的 HTML 打印为 PDF。
//
// 流程：在 127.0.0.1 随机端口起临时 HTTP 服务（根为工作目录，附
// /assets/ 供给嵌入的 KaTeX）→ chromedp 驱动 Edge 打开 final.html →
// 轮询 window.__ktxDone（KaTeX 渲染完成标志）→ page.printToPDF 落盘。
package export

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"

	"literature-translation/internal/render"
)

// browserOutputBuf 收集无头浏览器的 stdout/stderr。chromedp 的 CombinedOutput
// 会在独立 goroutine 里持续写入（浏览器存活期间不停止），必须加锁才能在
// 错误处理路径安全读取。
type browserOutputBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *browserOutputBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// Tail 取末尾至多 n 个 rune 的累积输出。
func (b *browserOutputBuf) Tail(n int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := []rune(strings.TrimSpace(b.buf.String()))
	if len(r) > n {
		return "…" + string(r[len(r)-n:])
	}
	return string(r)
}

// DebugLog 非空时输出导出过程诊断信息（CLI 注入 logf）。
var DebugLog func(string)

// FindEdge 按顺序探测 Edge 安装路径。
func FindEdge() (string, error) {
	candidates := []string{
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if out, err := exec.Command("reg", "query",
		`HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\msedge.exe`, "/ve").Output(); err == nil {
		if i := strings.Index(string(out), "REG_SZ"); i >= 0 {
			p := strings.TrimSpace(string(out[i+len("REG_SZ"):]))
			p = strings.Trim(p, "\" \r\n")
			if p != "" && strings.Contains(p, "msedge.exe") {
				if _, err := os.Stat(p); err == nil {
					return p, nil
				}
			}
		}
	}
	return "", errors.New("未找到 Microsoft Edge，无法导出 PDF（可安装 Edge 或改用 Chrome）")
}

// PrintPDF 把 htmlContent（排版好的 HTML）打印为 outPDF（绝对路径）。
// 返回实际写入的路径——目标被阅读器占用时自动改用带时间戳的备用名。
// userFontsDir 为用户导入字体目录（空串表示不挂载）。
// workDir 作为 HTTP 根目录，供 HTML 里的图片相对路径引用。
func PrintPDF(ctx context.Context, workDir, htmlContent, outPDF, userFontsDir string) (string, error) {
	edge, err := FindEdge()
	if err != nil {
		return "", err
	}

	// 临时 HTTP 服务：/ = 工作目录（图片相对路径），/assets/ = 嵌入的 KaTeX，
	// /__final = 内存中的排版结果（不再向用户目录写 final.html）
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.Dir(workDir)))
	mux.HandleFunc("/__final", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(htmlContent))
	})
	mux.Handle("/assets/", http.StripPrefix("/assets/",
		http.FileServer(http.FS(render.AssetsSub()))))
	if userFontsDir != "" {
		if _, err := os.Stat(userFontsDir); err == nil {
			mux.Handle("/userfonts/", http.StripPrefix("/userfonts/",
				http.FileServer(http.Dir(userFontsDir))))
		}
	}
	// 访问令牌：服务监听 127.0.0.1 随机端口，但本机其他进程仍可端口扫描
	// 直连。令牌经 chromedp 会话头下发（见下方 SetExtraHTTPHeaders），
	// 未携带令牌的请求一律 404——工作目录与用户字体读不走。
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("生成访问令牌失败: %w", err)
	}
	token := hex.EncodeToString(raw[:])
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Lit-Token") != token {
			http.NotFound(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})}
	go srv.Serve(ln)
	defer srv.Close()
	url := fmt.Sprintf("http://127.0.0.1:%d/__final", ln.Addr().(*net.TCPAddr).Port)

	profileDir, err := os.MkdirTemp("", "literature-edge-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(profileDir)

	var browserOutput browserOutputBuf
	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(edge),
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.UserDataDir(profileDir),
		chromedp.CombinedOutput(&browserOutput),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, allocOpts...)
	defer cancelAlloc()
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()
	// 整个打印限时 300 秒（大文档的字体加载+公式渲染+打印确实耗时，小文档不受影响）
	runCtx, cancelRun := context.WithTimeout(browserCtx, 300*time.Second)
	defer cancelRun()

	var ktxErr, diag, diag2 string
	written := outPDF
	err = chromedp.Run(runCtx,
		// 本会话所有请求带上访问令牌（对应上方 HTTP 服务的校验）
		network.Enable(),
		network.SetExtraHTTPHeaders(network.Headers{"X-Lit-Token": token}),
		chromedp.Navigate(url),
		chromedp.Poll(`window.__ktxDone === true`, nil,
			chromedp.WithPollingTimeout(30*time.Second)),
		chromedp.Evaluate(`window.__ktxErr || ""`, &ktxErr),
		// 打印前由 Go 侧再驱动两轮宽公式适配（字体晚到时布局会变，
		// 页面自身的定时器可能已经停了）；顺带抓取诊断快照
		chromedp.Sleep(300*time.Millisecond),
		chromedp.Evaluate(`(function () { fitWideFormulas(); return JSON.stringify({
			katex: document.querySelectorAll('.katex').length,
			overflow: [...document.querySelectorAll('.ktx-disp')].filter(function (e) { return e.scrollWidth > e.clientWidth + 2; }).length
		}); })()`, &diag),
		chromedp.Sleep(400*time.Millisecond),
		chromedp.Evaluate(`(function () { fitWideFormulas(); return JSON.stringify({
			katex: document.querySelectorAll('.katex').length,
			overflow: [...document.querySelectorAll('.ktx-disp')].filter(function (e) { return e.scrollWidth > e.clientWidth + 2; }).length
		}); })()`, &diag2),
		chromedp.Sleep(300*time.Millisecond),
		chromedp.Navigate(url),
		chromedp.Poll(`window.__ktxDone === true`, nil,
			chromedp.WithPollingTimeout(30*time.Second)),
		chromedp.Evaluate(`window.__ktxErr || ""`, &ktxErr),
		chromedp.ActionFunc(func(ctx context.Context) error {
			if ktxErr != "" {
				return fmt.Errorf("KaTeX 渲染报错: %s", ktxErr)
			}
			if DebugLog != nil {
				DebugLog("公式适配诊断: 首轮=" + diag + " 末轮=" + diag2)
			}
			params := page.PrintToPDFParams{
				PaperWidth:          8.27, // A4 竖版
				PaperHeight:         11.69,
				MarginTop:           0.78,
				MarginBottom:        0.78,
				MarginLeft:          0.70,
				MarginRight:         0.70,
				PrintBackground:     true,
				DisplayHeaderFooter: true,
				HeaderTemplate:      "<div></div>",
				FooterTemplate: `<div style="width:100%;text-align:center;` +
					`font-size:8px;color:#666;">— <span class="pageNumber"></span>` +
					` / <span class="totalPages"></span> —</div>`,
			}
			buf, _, err := params.Do(ctx)
			if err != nil {
				return fmt.Errorf("打印 PDF 失败: %w", err)
			}
			if err := os.MkdirAll(filepath.Dir(outPDF), 0o755); err != nil {
				return err
			}
			if werr := os.WriteFile(outPDF, buf, 0o644); werr != nil {
				// 目标可能被阅读器占用：写入带时间戳的备用名
				written = strings.TrimSuffix(outPDF, ".pdf") +
					time.Now().Format("_150405") + ".pdf"
				if err2 := os.WriteFile(written, buf, 0o644); err2 != nil {
					return fmt.Errorf("写入 PDF 失败：%v（文件可能被阅读器占用，请关闭后重试；备用路径也失败：%v）", werr, err2)
				}
			}
			return nil
		}),
	)
	if err != nil {
		// 打印超时单独说明（避免漏出英文的 context deadline exceeded）
		if errors.Is(err, context.DeadlineExceeded) {
			return written, fmt.Errorf("PDF 打印超时（5 分钟）：文档较大或公式较多时可能发生，可重试")
		}
		// 启动类错误（chrome failed to start）已由 chromedp 自带浏览器输出；
		// 其余错误（轮询超时、打印失败等）的浏览器输出本会被丢弃，这里补上，
		// 否则 Edge 静默失败时无任何诊断线索
		if out := browserOutput.Tail(400); out != "" && !strings.Contains(err.Error(), out) {
			err = fmt.Errorf("%w\n浏览器输出: %s", err, out)
		}
	}
	return written, err
}
