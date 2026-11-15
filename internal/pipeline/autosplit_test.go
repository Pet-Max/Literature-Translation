package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"literature-translation/internal/config"
	"literature-translation/internal/mineru"
	"literature-translation/internal/pdftext"
)

// writeTestPDF 生成 pages 页的最小合法 PDF（每页独立内容流，写出页码
// 文本），供 pdfium 数页、pdfcpu 拆分与合成样书测试共用。
func writeTestPDF(t *testing.T, path string, pages int) {
	t.Helper()
	var buf bytes.Buffer
	offsets := make([]int, 0, 2*pages+4)
	obj := func(body string) {
		offsets = append(offsets, buf.Len())
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}
	buf.WriteString("%PDF-1.4\n")
	kids := make([]string, pages)
	for i := range kids {
		kids[i] = fmt.Sprintf("%d 0 R", 3+2*i)
	}
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), pages))
	fontObj := 3 + 2*pages
	for i := 0; i < pages; i++ {
		stream := fmt.Sprintf("BT /F1 24 Tf 72 700 Td (Page %d) Tj ET", i+1)
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>", fontObj, 4+2*i))
		obj(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	}
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	xrefStart := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(offsets)+1)
	fmt.Fprintf(&buf, "0000000000 65535 f \n")
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xrefStart)
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// parseRecorder 记录假解析实现的调用；fakes 直接产出结果目录，不打网络。
// HTTP 层的正确性由 mineru 包的 client_test 覆盖，这里验证编排语义。
type parseRecorder struct {
	mu            sync.Mutex
	oneCalls      []string
	batchCalls    []string
	failPart2     int  // part-002 剩余模拟失败次数
	emptyPart2    int  // part-002 剩余空结果次数（F9 场景）
	failRetryable bool // 模拟失败是否为可重试错误（临时类/终态类）
	probeErr      error
	probeBlocks   func(windowStart int) []clBlock
	batchRunNo    int
}

