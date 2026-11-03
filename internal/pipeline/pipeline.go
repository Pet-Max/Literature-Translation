// Package pipeline 是Literature Translation 的核心编排层：
// 解析 → 拼写校准 → 翻译 → 排版导出的完整流程，供 CLI 与 GUI 共用。
// 调用方通过 Callbacks 注入日志与进度输出（CLI 打印到终端，GUI 推送事件）。
package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"literature-translation/internal/config"
	"literature-translation/internal/engine"
	"literature-translation/internal/export"
	"literature-translation/internal/fonts"
	"literature-translation/internal/mineru"
	"literature-translation/internal/pdftext"
	"literature-translation/internal/render"
	"literature-translation/internal/repair"
)

// LogFunc level: "info" | "warn" | "error" | "step"（step 表示阶段头）
type LogFunc func(level, msg string)

// ProgressFunc 翻译段进度。doneIdx/failedIdx 为已完成/失败段的 0 基编号
// （供前端逐段着色）；sweep 表示正处收尾补翻阶段；旧引擎事件不含时为零值。
type ProgressFunc func(done, total, failed int, doneIdx, failedIdx []int, sweep bool)

// ParseStateFunc MinerU 解析状态回调（pending/running/done…）。
type ParseStateFunc func(state string)

// StageFunc 阶段切换回调（解析中/校准/翻译中/排版中）。
type StageFunc func(stage string)

// Callbacks 调用方注入的输出通道。
type Callbacks struct {
	Log        LogFunc
	Progress   ProgressFunc
	ParseState ParseStateFunc
	Stage      StageFunc
}

// stage 内部辅助：切阶段。
func (p *Pipeline) stage(name string) {
	if p.CB.Stage != nil {
		p.CB.Stage(name)
	}
}

// Pipeline 核心编排器。Cfg 为当前任务的独立快照，执行期间保持不变。
type Pipeline struct {
	Cfg       *config.Config
	CB        Callbacks
	fontsDir  string
	ownsFonts bool
}

// New 创建进程内编排器；
// 保留 error 签名以兼容既有调用方。
func New(cfg *config.Config, cb Callbacks) (*Pipeline, error) {
	return &Pipeline{Cfg: cfg, CB: cb}, nil
}

func (p *Pipeline) logf(format string, a ...any) {
	if p.CB.Log != nil {
		p.CB.Log("info", fmt.Sprintf(format, a...))
	}
}

func (p *Pipeline) warnf(format string, a ...any) {
	if p.CB.Log != nil {
		p.CB.Log("warn", fmt.Sprintf(format, a...))
	}
}

// SegmentFailure 最终翻译失败的段落（engine.SegmentFailure 的别名：
// 两域共用一份定义，省去逐字段转换）。
type SegmentFailure = engine.SegmentFailure

// Result 一次翻译/导出的产物信息。
type Result struct {
	OutPDF         string           `json:"out_pdf"`
	ZHMD           string           `json:"zh_md"`
	WorkDir        string           `json:"work_dir"`
	Total          int              `json:"total"`
	Done           int              `json:"done"`
	KeptReferences int              `json:"kept_references"`
	Failed         []SegmentFailure `json:"failed"`
	Warnings       []string         `json:"warnings"`
}

// Check 预检：配置完整性 + 内置 Go 引擎自检 + Edge。
func (p *Pipeline) Check() error {
	for _, m := range p.Cfg.ValidateAndReport() {
		p.warnf("%s", m)
	}
	if err := engine.SelfCheck(); err != nil {
		return fmt.Errorf("引擎自检失败: %v", err)
	}
	p.logf("引擎: OK（内置 Go 引擎）")
	edge, err := export.FindEdge()
	if err != nil {
		return err
	}
	p.logf("Edge: %s", edge)
	p.logf("✓ 预检完成")
	return nil
}

