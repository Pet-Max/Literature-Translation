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
	"encoding/json"
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

// FindBrowser 按顺序探测可用于导出的浏览器路径：Edge 优先，Chrome 兜底
// （chromedp 对二者同等驱动；精简系统常见"无 Edge 有 Chrome"）。
// App Paths 注册表覆盖 HKLM 机器级与 HKCU 用户级安装。
func FindBrowser() (string, error) {
	for _, p := range []string{
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
	} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	for _, exe := range []string{`msedge.exe`, `chrome.exe`} {
		for _, root := range []string{`HKLM\SOFTWARE`, `HKCU\SOFTWARE`} {
			out, err := exec.Command("reg", "query",
				root+`\Microsoft\Windows\CurrentVersion\App Paths\`+exe, "/ve").Output()
			if err != nil {
				continue
			}
			if i := strings.Index(string(out), "REG_SZ"); i >= 0 {
				p := strings.TrimSpace(string(out[i+len("REG_SZ"):]))
				p = strings.Trim(p, "\" \r\n")
				if p != "" && strings.Contains(strings.ToLower(p), exe) {
					if _, err := os.Stat(p); err == nil {
						return p, nil
					}
				}
			}
		}
	}
	return "", errors.New("未找到 Microsoft Edge 或 Chrome，无法导出 PDF（任装其一即可）")
}

// PrintPDF 把 htmlContent（排版好的 HTML）打印为 outPDF（绝对路径）。
// 返回实际写入的路径——目标被阅读器占用时自动改用带时间戳的备用名。
// userFontsDir 为用户导入字体目录（空串表示不挂载）。
// workDir 作为 HTTP 根目录，供 HTML 里的图片相对路径引用。
// logf 接收渲染过程的用户可见提示（公式降级报告；nil 表示静默）。
func PrintPDF(ctx context.Context, workDir, htmlContent, outPDF, userFontsDir string, logf func(string)) (string, error) {
	browser, err := FindBrowser()
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
		chromedp.ExecPath(browser),
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		// Edge 157 起实测：stderr 走管道时被应用层全缓冲，DevTools 端点通告
		//（chromedp 靠扫描它启动）延迟不刷出，导致 "chrome failed to start"；
		// 此 flag 强制日志直写 stderr，恢复端点通告的即时可见。
		chromedp.Flag("enable-logging", "stderr"),
		chromedp.UserDataDir(profileDir),
		chromedp.CombinedOutput(&browserOutput),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, allocOpts...)
	defer cancelAlloc()
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()
	// 整个打印限时 9 分钟（大文档的字体加载+公式渲染+打印确实耗时——
	// 606 页书的打印实测需 4-5 分钟，300 秒上限会把合格成品误判为超时）
	runCtx, cancelRun := context.WithTimeout(browserCtx, 540*time.Second)
	defer cancelRun()

	var ktxStatsJSON string
	written := outPDF
	err = chromedp.Run(runCtx,
		// 本会话所有请求带上访问令牌（对应上方 HTTP 服务的校验）
		network.Enable(),
		network.SetExtraHTTPHeaders(network.Headers{"X-Lit-Token": token}),
		chromedp.Navigate(url),
		chromedp.Poll(`window.__ktxDone === true`, nil,
			chromedp.WithPollingTimeout(30*time.Second)),
		// 打印前由 Go 侧再驱动两轮适配（字体晚到时布局会变，页面自身
		// 的定时器可能已经停了）：宽公式 → 宽表格/代码块 → 打印期公式
		// 第二阶段（无下限续缩，见 render.js fitPrintFormulas 注释）
		chromedp.Sleep(300*time.Millisecond),
		chromedp.Evaluate(`fitWideFormulas();fitWideOverflow();fitPrintFormulas()`, nil),
		chromedp.Sleep(400*time.Millisecond),
		chromedp.Evaluate(`fitWideFormulas();fitWideOverflow();fitPrintFormulas()`, nil),
		chromedp.Sleep(300*time.Millisecond),
		// 公式渲染结果整体取回（数量+样例消息）；空串说明页面异常
		chromedp.Evaluate(`window.__renderStats && Array.isArray(window.__renderStats.errors)
			? JSON.stringify({ n: window.__renderStats.errors.length,
				msgs: window.__renderStats.errors.slice(0, 8) })
			: '' `, &ktxStatsJSON),
		chromedp.ActionFunc(func(ctx context.Context) error {
			if ktxStatsJSON == "" {
				return fmt.Errorf("缺少公式渲染结果")
			}
			var ktxStats struct {
				N    int      `json:"n"`
				Msgs []string `json:"msgs"`
			}
			if err := json.Unmarshal([]byte(ktxStatsJSON), &ktxStats); err != nil {
				return fmt.Errorf("解析公式渲染结果失败: %w", err)
			}
			// 降级放行：个别公式解析失败不再否决整个任务——失败公式已在
			// 页面上以红字源码呈现（render.js 的 catch 路径），PDF 照常
			// 产出。此前的一票否决会让一本 500 页的书因十几个坏公式
			// （扫描件上 MinerU 提取损伤很常见）整任务失败且无产物。
			if ktxStats.N > 0 && logf != nil {
				// 双语对照下同一坏公式在英文引用行与中文行各渲染一次、
				// 错误串相同——去重条数才对应「独立坏公式」数
				seen := map[string]bool{}
				uniq := 0
				for _, m := range ktxStats.Msgs {
					if !seen[m] {
						seen[m] = true
						uniq++
						logf(fmt.Sprintf("降级样例 %d/%d: %s", uniq, len(ktxStats.Msgs), m))
					}
				}
				logf(fmt.Sprintf("公式渲染降级 %d 处（样例去重 %d 条），坏公式以红字源码保留在 PDF 中", ktxStats.N, uniq))
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
			return written, fmt.Errorf("PDF 打印超时（9 分钟）：文档较大或公式较多时可能发生，可重试")
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