func (r *parseRecorder) install(pl *Pipeline) {
	pl.parseOneFn = func(ctx context.Context, pdfPath, workDir string, onState func(string)) (*mineru.Result, error) {
		r.mu.Lock()
		r.oneCalls = append(r.oneCalls, pdfPath)
		r.mu.Unlock()
		dir := filepath.Join(workDir, fmt.Sprintf("result-single-%03d", len(r.oneCalls)))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dir, "full.md"), []byte("SINGLE-PARSE\n"), 0o644); err != nil {
			return nil, err
		}
		return &mineru.Result{MDPath: filepath.Join(dir, "full.md")}, nil
	}
	pl.parseBatchFn = func(ctx context.Context, paths []string, workDir string, onState func(int, string)) ([]*mineru.Result, []error, error) {
		r.mu.Lock()
		if r.probeErr != nil {
			// probeErr 只代表「探针阶段失败」：降级后的片解析批次不受影响
			probePhase := false
			for _, p := range paths {
				if strings.HasPrefix(filepath.Base(p), "probe-") {
					probePhase = true
					break
				}
			}
			if probePhase {
				err := r.probeErr
				r.mu.Unlock()
				return nil, nil, err
			}
		}
		blocksFn := r.probeBlocks
		r.mu.Unlock()
		var results []*mineru.Result
		var errs []error
		for i, p := range paths {
			r.mu.Lock()
			r.batchCalls = append(r.batchCalls, p)
			emptyPart2 := strings.Contains(p, "part-002") && r.emptyPart2 > 0
			if emptyPart2 {
				r.emptyPart2--
			}
			failPart2 := strings.Contains(p, "part-002") && r.failPart2 > 0
			if failPart2 {
				r.failPart2--
			}
			failRetryable := r.failRetryable
			r.mu.Unlock()
			if failPart2 {
				if failRetryable {
					// 临时类错误（5xx）：按 retryable 分类应重试
					errs = append(errs, &mineru.APIError{Status: 500, Temporary: true, Message: "模拟临时失败"})
				} else {
					// 终态错误：不可重试，编排层应短路
					errs = append(errs, errors.New("模拟终态失败"))
				}
				results = append(results, nil)
				continue
			}
			base := filepath.Base(p)
			if strings.HasPrefix(base, "probe-") {
				// 探针窗口结果：full.md + content_list（判据数据源）
				start := parseWindowStart(p)
				dir := filepath.Join(workDir, fmt.Sprintf("result-probe-%02d", i+1))
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return nil, nil, err
				}
				if err := os.WriteFile(filepath.Join(dir, "full.md"), []byte(fmt.Sprintf("WINDOW %d-%d", start, start+5)), 0o644); err != nil {
					return nil, nil, err
				}
				var blocks []clBlock
				if blocksFn != nil {
					blocks = blocksFn(start)
				}
				if b, err := json.Marshal(blocks); err == nil {
					// 文件名匹配 readContentList 的 glob：*_content_list.json
					if err := os.WriteFile(filepath.Join(dir, "probe_content_list.json"), b, 0o644); err != nil {
						return nil, nil, err
					}
				}
				results = append(results, &mineru.Result{MDPath: filepath.Join(dir, "full.md")})
				errs = append(errs, nil)
				continue
			}
			// 片结果：full.md 带页码区间标记（页序/覆盖断言的数据源）；
			// 实际页数须与文件名区间长度一致
			start, end, rerr := partRangeFromFile(p)
			if rerr != nil {
				return nil, nil, rerr
			}
			pages, err := pdftext.PageCount(p)
			if err != nil {
				return nil, nil, err
			}
			if pages != end-start+1 {
				return nil, nil, fmt.Errorf("片 %s 实际页数 %d 与区间 %d-%d 不符", base, pages, start, end)
			}
			r.mu.Lock()
			r.batchRunNo++
			n := r.batchRunNo
			r.mu.Unlock()
			dir := filepath.Join(workDir, fmt.Sprintf("result-b%03d", n))
			if err := os.MkdirAll(filepath.Join(dir, "images"), 0o755); err != nil {
				return nil, nil, err
			}
			md := ""
			if !emptyPart2 {
				md = fmt.Sprintf("PART-MARKER pages=%d first=%d last=%d\n", pages, start, end)
				img := filepath.Join(dir, "images", fmt.Sprintf("img-%03d.jpg", n))
				if err := os.WriteFile(img, []byte(fmt.Sprintf("img-%03d", n)), 0o644); err != nil {
					return nil, nil, err
				}
				md += fmt.Sprintf("![](images/img-%03d.jpg)\n", n)
			}
			if err := os.WriteFile(filepath.Join(dir, "full.md"), []byte(md), 0o644); err != nil {
				return nil, nil, err
			}
			results = append(results, &mineru.Result{MDPath: filepath.Join(dir, "full.md")})
			errs = append(errs, nil)
		}
		return results, errs, nil
	}
}

func parseWindowStart(path string) int {
	base := filepath.Base(path)
	var start, end int
	if _, err := fmt.Sscanf(base, "probe-%04d-%04d.pdf", &start, &end); err != nil {
		return 0
	}
	return start
}

func newSplitTestPipeline(t *testing.T, rec *parseRecorder) *Pipeline {
	t.Helper()
	pl, err := New(&config.Config{MinerU: config.MinerU{Token: "tok", ModelVersion: "vlm"}}, Callbacks{
		Log: func(level, msg string) { t.Logf("[%s] %s", level, msg) },
	})
	if err != nil {
		t.Fatal(err)
	}
	rec.install(pl)
	return pl
}

// partRangeFromFile 从片文件名解析页码区间（part-NNN_pSSSS-EEEE.pdf）。
func partRangeFromFile(path string) (int, int, error) {
	var discard, start, end int
	if _, err := fmt.Sscanf(filepath.Base(path), "part-%d_p%d-%d.pdf", &discard, &start, &end); err != nil {
		return 0, 0, err
	}
	return start, end, nil
}

// partMarkerRanges 从合并 MD 抽取各片页码区间标记（按出现顺序）：
// {实际页数, 起始页, 末页}。
func partMarkerRanges(t *testing.T, mergedMD string) [][3]int {
	t.Helper()
	var out [][3]int
	for _, line := range strings.Split(mergedMD, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "PART-MARKER ") {
			continue
		}
		var pages, first, last int
		if _, err := fmt.Sscanf(line, "PART-MARKER pages=%d first=%d last=%d", &pages, &first, &last); err != nil {
			t.Fatalf("分片标记解析失败 %q: %v", line, err)
		}
		out = append(out, [][3]int{{pages, first, last}}...)
	}
	return out
}