// RunPDF 全流程：MinerU 解析 → 拼写校准 → 翻译 → 导出。
// plan 非 nil 时启用归档体系：过程文件留在 _process/mineru，四类产物分目录归档；
// plan 为 nil 时沿用旧平铺行为（工作目录在源文件旁，CLI 兼容）。
// 解析前做语言预检：中文 PDF 直接报错返回，不烧 MinerU 配额
// （文本层为空的扫描件放行，由解析后流程自然处理）。
func (p *Pipeline) RunPDF(ctx context.Context, pdfPath, outDirOverride string, plan *ArchivePlan) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("翻译已取消: %w", err)
	}
	cfg := p.Cfg
	pdfPath, _ = filepath.Abs(pdfPath)
	stem := strings.TrimSuffix(filepath.Base(pdfPath), filepath.Ext(pdfPath))
	input, err := snapshotInput(ctx, pdfPath, false)
	if err != nil {
		return nil, err
	}
	defer input.Close()

	var workDir, parseDir, outPDF, zhMD string
	if plan != nil {
		if plan.EnPDFPath != "" {
			if err := copyFileContext(ctx, input.Path, plan.EnPDFPath); err != nil {
				return nil, fmt.Errorf("归档原始 PDF 失败: %w", err)
			}
		}
		workDir = plan.MineruDir
		parseDir = plan.MineruDir
		outPDF = plan.ZhPDFPath
		zhMD = plan.ZhMDPath
	} else {
		workDir = filepath.Join(filepath.Dir(pdfPath), "literature_"+sanitize(stem))
		if outDirOverride != "" {
			workDir = outDirOverride
		}
		parseDir = filepath.Join(workDir, "parse")
		outPDF = filepath.Join(workDir, stem+"_zh.pdf")
		if cfg.Export.OutputDir != "" {
			outPDF = filepath.Join(cfg.Export.OutputDir, stem+"_zh.pdf")
		}
		zhMD = filepath.Join(parseDir, stem+"_zh.md")
	}

	// [0/4] 语言预检：本地抽文本层判定，中文 PDF 直接拒绝（不烧 MinerU 配额）。
	// 抽到的文本层留给 [1.5] 拼写校准复用，避免同一 PDF 抽两遍。
	var srcText string
	if text, err := pdftext.ExtractText(input.Path); err == nil {
		srcText = text
		if IsChineseText(text) {
			p.warnf("语言预检：中文 PDF，无需翻译流程")
			return nil, fmt.Errorf("检测到中文 PDF，无需翻译。")
		}
	}
	// 预检失败（无文本层/引擎异常）不拦路，走正常流程

	// [1/4] MinerU 解析
	p.stage("解析中")
	p.logf("MinerU 精准解析（%s 模式）…", cfg.MinerU.ModelVersion)
	start := time.Now()
	if err := input.check(ctx); err != nil {
		return nil, err
	}
	res, err := mineru.ParsePDF(ctx, cfg.MinerU.Token, input.Path,
		cfg.MinerU.ModelVersion, parseDir, func(state string) {
			if p.CB.ParseState != nil {
				p.CB.ParseState(state)
			}
		})
	if err != nil {
		return nil, err
	}
	if err := input.check(ctx); err != nil {
		return nil, err
	}
	p.logf("解析完成，用时 %s：%s", time.Since(start).Round(time.Second), res.MDPath)

	// [1.5/4] 校准（复用预检抽取的文本层；预检未取到时再抽一次）。
	// 符号修复与词级拼写校准是两个独立开关，全关则连文本层都不必抽
	if cfg.Repair.Spelling || cfg.Repair.Symbols {
		p.stage("拼写校准")
		if strings.TrimSpace(srcText) == "" {
			if text, err := pdftext.ExtractText(input.Path); err == nil {
				srcText = text
			}
		}
		if strings.TrimSpace(srcText) == "" {
			p.warnf("文本抽取失败，跳过拼写校准")
		} else {
			st, err := repair.Apply(res.MDPath, srcText, repair.Options{
				Spelling: cfg.Repair.Spelling,
				Symbols:  cfg.Repair.Symbols,
			})
			if err != nil {
				p.warnf("拼写校准失败（继续用未校准文本）：%v", err)
			} else {
				p.logf("拼写校准：修正 %d，合并 %d，拆分 %d，符号 %d，上下文 %d，表格 %d，重音符 %d，控制码 %d，行间公式 %d，上下标 %d，数学区 %d",
					st.Fixed, st.Merged, st.Split, st.Syms, st.Ctx, st.Tables, st.Accents, st.Ctrl, st.Display, st.SupSub, st.MathFix)
			}
		}
	} else {
		p.logf("拼写校准与符号修复均已关闭，跳过")
	}

	if err := input.check(ctx); err != nil {
		return nil, err
	}
	// [2/4] 翻译
	p.stage("翻译中")
	p.logf("翻译（%d 路并发）…", cfg.LLM.Concurrency)
	start = time.Now()
	tr, err := p.translate(ctx, input.Path, res.MDPath, zhMD, plan)
	if err != nil {
		return nil, err
	}
	p.logf("翻译完成，用时 %s：%d/%d 段成功", time.Since(start).Round(time.Second), tr.Done, tr.Total)
	for _, f := range tr.Failed {
		p.warnf("第 %d 段需人工处理：%s", f.I+1, f.Reasons)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := input.check(ctx); err != nil {
		return nil, err
	}
	renderDir := filepath.Dir(zhMD)
	if plan != nil {
		if err := copyFileContext(ctx, res.MDPath, plan.EnMDPath); err != nil {
			return nil, fmt.Errorf("归档英文 MD: %w", err)
		}
		if err := archiveResources(ctx, res.MDPath, plan.EnMDDir, plan.ZhMDDir); err != nil {
			return nil, err
		}
	} else if err := archiveResources(ctx, res.MDPath, renderDir); err != nil {
		return nil, err
	}
	if err := p.finishRun(ctx, pdfPath, res.MDPath, zhMD, plan, tr); err != nil {
		return nil, err
	}
	finalPath, err := p.export(ctx, zhTarget{MDPath: zhMD, Dir: renderDir, Title: stem}, outPDF, cfg.Export.TOC, cfg.Export.TwoColumn)
	if err != nil {
		return nil, err
	}
	if err := input.check(ctx); err != nil {
		return nil, err
	}
	archiveDir := workDir
	if plan != nil {
		archiveDir = plan.Dir
	}
	return &Result{
		OutPDF: finalPath, ZHMD: zhMD, WorkDir: archiveDir,
		Total: tr.Total, Done: tr.Done, KeptReferences: tr.KeptReferences,
		Failed: tr.Failed, Warnings: tr.Warnings,
	}, nil
}

