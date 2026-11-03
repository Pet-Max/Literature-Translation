// engine.go — 翻译主循环：切段 → 参考文献甄别 → 并发翻译（质量重试）
// → 收尾补翻 → 组装写盘。旧入口保留，管线通过 context 传递取消。
//
// 事件契约（前端依赖）：
//
//	{"event": "progress", "done": 12, "total": 34, "failed": 1,
//	 "done_idx": [0,1,2], "failed_idx": [5], "sweep": false}
//
// done 发「成功数」而非「已处理数」：与最终响应 done=total-failed 同口径。
package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Event 翻译进度事件，由引擎直接发送。
type Event struct {
	Done      int   `json:"done"`
	Total     int   `json:"total"`
	Failed    int   `json:"failed"`
	DoneIdx   []int `json:"done_idx"`
	FailedIdx []int `json:"failed_idx"`
	Sweep     bool  `json:"sweep"`
}

// 质量重试与收尾补翻参数。
const (
	qualityAttempts = 4 // 1 次初译 + 3 次质量重试
	sweepAttempts   = 2 // 收尾补翻时每段的重试次数
	sweepPause      = 2 // 补翻前的错峰秒数
)

// SegmentFailure 最终翻译失败的段落。
type SegmentFailure struct {
	I       int    `json:"i"`
	Reasons string `json:"reasons"`
}

// LLMOptions LLM 连接参数。
type LLMOptions struct {
	BaseURL     string
	APIKey      string
	Model       string
	Concurrency int
}

// TranslateOptions 一次翻译任务的全部输入。MDText 与 MDPath 二选一。
type TranslateOptions struct {
	MDText         string
	MDPath         string
	OutPath        string
	ProgressPath   string // 空 = OutPath + ".progress.jsonl"
	Glossary       [][]string
	SystemRules    string
	UserRules      string
	Bilingual      bool
	SkipReferences bool
	LLM            LLMOptions
	OnEvent        func(Event)                   // 进度事件回调（可为 nil）
	Logf           func(format string, a ...any) // 诊断日志回调（对齐引擎 stderr，可为 nil）
}

// TranslateResult 是翻译调用的结果结构。
type TranslateResult struct {
	Total          int              `json:"total"`
	Done           int              `json:"done"`
	Failed         []SegmentFailure `json:"failed"`
	KeptReferences int              `json:"kept_references"`
	Warnings       []string         `json:"warnings"`
	OutPath        string           `json:"out_path"`
}

// 参考文献甄别（engine.py make_reference_detector）。
var (
	citeHeadRe       = regexp.MustCompile(`^\[(\d{1,3})\]\s`)
	authorInitialsRe = regexp.MustCompile(`\b[A-Z]\. (\s*[A-Z]\. )+`)
	yearRe           = regexp.MustCompile(`\((?:19|20)\d{2}[a-z]?\)`)
)

// BuildCacheKey 任务指纹：规则/术语/开关/模型任一变化即弃用旧进度。
// 与历史版本哈希算法逐字节一致（键序 bilingual/model/skip_refs/system），
// 旧归档进度文件的指纹因此继续可用。
func BuildCacheKey(system string, bilingual, skipRefs bool, model string) string {
	return ChunkHash(`{"bilingual": ` + jsonBool(bilingual) +
		`, "model": ` + jsonString(model) +
		`, "skip_refs": ` + jsonBool(skipRefs) +
		`, "system": ` + jsonString(system) + `}`)
}