// assertContiguousRanges 页序/覆盖强断言：区间按片序无缝衔接、无重叠
// 无遗漏、覆盖 1..totalPages、每片实际页数与区间长度一致。
func assertContiguousRanges(t *testing.T, ranges [][3]int, totalPages int) {
	t.Helper()
	expectFirst := 1
	for i, r := range ranges {
		pages, first, last := r[0], r[1], r[2]
		if first != expectFirst {
			t.Fatalf("片 %d 起始页 %d，期望 %d（页序/覆盖断言）", i+1, first, expectFirst)
		}
		if last < first {
			t.Fatalf("片 %d 区间倒挂 %d-%d", i+1, first, last)
		}
		if pages != last-first+1 {
			t.Fatalf("片 %d 页数 %d 与区间 %d-%d 不符", i+1, pages, first, last)
		}
		expectFirst = last + 1
	}
	if expectFirst-1 != totalPages {
		t.Fatalf("页码覆盖 %d 页，期望 %d", expectFirst-1, totalPages)
	}
}

// TestComputeCuts 名义切点：页数超限按 190 页距；仅体积超限按页均等分；
// 未超限返回 nil。
func TestComputeCuts(t *testing.T) {
	MB := int64(1) << 20
	cases := []struct {
		pages int
		size  int64
		want  []int
	}{
		{150, 150 * MB, nil},
		{200, 200 * MB, nil},
		{201, 100 * MB, []int{190}},
		{400, 100 * MB, []int{190, 380}},
		{381, 100 * MB, []int{190, 380}},
		{150, 250 * MB, []int{75}},
		{40, 250 * MB, []int{20}},
	}
	for _, c := range cases {
		got := computeCuts(c.pages, c.size)
		if len(got) != len(c.want) {
			t.Fatalf("%d 页/%dMB：切点 %v，期望 %v", c.pages, c.size>>20, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("%d 页/%dMB：切点 %v，期望 %v", c.pages, c.size>>20, got, c.want)
			}
		}
	}
}

// TestOversizedSplitAndMerge 250 页样书：两片、页码无缝衔接、合并 MD
// 保序、图片归并进单一 images 目录。
func TestOversizedSplitAndMerge(t *testing.T) {
	rec := &parseRecorder{}
	pl := newSplitTestPipeline(t, rec)
	root := t.TempDir()
	src := filepath.Join(root, "book.pdf")
	writeTestPDF(t, src, 250)

	res, err := pl.parseForTask(context.Background(), src, filepath.Join(root, "parse"))
	if err != nil {
		t.Fatal(err)
	}
	merged, err := os.ReadFile(res.MDPath)
	if err != nil {
		t.Fatal(err)
	}
	markers := partMarkerRanges(t, string(merged))
	assertContiguousRanges(t, markers, 250)
	if len(markers) != 2 {
		t.Fatalf("分片数 %d，期望 2", len(markers))
	}
	// 图片归并：两片各一张，合并目录应有 2 张
	mergedDir := filepath.Dir(res.MDPath)
	entries, _ := os.ReadDir(filepath.Join(mergedDir, "images"))
	if len(entries) != 2 {
		t.Fatalf("合并 images 数量 %d，期望 2", len(entries))
	}
	// 引用有效：merged md 里的图片引用在合并目录中真实存在
	for _, line := range strings.Split(string(merged), "\n") {
		if i := strings.Index(line, "images/"); i >= 0 {
			ref := strings.Trim(strings.TrimSpace(line[i:]), "!)")
			if _, err := os.Stat(filepath.Join(mergedDir, ref)); err != nil {
				t.Fatalf("合并 MD 引用缺失 %s: %v", ref, err)
			}
		}
	}
}

