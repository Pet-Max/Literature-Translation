// Package render 把译文 Markdown 组装成学术排版的 HTML（KaTeX 渲染公式）。
//
// 数学公式（$…$ / $$…$$）在交给 goldmark 之前先替换为 ⟦Mn⟧ 占位符，
// 防止 Markdown 解析器把公式里的 *、_、& 等符号当作语法；HTML 生成后
// 再以 KaTeX 定界符 \( … \) / \[ … \] 原样回填（内容做 HTML 转义，
// 浏览器解码后 KaTeX 收到原始 LaTeX）。
package render

import (
	"embed"
	"fmt"
	"html"
	"io/fs"
	"net/http"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
)

//go:embed assets
var Assets embed.FS

// AssetsSub 返回 assets 子文件系统（导出模块挂到 /assets/ 路由）。
func AssetsSub() fs.FS {
	sub, err := fs.Sub(Assets, "assets")
	if err != nil {
		panic(err) // 编译期已保证存在
	}
	return sub
}

// Options 控制最终 HTML 组装。
type Options struct {
	Title       string // 文档标题（取文件名）
	FontCSS     string // 正文 font-family 声明
	UserFontCSS string // 用户导入字体的 @font-face 片段（空=无）
	TOC         bool   // 是否生成目录
	TwoColumn   bool   // 双栏排版
}

// Build 把 Markdown 转为完整可打印的 HTML 文档（不含 KaTeX 脚本挂载）。
func Build(md string, opt Options) string {
	// 封面标题优先取 MD 自身的一级标题（译文的真实标题），
	// 并从正文移除避免与 doc-title 重复；没有一级标题才退回文件名。
	title := strings.TrimSpace(opt.Title)
	if m := firstH1Re.FindStringSubmatchIndex(md); m != nil {
		if t := strings.TrimSpace(md[m[2]:m[3]]); t != "" {
			title = t
			md = strings.TrimLeft(md[m[1]:], "\n")
		}
	}
	body := markdownToHTML(md)
	toc := ""
	if opt.TOC {
		toc = buildTOC(body)
	}

	twoCol := ""
	if opt.TwoColumn {
		twoCol = ".content{columns:2;column-gap:2.2em}"
	}
	font := opt.FontCSS
	if strings.TrimSpace(font) == "" {
		font = `"Microsoft YaHei", sans-serif`
	}
	title = html.EscapeString(title)

	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html lang=\"zh-CN\">\n<head>\n<meta charset=\"utf-8\">\n")
	// CSP：只放行本地资源与内联样式（<style> 块与 KaTeX 的行内字号）。
	// 脚本一律外链自 /assets，源 Markdown 的原始 HTML 已被 goldmark
	// 默认转义——即使译文混入恶意标签也只剩纯文本。
	b.WriteString(`<meta http-equiv="Content-Security-Policy" content="default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'">` + "\n")
	b.WriteString("<title>" + title + "</title>\n")
	b.WriteString(`<link rel="stylesheet" href="/assets/katex/katex.min.css">` + "\n")
	// 霞鹜文楷随软件内嵌（unicode-range 切片按需加载）：
	// 任何机器上正文/标题都命中 "LXGW WenKai"，不再回退到系统字体
	b.WriteString(`<link rel="stylesheet" href="/assets/lxgw/lxgwwenkai-regular.css">` + "\n")
	b.WriteString(`<link rel="stylesheet" href="/assets/lxgw/lxgwwenkai-bold.css">` + "\n")
	// 公式横线减细：Edge 打印把 KaTeX 的 0.75pt 横线取整得偏粗，视觉上
	// 明显重于 LaTeX 标准（0.476pt）。scaleY(0.6) 实测压到 ~0.45pt
	// 该样式仅调整横线视觉厚度。
	b.WriteString("<style>\n" + css + "\n" + twoCol +
		"\n.mfrac .frac-line,.overline .overline-line{transform:scaleY(0.6);transform-origin:bottom}" +
		"\nbody{font-family:" + font + "}\n" + opt.UserFontCSS + "\n</style>\n")
	b.WriteString("</head>\n<body>\n")
	b.WriteString("<h1 class=\"doc-title\">" + title + "</h1>\n")
	if toc != "" {
		b.WriteString("<nav class=\"toc\"><div class=\"toc-head\">目录</div>\n" + toc + "</nav>\n")
	}
	b.WriteString("<article class=\"content\">\n" + body + "\n</article>\n")
	b.WriteString(scriptTag)
	b.WriteString("</body>\n</html>\n")
	return b.String()
}

