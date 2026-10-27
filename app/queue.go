package app

import (
	"fmt"
	"regexp"
	"sync"
	"time"
)

// TaskStatus 任务状态机：等待 → 解析中 → 翻译中 → 排版中 → 完成/失败。
type TaskStatus string

const (
	StatusPending     TaskStatus = "等待"
	StatusParsing     TaskStatus = "解析中"
	StatusTranslating TaskStatus = "翻译中"
	StatusRendering   TaskStatus = "排版中"
	StatusDone        TaskStatus = "完成"
	StatusFailed      TaskStatus = "失败"
)

// Task 队列中的一个翻译任务。
type Task struct {
	ID         string     `json:"id"`
	Code       string     `json:"code"`
	Kind       string     `json:"kind"` // pdf | md | export（仅排版）
	Mode       string     `json:"mode"` // 入队时的模式档位：auto | translate | export
	SourcePath string     `json:"source_path"`
	Status     TaskStatus `json:"status"`
	Stage      string     `json:"stage"`
	Done       int        `json:"done"`
	Total      int        `json:"total"`
	Failed     int        `json:"failed"`
	DoneIdx    []int      `json:"done_idx"`   // 已完成段的 0 基编号（前端逐段着色）
	FailedIdx  []int      `json:"failed_idx"` // 失败段的 0 基编号
	Sweeping   bool       `json:"sweeping"`   // 正处收尾补翻阶段
	RefsKept   int        `json:"refs_kept"`
	OutPDF     string     `json:"out_pdf"`
	ZHMD       string     `json:"zh_md"`
	WorkDir    string     `json:"work_dir"`
	Err        string     `json:"err"`
	AddedAt    time.Time  `json:"added_at"`
}

var codeRe = regexp.MustCompile(`^[A-Za-z]+\d+`)

// TaskQueue 串行任务队列：一次处理一篇，处理函数由 App 注入。
// 手动开始模式：入队只排队不执行。Start 后按序跑完（跑完即回到待开始）；
// StartOne 只跑指定的一篇；Pause 后当前这篇跑完即停，等待中的任务不动。
type TaskQueue struct {
	mu      sync.Mutex
	tasks   []*Task
	current *Task
	runner  func(*Task) error
	notify  chan struct{}
	paused  bool
	soloID  string // 非空 = 单跑模式：只跑这一个任务，跑完即回到待开始
	onIdle  func() // 每篇跑完后的回调（App 用于推送队列状态变化）
}

// NewTaskQueue 创建队列（调用 Worker 启动处理循环）。初始为待开始态。
func NewTaskQueue(runner func(*Task) error) *TaskQueue {
	return &TaskQueue{runner: runner, notify: make(chan struct{}, 8), paused: true}
}

// Start 开始/继续处理队列中的等待任务（一次性触发，跑到队列为空）。
func (q *TaskQueue) Start() {
	q.mu.Lock()
	q.paused = false
	q.soloID = ""
	q.mu.Unlock()
	q.wake()
}

// StartOne 只开始指定的一篇：提到队首，跑完即回到待开始，其余等待任务不动。
func (q *TaskQueue) StartOne(id string) error {
	q.mu.Lock()
	idx := -1
	for i, t := range q.tasks {
		if t.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 || q.tasks[idx].Status != StatusPending {
		q.mu.Unlock()
		return fmt.Errorf("任务不在等待状态")
	}
	t := q.tasks[idx]
	q.tasks = append(q.tasks[:idx], q.tasks[idx+1:]...)
	q.tasks = append([]*Task{t}, q.tasks...)
	q.paused = false
	q.soloID = id
	q.mu.Unlock()
	q.wake()
	return nil
}

// Pause 暂停：正在跑的这篇自然跑完，之后不再取新任务。
func (q *TaskQueue) Pause() {
	q.mu.Lock()
	q.paused = true
	q.mu.Unlock()
}

// IsPaused 队列是否处于暂停/待开始状态。
func (q *TaskQueue) IsPaused() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.paused
}

// PendingCount 等待中的任务数。
func (q *TaskQueue) PendingCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for _, t := range q.tasks {
		if t.Status == StatusPending {
			n++
		}
	}
	return n
}

// Add 入队一个任务（同源路径去重）。mode 记录入队时的模式档位。
func (q *TaskQueue) Add(kind, path, mode string) (Task, error) {
	q.mu.Lock()
	for _, t := range q.tasks {
		if t.SourcePath == path && (t.Status == StatusPending || t == q.current) {
			q.mu.Unlock()
			return *t, nil
		}
	}
	code := codeRe.FindString(baseName(path))
	if code == "" {
		code = fmt.Sprintf("%d", time.Now().UnixNano()%100000)
	}
	t := &Task{
		ID:         fmt.Sprintf("T%d", time.Now().UnixNano()),
		Code:       code,
		Kind:       kind,
		Mode:       mode,
		SourcePath: path,
		Status:     StatusPending,
		Stage:      "待开始",
		AddedAt:    time.Now(),
	}
	q.tasks = append(q.tasks, t)
	q.mu.Unlock()
	q.wake()
	return *t, nil
}

