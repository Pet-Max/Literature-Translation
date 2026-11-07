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
	"sync/atomic"
	"time"

	"literature-translation/internal/config"
	"literature-translation/internal/fonts"
	"literature-translation/internal/pipeline"
	"literature-translation/internal/version"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App Wails 绑定结构体。
type App struct {
	ctx           context.Context
	queueCtx      context.Context
	queueCancel   context.CancelFunc
	workerDone    chan struct{}
	cfg           atomic.Pointer[config.Config]
	cfgMu         sync.Mutex
	configBlocked atomic.Bool
	frontendReady atomic.Bool
	closeAllowed  atomic.Bool
	pl            *pipeline.Pipeline
	queue         *TaskQueue
	logBuffer     []LogLine // 环形保留最近 500 条
	logMu         sync.Mutex
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
// 先初始化唯一的回调模板，再启动 worker；每个任务分别取得配置和字体快照。
func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx
	a.queueCtx, a.queueCancel = context.WithCancel(ctx)
	a.workerDone = make(chan struct{})
	loaded, loadErr := config.Load()
	if loadErr != nil {
		loaded = config.Default()
		a.configBlocked.Store(true)
	}
	a.cfg.Store(loaded)
	a.logBuffer = []LogLine{}
	a.cleanupLegacyWebviewData()
	a.queue = NewTaskQueue(a.runTaskSafe)
	a.queue.onIdle = a.afterQueueChange
	a.pl, _ = a.newPipeline()
	a.pushLog("info", "应用就绪")
	if loadErr != nil {
		a.pushLog("error", "配置读取失败，请在设置页恢复；原文件已保留")
	}
	go func() { defer close(a.workerDone); a.queue.Worker(a.queueCtx) }()
}

// cleanupLegacyWebviewData 一次性清理早期版本遗留的 WebView2 默认数据目录
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