// Recalibrate 对已解析的英文 MD 离线重跑校准（不烧 MinerU 配额）：
// 取原 PDF 文本层 → repair.Apply 原地改写（改写前备份 <名>.md.bak）。
// 供存量文档在新版校准规则下重新加工，也用于人工抽查修复明细。
// 显式的修复命令，始终跑全量（符号 + 词级），不受 repair 配置开关限制。
func (p *Pipeline) Recalibrate(ctx context.Context, mdPath, pdfPath string) (*repair.Stats, error) {
	srcText, err := pdftext.ExtractText(pdfPath)
	if err != nil {
		return nil, fmt.Errorf("文本抽取失败: %w", err)
	}
	if strings.TrimSpace(srcText) == "" {
		return nil, fmt.Errorf("PDF 文本层为空（扫描件无法校准）")
	}
	// 备份只保留首次：二次运行若覆盖 .bak，第一次的原件就丢了
	bakPath := mdPath + ".bak"
	if _, err := os.Stat(bakPath); os.IsNotExist(err) {
		if bak, err := os.ReadFile(mdPath); err == nil {
			if err := os.WriteFile(bakPath, bak, 0o644); err != nil {
				p.warnf("备份失败（继续校准，无备份）：%v", err)
			}
		}
	}
	st, err := repair.Apply(mdPath, srcText, repair.Options{Spelling: true, Symbols: true})
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// TranslateMD 翻译已有 MD（跳过解析；无原 PDF 时拼写校准自动跳过）。
// plan 非 nil 时启用归档体系（英文 md/图片拷入 02，zh md → 03，pdf → 04）。
func (p *Pipeline) TranslateMD(ctx context.Context, mdPath, outPDFOverride string, plan *ArchivePlan) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("翻译已取消: %w", err)
	}
	cfg := p.Cfg
	mdPath, _ = filepath.Abs(mdPath)
	stem := strings.TrimSuffix(filepath.Base(mdPath), filepath.Ext(mdPath))
	input, err := snapshotInput(ctx, mdPath, true)
	if err != nil {
		return nil, err
	}
	defer input.Close()

	var zhMD, outPDF, renderDir string
	if plan != nil {
		zhMD = plan.ZhMDPath
		outPDF = plan.ZhPDFPath
		renderDir = plan.ZhMDDir
		// 归档：英文原件拷入 02（连同其旁 images/），图片同时拷给 03 供相对链接
		if err := copyFileContext(ctx, input.Path, plan.EnMDPath); err != nil {
			return nil, fmt.Errorf("归档英文 MD 失败: %w", err)
		}
		if err := archiveResources(ctx, input.Path, plan.EnMDDir, plan.ZhMDDir); err != nil {
			return nil, err
		}
	} else {
		zhMD = strings.TrimSuffix(mdPath, filepath.Ext(mdPath)) + "_zh.md"
		outPDF = strings.TrimSuffix(mdPath, filepath.Ext(mdPath)) + "_zh.pdf"
		if outPDFOverride != "" {
			outPDF = outPDFOverride
		} else if cfg.Export.OutputDir != "" {
			outPDF = filepath.Join(cfg.Export.OutputDir, stem+"_zh.pdf")
		}
		renderDir = filepath.Dir(zhMD)
	}

	if err := input.check(ctx); err != nil {
		return nil, err
	}
	p.stage("翻译中")
	p.logf("翻译（%d 路并发，参考文献保留原文）…", cfg.LLM.Concurrency)
	start := time.Now()
	tr, err := p.translate(ctx, input.Path, input.Path, zhMD, plan)
	if err != nil {
		return nil, err
	}
	p.logf("翻译完成，用时 %s：%d/%d 段成功", time.Since(start).Round(time.Second), tr.Done, tr.Total)
	for _, f := range tr.Failed {
		p.warnf("第 %d 段需人工处理：%s", f.I+1, f.Reasons)
	}

	if err := input.check(ctx); err != nil {
		return nil, err
	}
	if err := p.finishRun(ctx, mdPath, input.Path, zhMD, plan, tr); err != nil {
		return nil, err
	}
	renderMD := zhMD
	renderRoot := renderDir
	if plan == nil {
		renderMD = filepath.Join(input.dir, filepath.Base(zhMD))
		renderRoot = input.dir
		if err := copyFileContext(ctx, zhMD, renderMD); err != nil {
			return nil, err
		}
	}
	finalPath, err := p.export(ctx, zhTarget{MDPath: renderMD, Dir: renderRoot, Title: stem}, outPDF,
		cfg.Export.TOC, cfg.Export.TwoColumn)
	if err != nil {
		return nil, err
	}
	if err := input.check(ctx); err != nil {
		return nil, err
	}
	archiveDir := renderDir
	if plan != nil {
		archiveDir = plan.Dir
	}
	return &Result{
		OutPDF: finalPath, ZHMD: zhMD, WorkDir: archiveDir,
		Total: tr.Total, Done: tr.Done, KeptReferences: tr.KeptReferences,
		Failed: tr.Failed, Warnings: tr.Warnings,
	}, nil
}

