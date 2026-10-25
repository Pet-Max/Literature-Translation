// Package pipeline 是Literature Translation 的核心编排层：
// 解析 → 拼写校准 → 翻译 → 排版导出的完整流程，供 CLI 与 GUI 共用。
// 调用方通过 Callbacks 注入日志与进度输出（CLI 打印到终端，GUI 推送事件）。
package pipeline

import (
	"context"
	"encoding/json"
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
	"literature-translation/internal/render"
	"literature-translation/internal/repair"
)

// LogFunc level: "info" | "warn" | "error" | "step"（step 表示阶段头）
type LogFunc func(level, msg string)

// ProgressFunc 翻译段进度。
type ProgressFunc func(done, total, failed int)

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

// Pipeline 核心编排器。Cfg 为共享指针——GUI 修改配置后原地生效。
type Pipeline struct {
	Cfg       *config.Config
	EngineDir string
	CB        Callbacks
}

// New 创建编排器并定位翻译引擎目录。
func New(cfg *config.Config, cb Callbacks) (*Pipeline, error) {
	engDir, err := findEngineDir()
	if err != nil {
		return nil, err
	}
	return &Pipeline{Cfg: cfg, EngineDir: engDir, CB: cb}, nil
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

// SegmentFailure 最终翻译失败的段落。
type SegmentFailure struct {
	I       int    `json:"i"`
	Reasons string `json:"reasons"`
}

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

// Check 预检：配置完整性 + Python 引擎 + Edge。
func (p *Pipeline) Check() error {
	for _, m := range p.Cfg.ValidateAndReport() {
		p.warnf("%s", m)
	}
	sup, err := p.startSupervisor()
	if err != nil {
		return err
	}
	defer sup.Stop()
	if err := sup.Ping(); err != nil {
		return fmt.Errorf("引擎 ping 失败: %v", err)
	}
	p.logf("引擎: OK（%s）", p.EngineDir)
	edge, err := export.FindEdge()
	if err != nil {
		return err
	}
	p.logf("Edge: %s", edge)
	p.logf("✓ 预检完成")
	return nil
}

// startSupervisor 拉起 Python 翻译引擎。
func (p *Pipeline) startSupervisor() (*engine.Supervisor, error) {
	return engine.Start(p.EngineDir, nil)
}

// RunPDF 全流程：MinerU 解析 → 拼写校准 → 翻译 → 导出。
// plan 非 nil 时启用归档体系：过程文件留在 _process/mineru，四类产物分目录归档；
// plan 为 nil 时沿用旧平铺行为（工作目录在源文件旁，CLI 兼容）。
// 解析前做语言预检：中文 PDF 直接报错返回，不烧 MinerU 配额
// （文本层为空的扫描件放行，由解析后流程自然处理）。
func (p *Pipeline) RunPDF(ctx context.Context, pdfPath, outDirOverride string, plan *ArchivePlan) (*Result, error) {
	cfg := p.Cfg
	pdfPath, _ = filepath.Abs(pdfPath)
	stem := strings.TrimSuffix(filepath.Base(pdfPath), filepath.Ext(pdfPath))

	var workDir, parseDir, outPDF, zhMD string
	if plan != nil {
		if plan.EnPDFPath != "" {
			if err := copyFile(pdfPath, plan.EnPDFPath); err != nil {
				return nil, fmt.Errorf("归档原始 PDF 失败: %w", err)
			}
		}
		workDir = plan.MineruDir
		parseDir = plan.MineruDir
		outPDF = plan.ZhPDFPath
		zhMD = plan.ZhMDPath
		// MinerU 每次解包都会生成一组新 uuid 文件；清空后重解，避免历史堆积
		if err := os.RemoveAll(parseDir); err != nil {
			return nil, fmt.Errorf("清理过程目录失败: %w", err)
		}
		if err := os.MkdirAll(parseDir, 0o755); err != nil {
			return nil, err
		}
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

	sup, err := p.startSupervisor()
	if err != nil {
		return nil, err
	}
	defer sup.Stop()

	// [0/4] 语言预检：本地抽文本层判定，中文 PDF 直接拒绝（不烧 MinerU 配额）
	if raw, err := sup.Call("pdf_text", map[string]any{"pdf_path": pdfPath}, 2*time.Minute); err == nil {
		var pt struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &pt) == nil && IsChineseText(pt.Text) {
			p.warnf("语言预检：中文 PDF，无需翻译流程")
			return nil, fmt.Errorf("检测到中文 PDF，无需翻译。")
		}
	}
	// 预检失败（无文本层/引擎异常）不拦路，走正常流程

	// [1/4] MinerU 解析
	p.stage("解析中")
	p.logf("MinerU 精准解析（%s 模式）…", cfg.MinerU.ModelVersion)
	start := time.Now()
	res, err := mineru.ParsePDF(ctx, cfg.MinerU.Token, pdfPath,
		cfg.MinerU.ModelVersion, parseDir, func(state string) {
			if p.CB.ParseState != nil {
				p.CB.ParseState(state)
			}
		})
	if err != nil {
		return nil, err
	}
	p.logf("解析完成，用时 %s：%s", time.Since(start).Round(time.Second), res.MDPath)

	// [1.5/4] 拼写校准
	if cfg.Repair.Spelling {
		raw, err := sup.Call("pdf_text", map[string]any{"pdf_path": pdfPath}, 2*time.Minute)
		p.stage("拼写校准")
		if err != nil {
			p.warnf("文本抽取失败，跳过拼写校准：%v", err)
		} else {
			var pt struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(raw, &pt); err == nil && strings.TrimSpace(pt.Text) != "" {
				st, err := repair.Apply(res.MDPath, pt.Text)
				if err != nil {
					p.warnf("拼写校准失败（继续用未校准文本）：%v", err)
				} else {
					p.logf("拼写校准：修正 %d 处，合并 %d，拆分 %d", st.Fixed, st.Merged, st.Split)
				}
			}
		}
	}

	// [2/4] 翻译
	p.stage("翻译中")
	p.logf("翻译（%d 路并发）…", cfg.LLM.Concurrency)
	go p.pumpProgress(sup)
	start = time.Now()
	tr, err := p.translate(ctx, sup, res.MDPath, zhMD, plan)
	if err != nil {
		return nil, err
	}
	p.logf("翻译完成，用时 %s：%d/%d 段成功", time.Since(start).Round(time.Second), tr.Done, tr.Total)
	for _, f := range tr.Failed {
		p.warnf("第 %d 段需人工处理：%s", f.I+1, f.Reasons)
	}

	// 归档：英文 MD（解析+校准后的成品）与图片拷入 02_en_md，图片同时拷给 03_zh_md
	if plan != nil {
		if err := copyFile(res.MDPath, plan.EnMDPath); err != nil {
			p.warnf("英文 MD 归档失败：%v", err)
		}
		if err := copyDir(filepath.Join(parseDir, "images"), filepath.Join(plan.EnMDDir, "images")); err != nil {
			p.warnf("英文 MD 图片归档失败：%v", err)
		}
		if err := copyDir(filepath.Join(parseDir, "images"), filepath.Join(plan.ZhMDDir, "images")); err != nil {
			p.warnf("中文 MD 图片归档失败：%v", err)
		}
	}

	// [3/4]+[4/4] 排版导出（渲染 HTTP root = parseDir，图片相对链接有效）
	finalPath, err := p.export(zhTarget{MDPath: zhMD, Dir: parseDir, Title: stem}, outPDF,
		cfg.Export.TOC, cfg.Export.TwoColumn)
	if err != nil {
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

// TranslateMD 翻译已有 MD（跳过解析；无原 PDF 时拼写校准自动跳过）。
// plan 非 nil 时启用归档体系（英文 md/图片拷入 02，zh md → 03，pdf → 04）。
func (p *Pipeline) TranslateMD(ctx context.Context, mdPath, outPDFOverride string, plan *ArchivePlan) (*Result, error) {
	cfg := p.Cfg
	mdPath, _ = filepath.Abs(mdPath)
	stem := strings.TrimSuffix(filepath.Base(mdPath), filepath.Ext(mdPath))
	srcDir := filepath.Dir(mdPath)

	var zhMD, outPDF, renderDir string
	if plan != nil {
		zhMD = plan.ZhMDPath
		outPDF = plan.ZhPDFPath
		renderDir = plan.ZhMDDir
		// 归档：英文原件拷入 02（连同其旁 images/），图片同时拷给 03 供相对链接
		if err := copyFile(mdPath, plan.EnMDPath); err != nil {
			return nil, fmt.Errorf("归档英文 MD 失败: %w", err)
		}
		if err := copyDir(filepath.Join(srcDir, "images"), filepath.Join(plan.EnMDDir, "images")); err != nil {
			return nil, fmt.Errorf("归档英文图片失败: %w", err)
		}
		if err := copyDir(filepath.Join(srcDir, "images"), filepath.Join(plan.ZhMDDir, "images")); err != nil {
			return nil, fmt.Errorf("归档译文图片失败: %w", err)
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

	sup, err := p.startSupervisor()
	if err != nil {
		return nil, err
	}
	defer sup.Stop()

	p.stage("翻译中")
	p.logf("翻译（%d 路并发，参考文献保留原文）…", cfg.LLM.Concurrency)
	go p.pumpProgress(sup)
	start := time.Now()
	tr, err := p.translate(ctx, sup, mdPath, zhMD, plan)
	if err != nil {
		return nil, err
	}
	p.logf("翻译完成，用时 %s：%d/%d 段成功", time.Since(start).Round(time.Second), tr.Done, tr.Total)
	for _, f := range tr.Failed {
		p.warnf("第 %d 段需人工处理：%s", f.I+1, f.Reasons)
	}

	finalPath, err := p.export(zhTarget{MDPath: zhMD, Dir: renderDir, Title: stem}, outPDF,
		cfg.Export.TOC, cfg.Export.TwoColumn)
	if err != nil {
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
func (p *Pipeline) ExportMD(mdPath, outPDF string, toc, twoCol *bool) (string, error) {
	useTOC, useTwo := p.Cfg.Export.TOC, p.Cfg.Export.TwoColumn
	if toc != nil {
		useTOC = *toc
	}
	if twoCol != nil {
		useTwo = *twoCol
	}
	return p.export(zhTarget{MDPath: mdPath, Dir: filepath.Dir(mdPath),
		Title: strings.TrimSuffix(filepath.Base(mdPath), filepath.Ext(mdPath))}, outPDF, useTOC, useTwo)
}

// ExportMDPlan 归档体系的仅排版导出（中文 MD → 04_zh_pdf/<原名>.pdf）。
// 源 md 原名拷入 03_zh_md（含旁侧 images/），渲染 HTTP root 用源目录保证
// 图片相对链接有效。不启动翻译引擎，纯 Go 渲染 + Edge 打印。
func (p *Pipeline) ExportMDPlan(ctx context.Context, mdPath string, plan *ArchivePlan) (*Result, error) {
	mdPath, _ = filepath.Abs(mdPath)
	base := filepath.Base(mdPath)
	stem := strings.TrimSuffix(base, filepath.Ext(mdPath))
	srcDir := filepath.Dir(mdPath)

	archivedMD := filepath.Join(plan.ZhMDDir, base)
	if err := copyFile(mdPath, archivedMD); err != nil {
		return nil, fmt.Errorf("归档中文 MD 失败: %w", err)
	}
	if err := copyDir(filepath.Join(srcDir, "images"), filepath.Join(plan.ZhMDDir, "images")); err != nil {
		p.warnf("中文 MD 图片归档失败：%v", err)
	}

	outPDF := filepath.Join(plan.ZhPDFDir, stem+".pdf")
	finalPath, err := p.export(zhTarget{MDPath: mdPath, Dir: srcDir, Title: stem},
		outPDF, p.Cfg.Export.TOC, p.Cfg.Export.TwoColumn)
	if err != nil {
		return nil, err
	}
	return &Result{OutPDF: finalPath, ZHMD: archivedMD, WorkDir: plan.Dir}, nil
}

// pumpProgress 把引擎的进度事件转给回调。
func (p *Pipeline) pumpProgress(sup *engine.Supervisor) {
	if p.CB.Progress == nil {
		return
	}
	for ev := range sup.Events {
		p.CB.Progress(ev.Done, ev.Total, ev.Failed)
	}
}

type translateOutcome struct {
	Total          int              `json:"total"`
	Done           int              `json:"done"`
	KeptReferences int              `json:"kept_references"`
	Failed         []SegmentFailure `json:"failed"`
	Warnings       []string         `json:"warnings"`
	OutPath        string           `json:"out_path"`
}

// translate 共用的翻译环节（RunPDF 与 TranslateMD 唯一的差异在输入输出路径）。
func (p *Pipeline) translate(ctx context.Context, sup *engine.Supervisor, mdPath, outPath string, plan *ArchivePlan) (*translateOutcome, error) {
	cfg := p.Cfg
	progressPath := ""
	if plan != nil {
		progressPath = plan.ProgressPath
	}
	raw, err := sup.Call("translate", map[string]any{
		"md_path":         mdPath,
		"out_path":        outPath,
		"progress_path":   progressPath,
		"glossary":        glossaryPairs(cfg),
		"system_rules":    cfg.Rules.ActiveText(),
		"bilingual":       cfg.Export.Bilingual,
		"skip_references": cfg.Translate.SkipReferences,
		"llm": map[string]any{
			"base_url":    cfg.LLM.BaseURL,
			"api_key":     cfg.LLM.APIKey,
			"model":       cfg.LLM.Model,
			"concurrency": cfg.LLM.Concurrency,
		},
	}, 60*time.Minute)
	if err != nil {
		return nil, err
	}
	var tr translateOutcome
	if err := json.Unmarshal(raw, &tr); err != nil {
		return nil, err
	}
	for _, w := range tr.Warnings {
		p.warnf("成品校验警告：%s", w)
	}
	return &tr, nil
}

// zhTarget 排版导出的输入。
type zhTarget struct {
	MDPath string // 译文 Markdown 路径
	Dir    string // 工作目录（图片相对路径的根）
	Title  string // 文档标题（无一级标题时的回退）
}

// export 排版渲染 + Edge 打印。中间产物 final.html 写入系统临时目录，
// 通过 /__final 内存路由提供服务——不再污染用户的文档文件夹。
func (p *Pipeline) export(t zhTarget, outPDF string, useTOC, useTwo bool) (string, error) {
	p.stage("排版中")
	cfg := p.Cfg
	mdBytes, err := os.ReadFile(t.MDPath)
	if err != nil {
		return "", err
	}
	htmlDoc := render.Build(string(mdBytes), render.Options{
		Title:       t.Title,
		FontCSS:     fontStack(cfg),
		UserFontCSS: userFontCSS(cfg),
		TOC:         useTOC,
		TwoColumn:   useTwo,
	})
	p.logf("排版渲染完成，Edge 打印 PDF …")
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	fontsDir, _ := fonts.Dir()
	finalPath, err := export.PrintPDF(ctx, t.Dir, htmlDoc, outPDF, fontsDir)
	if err != nil {
		return "", err
	}
	p.logf("✓ 成品：%s", finalPath)
	if finalPath != outPDF {
		p.warnf("原目标文件被占用（可能正被阅读器打开），已另存为新文件")
	}
	return finalPath, nil
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

// findEngineDir 定位 engine/ 目录：LITERATURE_ENGINE → exe 旁 → exe 上级逐级
// （开发树里 exe 在 build\bin 下，仓库根才有 engine/）→ 当前目录。
func findEngineDir() (string, error) {
	if p := os.Getenv("LITERATURE_ENGINE"); p != "" {
		return p, nil
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		for i := 0; i < 4; i++ {
			p := filepath.Join(dir, "engine")
			if fi, err := os.Stat(p); err == nil && fi.IsDir() {
				return p, nil
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	if fi, err := os.Stat("engine"); err == nil && fi.IsDir() {
		return filepath.Abs("engine")
	}
	return "", fmt.Errorf("找不到 engine 目录（可设 LITERATURE_ENGINE 指定）")
}
