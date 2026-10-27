// literature-translation — Literature Translation 命令行版（GUI 与本 CLI 共用 internal/pipeline 核心编排）。
//
//	literature-translation check              预检：配置 / Python 引擎 / Edge
//	literature-translation run paper.pdf      全流程：解析 → 校准 → 翻译 → 导出中文 PDF
//	literature-translation translate doc.md   翻译已有 MD 并导出（跳过解析）
//	literature-translation print doc.md       本地排版导出 PDF（不翻译）
//	literature-translation font add|list|use|remove  字体管理
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"literature-translation/internal/config"
	"literature-translation/internal/fonts"
	"literature-translation/internal/pipeline"
	"literature-translation/internal/version"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "check":
		err = checkCmd()
	case "print":
		err = printCmd(os.Args[2:])
	case "run":
		err = runCmd(os.Args[2:])
	case "translate":
		err = translateCmd(os.Args[2:])
	case "font":
		err = fontCmd(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n✗ %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `Literature Translation %s（CLI）

用法:
  literature-translation check                预检配置与环境
  literature-translation run <paper.pdf>      全流程：解析 → 校准 → 翻译 → 导出
  literature-translation translate <doc.md>   翻译已有 MD 并导出（跳过解析）
  literature-translation print <doc.md>       仅排版导出 PDF（不翻译）
  literature-translation font add|list|use|remove  字体管理

配置文件: %%APPDATA%%\Literature Translation\config.json
`, version.Version)
}

// newPipeline 构建 CLI 回调（终端输出）的编排器。
func newPipeline(cfg *config.Config) (*pipeline.Pipeline, error) {
	return pipeline.New(cfg, pipeline.Callbacks{
		Log: cliLog,
		Progress: func(done, total, failed int, doneIdx, failedIdx []int, sweep bool) {
			tag := ""
			if sweep {
				tag = "（补翻）"
			}
			fmt.Printf("\r[翻译%s] 进度 %d/%d 失败 %d   ", tag, done, total, failed)
			// \r 行未换行：标记之，让下一条日志（含引擎转发的警告）先补 \n
			progressPending = true
		},
		ParseState: func(state string) {
			fmt.Printf("\r[解析] %-12s", state)
			progressPending = true
		},
	})
}

// progressPending 标记 \r 行尚未换行，下一条日志前先补换行。
var progressPending bool

func cliLog(level, msg string) {
	if progressPending {
		fmt.Println()
		progressPending = false
	}
	switch level {
	case "warn":
		fmt.Printf("    ⚠ %s\n", msg)
	case "error":
		fmt.Fprintf(os.Stderr, "✗ %s\n", msg)
	case "step":
		fmt.Printf("── %s\n", msg)
	default:
		fmt.Printf("• %s\n", msg)
	}
}

func signalCtx() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

// ---------------------------------------------------------------- check

func checkCmd() error {
	p, err := newPipeline(cfgLoad())
	if err != nil {
		return err
	}
	cp, _ := config.Path()
	fmt.Println("配置文件:", cp)
	return p.Check()
}

func cfgLoad() *config.Config {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ 配置读取失败: %v\n", err)
		os.Exit(1)
	}
	return cfg
}

// ---------------------------------------------------------------- run

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	out := fs.String("o", "", "成品输出目录（默认 PDF 同目录的 literature_<名称>/）")
	mode := fs.String("mode", "", "覆盖解析模式：pipeline（文本层优先）或 vlm（视觉重识别）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("用法: literature-translation run [-o outdir] paper.pdf")
	}
	cfg := cfgLoad()
	if *mode != "" {
		cfg.MinerU.ModelVersion = *mode
		cfg.Normalize()
	}
	reportMissing(cfg)

	p, err := newPipeline(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := signalCtx()
	defer cancel()

	res, err := p.RunPDF(ctx, fs.Arg(0), *out, nil)
	if err != nil {
		return err
	}
	if res.KeptReferences > 0 {
		fmt.Printf("    参考文献 %d 条按原文保留（未翻译）\n", res.KeptReferences)
	}
	fmt.Println("    译文 Markdown:", res.ZHMD)
	return nil
}

func reportMissing(cfg *config.Config) {
	if missing := cfg.ValidateAndReport(); len(missing) > 0 {
		for _, m := range missing {
			fmt.Println("✗", m)
		}
		fmt.Fprintf(os.Stderr, "编辑 %s 后重试\n", must(config.Path()))
		os.Exit(1)
	}
}

// ---------------------------------------------------------------- translate