// TestProbeAdjustment 边界页含表格时切点前移：190 → 189（前片 189 页、
// 后片仍 ≤200 页），且窗口只发一次 batch。
func TestProbeAdjustment(t *testing.T) {
	rec := &parseRecorder{}
	// 窗口 [187,193]：页 190（idx 3）以表格块收尾 → 名义切点 190 脏；
	// 页 189（idx 2）纯文本、页 190 首块为文本 → 189 干净
	rec.probeBlocks = func(windowStart int) []clBlock {
		return []clBlock{
			{Type: "text", PageIdx: 0},
			{Type: "text", PageIdx: 2},
			{Type: "text", PageIdx: 3},
			{Type: "table", PageIdx: 3},
		}
	}
	pl := newSplitTestPipeline(t, rec)
	root := t.TempDir()
	src := filepath.Join(root, "book.pdf")
	writeTestPDF(t, src, 400)

	res, err := pl.parseForTask(context.Background(), src, filepath.Join(root, "parse"))
	if err != nil {
		t.Fatal(err)
	}
	markers := partMarkerRanges(t, mustRead(t, res.MDPath))
	// 400 页两切点 [190,380]；同一份假块数据命中两个窗口，切点各自
	// 前移一页：片 1-189 + 片 190-379 + 片 380-400，均 ≤200
	assertContiguousRanges(t, markers, 400)
	if len(markers) != 3 {
		t.Fatalf("微调后分片数 %d，期望 3", len(markers))
	}
	// 探针窗口合并为一次 batch 调用：2 个切点 2 个窗口提交（batchCalls
	// 同时含 3 个片提交，此处只数探针窗口）
	probeSubmits := 0
	for _, p := range rec.batchCalls {
		if strings.Contains(filepath.Base(p), "probe-") {
			probeSubmits++
		}
	}
	if probeSubmits != 2 {
		t.Fatalf("探针窗口提交数不符（期望 2）：实际 %d", probeSubmits)
	}
}

// TestRetryIncrementalRecovery 片 2 临时类错误连续失败耗尽重试 → 任务
// 失败；同目录重试第二次：片 1 走增量恢复（不重烧），片 2 成功。
func TestRetryIncrementalRecovery(t *testing.T) {
	rec := &parseRecorder{}
	rec.failPart2 = 3 // 首次 + 2 次重试全部失败（临时类，可重试）
	rec.failRetryable = true
	pl := newSplitTestPipeline(t, rec)
	root := t.TempDir()
	src := filepath.Join(root, "book.pdf")
	writeTestPDF(t, src, 250)
	parseDir := filepath.Join(root, "parse")

	if _, err := pl.parseForTask(context.Background(), src, parseDir); err == nil {
		t.Fatal("片 2 全部失败时应返回错误")
	}
	// 第二次重试：片 2 恢复成功
	res, err := pl.parseForTask(context.Background(), src, parseDir)
	if err != nil {
		t.Fatalf("增量恢复重试失败: %v", err)
	}
	markers := partMarkerRanges(t, mustRead(t, res.MDPath))
	assertContiguousRanges(t, markers, 250)
	if len(markers) != 2 {
		t.Fatalf("恢复后分片数 %d，期望 2", len(markers))
	}
	part1Calls, part2Calls := 0, 0
	for _, p := range rec.batchCalls {
		switch {
		case strings.Contains(p, "part-001"):
			part1Calls++
		case strings.Contains(p, "part-002"):
			part2Calls++
		}
	}
	if part1Calls != 1 {
		t.Fatalf("片 1 应只解析一次（增量恢复），实际 %d 次", part1Calls)
	}
	if part2Calls != 4 { // 3 轮失败 + 1 次成功（批量逐轮重试）
		t.Fatalf("片 2 调用次数不符（期望 4），实际 %d", part2Calls)
	}
}

// TestNonRetryableShortCircuit 终态错误（配额耗尽/Token 无效/文件超限
// 等不可重试类）立即失败，不空转耗尽 partParseRetries。
func TestNonRetryableShortCircuit(t *testing.T) {
	rec := &parseRecorder{}
	rec.failPart2 = 5 // 远多于重试额度：若未短路会被连续消耗
	pl := newSplitTestPipeline(t, rec)
	root := t.TempDir()
	src := filepath.Join(root, "book.pdf")
	writeTestPDF(t, src, 250)

	_, err := pl.parseForTask(context.Background(), src, filepath.Join(root, "parse"))
	if err == nil || !strings.Contains(err.Error(), "模拟终态失败") {
		t.Fatalf("应报终态失败，实际: %v", err)
	}
	part2Calls := 0
	for _, p := range rec.batchCalls {
		if strings.Contains(p, "part-002") {
			part2Calls++
		}
	}
	if part2Calls != 1 {
		t.Fatalf("终态错误应短路（1 次调用），实际 %d 次", part2Calls)
	}
}