// ExportMD 纯排版导出（不翻译）。toc/twoCol 传 nil 表示跟随配置。
func (p *Pipeline) ExportMD(ctx context.Context, mdPath, outPDF string, toc, twoCol *bool) (string, error) {
	input, err := snapshotInput(ctx, mdPath, true)
	if err != nil {
		return "", err
	}
	defer input.Close()
	useTOC, useTwo := p.Cfg.Export.TOC, p.Cfg.Export.TwoColumn
	if toc != nil {
		useTOC = *toc
	}
	if twoCol != nil {
		useTwo = *twoCol
	}
	result, err := p.export(ctx, zhTarget{MDPath: input.Path, Dir: input.dir,
		Title: strings.TrimSuffix(filepath.Base(mdPath), filepath.Ext(mdPath))}, outPDF, useTOC, useTwo)
	if err != nil {
		return "", err
	}
	if err = input.check(ctx); err != nil {
		return "", err
	}
	return result, nil
}

// ExportMDPlan 归档体系的仅排版导出（中文 MD → 04_zh_pdf/<原名>.pdf）。
// 源 md 原名拷入 03_zh_md（含旁侧 images/），渲染 HTTP root 用源目录保证
// 图片相对链接有效。不启动翻译引擎，纯 Go 渲染 + Edge 打印。
func (p *Pipeline) ExportMDPlan(ctx context.Context, mdPath string, plan *ArchivePlan) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	mdPath, _ = filepath.Abs(mdPath)
	base := filepath.Base(mdPath)
	stem := strings.TrimSuffix(base, filepath.Ext(mdPath))
	input, err := snapshotInput(ctx, mdPath, true)
	if err != nil {
		return nil, err
	}
	defer input.Close()

	archivedMD := filepath.Join(plan.ZhMDDir, base)
	if err := copyFileContext(ctx, input.Path, archivedMD); err != nil {
		return nil, fmt.Errorf("归档中文 MD 失败: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := archiveResources(ctx, input.Path, plan.ZhMDDir); err != nil {
		return nil, err
	}

	if err := input.check(ctx); err != nil {
		return nil, err
	}
	outPDF := filepath.Join(plan.ZhPDFDir, stem+".pdf")
	finalPath, err := p.export(ctx, zhTarget{MDPath: archivedMD, Dir: plan.ZhMDDir, Title: stem},
		outPDF, p.Cfg.Export.TOC, p.Cfg.Export.TwoColumn)
	if err != nil {
		return nil, err
	}
	if err := input.check(ctx); err != nil {
		return nil, err
	}
	return &Result{OutPDF: finalPath, ZHMD: archivedMD, WorkDir: plan.Dir}, nil
}