// 渲染占位符使用 U+27E6/U+27E7，编号空间独立于翻译引擎。
var (
	fenceRe   = regexp.MustCompile("(?ms)^(?:```|~~~)[^\n]*\n.*?^(?:```|~~~)")
	dispRe    = regexp.MustCompile(`(?s)\$\$.+?\$\$`)
	escDollar = strings.NewReplacer(`\$`, "\x00ESC\x00")
	unescape  = strings.NewReplacer("\x00ESC\x00", `\$`)
)

// 上下标（MinerU 用行内 HTML 表达：作者角标 <sup>2</sup>、化学式
// <sub>2</sub>）。goldmark 默认把原始 HTML 换成 <!-- raw HTML omitted -->
// 注释，标签剥掉后 "x²" 退化成 "x2"——与公式同构的冻结-回填通道放行
// 这两个标签，回填时内容整体转义，不放宽 Unsafe（见 restoreMath）。
var (
	supRe = regexp.MustCompile(`<sup>(.*?)</sup>`)
	subRe = regexp.MustCompile(`<sub>(.*?)</sub>`)
)

// boldCmdRe 把 \pmb / \bm（KaTeX 里是微弱的 text-shadow 假粗体）统一改写为
// \boldsymbol（真粗体数学字形）。向量粗体承载语义（向量 vs 标量），
// 假粗体在打印 PDF 里几乎不可见，等于把语义弄丢。
var boldCmdRe = regexp.MustCompile(`\\(?:pmb|bm)\b`)

// nestedMathDelimRe 剥离公式内嵌套的 \( \)：MinerU 偶发把定界符写进
// \tag 参数（如 \tag{\(\sigma_{11}(t)\)}），KaTeX 解析不了会让整条
// 公式变红字/裸文本；在已是数学模式的上下文里 \( \) 本就无意义。
var nestedMathDelimRe = regexp.MustCompile(`\\[()]`)

// markdownToHTML：冻结数学/代码/上下标 → goldmark → 回填（KaTeX 定界符）。
func markdownToHTML(md string) string {
	freezeMap := map[string]string{}
	n := 0
	freezeSpan := func(s string) string {
		var key string
		for {
			n++
			key = fmt.Sprintf("⟦M%d⟧", n)
			if !strings.Contains(md, key) {
				break
			}
		}
		freezeMap[key] = s
		return key
	}
	add := func(s string) string {
		for key := range freezeMap {
			if strings.Contains(s, key) {
				return s
			}
		}
		return freezeSpan(s)
	}

	// \$ 不是定界符，临时替换避免干扰匹配
	text := escDollar.Replace(md)
	// 冻结顺序：围栏代码 → 行间公式 → 行内公式 → 上下标（先长后短）。
	// 已含占位符的匹配不再冻结（如 $$…$$ 夹住代码围栏时，那不是公式，
	// 嵌套冻结曾让回填顺序错乱、占位符以字面量泄漏进正文）。
	// 上下标冻结不能用 add：内含公式的 <sub>$…$</sub> 已持有占位符，
	// 跳过会让 goldmark 剥掉标签；此处允许嵌套，回填先外后内自然解开。
	text = fenceRe.ReplaceAllStringFunc(text, add)
	text = dispRe.ReplaceAllStringFunc(text, add)
	text = regexp.MustCompile(`\$[^$\n]+?\$`).ReplaceAllStringFunc(text, add)
	text = supRe.ReplaceAllStringFunc(text, freezeSpan)
	text = subRe.ReplaceAllStringFunc(text, freezeSpan)
	text = unescape.Replace(text)

	gm := goldmark.New(
		goldmark.WithExtensions(extension.Table),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	)
	// 不开 gmhtml.WithUnsafe()：默认配置会把源 Markdown 里的原始 HTML
	// （<script>、<img onerror> 等）转义成纯文本。数学/代码回填发生在
	// 转换之后、由本包自己拼接可信 HTML，不依赖 raw pass-through。
	var buf strings.Builder
	if err := gm.Convert([]byte(text), &buf); err != nil {
		return "<pre>" + html.EscapeString(md) + "</pre>"
	}
	out := buf.String()

	// 外层后冻结，按编号逆序恢复；不重新扫描恢复出的原文字面占位符。
	// 编号在创建时避开原文，因此每个键只会展开一次，循环有明确上界。
	for i := n; i > 0; i-- {
		key := fmt.Sprintf("⟦M%d⟧", i)
		if raw, ok := freezeMap[key]; ok {
			out = strings.ReplaceAll(out, key, restoreMath(key, raw))
		}
	}
	return out
}

