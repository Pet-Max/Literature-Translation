// Package app 是 Wails GUI 的绑定层：把 pipeline 核心能力暴露给前端，
// 并管理串行任务队列与事件推送。
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"literature-translation/internal/config"
	"literature-translation/internal/fonts"
	"literature-translation/internal/pipeline"
	"literature-translation/internal/version"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App Wails 绑定结构体。
type App struct {
	ctx       context.Context
	queueCtx  context.Context
	cfg       *config.Config
	pl        *pipeline.Pipeline
	queue     *TaskQueue
	logBuffer []LogLine // 环形保留最近 500 条
	logMu     sync.Mutex
}

// LogLine 日志页的一条记录。
type LogLine struct {
	Time  string `json:"time"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

// NewApp 构造（Startup 前不依赖 ctx）。
func NewApp() *App { return &App{} }

// Startup Wails 启动钩子：加载配置、构建 pipeline、启动队列 worker。
// 引擎初始化失败不阻断启动：队列照常工作，任务在运行时惰性重试引擎。
// 注意顺序：先完成 a.pl 的唯一一次写入、再启动 worker goroutine——
// 两者并发写 a.pl 会构成数据竞争（runTask 的惰性重试也写它）。
func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx
	a.queueCtx = ctx
	a.cfg = config.MustLoad()
	a.logBuffer = []LogLine{}
	a.cleanupLegacyWebviewData()
	a.queue = NewTaskQueue(a.runTaskSafe)
	a.queue.onIdle = a.afterQueueChange
	pl, err := a.newPipeline()
	if err != nil {
		a.pushLog("error", "引擎初始化失败（任务运行时会自动重试）: "+err.Error())
	} else {
		a.pl = pl
		a.pushLog("info", "应用就绪")
	}
	go a.queue.Worker(ctx)
}

// cleanupLegacyWebviewData 一次性清理 v1.0.1 及以前 WebView2 默认数据目录
// （纯内核缓存，无用户数据）。仅在新目录生效时才清——防止把回退目标删掉；
// 目录被其他实例占用时删除失败静默跳过，下次启动再试。
func (a *App) cleanupLegacyWebviewData() {
	if config.WebviewDataDir() == "" {
		return
	}
	old := config.LegacyWebviewDataDir()
	if old == "" {
		return
	}
	if _, err := os.Stat(old); err != nil {
		return
	}
	if err := os.RemoveAll(old); err == nil {
		a.pushLog("info", "已清理旧版 WebView 缓存目录")
	}
}

// newPipeline 用统一的回调集构建 pipeline。
// 回调跑在 pipeline 的进度 goroutine 里，与 worker 并发——所有 Task
// 字段写入一律走 queue.Mutate（队列锁内），推送的是值快照。
func (a *App) newPipeline() (*pipeline.Pipeline, error) {
	return pipeline.New(a.cfg, pipeline.Callbacks{
		Log: func(level, msg string) {
			line := a.pushLog(level, msg)
			runtime.EventsEmit(a.ctx, "log:line", line)
		},
		Progress: func(done, total, failed int, doneIdx, failedIdx []int, sweep bool) {
			a.setTask(func(t *Task) {
				t.Done, t.Total, t.Failed = done, total, failed
				t.DoneIdx, t.FailedIdx, t.Sweeping = doneIdx, failedIdx, sweep
				// 状态只前进：迟到的事件不能把「排版中」拉回「翻译中」
				if t.Status != StatusDone && t.Status != StatusFailed &&
					t.Status != StatusRendering {
					t.Status = StatusTranslating
				}
			})
		},
		ParseState: func(state string) {
			// MinerU 原始状态词翻译为可读中文，未知值原样透传
			name := map[string]string{
				"pending": "排队中", "running": "解析中", "done": "已完成", "failed": "失败",
			}[state]
			if name == "" {
				name = state
			}
			a.setTask(func(t *Task) { t.Stage = "解析中（" + name + "）" })
		},
		Stage: func(stage string) {
			a.setTask(func(t *Task) {
				// Stage 始终同步（失败时前端按 Stage 定位失败步骤）；
				// Status 只认状态机枚举——「拼写校准」这类子步骤只改 Stage，
				// 不往 Status 里写非枚举值
				t.Stage = stage
				switch TaskStatus(stage) {
				case StatusParsing, StatusTranslating, StatusRendering:
					t.Status = TaskStatus(stage)
				}
			})
		},
	})
}

// Shutdown 退出钩子。
func (a *App) Shutdown(ctx context.Context) {}

// BeforeClose 关闭前钩子——v1 直接允许关闭。
func (a *App) BeforeClose(ctx context.Context) bool { return false }

// ---------------------------------------------------------------- 事件

// emitTask 推送任务快照（值拷贝：前端拿到的 JSON 不与后续写入共享内存）。
func (a *App) emitTask(t Task) {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "task:update", t)
	}
}

// setTask 在队列锁内更新当前任务并推送快照。runTask 与 pipeline 回调
// 的所有 Task 字段写入统一走这里。
func (a *App) setTask(fn func(*Task)) {
	if t, ok := a.queue.Mutate(fn); ok {
		a.emitTask(t)
	}
}

// ---------------------------------------------------------------- 日志

// logEmit 记一条日志并立即推给前端。必须用 pushLog 的返回值而非回读
// logBuffer 索引：并发追加可能触发切片重建，索引读会拿到搬迁前的旧内存。
func (a *App) logEmit(level, msg string) {
	runtime.EventsEmit(a.ctx, "log:line", a.pushLog(level, msg))
}

func (a *App) pushLog(level, msg string) LogLine {
	line := LogLine{Time: time.Now().Format("15:04:05"), Level: level, Msg: msg}
	a.logMu.Lock()
	a.logBuffer = append(a.logBuffer, line)
	if len(a.logBuffer) > 500 {
		a.logBuffer = a.logBuffer[len(a.logBuffer)-500:]
	}
	a.logMu.Unlock()
	return line
}

// GetLogTail 返回最近日志（页面刷新时补齐）。
func (a *App) GetLogTail() []LogLine {
	a.logMu.Lock()
	defer a.logMu.Unlock()
	out := make([]LogLine, len(a.logBuffer))
	copy(out, a.logBuffer)
	return out
}

// ClearLog 清空日志。
func (a *App) ClearLog() { a.logMu.Lock(); a.logBuffer = []LogLine{}; a.logMu.Unlock() }

// ---------------------------------------------------------------- 任务

// runTaskSafe runTask 的 panic 兜底：任何一处 panic 都记为该任务失败
// （调用栈进日志页），不再带崩整个 Wails 进程。
func (a *App) runTaskSafe(t *Task) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("内部错误: %v", r)
			stack := string(debug.Stack())
			if r := []rune(stack); len(r) > 2000 {
				stack = string(r[:2000]) + "…"
			}
			a.setTask(func(x *Task) {
				x.Status = StatusFailed
				x.Err = err.Error()
			})
			a.logEmit("error", t.Code+" 内部错误，调用栈：\n"+stack)
		}
	}()
	return a.runTask(t)
}

// runTask 队列 worker 的执行体：按任务类型调 pipeline 并维护状态机。
// 注意：t 的可变字段（Status/Stage/Done 等）只经 setTask 在队列锁内改；
// 本函数直接读的 t.Code/Kind/SourcePath 创建后不再变化，无竞争。
func (a *App) runTask(t *Task) error {
	// 引擎此前初始化失败（如 engine 目录缺失）时惰性重试一次
	if a.pl == nil {
		pl, err := a.newPipeline()
		if err != nil {
			a.setTask(func(x *Task) {
				x.Status = StatusFailed
				x.Err = "引擎未就绪: " + err.Error()
			})
			a.logEmit("error", t.Code+" 失败: 引擎未就绪: "+err.Error())
			return err
		}
		a.pl = pl
		a.pushLog("info", "引擎已就绪")
	}
	a.setTask(func(x *Task) {
		x.Status = StatusPending
		x.Err = ""
		x.DoneIdx, x.FailedIdx, x.Sweeping = nil, nil, false
	})

	// 归档方案：每篇文献在归档根下建独立档案夹（四类产物 + _process）
	plan, err := pipeline.NewArchivePlan(a.cfg.Export.OutputDir, t.SourcePath)
	if err != nil {
		a.setTask(func(x *Task) {
			x.Status = StatusFailed
			x.Err = "建立归档失败: " + err.Error()
		})
		a.logEmit("error", t.Code+" 失败: 建立归档失败: "+err.Error())
		return err
	}

	var res *pipeline.Result
	switch t.Kind {
	case "pdf":
		a.setTask(func(x *Task) {
			x.Status = StatusParsing
			x.Stage = "MinerU 精准解析"
		})
		res, err = a.pl.RunPDF(a.queueCtx, t.SourcePath, "", plan)
	case "md":
		a.setTask(func(x *Task) {
			x.Status = StatusTranslating
			x.Stage = "翻译中"
		})
		res, err = a.pl.TranslateMD(a.queueCtx, t.SourcePath, "", plan)
	case "export":
		a.setTask(func(x *Task) {
			x.Status = StatusRendering
			x.Stage = "排版中"
		})
		res, err = a.pl.ExportMDPlan(a.queueCtx, t.SourcePath, plan)
	default:
		return fmt.Errorf("未知任务类型: %s", t.Kind)
	}

	if err != nil {
		a.setTask(func(x *Task) {
			x.Status = StatusFailed
			x.Err = err.Error()
		})
		a.logEmit("error", t.Code+" 失败: "+err.Error())
		return err
	}

	a.setTask(func(x *Task) {
		x.Status = StatusDone
		x.Stage = "完成"
		x.OutPDF = res.OutPDF
		x.ZHMD = res.ZHMD
		x.WorkDir = res.WorkDir
		x.Done = res.Done
		x.Total = res.Total
		x.Failed = len(res.Failed)
		x.RefsKept = res.KeptReferences
	})
	return nil
}

// QueueAddPDF 入队 PDF 全流程任务（可一次传多个）。
// 仅自动档收 PDF：翻译档跳过解析、导出档只出 PDF，均拒绝。
func (a *App) QueueAddPDF(paths []string, mode string) ([]Task, error) {
	if mode == "export" {
		return nil, fmt.Errorf("导出模式仅支持 MD 文件")
	}
	if mode == "translate" {
		return nil, fmt.Errorf("翻译模式跳过解析，仅收 MD；PDF 请用自动档")
	}
	mode = "auto"
	out := []Task{}
	for _, p := range paths {
		t, err := a.queue.Add("pdf", p, mode)
		if err != nil {
			return out, err
		}
		out = append(out, t)
	}
	a.afterQueueChange()
	return out, nil
}

// QueueAddMD 入队 MD 任务，按模式与语言路由：
// 翻译档 = 英文 MD 专线（中文拒收）；导出档 = MD 转 PDF 专线；
// 自动档 = 中文导出 / 英文翻译。
func (a *App) QueueAddMD(paths []string, mode string) ([]Task, error) {
	out := []Task{}
	for _, p := range paths {
		kind := "md"
		switch mode {
		case "export":
			kind = "export"
		case "translate":
			if pipeline.FileIsChinese(p) {
				a.afterQueueChange() // 此前文件可能已入队，先广播再报错，避免前端列表隐身
				return out, fmt.Errorf("翻译模式仅收英文 MD；中文 MD（%s）请用导出", filepath.Base(p))
			}
		default: // auto：中文 → 仅导出
			mode = "auto"
			if pipeline.FileIsChinese(p) {
				kind = "export"
			}
		}
		t, err := a.queue.Add(kind, p, mode)
		if err != nil {
			return out, err
		}
		out = append(out, t)
	}
	a.afterQueueChange()
	return out, nil
}

// QueueList 当前任务列表。
func (a *App) QueueList() []Task { return a.queue.List() }

// Version 应用版本号（侧栏状态显示；唯一定义在 internal/version）。
func (a *App) Version() string { return version.Version }

// QueueState 队列运行状态（前端开始/暂停按钮的依据）。
func (a *App) QueueState() map[string]any {
	return map[string]any{
		"running": a.queue.Running(),
		"paused":  a.queue.IsPaused(),
		"pending": a.queue.PendingCount(),
	}
}

// QueueStart 开始处理队列中的等待任务（跑完自动回到待开始）。
func (a *App) QueueStart() {
	a.queue.Start()
	a.afterQueueChange()
	a.logEmit("info", "队列已开始")
}

// QueuePause 暂停队列：正在跑的这篇完整跑完，之后不再开始下一篇。
func (a *App) QueuePause() {
	a.queue.Pause()
	a.afterQueueChange()
	a.logEmit("info", "队列已暂停：当前任务完成后停止")
}

// QueueStartOne 只开始一个等待中的任务（提到队首，跑完即停，其余任务不动）。
func (a *App) QueueStartOne(id string) error {
	if err := a.queue.StartOne(id); err != nil {
		return err
	}
	a.afterQueueChange()
	a.logEmit("info", "已开始单个任务：跑完这一篇即暂停")
	return nil
}

// QueueRetry 重试一个任务。
func (a *App) QueueRetry(id string) (Task, error) {
	t, err := a.queue.Retry(id)
	if err == nil {
		a.afterQueueChange()
	}
	return t, err
}

// QueueRemove 取消一个等待中的任务。
func (a *App) QueueRemove(id string) error {
	err := a.queue.Remove(id)
	if err == nil {
		a.afterQueueChange()
	}
	return err
}

// QueueClearFinished 清掉已完成/失败的任务卡片。
func (a *App) QueueClearFinished() []Task {
	tasks := a.queue.ClearFinished()
	a.afterQueueChange()
	return tasks
}

func (a *App) afterQueueChange() {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "queue:change", a.queue.List())
	}
}

// ---------------------------------------------------------------- 配置

// GetConfig 返回当前配置（前端表单初值）。
func (a *App) GetConfig() *config.Config { return a.cfg }

// SaveConfig 用前端提交的配置原地更新并落盘（pipeline 共享指针，立即生效）。
// 凭据无条件采纳提交值：前端表单是唯一写入方且总是回传完整值
// （collectSettings 提交 token/key），清空保存即删除凭据。
// 字体注册表与已选字体由后端保留（前端表单不管理这两项）。
func (a *App) SaveConfig(newCfg *config.Config) error {
	if newCfg == nil {
		return fmt.Errorf("配置为空")
	}
	userFonts := a.cfg.UserFonts
	userFontSel := a.cfg.Export.UserFont
	*a.cfg = *newCfg
	a.cfg.UserFonts = userFonts
	a.cfg.Export.UserFont = userFontSel
	err := a.cfg.Save()
	if err == nil {
		a.pushLog("info", "配置已保存")
	}
	return err
}

// IronRules 系统默认规则文本（前端「恢复默认」的还原源）。
func (a *App) IronRules() string { return pipeline.IronRules }

// SaveRules 只落盘翻译规则（多套预设 + 生效指向），与「保存设置」解耦。
// 前端的规则卡保存、就地改名、切换生效、删除、新建都走这里。
func (a *App) SaveRules(active string, presets []config.RulePreset) error {
	if len(presets) == 0 {
		return fmt.Errorf("至少保留一套规则预设")
	}
	a.cfg.Rules.Active = active
	a.cfg.Rules.Presets = presets
	return a.cfg.Save()
}

// PickFiles 打开原生多选文件对话框（PDF / MD）。取消或未选返回空切片。
func (a *App) PickFiles() ([]string, error) {
	paths, err := runtime.OpenMultipleFilesDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择文献（PDF / MD）",
		Filters: []runtime.FileFilter{
			{DisplayName: "文献 (*.pdf;*.md)", Pattern: "*.pdf;*.md"},
		},
	})
	if err != nil {
		return nil, err
	}
	return paths, nil
}

// ---------------------------------------------------------------- 字体

// FontList 已导入字体列表。绑定层不变式：永不返回 nil——nil 会被序列化
// 成 null，前端对返回值直接 .map 会崩。
func (a *App) FontList() []config.UserFont {
	if a.cfg.UserFonts == nil {
		return []config.UserFont{}
	}
	return a.cfg.UserFonts
}

// FontAdd 导入字体文件（三连检）。
func (a *App) FontAdd(path string) (config.UserFont, error) {
	uf := config.UserFont{}
	info, err := fonts.Validate(path)
	if err != nil {
		return uf, err
	}
	names := make([]string, 0, len(a.cfg.UserFonts))
	for _, u := range a.cfg.UserFonts {
		names = append(names, u.InternalName)
	}
	internal := fonts.InternalName(fonts.NextNumber(names))
	info, err = fonts.Import(path, internal)
	if err != nil {
		return uf, err
	}
	uf = config.UserFont{InternalName: internal, DisplayName: info.DisplayName, FileName: info.FileName}
	a.cfg.UserFonts = append(a.cfg.UserFonts, uf)
	if err := a.cfg.Save(); err != nil {
		return uf, err
	}
	a.pushLog("info", fmt.Sprintf("字体已导入: %s", uf.DisplayName))
	return uf, nil
}

// SetFontPreset 切换正文字体预设并落盘。preset: lxgw | songti | yahei |
// custom（userFont 为导入字体内部名）。字体栈与预设同步更新。
func (a *App) SetFontPreset(preset, userFont string) error {
	switch preset {
	case "songti":
		a.cfg.Export.Font = `"Times New Roman", "SimSun", "Songti SC", "STSong", serif`
	case "yahei":
		a.cfg.Export.Font = `"Arial", "Microsoft YaHei", "PingFang SC", sans-serif`
	case "custom":
		if userFont == "" {
			return fmt.Errorf("custom 预设需要字体内部名")
		}
	default: // lxgw
		preset = "lxgw"
		a.cfg.Export.Font = config.Default().Export.Font
	}
	a.cfg.Export.FontPreset = preset
	if userFont == "builtin" {
		userFont = ""
	}
	if userFont != "" && a.cfg.FindUserFont(userFont) == nil {
		return fmt.Errorf("未找到字体 %s", userFont)
	}
	a.cfg.Export.UserFont = userFont
	return a.cfg.Save()
}

// FetchModels 从上游 OpenAI 兼容接口拉取模型列表（GET {base_url}/models）。
func (a *App) FetchModels() ([]string, error) {
	base := strings.TrimRight(strings.TrimSpace(a.cfg.LLM.BaseURL), "/")
	if base == "" || strings.TrimSpace(a.cfg.LLM.APIKey) == "" {
		return nil, fmt.Errorf("请先填写模型 API 地址与 API Key")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(a.cfg.LLM.APIKey))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求上游失败: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("上游返回 %d: %s", resp.StatusCode, string(body[:min(len(body), 180)]))
	}
	// 兼容两种形态：{"data":[{"id":...}]} 与 顶层数组 [{"id":...}] / ["name"]
	var wrap struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	var ids []string
	if err := json.Unmarshal(body, &wrap); err == nil && len(wrap.Data) > 0 {
		for _, m := range wrap.Data {
			if m.ID != "" {
				ids = append(ids, m.ID)
			}
		}
	} else {
		var arr []json.RawMessage
		if err := json.Unmarshal(body, &arr); err == nil {
			for _, raw := range arr {
				var m struct {
					ID string `json:"id"`
				}
				if json.Unmarshal(raw, &m) == nil && m.ID != "" {
					ids = append(ids, m.ID)
					continue
				}
				var name string
				if json.Unmarshal(raw, &name) == nil && name != "" {
					ids = append(ids, name)
				}
			}
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("上游未返回任何模型")
	}
	sort.Strings(ids)
	return ids, nil
}

const releaseAPIURL = "https://api.github.com/repos/Pet-Max/Literature-Translation/releases/latest"

// UpdateInfo 检查更新的结果。
type UpdateInfo struct {
	HasUpdate bool   `json:"has_update"`
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	URL       string `json:"url"`
}

// CheckUpdate 查询 GitHub 最新 Release 并与当前版本比较。手动触发：
// 不后台轮询、无鉴权、无遥测；不做应用内自动更新，更新走 Release 页手动下载。
func (a *App) CheckUpdate() (*UpdateInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseAPIURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("无法连接 GitHub（检查网络后重试）: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub 返回 %d", resp.StatusCode)
	}
	var rel struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil || rel.TagName == "" {
		return nil, fmt.Errorf("响应解析失败")
	}
	info := &UpdateInfo{
		Current: version.Version,
		Latest:  strings.TrimSpace(rel.TagName),
		URL:     rel.HTMLURL,
	}
	info.HasUpdate = compareSemver(info.Latest, info.Current) > 0
	return info, nil
}

// compareSemver 比较 vX.Y.Z 形式版本号：>0 表示 a 更新，0 相等，<0 更旧。
func compareSemver(a, b string) int {
	num := func(v string) (n [3]int) {
		v = strings.TrimPrefix(strings.TrimSpace(v), "v")
		for i, p := range strings.SplitN(v, ".", 3) {
			n[i], _ = strconv.Atoi(strings.TrimSpace(p))
		}
		return
	}
	an, bn := num(a), num(b)
	for i := 0; i < 3; i++ {
		if an[i] != bn[i] {
			if an[i] > bn[i] {
				return 1
			}
			return -1
		}
	}
	return 0
}

// TestLLM 测试翻译模型接口连通性：复用 /models 拉取，返回可用模型数。
func (a *App) TestLLM() (int, error) {
	models, err := a.FetchModels()
	if err != nil {
		return 0, err
	}
	return len(models), nil
}

// TestMinerU 测试 MinerU Token 有效性：以假 batch id 查询，借鉴权层差异判定
// （Token 无效 → 401/403/A0202；有效 → 业务层"任务不存在"类错误，同样证明通过鉴权）。
func (a *App) TestMinerU() (string, error) {
	token := strings.TrimSpace(a.cfg.MinerU.Token)
	if token == "" {
		return "", fmt.Errorf("请先填写 API Token")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	url := "https://mineru.net/api/v4/extract-results/batch/conn-test"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("网络不通: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	text := string(body)
	if strings.Contains(text, `"code":[401,403]`) || strings.Contains(text, "A0202") ||
		resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", fmt.Errorf("Token 无效（鉴权被拒，HTTP %d）", resp.StatusCode)
	}
	return "Token 有效", nil
}

// PickFontFile 打开单选字体文件对话框（ttf / otf）。取消返回空串。
func (a *App) PickFontFile() (string, error) {
	return runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   "导入字体（ttf / otf）",
		Filters: []runtime.FileFilter{{DisplayName: "字体文件 (*.ttf;*.otf)", Pattern: "*.ttf;*.otf"}},
	})
}

// ---------------------------------------------------------------- 其他

// OpenPath 在资源管理器中打开文件所在位置（或直接打开文件夹）。
func (a *App) OpenPath(path string) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	if filepath.Ext(path) != "" && filepath.Ext(path) != "." {
		return exec.Command("explorer", "/select,", path).Start()
	}
	return exec.Command("explorer", path).Start()
}