// ResumeExport 「翻译已完成、仅重试导出」的短路路径：上次失败于排版阶段
// 的任务重试时，若译文已归档、断点指纹与当前配置一致且无失败段，直接
// 排版导出归档译文，不再重烧 MinerU / LLM 配额。图片相对链接用归档目录
// 解析（03_zh_md/images 在翻译完成后即已同步）。
// 返回值三态：
//   - attempted=false：守卫未过（原因已写日志），调用方回退全流程；
//   - (res, true, nil)：已短路导出成功；
//   - (nil, true, err)：短路导出本身失败——多为确定性错误（KaTeX/Edge），
//     自动回退全流程只会白烧配额，调用方应按任务失败处理；
//     用户可删除归档 03_zh_md 译文后重试来强制全流程。
func (p *Pipeline) ResumeExport(ctx context.Context, plan *ArchivePlan) (*Result, bool, error) {
	m, checkErr := p.resumableRun(plan)
	if checkErr != nil {
		p.logf("仅重试导出未生效（%v），走全流程", checkErr)
		return nil, false, nil
	}
	doneCount := m.Done
	p.logf("检测到上次翻译已完成（%d 段），跳过解析与翻译，直接排版导出", doneCount)
	title := strings.TrimSuffix(filepath.Base(plan.SourcePath), filepath.Ext(plan.SourcePath))
	finalPath, err := p.export(ctx, zhTarget{MDPath: plan.ZhMDPath, Dir: plan.ZhMDDir, Title: title},
		plan.ZhPDFPath, p.Cfg.Export.TOC, p.Cfg.Export.TwoColumn)
	if err != nil {
		return nil, true, fmt.Errorf("仅重试导出失败: %w（如需全流程重译，删除归档 03_zh_md 中的译文后重试）", err)
	}
	return &Result{
		OutPDF: finalPath, ZHMD: plan.ZhMDPath, WorkDir: plan.Dir,
		Total: m.Total, Done: doneCount, KeptReferences: m.References,
	}, true, nil
}

type translateOutcome struct {
	Total          int              `json:"total"`
	Done           int              `json:"done"`
	KeptReferences int              `json:"kept_references"`
	Failed         []SegmentFailure `json:"failed"`
	Warnings       []string         `json:"warnings"`
}

// translate 共用的翻译环节（RunPDF 与 TranslateMD 唯一的差异在输入输出路径）。
// 直接调用可取消的翻译引擎；进度事件经 OnEvent 转给回调。
// 引擎诊断日志（切段数、断点复用、校验未过原因、收尾补翻过程等）
// 沿用 warn 级诊断日志。
func (p *Pipeline) translate(ctx context.Context, sourcePath, mdPath, outPath string, plan *ArchivePlan) (*translateOutcome, error) {
	cfg := p.Cfg
	progressPath, err := p.beginRun(ctx, sourcePath, mdPath, outPath, plan)
	if err != nil {
		return nil, err
	}
	tr, err := engine.TranslateContext(ctx, engine.TranslateOptions{
		MDPath: mdPath, OutPath: outPath, ProgressPath: progressPath,
		Glossary: glossaryPairs(cfg), SystemRules: cfg.Rules.ActiveText(),
		Bilingual: cfg.Export.Bilingual, SkipReferences: cfg.Translate.SkipReferences,
		LLM: engine.LLMOptions{BaseURL: cfg.LLM.BaseURL, APIKey: cfg.LLM.APIKey, Model: cfg.LLM.Model, Concurrency: cfg.LLM.Concurrency},
		OnEvent: func(ev engine.Event) {
			if p.CB.Progress != nil {
				p.CB.Progress(ev.Done, ev.Total, ev.Failed, ev.DoneIdx, ev.FailedIdx, ev.Sweep)
			}
		},
		Logf: func(format string, a ...any) { p.warnf(format, a...) },
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("翻译已取消: %w", ctx.Err())
		}
		return nil, err
	}
	for _, w := range tr.Warnings {
		p.warnf("成品校验警告：%s", w)
	}
	return &translateOutcome{
		Total: tr.Total, Done: tr.Done, KeptReferences: tr.KeptReferences,
		Failed: tr.Failed, Warnings: tr.Warnings,
	}, nil
}