// restoreMath 把单个冻结区段还原为 HTML（代码块/ KaTeX 占位 span/上下标）。
// 入口先还原冻结前转义的 \$：冻结发生在 \$→占位符替换之后，冻结区捕获
// 的内容里残留 NUL+"ESC"+NUL 标记，不还原会以乱码进导出 HTML。
func restoreMath(key, raw string) string {
	raw = unescape.Replace(raw)
	if strings.HasPrefix(raw, "```") || strings.HasPrefix(raw, "~~~") {
		langAndCode := strings.TrimPrefix(strings.TrimPrefix(raw, "```"), "~~~")
		nl := strings.IndexByte(langAndCode, '\n')
		code := langAndCode
		if nl >= 0 {
			code = langAndCode[nl+1:]
		}
		// fenceRe 的匹配包含闭合围栏行，剥掉（否则代码块末尾显示 ```）
		for _, closer := range []string{"\n```", "\n~~~", "```", "~~~"} {
			code = strings.TrimSuffix(code, closer)
			code = strings.TrimRight(code, " \t")
		}
		return "<pre><code>" + html.EscapeString(strings.TrimSuffix(code, "\n")) + "</code></pre>"
	}
	// 上下标白名单回填：只放行标签名本身，内容整体 HTML 转义——
	// 源 MD 混入的 <script> 等只会以纯文本出现，CSP/安全姿态不变。
	// 内容里可能还嵌着内层公式的占位符（⟦Mn⟧ 不在转义范围），外层
	// 先还原、内层数学由恢复循环的下一轮处理。
	if strings.HasPrefix(raw, "<sup>") {
		return "<sup>" + html.EscapeString(strings.TrimSuffix(strings.TrimPrefix(raw, "<sup>"), "</sup>")) + "</sup>"
	}
	if strings.HasPrefix(raw, "<sub>") {
		return "<sub>" + html.EscapeString(strings.TrimSuffix(strings.TrimPrefix(raw, "<sub>"), "</sub>")) + "</sub>"
	}
	if strings.HasPrefix(raw, "$$") {
		tex := strings.TrimSuffix(strings.TrimPrefix(raw, "$$"), "$$")
		return `<span class="ktx-disp" data-tex="` + html.EscapeString(nestedMathDelimRe.ReplaceAllString(boldCmdRe.ReplaceAllString(tex, `\boldsymbol`), "")) + `"></span>`
	}
	tex := strings.TrimPrefix(raw, "$")
	tex = strings.TrimSuffix(tex, "$")
	return `<span class="ktx" data-tex="` + html.EscapeString(nestedMathDelimRe.ReplaceAllString(boldCmdRe.ReplaceAllString(tex, `\boldsymbol`), "")) + `"></span>`
}

var headingRe = regexp.MustCompile(`<h([1-3]) id="([^"]*)"[^>]*>(.*?)</h[1-3]>`)

// firstH1Re 匹配 MD 开头（允许前导空行）的一级标题。
var firstH1Re = regexp.MustCompile(`\A\s*#\s+(.+)`)