// jsonBool 布尔值的 JSON 字面量。
func jsonBool(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// SelfCheck 引擎自检（预检用）：核心纯函数冒烟，不发起任何网络调用。
func SelfCheck() error {
	if got := len(Split("# a\n\ntext", DefaultMaxChars)); got != 1 {
		return fmt.Errorf("segmenter 异常: %d 段", got)
	}
	if BuildSystem(nil, "", "") == "" || ChunkHash("selfcheck") == "" {
		return fmt.Errorf("prompt/checkpoints 异常")
	}
	return nil
}

// refDetector 参考文献甄别器：段首 [N] + 编号单调递增 + 文献特征 → 文献条目。
// 防误伤三重保险：编号必须严格递增、段内须有作者缩写或年份、条目长度受限。
// 判定必须在切段后、进入线程池前按顺序单线程执行（内部有状态）。
type refDetector struct {
	enabled bool
	last    int
	started bool
}

func newRefDetector(enabled bool) *refDetector { return &refDetector{enabled: enabled} }

func (d *refDetector) detect(chunk string) bool {
	if !d.enabled {
		return false
	}
	m := citeHeadRe.FindStringSubmatch(strings.TrimLeftFunc(chunk, unicode.IsSpace))
	if m == nil {
		return false
	}
	n, _ := strconv.Atoi(m[1])
	if d.started {
		if !(d.last < n && n <= d.last+3) {
			return false
		}
	}
	if runeLen(chunk) > 800 {
		return false
	}
	if authorInitialsRe.MatchString(chunk) || yearRe.MatchString(chunk) {
		d.started = true
		d.last = n
		return true
	}
	return false
}

// TranslateContext 执行完整翻译并写出译文 MD；ctx 取消会中止全部阶段。
func TranslateContext(ctx context.Context, opts TranslateOptions) (*TranslateResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	text := opts.MDText
	if text == "" && opts.MDPath != "" {
		data, err := os.ReadFile(opts.MDPath)
		if err != nil {
			return nil, err
		}
		text = string(data)
	}
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("payload 需要 md_text 或 md_path")
	}

	logf := func(format string, a ...any) {
		if opts.Logf != nil {
			opts.Logf(format, a...)
		}
	}
	emit := func(ev Event) {
		if opts.OnEvent != nil {
			opts.OnEvent(ev)
		}
	}

	chunks := Split(text, DefaultMaxChars)
	total := len(chunks)
	logf("切段完成：%d 段", total)

	// 参考文献甄别：线程池前按序单线程判定（内部有编号状态）
	detector := newRefDetector(opts.SkipReferences)
	refFlags := make([]bool, total)
	nRefs := 0
	for i, c := range chunks {
		refFlags[i] = detector.detect(c)
		if refFlags[i] {
			nRefs++
		}
	}
	if nRefs > 0 {
		logf("识别参考文献条目 %d 段，按原文保留不翻译", nRefs)
	}

	system := BuildSystem(opts.Glossary, opts.SystemRules, opts.UserRules)
	cacheKey := BuildCacheKey(system, opts.Bilingual, opts.SkipReferences, opts.LLM.Model)
	progressPath := opts.ProgressPath
	if progressPath == "" {
		progressPath = opts.OutPath + ".progress.jsonl"
	}
	progress, err := NewProgress(progressPath, cacheKey)
	if err != nil {
		return nil, err
	}
	reused := 0
	for i := 0; i < total; i++ {
		if progress.Reusable(i, chunks[i]) {
			reused++
		}
	}
	if reused > 0 {
		logf("断点续跑：复用已完成的 %d 段（源段哈希匹配）", reused)
	}

	client := NewLLMClient(opts.LLM.BaseURL, opts.LLM.APIKey, opts.LLM.Model, 180.0, 4)
	defer client.Close()
	concurrency := opts.LLM.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > 8 {
		concurrency = 8
	}

	var (
		mu      sync.Mutex
		results = make(map[int]string)
		okSet   = make(map[int]bool)
		failed  []SegmentFailure
	)

	emitProgress := func(sweep bool) {
		mu.Lock()
		badSet := make(map[int]bool, len(failed))
		for _, f := range failed {
			badSet[f.I] = true
		}
		// 非 nil 空切片：事件契约要求空列表为 []，JSON 序列化不得出现 null
		doneIdx := make([]int, 0, len(okSet))
		failedIdx := make([]int, 0, len(badSet))
		for i := range okSet {
			if !badSet[i] {
				doneIdx = append(doneIdx, i)
			}
		}
		for i := range badSet {
			failedIdx = append(failedIdx, i)
		}
		sort.Ints(doneIdx)
		sort.Ints(failedIdx)
		ev := Event{
			Done: len(doneIdx), Total: total, Failed: len(failedIdx),
			DoneIdx: doneIdx, FailedIdx: failedIdx, Sweep: sweep,
		}
		mu.Unlock()
		emit(ev)
	}

	appendFailed := func(i int, reasons string) {
		mu.Lock()
		failed = append(failed, SegmentFailure{I: i, Reasons: reasons})
		mu.Unlock()
	}

	// attemptSegment 单段翻译 attempts 次（重试附上次的译文与纠错提示），
	// 仍不过则登记失败。重试请求带上上次的 assistant 译文：请求本是无状态
	// 的，不带的话纠错提示指向的「上一次输出」模型根本看不到，等于盲重试。
	attemptSegment := func(i int, frozen Frozen, attempts int) (string, bool) {
		userMsg := BuildUser(frozen.Text)
		var history []ChatMessage
		lastReasons := []string{}
		for attempt := 1; attempt <= attempts; attempt++ {
			if ctx.Err() != nil {
				return "", false
			}
			resp, err := client.CompleteContext(ctx, system, userMsg, 0.1, history)
			if err != nil {
				// LLMError：网络层已重试仍失败或确定性失败（4xx 等）
				logf("第 %d 段调用失败：%v", i+1, err)
				lastReasons = []string{err.Error()}
				break
			}
			reasons := Validate(frozen.Text, resp)
			if len(reasons) == 0 {
				if attempt > 1 {
					logf("第 %d 段第 %d 次尝试通过", i+1, attempt)
				}
				return resp, true
			}
			lastReasons = reasons
			logf("第 %d 段校验未过（第 %d 次）：%s", i+1, attempt, strings.Join(reasons, "；"))
			history = []ChatMessage{
				{Role: "assistant", Content: resp},
				{Role: "user", Content: BuildCorrection(reasons)},
			}
		}
		if ctx.Err() != nil {
			return "", false
		}
		reason := strings.Join(lastReasons, "；")
		progress.RecordFailed(i, reason)
		appendFailed(i, reason)
		return "", false
	}

	translateOne := func(i int) {
		if ctx.Err() != nil {
			return
		}
		defer func() {
			// 兜底（翻译/回填/断点落盘的意外 panic）：登记该段失败并保留
			// 原文，不让一个畸形响应或磁盘错误杀掉整轮 run。
			if r := recover(); r != nil {
				logf("第 %d 段内部错误：%v\n%s", i+1, r, debug.Stack())
				mu.Lock()
				failed = append(failed, SegmentFailure{I: i, Reasons: fmt.Sprintf("内部错误: %v", r)})
				results[i] = chunks[i]
				mu.Unlock()
			}
		}()

		chunk := chunks[i]
		if refFlags[i] {
			mu.Lock()
			results[i] = chunk // 文献条目：原文保留，不计费不进校验
			okSet[i] = true
			mu.Unlock()
			return
		}
		if progress.Reusable(i, chunk) {
			zh, _ := progress.Done(i)
			mu.Lock()
			results[i] = zh
			okSet[i] = true
			mu.Unlock()
			return
		}
		frozen := Freeze(chunk, 1)
		zh, ok := attemptSegment(i, frozen, qualityAttempts)
		if !ok {
			// 最终失败：保留原文占位，标记转人工
			mu.Lock()
			results[i] = chunk
			mu.Unlock()
			return
		}
		if ctx.Err() != nil {
			return
		}
		acceptSegment(i, frozen, zh, chunks, progress, &mu, results, okSet)
	}

	// 有序工作池：索引经 channel FIFO 派发（对齐 ThreadPoolExecutor 的
	// 提交序语义——并发=1 时严格按段序执行，保持既有事件顺序）。
	doneCh := make(chan int, total)
	idxCh := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idxCh {
				if ctx.Err() != nil {
					return
				}
				translateOne(i)
				doneCh <- i
			}
		}()
	}
	// 派发与收割并行：收割 goroutine 按 doneCh FIFO 逐个发进度事件
	//（每完成一段就发送一次事件；附带索引会
	// 阻塞在 worker 空闲上，若先派发完再收割，事件全变终态）。
	emitDone := make(chan struct{})
	go func() {
		defer close(emitDone)
		for range doneCh {
			if ctx.Err() == nil {
				emitProgress(false)
			}
		}
	}()