// zhTarget 排版导出的输入。
type zhTarget struct {
	MDPath string // 译文 Markdown 路径
	Dir    string // 工作目录（图片相对路径的根）
	Title  string // 文档标题（无一级标题时的回退）
}

// export 排版渲染 + Edge 打印。中间产物 final.html 写入系统临时目录，
// 通过 /__final 内存路由提供服务——不再污染用户的文档文件夹。
// 超时派生自任务 ctx（此前用 Background，任务取消拦不住导出）。
func (p *Pipeline) export(ctx context.Context, t zhTarget, outPDF string, useTOC, useTwo bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if _, err := resourceFiles(t.MDPath); err != nil {
		return "", err
	}
	p.stage("排版中")
	cfg := p.Cfg
	mdBytes, err := os.ReadFile(t.MDPath)
	if err != nil {
		return "", fmt.Errorf("读取译文 Markdown 失败: %w", err)
	}
	htmlDoc := render.Build(string(mdBytes), render.Options{
		Title:       t.Title,
		FontCSS:     fontStack(cfg),
		UserFontCSS: userFontCSS(cfg),
		TOC:         useTOC,
		TwoColumn:   useTwo,
	})
	p.logf("排版渲染完成，Edge 打印 PDF …")
	ctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	fontsDir := p.fontsDir
	if fontsDir == "" {
		fontsDir, _ = fonts.Dir()
	}
	// 浏览器启动类故障（Edge 后台更新换版、被安全软件瞬时拦截、内存吃紧等）
	// 过几秒重开一次通常就能恢复，自动重试；KaTeX/打印/写盘类错误是确定性的，
	// 不重试，避免白等。打印本身幂等，重试零风险。
	var finalPath string
	for attempt := 1; ; attempt++ {
		finalPath, err = export.PrintPDF(ctx, t.Dir, htmlDoc, outPDF, fontsDir)
		if err == nil || attempt >= 3 || !isBrowserLaunchErr(err) {
			break
		}
		p.logf("浏览器未起来（第 %d 次）：%v —— 5 秒后重试", attempt, err)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	if err != nil {
		return "", err
	}
	p.logf("✓ 成品：%s", finalPath)
	if finalPath != outPDF {
		p.warnf("原目标文件被占用（可能正被阅读器打开），已另存为新文件")
	}
	return finalPath, nil
}

// isBrowserLaunchErr 识别无头浏览器启动阶段的瞬时故障：进程未及打印
// DevTools 端点就退出（chrome failed to start），或 20 秒内没等到端点
// （websocket url timeout reached）。文案来自 chromedp，升级其版本时需留意。
func isBrowserLaunchErr(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "chrome failed to start") ||
		strings.Contains(msg, "websocket url timeout reached")
}

func glossaryPairs(cfg *config.Config) [][]string {
	pairs := make([][]string, 0, len(cfg.Glossary))
	for _, t := range cfg.Glossary {
		pairs = append(pairs, []string{t.En, t.Zh})
	}
	return pairs
}

func fontStack(cfg *config.Config) string {
	base := cfg.Export.Font
	if strings.TrimSpace(base) == "" {
		base = config.Default().Export.Font
	}
	if cfg.Export.UserFont == "" {
		return base
	}
	if uf := cfg.FindUserFont(cfg.Export.UserFont); uf != nil {
		return fmt.Sprintf(`"%s", %s`, uf.InternalName, base)
	}
	return base
}

func userFontCSS(cfg *config.Config) string {
	if cfg.Export.UserFont == "" {
		return ""
	}
	uf := cfg.FindUserFont(cfg.Export.UserFont)
	if uf == nil || uf.FileName == "" {
		return ""
	}
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			return r
		}
		return -1
	}, uf.FileName)
	if safe == "" || (!strings.HasSuffix(strings.ToLower(safe), ".ttf") &&
		!strings.HasSuffix(strings.ToLower(safe), ".otf")) {
		return ""
	}
	return fmt.Sprintf(`@font-face{font-family:"%s";src:url("/userfonts/%s");}`,
		uf.InternalName, safe)
}

func sanitize(name string) string {
	r := strings.NewReplacer(`\`, "_", "/", "_", ":", "_", "*", "_", "?", "_",
		`"`, "_", "<", "_", ">", "_", "|", "_", " ", "_")
	return r.Replace(name)
}