// newPipeline constructs the callback template; each task supplies its own config.
// 回调跑在 pipeline 的进度 goroutine 里，与 worker 并发——所有 Task
// 字段写入一律走 queue.Mutate（队列锁内），推送的是值快照。
func (a *App) newPipeline() (*pipeline.Pipeline, error) {
	return pipeline.New(nil, pipeline.Callbacks{
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
func (a *App) Shutdown(ctx context.Context) { a.stopQueue() }

func (a *App) stopQueue() {
	if a.queueCancel != nil {
		a.queueCancel()
	}
	if a.workerDone != nil {
		<-a.workerDone
	}
}

// BeforeClose waits for the frontend's pending settings before normal shutdown.
func (a *App) BeforeClose(ctx context.Context) bool {
	if !a.frontendReady.Load() || a.closeAllowed.Load() {
		return false
	}
	runtime.EventsEmit(ctx, "settings:before-close")
	return true
}

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
	if a.configBlocked.Load() {
		return fmt.Errorf("请先恢复配置")
	}
	a.setTask(func(x *Task) {
		x.Status = StatusPending
		x.Err = ""
		x.DoneIdx, x.FailedIdx, x.Sweeping = nil, nil, false
	})

	// One immutable snapshot for every stage of this task.
	pl := a.taskPipeline()
	if err := pl.PrepareTask(true); err != nil {
		return err
	}
	defer pl.Close()
	taskCfg := pl.Cfg
	if err := validateTaskSettings(t.Kind, taskCfg); err != nil {
		a.setTask(func(x *Task) { x.Status = StatusFailed; x.Err = err.Error() })
		a.logEmit("error", t.Code+" 未能开始: "+err.Error())
		return err
	}

	// 归档方案：每篇文献在归档根下建独立档案夹（四类产物 + _process）
	plan, err := pipeline.NewArchivePlan(taskCfg.Export.OutputDir, t.SourcePath)
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
		// 上次失败于排版阶段的重试：译文已归档且配置未变时仅重导出，
		// 不重烧 MinerU / LLM 配额；守卫未过则照旧走全流程
		if t.ResumeExport {
			var attempted bool
			res, attempted, err = pl.ResumeExport(a.queueCtx, plan)
			if attempted {
				break // 短路已成或导出本身失败（err 走统一失败路径），不再全流程
			}
		}
		a.setTask(func(x *Task) {
			x.Status = StatusParsing
			x.Stage = "MinerU 精准解析"
		})
		res, err = pl.RunPDF(a.queueCtx, t.SourcePath, "", plan)
	case "md":
		// 同 pdf：MD 任务失败于排版阶段的重试也可仅重导出
		if t.ResumeExport {
			var attempted bool
			res, attempted, err = pl.ResumeExport(a.queueCtx, plan)
			if attempted {
				break
			}
		}
		a.setTask(func(x *Task) {
			x.Status = StatusTranslating
			x.Stage = "翻译中"
		})
		res, err = pl.TranslateMD(a.queueCtx, t.SourcePath, "", plan)
	case "export":
		a.setTask(func(x *Task) {
			x.Status = StatusRendering
			x.Stage = "排版中"
		})
		res, err = pl.ExportMDPlan(a.queueCtx, t.SourcePath, plan)
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

// taskPipeline binds a detached snapshot without changing the reusable callback set.
func (a *App) taskPipeline() *pipeline.Pipeline {
	p := *a.pl
	p.Cfg = a.GetConfig()
	return &p
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
func (a *App) QueueStart() error {
	if a.configBlocked.Load() {
		return fmt.Errorf("请先恢复配置")
	}
	cfg, err := a.latestConfig()
	if err != nil {
		return err
	}
	for _, t := range a.queue.List() {
		if t.Status == StatusPending {
			if err := validateTaskSettings(t.Kind, cfg); err != nil {
				return err
			}
		}
	}
	a.queue.Start()
	a.afterQueueChange()
	a.logEmit("info", "队列已开始")
	return nil
}

// QueuePause 暂停队列：正在跑的这篇完整跑完，之后不再开始下一篇。
func (a *App) QueuePause() {
	a.queue.Pause()
	a.afterQueueChange()
	a.logEmit("info", "队列已暂停：当前任务完成后停止")
}

// QueueStartOne 只开始一个等待中的任务（提到队首，跑完即停，其余任务不动）。
func (a *App) QueueStartOne(id string) error {
	if a.configBlocked.Load() {
		return fmt.Errorf("请先恢复配置")
	}
	cfg, err := a.latestConfig()
	if err != nil {
		return err
	}
	for _, t := range a.queue.List() {
		if t.ID == id {
			if err := validateTaskSettings(t.Kind, cfg); err != nil {
				return err
			}
		}
	}
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
func (a *App) GetConfig() *config.Config { return a.cfg.Load().Clone() }

// IronRules 系统默认规则文本（前端「恢复默认」的还原源）。
func (a *App) IronRules() string { return pipeline.IronRules }

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
	list := a.GetConfig().UserFonts
	if list == nil {
		return []config.UserFont{}
	}
	return list
}

// FontAdd 导入字体文件（三连检）。
func (a *App) FontAdd(path string) (config.UserFont, error) {
	uf := config.UserFont{}
	imported := false
	_, err := a.updateConfigWithRollback(func(c *config.Config) error {
		info, err := fonts.Validate(path)
		if err != nil {
			return err
		}
		names := []string{}
		for _, u := range c.UserFonts {
			names = append(names, u.InternalName)
		}
		internal := fonts.InternalName(fonts.NextNumber(names))
		info, err = fonts.Import(path, internal)
		if err != nil {
			return err
		}
		uf = config.UserFont{InternalName: internal, DisplayName: info.DisplayName, FileName: info.FileName}
		imported = true
		c.UserFonts = append(c.UserFonts, uf)
		return nil
	}, func() {
		if imported {
			_ = fonts.Remove(uf.FileName)
		}
	})
	return uf, err
}

// SetFontPreset 切换正文字体预设并落盘。preset: lxgw | songti | yahei |
// custom（userFont 为导入字体内部名）。字体栈与预设同步更新。
func (a *App) SetFontPreset(preset, userFont string) error {
	_, err := a.updateConfig(func(nc *config.Config) error {
		switch preset {
		case "songti":
			nc.Export.Font = `"Times New Roman", "SimSun", "Songti SC", "STSong", serif`
		case "yahei":
			nc.Export.Font = `"Arial", "Microsoft YaHei", "PingFang SC", sans-serif`
		case "custom":
			if userFont == "" {
				return fmt.Errorf("custom 预设需要字体内部名")
			}
		default: // lxgw
			preset = "lxgw"
			nc.Export.Font = config.Default().Export.Font
		}
		nc.Export.FontPreset = preset
		if userFont == "builtin" {
			userFont = ""
		}
		if userFont != "" && nc.FindUserFont(userFont) == nil {
			return fmt.Errorf("未找到字体 %s", userFont)
		}
		nc.Export.UserFont = userFont
		return nil
	})
	return err
}

// FetchModelsWithConfig 从上游 OpenAI 兼容接口拉取模型列表（GET {base_url}/models）。
func (a *App) FetchModelsWithConfig(baseURL, apiKey string) ([]string, error) {
	cfg := config.Default()
	cfg.LLM.BaseURL = baseURL
	cfg.LLM.APIKey = apiKey
	cfg.Normalize()
	base := cfg.LLM.BaseURL
	if base == "" || cfg.LLM.APIKey == "" {
		return nil, fmt.Errorf("请先填写模型 API 地址与 API Key")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("模型 API 地址格式无效")
	}
	req.Header.Set("Authorization", "Bearer "+cfg.LLM.APIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求上游失败，请检查地址、网络或证书")
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("模型列表接口返回 HTTP %d，请检查地址、凭据或服务状态", resp.StatusCode)
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
		Current: version.Display(),
		Latest:  strings.TrimSpace(rel.TagName),
		URL:     rel.HTMLURL,
	}
	info.HasUpdate = compareSemver(info.Latest, version.Version) > 0
	return info, nil
}

// compareSemver 比较 vX.Y.Z 形式版本号：>0 表示 a 更新，0 相等，<0 更旧。
func compareSemver(a, b string) int {
	num := func(v string) (n [3]int) {
		v = strings.TrimPrefix(strings.TrimSpace(v), "v")
		v = strings.SplitN(strings.SplitN(v, "+", 2)[0], "-", 2)[0]
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

// parseConnTestCode 解析 conn-test 响应的 code 字段（标量数字或数组均可）。
// 返回 (无效, 有效) 两个判定：含 401/403 → 无效；含 200/0 → 有效；
// code 为 null/缺失 → 不确认有效；解析失败 → 双 false（调用方报「无法识别」）。
func parseConnTestCode(raw json.RawMessage) (bad, ok bool) {
	t := strings.TrimSpace(string(raw))
	if t == "" || t == "null" {
		return false, false
	}
	var codes []json.RawMessage
	if t[0] == '[' {
		if json.Unmarshal(raw, &codes) != nil {
			return false, false
		}
	} else {
		codes = []json.RawMessage{raw}
	}
	unknown := false
	for _, rawCode := range codes {
		var n float64
		if string(rawCode) == "null" || json.Unmarshal(rawCode, &n) != nil {
			unknown = true
			continue
		}
		switch n {
		case 401, 403:
			bad = true
		case 200, 0:
			ok = true
		default:
			unknown = true
		}
	}
	return bad, ok && !unknown
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