// TestProbeFailureDegradation 探针失败 → 降级固定切点，流程仍完整。
func TestProbeFailureDegradation(t *testing.T) {
	rec := &parseRecorder{}
	rec.probeErr = errors.New("模拟探针服务不可用")
	pl := newSplitTestPipeline(t, rec)
	root := t.TempDir()
	src := filepath.Join(root, "book.pdf")
	writeTestPDF(t, src, 250)

	res, err := pl.parseForTask(context.Background(), src, filepath.Join(root, "parse"))
	if err != nil {
		t.Fatalf("探针失败应降级而非失败: %v", err)
	}
	markers := partMarkerRanges(t, mustRead(t, res.MDPath))
	assertContiguousRanges(t, markers, 250)
	if len(markers) != 2 {
		t.Fatalf("降级后分片数 %d，期望 2", len(markers))
	}
}

// TestEmptyPartResultRetried 片解析返回空结果：按片失败入账（临时类
// 可重试），重试仍空则任务明确报错；同目录重试第二次：空片成功后合并
// 完整（F9：空结果静默跳过会造成内容缺失且无告警）。
func TestEmptyPartResultRetried(t *testing.T) {
	rec := &parseRecorder{}
	rec.emptyPart2 = 3 // 首次 + 2 轮重试全部返回空结果
	pl := newSplitTestPipeline(t, rec)
	root := t.TempDir()
	src := filepath.Join(root, "book.pdf")
	writeTestPDF(t, src, 250)
	parseDir := filepath.Join(root, "parse")

	_, err := pl.parseForTask(context.Background(), src, parseDir)
	if err == nil || !strings.Contains(err.Error(), "空解析结果") {
		t.Fatalf("空片应明确报错，实际: %v", err)
	}
	// 第二次重试：片 2 恢复成功（重试期间片 1 已增量恢复）
	res, err := pl.parseForTask(context.Background(), src, parseDir)
	if err != nil {
		t.Fatalf("重试失败: %v", err)
	}
	assertContiguousRanges(t, partMarkerRanges(t, mustRead(t, res.MDPath)), 250)
}