// buildTOC 从生成的 HTML 里抓 h1-h3 构建目录（缩进按层级）。
func buildTOC(body string) string {
	var b strings.Builder
	b.WriteString("<ul>\n")
	for _, m := range headingRe.FindAllStringSubmatch(body, -1) {
		level := int(m[1][0] - '0')
		id, text := m[2], m[3]
		indent := ""
		switch level {
		case 1:
			indent = ` class="lv1"`
		case 2:
			indent = ` class="lv2"`
		default:
			indent = ` class="lv3"`
		}
		b.WriteString(`<li` + indent + `><a href="#` + id + `">` + text + `</a></li>` + "\n")
	}
	b.WriteString("</ul>\n")
	return b.String()
}

// KaTeX 挂载脚本：全部外链（katex.min.js + render.js），渲染完成后
// 置 __ktxDone 供导出模块轮询。逻辑见 assets/katex/render.js。
const scriptTag = `<script src="/assets/katex/katex.min.js"></script>
<script src="/assets/katex/render.js"></script>
`

const css = `
:root { --ink:#1e2320; --ink2:#555b55; --line:#d8dad2; --teal:#2E5A57; }
html { font-size:15px; }
body {
  color:var(--ink); line-height:1.75; margin:0;
}
.doc-title {
  font-size:1.55em; text-align:center; margin:0 0 .4em;
  letter-spacing:.02em;
}
.toc {
  border:1px solid var(--line); border-radius:6px; background:#fafaf7;
  padding:.7em 1.2em .8em; margin:0 auto 1.8em; max-width:44em;
}
.toc-head { font-weight:600; margin-bottom:.3em; color:var(--teal); }
.toc ul { list-style:none; margin:.2em 0; padding-left:0; }
.toc li { margin:.14em 0; }
.toc li.lv2 { padding-left:1.3em; }
.toc li.lv3 { padding-left:2.6em; font-size:.95em; color:var(--ink2); }
.toc a { color:inherit; text-decoration:none; }
.toc a:hover { color:var(--teal); text-decoration:underline; }
/* 44em ≈ A4 去页边距后的可排宽度（6.87in×96dpi/15px），屏幕与打印同宽，
   公式的宽度自适应结果在 printToPDF 重排版后依然成立 */
.content { max-width:44em; margin:0 auto; }
h1, h2, h3, h4, h5, h6 { line-height:1.35; margin:1.5em 0 .55em; }
h1, h2, h3, h4, h5, h6, strong, b { font-weight:700; }
.doc-title { font-weight:700; }
h1 { font-size:1.35em; } h2 { font-size:1.2em; } h3 { font-size:1.08em; }
p { margin:.55em 0; text-align:justify; }
blockquote {
  margin:.6em 0; padding:.35em 1em; border-left:3px solid var(--line);
  color:var(--ink2); background:#fafaf7; border-radius:0 4px 4px 0;
  font-size:.94em;
}
blockquote p { margin:.25em 0; }
img { max-width:100%; display:block; margin:1em auto; }
table { border-collapse:collapse; margin:1em auto; font-size:.95em; }
th, td { border:1px solid var(--line); padding:.4em .8em; }
th { background:#f1f2ec; }
pre {
  background:#f4f4ef; border:1px solid var(--line); border-radius:5px;
  padding:.7em 1em; overflow-x:auto; font-size:.88em;
}
code { font-family:Consolas, monospace; }
pre code { padding:0; background:none; }
/* 上标不撑大行距：作者角标密集的标题区，默认 sup 会把该行 line-height
   拉高，打印后与邻行疏密不均 */
sup, sub { line-height:0; }
.ktx { white-space:nowrap; }
.ktx-disp {
  display:block; text-align:center; margin:.7em 0;
  overflow-x:auto; overflow-y:hidden;
}
.katex { font-size:1.06em; }
.formula-error { color:#b42318; white-space:pre-wrap; }
.katex-display { margin:0; }
a { color:var(--teal); }
hr { border:none; border-top:1px solid var(--line); margin:1.6em 0; }
`

// AssetHandler 把嵌入的 katex/lxgw 资产以 /assets/ 前缀暴露，
// 供 GUI 预览与导出共用同一份字体与样式（同源预览）。
func AssetHandler() http.Handler {
	return http.StripPrefix("/assets/", http.FileServer(http.FS(AssetsSub())))
}