func translateCmd(args []string) error {
	fs := flag.NewFlagSet("translate", flag.ExitOnError)
	out := fs.String("o", "", "输出 PDF 路径（默认 MD 同目录的 <名称>_zh.pdf）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("用法: literature-translation translate [-o out.pdf] doc.md")
	}
	if strings.ContainsAny(fs.Arg(0), "\n\r") {
		return fmt.Errorf("路径参数包含换行（可能同时匹配到了原文与 *_zh.md）: %q", fs.Arg(0))
	}
	cfg := cfgLoad()
	reportMissing(cfg)
	if strings.HasSuffix(strings.ToLower(fs.Arg(0)), "_zh.md") {
		return fmt.Errorf("传入的是译文文件（*_zh.md）——请传入原文 MD，译文会自动生成")
	}

	p, err := newPipeline(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := signalCtx()
	defer cancel()

	res, err := p.TranslateMD(ctx, fs.Arg(0), *out, nil)
	if err != nil {
		return err
	}
	if res.KeptReferences > 0 {
		fmt.Printf("    参考文献 %d 条按原文保留（未翻译）\n", res.KeptReferences)
	}
	fmt.Println("    译文 Markdown:", res.ZHMD)
	return nil
}

// ---------------------------------------------------------------- print

func printCmd(args []string) error {
	cfg := cfgLoad()
	fs := flag.NewFlagSet("print", flag.ExitOnError)
	out := fs.String("o", "", "输出 PDF 路径")
	// 开关默认值跟随配置文件（命令行旗标仅作临时覆盖）
	toc := fs.Bool("toc", cfg.Export.TOC, "生成目录")
	twoCol := fs.Bool("twocolumn", cfg.Export.TwoColumn, "双栏排版")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("用法: literature-translation print [-o out.pdf] doc.md")
	}
	if strings.ContainsAny(fs.Arg(0), "\n\r") {
		return fmt.Errorf("路径参数包含换行: %q", fs.Arg(0))
	}
	p, err := newPipeline(cfg)
	if err != nil {
		return err
	}
	_, err = p.ExportMD(fs.Arg(0), *out, toc, twoCol)
	return err
}

// ---------------------------------------------------------------- font

func fontCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("用法: literature-translation font add|list|use|remove")
	}
	cfg := cfgLoad()
	switch args[0] {
	case "add":
		if len(args) != 2 {
			return fmt.Errorf("用法: literature-translation font add <字体文件.ttf|otf>")
		}
		info, err := fonts.Validate(args[1])
		if err != nil {
			return err
		}
		fmt.Printf("字体名: %s\n", info.DisplayName)
		fmt.Printf("大小:   %.1f MB\n", float64(info.SizeBytes)/1024/1024)
		fmt.Printf("中文:   %s\n", map[bool]string{true: "覆盖", false: "不含中文字形"}[info.HasCJK])
		for _, w := range info.Warnings {
			fmt.Println("⚠", w)
		}
		n := fonts.NextNumber(internalNames(cfg))
		internal := fonts.InternalName(n)
		info, err = fonts.Import(args[1], internal)
		if err != nil {
			return err
		}
		cfg.UserFonts = append(cfg.UserFonts, config.UserFont{
			InternalName: internal,
			DisplayName:  info.DisplayName,
			FileName:     info.FileName,
		})
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("✓ 已导入并注册为 %s（用 literature-translation font use %s 启用）\n", internal, internal)
	case "list":
		if len(cfg.UserFonts) == 0 {
			fmt.Println("（无导入字体）")
			return nil
		}
		for _, uf := range cfg.UserFonts {
			mark := ""
			if uf.InternalName == cfg.Export.UserFont {
				mark = "  ← 当前使用"
			}
			fmt.Printf("%s  %s（%s）%s\n", uf.InternalName, uf.DisplayName, uf.FileName, mark)
		}
	case "use":
		if len(args) != 2 {
			return fmt.Errorf("用法: literature-translation font use <内部名|none>")
		}
		name := args[1]
		if name != "none" && cfg.FindUserFont(name) == nil {
			return fmt.Errorf("未找到字体 %s（literature-translation font list 查看）", name)
		}
		if name == "none" {
			name = ""
		}
		cfg.Export.UserFont = name
		if err := cfg.Save(); err != nil {
			return err
		}
		if name == "" {
			fmt.Println("✓ 已恢复默认字体（文楷内嵌）")
		} else {
			fmt.Printf("✓ 导出正文将使用 %s\n", name)
		}
	case "remove":
		if len(args) != 2 {
			return fmt.Errorf("用法: literature-translation font remove <内部名>")
		}
		uf := cfg.FindUserFont(args[1])
		if uf == nil {
			return fmt.Errorf("未找到字体 %s", args[1])
		}
		if err := fonts.Remove(uf.FileName); err != nil && !os.IsNotExist(err) {
			return err
		}
		removed := uf.InternalName
		kept := cfg.UserFonts[:0]
		for _, u := range cfg.UserFonts {
			if u.InternalName != removed {
				kept = append(kept, u)
			}
		}
		cfg.UserFonts = kept
		if cfg.Export.UserFont == removed {
			cfg.Export.UserFont = ""
		}
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("✓ 已移除 %s\n", removed)
	default:
		return fmt.Errorf("未知子命令: literature-translation font %s", args[0])
	}
	return nil
}

func internalNames(cfg *config.Config) []string {
	names := make([]string, 0, len(cfg.UserFonts))
	for _, uf := range cfg.UserFonts {
		names = append(names, uf.InternalName)
	}
	return names
}

func must(s string, err error) string {
	if err != nil {
		return "<配置路径获取失败>"
	}
	return s
}