// TestJoinMDsStitch 拼接：表格行跨片用单换行续接成一张表，段落间保持
// 空行分隔。
func TestJoinMDsStitch(t *testing.T) {
	got := joinMDs([]string{
		"前文段落。\n\n| a | b |\n| 1 | 2 |",
		"| 3 | 4 |\n\n后文段落。",
	})
	want := "前文段落。\n\n| a | b |\n| 1 | 2 |\n| 3 | 4 |\n\n后文段落。\n"
	if got != want {
		t.Fatalf("表格缝合不符：\n%s\n期望：\n%s", got, want)
	}
	got = joinMDs([]string{"第一段。", "第二段。"})
	if got != "第一段。\n\n第二段。\n" {
		t.Fatalf("段落拼接不符：%q", got)
	}
	// F10 段落跨切点续接：前片末行无句末标点 + 后片首行非结构块
	// → 单换行拼回同段（半句截断的主场景）
	got = joinMDs([]string{"……实验结果表明纠缠度随时间", "演化并呈现周期性复苏。"})
	if got != "……实验结果表明纠缠度随时间\n演化并呈现周期性复苏。\n" {
		t.Fatalf("段落续接不符：%q", got)
	}
	// 句号收尾 + 新段落 → 保持空行分隔（句号断页盲区：文本层面无法
	// 判定，维持现状不误粘）
	got = joinMDs([]string{"第一段完。", "第二段开始。"})
	if got != "第一段完。\n\n第二段开始。\n" {
		t.Fatalf("句号后新段落分隔不符：%q", got)
	}
	// 后片首行是标题等结构块 → 不误粘（即使前片末行无句末标点）
	got = joinMDs([]string{"……上半句没写完", "# II. 模型"})
	if got != "……上半句没写完\n\n# II. 模型\n" {
		t.Fatalf("结构块误粘：%q", got)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestArchivePlanOversizedSplit 归档模式（ArchivePlan）：分片产物布局
// 落在 plan.MineruDir 下（parts/probes/merged），合并 MD 正确。
func TestArchivePlanOversizedSplit(t *testing.T) {
	rec := &parseRecorder{}
	rec.probeBlocks = func(windowStart int) []clBlock {
		return []clBlock{{Type: "text", PageIdx: 0}}
	}
	pl := newSplitTestPipeline(t, rec)
	root := t.TempDir()
	src := filepath.Join(root, "book.pdf")
	writeTestPDF(t, src, 250)

	plan, err := NewArchivePlan(root, src)
	if err != nil {
		t.Fatal(err)
	}
	res, err := pl.parseForTask(context.Background(), src, plan.MineruDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.MDPath, plan.MineruDir) {
		t.Fatalf("合并结果不在归档目录：%s", res.MDPath)
	}
	markers := partMarkerRanges(t, mustRead(t, res.MDPath))
	assertContiguousRanges(t, markers, 250)
	if len(markers) != 2 {
		t.Fatalf("归档模式分片数 %d，期望 2", len(markers))
	}
	if _, err := os.Stat(filepath.Join(plan.MineruDir, "parts")); err != nil {
		t.Fatalf("parts 目录缺失: %v", err)
	}
}

// TestCleanupSplitArtifacts 任务成功清理：parts/probes 移除、merged 保留
// （失败路径不清理，重试增量恢复依赖片结果）。
func TestCleanupSplitArtifacts(t *testing.T) {
	parseDir := t.TempDir()
	parts := filepath.Join(parseDir, "parts", "part-001")
	if err := os.MkdirAll(parts, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parts, "full.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	probes := filepath.Join(parseDir, "probes", "probe-1")
	if err := os.MkdirAll(probes, 0o755); err != nil {
		t.Fatal(err)
	}
	merged := filepath.Join(parseDir, "result-merged-1")
	if err := os.MkdirAll(merged, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(merged, "full.md"), []byte("m"), 0o644); err != nil {
		t.Fatal(err)
	}
	cleanupSplitArtifacts(parseDir)
	if _, err := os.Stat(parts); !os.IsNotExist(err) {
		t.Fatal("parts 未清理")
	}
	if _, err := os.Stat(probes); !os.IsNotExist(err) {
		t.Fatal("probes 未清理")
	}
	if _, err := os.Stat(filepath.Join(merged, "full.md")); err != nil {
		t.Fatal("merged 被误删")
	}
}

// TestMergeImageCollisionRename 同名不同内容的图片：后片改名为 -pNNN
// 并改写该片 md 引用，前片引用与内容不变。
func TestMergeImageCollisionRename(t *testing.T) {
	parseDir := t.TempDir()
	var parts []partFile
	var ress []partInput
	for i, content := range []string{"img-one", "img-two"} {
		dir := filepath.Join(parseDir, fmt.Sprintf("part-%03d", i+1), "result-1")
		if err := os.MkdirAll(filepath.Join(dir, "images"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "images", "shared.jpg"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		own := fmt.Sprintf("own-%d.jpg", i+1)
		if err := os.WriteFile(filepath.Join(dir, "images", own), []byte(strings.TrimSuffix(own, ".jpg")), 0o644); err != nil {
			t.Fatal(err)
		}
		md := fmt.Sprintf("PART %d\n![](images/shared.jpg)\n![](images/%s)\n", i+1, own)
		if err := os.WriteFile(filepath.Join(dir, "full.md"), []byte(md), 0o644); err != nil {
			t.Fatal(err)
		}
		parts = append(parts, partFile{path: fmt.Sprintf("part-%d.pdf", i+1), start: i*2 + 1, end: i*2 + 2})
		ress = append(ress, partInput{dir: dir, md: md})
	}
	merged, err := mergePartResults(parseDir, ress)
	if err != nil {
		t.Fatal(err)
	}
	one, err := os.ReadFile(filepath.Join(merged, "images", "shared.jpg"))
	if err != nil || string(one) != "img-one" {
		t.Fatalf("前片 shared.jpg 内容不符: %v", err)
	}
	two, err := os.ReadFile(filepath.Join(merged, "images", "shared-p002.jpg"))
	if err != nil || string(two) != "img-two" {
		t.Fatalf("后片改写图缺失或内容不符: %v", err)
	}
	own2, err := os.ReadFile(filepath.Join(merged, "images", "own-2.jpg"))
	if err != nil || string(own2) != "own-2" {
		t.Fatalf("后片自有图缺失: %v", err)
	}
	out := mustRead(t, filepath.Join(merged, "full.md"))
	if strings.Contains(out, "PART 2\n![](images/shared.jpg)") {
		t.Fatal("后片 md 引用未改写")
	}
	if !strings.Contains(out, "![](images/shared-p002.jpg)") {
		t.Fatal("改写后的引用缺失")
	}
}