// List 全部任务快照（值拷贝）。返回快照而非指针：前端绑定 goroutine
// 序列化 JSON 时不能与进度回调的并发写入共享同一块内存。
// 约定：Task 的切片字段（DoneIdx/FailedIdx）只整体替换、从不原地改，
// 因此结构体浅拷贝即安全。
func (q *TaskQueue) List() []Task {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]Task, len(q.tasks))
	for i, t := range q.tasks {
		out[i] = *t
	}
	return out
}

// Running 是否有任务正在处理。
func (q *TaskQueue) Running() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.current != nil
}

// Mutate 在队列锁内对当前任务执行变更，返回变更后的值快照。
// 所有跨 goroutine 的 Task 字段写入（进度/阶段回调、runTask）都走这里，
// 队列锁由此同时保护任务本体，不再只保护切片与 current 指针。
func (q *TaskQueue) Mutate(fn func(*Task)) (Task, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.current == nil {
		return Task{}, false
	}
	fn(q.current)
	return *q.current, true
}

// ClearFinished 清掉已完成/失败的任务卡片。
func (q *TaskQueue) ClearFinished() []Task {
	q.mu.Lock()
	kept := q.tasks[:0]
	for _, t := range q.tasks {
		if t.Status != StatusDone && t.Status != StatusFailed {
			kept = append(kept, t)
		}
	}
	q.tasks = kept
	q.mu.Unlock()
	q.wake()
	return q.List()
}

// Remove 从队列移除一个等待中的任务（进行中的不可取消）。
func (q *TaskQueue) Remove(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, t := range q.tasks {
		if t.ID == id {
			if t == q.current {
				return fmt.Errorf("任务正在处理，暂不支持取消")
			}
			// 等待/完成/失败均可单独移除（仅进行中不可）
			q.tasks = append(q.tasks[:i], q.tasks[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("任务 %s 不存在", id)
}

// Retry 把一个失败/完成任务重置为等待并重新处理。
func (q *TaskQueue) Retry(id string) (Task, error) {
	q.mu.Lock()
	var t *Task
	for _, cand := range q.tasks {
		if cand.ID == id {
			t = cand
			break
		}
	}
	if t == nil {
		q.mu.Unlock()
		return Task{}, fmt.Errorf("任务 %s 不存在", id)
	}
	if t == q.current {
		// 运行中的任务改回等待会被 runner 结束时的终态覆盖（看似无效的竞态），直接拒绝
		q.mu.Unlock()
		return Task{}, fmt.Errorf("任务正在处理，跑完后再重跑")
	}
	t.Status = StatusPending
	t.Stage = "重新排队"
	t.Err = ""
	t.Done, t.Total, t.Failed = 0, 0, 0
	t.RefsKept = 0
	// 上次产物一并清空：重排队的卡片不该显示旧成品路径
	t.OutPDF, t.ZHMD, t.WorkDir = "", "", ""
	t.DoneIdx, t.FailedIdx, t.Sweeping = nil, nil, false
	q.mu.Unlock()
	q.wake()
	return *t, nil
}

func baseName(path string) string {
	i := len(path) - 1
	for i >= 0 && path[i] != '/' && path[i] != '\\' {
		i--
	}
	return path[i+1:]
}

func (q *TaskQueue) wake() {
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

func (q *TaskQueue) nextPending() *Task {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, t := range q.tasks {
		if t.Status != StatusPending {
			continue
		}
		if q.soloID != "" && t.ID != q.soloID {
			continue // 单跑模式：其余任务跳过
		}
		return t
	}
	return nil
}

// Worker 串行处理循环（ctx 取消时退出）。仅在未暂停时取任务；
// 每篇跑完后回调 onIdle（供 App 推送队列状态，刷新开始/暂停按钮）。
func (q *TaskQueue) Worker(ctx interface{ Done() <-chan struct{} }) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-q.notify:
			for {
				if q.IsPaused() {
					break
				}
				t := q.nextPending()
				if t == nil {
					break
				}
				q.mu.Lock()
				q.current = t
				q.mu.Unlock()
				if err := q.runner(t); err != nil {
					q.mu.Lock()
					t.Status = StatusFailed
					t.Err = err.Error()
					q.mu.Unlock()
				}
				q.mu.Lock()
				q.current = nil
				if q.soloID != "" && q.soloID == t.ID { // 单跑模式：跑完这一篇即回到待开始
					q.soloID = ""
					q.paused = true
				}
				q.mu.Unlock()
				if q.onIdle != nil {
					q.onIdle()
				}
			}
			// 队列清空或被暂停：回到待开始态，新入队的任务等下一次「开始」
			q.mu.Lock()
			q.paused = true
			q.mu.Unlock()
		}
	}
}