dispatch:
	for i := 0; i < total; i++ {
		select {
		case <-ctx.Done():
			break dispatch
		case idxCh <- i:
		}
	}
	close(idxCh)
	wg.Wait()
	close(doneCh)
	<-emitDone
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// 收尾补翻：全池跑完后对失败段集中再捞一轮——此时并发已归零，上游
	// 限流/瞬时故障大多已恢复；成功段已在断点里，不受影响。
	if len(failed) > 0 {
		logf("收尾补翻：%d 段未过，%d 秒后集中重试", len(failed), sweepPause)
		if err := waitContext(ctx, sweepPause*time.Second); err != nil {
			return nil, err
		}
		mu.Lock()
		retryIdx := make([]int, 0, len(failed))
		for _, f := range failed {
			retryIdx = append(retryIdx, f.I)
		}
		mu.Unlock()
		// 先发事件再清空：事件如实带上待补翻段（failed 计数与编号），
		// 不闪「失败 0」的假事件
		emitProgress(true)
		mu.Lock()
		failed = nil // attemptSegment 对仍失败的段会重新登记，避免重复条目
		mu.Unlock()
		for _, i := range retryIdx {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			frozen := Freeze(chunks[i], 1)
			zh, ok := attemptSegment(i, frozen, sweepAttempts)
			if !ok {
				continue
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			acceptSegment(i, frozen, zh, chunks, progress, &mu, results, okSet)
			logf("收尾补翻：第 %d 段已救回", i+1)
			emitProgress(true)
		}
		mu.Lock()
		nFail := len(failed)
		mu.Unlock()
		if nFail > 0 {
			logf("收尾补翻后仍有 %d 段失败，保留英文原文待人工处理", nFail)
		} else {
			logf("收尾补翻：失败段全部救回")
		}
		emitProgress(true)
	}

	// 组装：完成段用译文，失败段保留英文原文
	parts := make([]string, 0, total)
	for i := 0; i < total; i++ {
		mu.Lock()
		zh := results[i]
		mu.Unlock()
		if opts.Bilingual && !refFlags[i] {
			// 文献段（原文保留）不再附加引用块，避免同段英文出现两遍
			orig := strings.TrimSpace(chunks[i])
			zhBlock := strings.TrimSpace(zh)
			lines := strings.Split(orig, "\n")
			quoted := make([]string, len(lines))
			for j, ln := range lines {
				quoted[j] = "> " + ln
			}
			parts = append(parts, fmt.Sprintf("%s\n\n%s\n", zhBlock, strings.Join(quoted, "\n")))
		} else {
			parts = append(parts, strings.TrimSpace(zh))
		}
	}
	finalMD := strings.Join(parts, "\n\n") + "\n"

	if dir := filepath.Dir(absPath(opts.OutPath)); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.WriteFile(opts.OutPath, []byte(finalMD), 0o644); err != nil {
		return nil, err
	}

	warnings := ValidateFinal(finalMD)
	if len(warnings) > 0 {
		logf("成品校验警告：%s", strings.Join(warnings, "；"))
	}

	mu.Lock()
	defer mu.Unlock()
	// Failed/Warnings 保证非 nil：结果契约要求列表，JSON 不得出现 null
	failedOut := make([]SegmentFailure, 0, len(failed))
	failedOut = append(failedOut, failed...)
	if warnings == nil {
		warnings = []string{}
	}
	return &TranslateResult{
		Total:          total,
		Done:           total - len(failed),
		Failed:         failedOut,
		KeptReferences: nRefs,
		Warnings:       warnings,
		OutPath:        opts.OutPath,
	}, nil
}

// acceptSegment 回填占位符、落断点、登记成功。
func acceptSegment(i int, frozen Frozen, zhRaw string, chunks []string, progress *Progress,
	mu *sync.Mutex, results map[int]string, okSet map[int]bool) {
	zh := Unfreeze(zhRaw, frozen.Mapping)
	progress.Record(i, chunks[i], zh)
	mu.Lock()
	results[i] = zh
	okSet[i] = true
	mu.Unlock()
}

func absPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}
