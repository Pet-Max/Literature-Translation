// autosplit.go — 超限 PDF 分片接续解析。
//
// MinerU 云端单次解析 ≤200 页 / ≤200MB；超限文档按切点拆片、批量并行解析、
// 结果合并为单任务 MD，下游翻译/断点/导出完全复用（合并发生在解析
// 结果层，校准/翻译/导出对分片无感知）。
//
// 切点质量三层保障：
//  1. 探针层：每个名义切点 ±3 页窗口先发一次 batch 小单，读结果 zip
//     自带的 content_list（权威块类型 + 0 基页号）；
//  2. 微调层：±2 页范围内寻找边界页干净的切点（片尾页最后一块与片首
//     页第一块都不是表格/图表/图片）；
//  3. 缝合层：无干净切点时保持名义切点，合并按行拼接让跨页表格行
//     自然续接（见 joinMDs）。
//
// 每片结果落在独立子目录（parts/part-NXX），避开 purgeStale 的顶层
// 清理；重试时已成功且指纹一致的片直接复用，只补失败片（长文档重试
// 不重烧已成功片的配额）。降级链：探针失败 → 固定切点（缝合兜底）。
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/api"

	"literature-translation/internal/mineru"
	"literature-translation/internal/pdftext"
)

const (
	// 每片目标页数：MaxPages(200) 留出微调余量（切点最多 ±2 页）。
	splitPagesPerPart = 190
	// 每片目标体积：MaxBytes 留出余量（仅体积超限时按页均等分的依据）。
	splitBytesPerPart = 180 << 20
	// 探针窗口半宽与切点微调搜索范围（页）。耦合约束：adjustRange 不得
	// 超过 probeHalfWindow-1——微调候选的边界页必须落在探针窗口内，
	// 否则脏页判定拿不到数据、候选会被静默当作干净切点。
	probeHalfWindow = 3
	adjustRange     = 2
	// 失败片的批量重试轮次（不含首次；每轮只补失败片，终态错误短路）。
	partParseRetries = 2
)

// structuralBlock 表格/图表/图片块：跨页延续会在拼接缝上断结构。
// 方程块按页独立、不构成切点风险，不计入判据（否则公式密集的论文
// 几乎无干净切点）。
func structuralBlock(t string) bool {
	switch t {
	case "table", "chart", "image":
		return true
	}
	return false
}

// window 探针窗口（原书 1 基含端点页码）。
type window struct{ start, end int }

// clBlock MinerU content_list 的块（探针判据只需类型与 0 基页号）。
type clBlock struct {
	Type    string    `json:"type"`
	PageIdx int       `json:"page_idx"`
	BBox    []float64 `json:"bbox"`
}

// readContentList 读取 MinerU 结果目录里自带的 content_list。zip 中
// 自随，无需额外请求；v2 变体（*_content_list_v2.json）不参与匹配。
func readContentList(resultDir string) ([]clBlock, error) {
	matches, err := filepath.Glob(filepath.Join(resultDir, "*_content_list.json"))
	if err != nil || len(matches) == 0 {
		return nil, fmt.Errorf("结果目录缺少 content_list")
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		return nil, err
	}
	var blocks []clBlock
	if err := json.Unmarshal(data, &blocks); err != nil {
		return nil, err
	}
	return blocks, nil
}

// computeCuts 返回名义切点（1 基：切点 c 表示前一片覆盖 1..c 页）。
// 未超限返回 nil。页数超限按固定页距切；仅体积超限按页均体积等分
// （页均体积近似均匀，单片仍超限时由拆分层的递归对半兜底）。
func computeCuts(pages int, size int64) []int {
	if pages <= mineru.MaxPages && size <= mineru.MaxBytes {
		return nil
	}
	var cuts []int
	if pages > mineru.MaxPages {
		for c := splitPagesPerPart; c < pages; c += splitPagesPerPart {
			cuts = append(cuts, c)
		}
		return cuts
	}
	n := int(math.Ceil(float64(size) / float64(splitBytesPerPart)))
	if n < 2 {
		n = 2
	}
	chunk := (pages + n - 1) / n
	if chunk < 1 {
		chunk = 1
	}
	for c := chunk; c < pages; c += chunk {
		cuts = append(cuts, c)
	}
	return cuts
}

// parseForTask 任务解析入口：本地判定页数/体积，未超限走单文件解析；
// 超限走分片接续。页数读取失败（加密/损坏）不拦路：让 MinerU 侧给出
// 既有报错。
func (p *Pipeline) parseForTask(ctx context.Context, pdfPath, parseDir string) (*mineru.Result, error) {
	pages, err := pdftext.PageCount(pdfPath)
	if err != nil {
		p.warnf("页数预检失败（继续单文件解析）：%v", err)
		return p.parseOne(ctx, pdfPath, parseDir, p.rawParseState)
	}
	size := fileSize(pdfPath)
	if pages <= mineru.MaxPages && size <= mineru.MaxBytes {
		return p.parseOne(ctx, pdfPath, parseDir, p.rawParseState)
	}
	p.logf("超限预检：%s，启用分片接续解析", oversizeIssue(pages, size))
	return p.parseOversized(ctx, pdfPath, pages, size, parseDir)
}

func (p *Pipeline) rawParseState(state string) {
	if p.CB.ParseState != nil {
		p.CB.ParseState(state)
	}
}

func (p *Pipeline) emitParseState(state string) {
	if p.CB.ParseState != nil {
		p.CB.ParseState(state)
	}
}

// partFile 一片物理 PDF：start/end 为原书 1 基含端点页码。
type partFile struct {
	path       string
	start, end int
}

// EstimateParts 预估超限文档的分片数（未超限返回 1）。入队日志预告用，
// 与实际拆分共用 computeCuts，避免两处口径漂移。
func EstimateParts(pages int, size int64) int {
	if cuts := computeCuts(pages, size); len(cuts) > 0 {
		return len(cuts) + 1
	}
	return 1
}

// splitPlan 切点决策缓存：重试时源文档指纹一致即复用切点，跳过整个
// 探针阶段（省时省配额）。源文档变化自动失效重探。
type splitPlan struct {
	SrcHash string `json:"src_hash"`
	Cuts    []int  `json:"cuts"`
}

const splitPlanFile = "autosplit-plan.json"

func loadSplitPlan(parseDir, srcHash string, pages int) []int {
	data, err := os.ReadFile(filepath.Join(parseDir, splitPlanFile))
	if err != nil {
		return nil
	}
	var plan splitPlan
	if json.Unmarshal(data, &plan) != nil || plan.SrcHash != srcHash || len(plan.Cuts) == 0 {
		return nil
	}
	prev := 0
	for _, c := range plan.Cuts {
		if c <= prev || c >= pages {
			return nil
		}
		prev = c
	}
	return plan.Cuts
}

func saveSplitPlan(parseDir, srcHash string, cuts []int) {
	data, err := json.Marshal(splitPlan{SrcHash: srcHash, Cuts: cuts})
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(parseDir, splitPlanFile), data, 0o644)
}

// parseOversized 编排：探针定切点（或复用缓存切点）→ 拆片 → 并行批量解析
// （重试增量恢复）→ 结果合并为单任务 MD。
func (p *Pipeline) parseOversized(ctx context.Context, pdfPath string, pages int, size int64, parseDir string) (*mineru.Result, error) {
	// 源文档指纹：片增量恢复与切点缓存共用的身份依据（快照内容 + 页
	// 区间），与 pdfcpu 产物的字节确定性解耦。
	srcHash := fileSHA256(pdfPath)

	cuts := loadSplitPlan(parseDir, srcHash, pages)
	if cuts != nil {
		p.logf("复用缓存切点（%d 个），跳过探针", len(cuts))
	} else {
		var probeOK bool
		cuts, probeOK = p.adjustCuts(ctx, pdfPath, computeCuts(pages, size), pages, parseDir)
		// 探针失败/降级路径不缓存切点：服务恢复后下次重试仍会正常探针，
		// 降级结果固化会让质量降级永久化
		if probeOK {
			saveSplitPlan(parseDir, srcHash, cuts)
		}
	}

	partsDir := filepath.Join(parseDir, "parts")
	parts, err := p.splitParts(ctx, pdfPath, cuts, pages, partsDir)
	if err != nil {
		return nil, fmt.Errorf("拆分超限 PDF 失败: %w", err)
	}

	ress, err := p.parsePartsBatch(ctx, parts, srcHash, partsDir)
	if err != nil {
		return nil, err
	}

	mergedDir, err := mergePartResults(parseDir, ress)
	if err != nil {
		return nil, fmt.Errorf("合并分片解析结果失败: %w", err)
	}
	p.logf("分片接续解析完成：%d 片（%d 页）→ %s", len(parts), pages, mergedDir)
	return &mineru.Result{MDPath: filepath.Join(mergedDir, "full.md")}, nil
}

// adjustCuts 探针层 + 微调层：为每个名义切点发探针窗口（全部窗口合并
// 为一次 batch），按权威 content_list 微调切点。第二返回值表示探针
// 是否完整成功——失败/降级路径为 false，调用方据此**不缓存切点**
// （服务恢复后下次重试仍会正常探针；降级结果固化会让质量降级永久化）。
func (p *Pipeline) adjustCuts(ctx context.Context, pdfPath string, cuts []int, pages int, parseDir string) ([]int, bool) {
	adjusted := append([]int(nil), cuts...)
	if len(cuts) == 0 {
		return adjusted, true
	}
	// 窗口去重构建（体积等分可能产生近距切点，窗口共用）
	clampPage := func(pg int) int {
		if pg < 1 {
			return 1
		}
		if pg > pages {
			return pages
		}
		return pg
	}
	var winList []window
	index := map[window]int{}
	winOfCut := make([]int, len(cuts))
	for i, c := range cuts {
		w := window{clampPage(c - probeHalfWindow), clampPage(c + probeHalfWindow)}
		if idx, ok := index[w]; ok {
			winOfCut[i] = idx
			continue
		}
		index[w] = len(winList)
		winOfCut[i] = len(winList)
		winList = append(winList, w)
	}

	p.logf("探针定切点：%d 个窗口…", len(winList))
	// 探针结果用后即弃：整体重建 probes 目录，清掉历史 result-probe-*
	// 与中断残留的 .probe-* 临时目录，避免多次重试后无谓累积。
	probesDir := filepath.Join(parseDir, "probes")
	_ = os.RemoveAll(probesDir)
	if err := os.MkdirAll(probesDir, 0o755); err != nil {
		return adjusted, false
	}
	var probePaths []string
	for _, w := range winList {
		dst := filepath.Join(probesDir, fmt.Sprintf("probe-%04d-%04d.pdf", w.start, w.end))
		os.Remove(dst)
		if err := api.TrimFile(ctx, pdfPath, dst, []string{fmt.Sprintf("%d-%d", w.start, w.end)}, nil); err != nil {
			p.warnf("探针窗口切割失败（降级固定切点）：%v", err)
			return adjusted, false
		}
		probePaths = append(probePaths, dst)
	}
	results, itemErrs, err := p.parseBatch(ctx, probePaths, probesDir, func(i int, state string) {
		p.emitParseState(fmt.Sprintf("探针 %d/%d %s", i+1, len(probePaths), state))
	})
	if err != nil {
		p.warnf("探针解析失败（降级固定切点）：%v", err)
		return adjusted, false
	}
	for i, e := range itemErrs {
		if e != nil {
			p.warnf("探针条目 %d 失败（降级固定切点）：%v", i+1, e)
			return adjusted, false
		}
	}
	winBlocks := make([][]clBlock, len(winList))
	for i, res := range results {
		blocks, err := readContentList(filepath.Dir(res.MDPath))
		if err != nil {
			p.warnf("探针 content_list 读取失败（降级固定切点）：%v", err)
			return adjusted, false
		}
		winBlocks[i] = blocks
	}

	for i, c := range cuts {
		w := winList[winOfCut[i]]
		if c2, moved := adjustCut(c, w, winBlocks[winOfCut[i]], pages); moved {
			p.logf("切点微调：%d → %d（避开边界页表格/图表延续）", c, c2)
			adjusted[i] = c2
		}
	}
	// 合法性校验：切点严格递增且各片至少 1 页（体积等分可能产生近距
	// 切点，独立微调后理论上可能互相越界——整体回退名义切点）
	prev := 0
	for _, c := range adjusted {
		if c <= prev || c >= pages {
			p.warnf("微调后切点非法，回退名义切点")
			return cuts, false
		}
		prev = c
	}
	return adjusted, true
}

// adjustCut 单切点微调：名义切点干净则原样；否则 ±adjustRange 内由近
// 到远取第一个干净切点。无干净切点返回名义值（缝合兜底）。
func adjustCut(cut int, w window, blocks []clBlock, pages int) (int, bool) {
	if !dirtyCut(cut, w, blocks) {
		return cut, false
	}
	for delta := 1; delta <= adjustRange; delta++ {
		for _, c := range []int{cut - delta, cut + delta} {
			if c < 1 || c > pages-1 {
				continue
			}
			if p := c; p >= w.start && p <= w.end && !dirtyCut(c, w, blocks) {
				return c, true
			}
		}
	}
	return cut, false
}

// dirtyCut 切点 c 的边界页判脏：片尾页（第 c 页）最后一块是结构性块
// （大概率延续到下一页），或片首页（第 c+1 页）第一块是结构性块
// （疑似上一页延续）。page_idx 为窗口内 0 基。
func dirtyCut(cut int, w window, blocks []clBlock) bool {
	lastIdx := cut - w.start
	firstIdx := cut + 1 - w.start
	var lastPage, firstPage []clBlock
	for _, b := range blocks {
		switch b.PageIdx {
		case lastIdx:
			lastPage = append(lastPage, b)
		case firstIdx:
			firstPage = append(firstPage, b)
		}
	}
	if len(lastPage) > 0 && structuralBlock(lastPage[len(lastPage)-1].Type) {
		return true
	}
	if len(firstPage) > 0 && structuralBlock(firstPage[0].Type) {
		return true
	}
	return false
}

// splitParts 物理拆分：按切点把 pdfPath 拆成若干片，落在 partsDir。
// 单片仍超体积时递归对半再切（页均体积不均匀的防御）。
// 清理边界：只清顶层旧片 PDF（切点变化后文件名不同会累积）；
// part-NNX/ 子目录是增量恢复的解析结果，绝不可清——清掉就丢失
// 「重试不重烧已成功片」的语义。
func (p *Pipeline) splitParts(ctx context.Context, pdfPath string, cuts []int, pages int, partsDir string) ([]partFile, error) {
	if err := os.MkdirAll(partsDir, 0o755); err != nil {
		return nil, err
	}
	if entries, err := os.ReadDir(partsDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".pdf") {
				continue
			}
			_ = os.Remove(filepath.Join(partsDir, e.Name()))
		}
	}
	var parts []partFile
	start := 1
	bounds := append(append([]int{}, cuts...), pages)
	for i, end := range bounds {
		base := filepath.Join(partsDir, fmt.Sprintf("part-%03d", i+1))
		if err := p.splitRange(ctx, pdfPath, start, end, base, &parts); err != nil {
			return nil, err
		}
		start = end + 1
	}
	return parts, nil
}

// splitRange 拆出原书 [start, end] 页为一片；产物体积仍超限且多于
// 1 页时对半递归（保持文档顺序）。
func (p *Pipeline) splitRange(ctx context.Context, src string, start, end int, dstBase string, parts *[]partFile) error {
	dst := fmt.Sprintf("%s_p%04d-%04d.pdf", dstBase, start, end)
	os.Remove(dst)
	if err := api.TrimFile(ctx, src, dst, []string{fmt.Sprintf("%d-%d", start, end)}, nil); err != nil {
		return fmt.Errorf("拆分 %d-%d 页失败: %w", start, end, err)
	}
	st, err := os.Stat(dst)
	if err != nil {
		return err
	}
	if st.Size() > mineru.MaxBytes && end > start {
		mid := (start + end) / 2
		if err := p.splitRange(ctx, src, start, mid, dstBase+"a", parts); err != nil {
			return err
		}
		return p.splitRange(ctx, src, mid+1, end, dstBase+"b", parts)
	}
	*parts = append(*parts, partFile{path: dst, start: start, end: end})
	return nil
}

// parsePartsBatch 片解析（批量化）：全部待解析片合并为一次 batch 提交，
// MinerU 服务端并行解析——墙钟时间从「各片之和」变为「最慢片」。失败
// 片按 partParseRetries 轮次重试（每轮只补失败片），终态错误短路；
// 已有身份指纹一致的片走增量恢复不重烧。返回各片发布目录与 full.md
// 内容（按片序）。
func (p *Pipeline) parsePartsBatch(ctx context.Context, parts []partFile, srcHash, partsDir string) ([]partInput, error) {
	doneByPart := make([]partInput, len(parts))
	pending := map[int]bool{}
	for i := range parts {
		partWork := filepath.Join(partsDir, fmt.Sprintf("part-%03d", i+1))
		fp := partFingerprint(srcHash, parts[i].start, parts[i].end)
		if md, dir, ok := reusePartResult(partWork, fp); ok {
			p.logf("片 %d/%d 已有解析结果，跳过（增量恢复）", i+1, len(parts))
			doneByPart[i] = partInput{dir: dir, md: md}
			continue
		}
		pending[i] = true
	}

	lastErrs := make(map[int]error)
	for attempt := 0; len(pending) > 0 && attempt <= partParseRetries; attempt++ {
		if attempt > 0 {
			// 终态错误短路：剩余片全部不可重试（配额耗尽/Token 无效/
			// 文件超限等）时立即失败，不空转
			allTerminal := true
			for i := range pending {
				if mineru.Retryable(lastErrs[i]) {
					allTerminal = false
					break
				}
			}
			if allTerminal {
				break
			}
			p.warnf("分片解析 %d 片失败，第 %d 轮重试", len(pending), attempt)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// 本轮待解析片按序提交；超过官方批量条目上限时分组顺次发
		idxs := sortedPending(pending)
		for _, group := range chunkInts(idxs, mineru.MaxFilesPerBatch) {
			paths := make([]string, len(group))
			for j, i := range group {
				paths[j] = parts[i].path
			}
			results, itemErrs, err := p.parseBatch(ctx, paths, partsDir, func(i int, state string) {
				p.emitParseState(fmt.Sprintf("片 %d/%d %s", group[i]+1, len(parts), state))
			})
			if err != nil {
				// 批次级失败（网络/限流/服务故障）：整轮片保持待重试
				for _, i := range group {
					lastErrs[i] = err
				}
				continue
			}
			for j, partIdx := range group {
				if itemErr := itemErrs[j]; itemErr != nil || results[j] == nil {
					if itemErr == nil {
						itemErr = errors.New("MinerU 未返回条目结果")
					}
					lastErrs[partIdx] = itemErr
					continue
				}
				data, rerr := os.ReadFile(results[j].MDPath)
				if rerr != nil {
					lastErrs[partIdx] = rerr
					continue
				}
				// 空结果按片失败入账（临时类可重试）：静默合并会造成
				// 内容缺失且无告警
				if strings.TrimSpace(string(data)) == "" {
					lastErrs[partIdx] = &mineru.APIError{Status: 500, Temporary: true,
						Message: "MinerU 返回空解析结果"}
					continue
				}
				// 发布目录迁移到 part-NNX/（保持增量恢复的目录结构），
				// 并写入片身份指纹；迁移失败时原地使用，仅恢复语义弱化
				partWork := filepath.Join(partsDir, fmt.Sprintf("part-%03d", partIdx+1))
				if err := os.MkdirAll(partWork, 0o755); err != nil {
					return nil, err
				}
				newDir := filepath.Join(partWork, fmt.Sprintf("result-%s", time.Now().Format("20060102150405")))
				if rerr := os.Rename(filepath.Dir(results[j].MDPath), newDir); rerr == nil {
					results[j].MDPath = filepath.Join(newDir, filepath.Base(results[j].MDPath))
				}
				if werr := os.WriteFile(filepath.Join(filepath.Dir(results[j].MDPath), "part-fingerprint.txt"),
					[]byte(partFingerprint(srcHash, parts[partIdx].start, parts[partIdx].end)), 0o644); werr != nil {
					lastErrs[partIdx] = werr
					continue
				}
				doneByPart[partIdx] = partInput{dir: filepath.Dir(results[j].MDPath), md: string(data)}
				delete(pending, partIdx)
				delete(lastErrs, partIdx)
			}
		}
	}
	if len(pending) > 0 {
		var msgs []string
		for _, i := range sortedPending(pending) {
			msgs = append(msgs, fmt.Sprintf("片 %d/%d: %v", i+1, len(parts), lastErrs[i]))
		}
		return nil, fmt.Errorf("%d 片解析失败：%s", len(pending), strings.Join(msgs, "；"))
	}
	return doneByPart, nil
}

// sortedPending 待解析片下标升序（提交顺序 = 文档顺序）。
func sortedPending(pending map[int]bool) []int {
	out := make([]int, 0, len(pending))
	for i := range pending {
		out = append(out, i)
	}
	sort.Ints(out)
	return out
}

// chunkInts 按上限分组（保持顺序）。
func chunkInts(in []int, size int) [][]int {
	var out [][]int
	for len(in) > 0 {
		n := size
		if n > len(in) {
			n = len(in)
		}
		out = append(out, in[:n])
		in = in[n:]
	}
	return out
}

// reusePartResult 复用判定：partWork 下已有发布结果、full.md 非空、
// 记录的片身份指纹与当前片一致。
func reusePartResult(partWork, want string) (string, string, bool) {
	if want == "" {
		return "", "", false
	}
	entries, err := os.ReadDir(partWork)
	if err != nil {
		return "", "", false
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "result-") {
			continue
		}
		d := filepath.Join(partWork, e.Name())
		data, err := os.ReadFile(filepath.Join(d, "full.md"))
		if err != nil || strings.TrimSpace(string(data)) == "" {
			continue
		}
		fp, err := os.ReadFile(filepath.Join(d, "part-fingerprint.txt"))
		if err != nil || strings.TrimSpace(string(fp)) != want {
			continue
		}
		return string(data), d, true
	}
	return "", "", false
}

// partFingerprint 片身份指纹：源文档哈希 + 页区间的 SHA-256（与
// pdfcpu 产物的字节确定性解耦，同一来源同一区间即同一身份）。
func partFingerprint(srcHash string, start, end int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d", srcHash, start, end)))
	return hex.EncodeToString(sum[:])
}

// fileSHA256 文件内容的 SHA-256；读取失败返回空串。
func fileSHA256(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// partInput 合并输入：片结果目录与 full.md 内容（可能含改名后的图片引用）。
type partInput struct {
	dir string
	md  string
}

// mergePartResults 把各片结果合并为单个任务级结果目录：full.md 拼接
// （表格续接单换行、其余空行分隔）、images 归并（同名不同内容时改写
// 引用）。发布后清理 parseDir 顶层旧 result-*（与单文件流程的
// purgeStale 语义一致）。
func mergePartResults(parseDir string, ress []partInput) (string, error) {
	merged := filepath.Join(parseDir, fmt.Sprintf("result-merged-%s", time.Now().Format("20060102150405")))
	if err := os.MkdirAll(filepath.Join(merged, "images"), 0o755); err != nil {
		return "", err
	}
	mds := make([]string, len(ress))
	for i, r := range ress {
		mds[i] = r.md
		imgDir := filepath.Join(r.dir, "images")
		entries, err := os.ReadDir(imgDir)
		if err != nil {
			continue // 片无图片目录属正常（纯文字片）
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			src := filepath.Join(imgDir, e.Name())
			dst := filepath.Join(merged, "images", e.Name())
			if _, err := os.Stat(dst); err == nil {
				// 同名不同内容（理论上哈希名不会发生）：改写该片引用
				if sameFileBytes(src, dst) {
					continue
				}
				ext := filepath.Ext(e.Name())
				newName := strings.TrimSuffix(e.Name(), ext) + fmt.Sprintf("-p%03d", i+1) + ext
				if err := copyFile(src, filepath.Join(merged, "images", newName)); err != nil {
					return "", err
				}
				mds[i] = strings.ReplaceAll(mds[i], "images/"+e.Name(), "images/"+newName)
				continue
			}
			if err := copyFile(src, dst); err != nil {
				return "", err
			}
		}
	}
	content := joinMDs(mds)
	if err := os.WriteFile(filepath.Join(merged, "full.md"), []byte(content), 0o644); err != nil {
		return "", err
	}
	cleanupStaleResults(parseDir, merged)
	return merged, nil
}

// cleanupSplitArtifacts 任务成功后清理分片接续的中间产物：parts/（片
// PDF 与片结果）、probes/（探针结果）。保留合并结果目录（其 full.md
// 是本次任务的解析产物）。任务失败时保留全部——重试的增量恢复依赖
// 它们。
func cleanupSplitArtifacts(parseDir string) {
	_ = os.RemoveAll(filepath.Join(parseDir, "parts"))
	_ = os.RemoveAll(filepath.Join(parseDir, "probes"))
}

// joinMDs 拼接各片 full.md。接缝三规则（按优先级）：
//  1. 表格续接：前后片均为表格行 → 单换行（行拼回同一张表）；
//  2. 段落续接：前片末行无句末标点（半句截断）且后片首行非结构性块
//     开头（不是标题/表格/图片/列表/引用/代码/公式块）→ 单换行（同段
//     延续）；
//  3. 其余 → 空行分隔（独立段落/新块）。
//
// 误判方向安全：最坏结果是「本该分段的被合成一段」，不会丢字、不会
// 错序、不会破坏表格标题。已知盲区：页底恰断在句号上但段落实际延续
// 的情况（与「句号后接新段落」在文本层面无法区分），如实接受。
func joinMDs(parts []string) string {
	var b strings.Builder
	prevRow := false
	prevLastLine := ""
	first := true
	for _, md := range parts {
		cur := strings.TrimSpace(md)
		if cur == "" {
			continue
		}
		curFirstLine := firstLineOf(cur)
		if !first {
			switch {
			case prevRow && tableRowStart(curFirstLine):
				b.WriteString("\n") // 表格行跨片续接
			case !prevRow && !lineEndsSentence(prevLastLine) &&
				!structBlockStart(curFirstLine) && !tableRowStart(curFirstLine):
				b.WriteString("\n") // 段落跨切点续接（半句截断）
			default:
				b.WriteString("\n\n")
			}
		}
		b.WriteString(cur)
		prevRow = tableRowEnd(cur)
		prevLastLine = lastLineOf(cur)
		first = false
	}
	if b.Len() == 0 {
		return ""
	}
	return b.String() + "\n"
}

// lineEndsSentence 行是否以句末标点收尾（句子完整结束的判据）。剥掉
// 行尾的 markdown 强调符与引号/括号后再看末字符；全角/半角兼顾。
func lineEndsSentence(line string) bool {
	s := strings.TrimRight(line, " \t\r\n")
	s = strings.TrimRight(s, "*_~`\"'」”）』〉》]")
	if s == "" {
		return false
	}
	runes := []rune(s)
	switch runes[len(runes)-1] {
	case '。', '！', '？', '…', '；', '.', '!', '?', ';':
		return true
	}
	return false
}

// structBlockStart 后片首行是否为结构性块的开头（标题/表格/图片/列表/
// 引用/代码/公式块）——这些不是上一段的延续。
func structBlockStart(md string) bool {
	line := strings.TrimSpace(md)
	for _, p := range []string{"#", "|", "> ", "- ", "* ", "+ ", "!["} {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") || strings.HasPrefix(line, "$$") {
		return true
	}
	// 有序列表：行首数字（≤3 位）后跟 [.、)]（rune 比较：、为多字节）
	runes := []rune(line)
	d := 0
	for d < len(runes) && d < 4 && runes[d] >= '0' && runes[d] <= '9' {
		d++
	}
	if d > 0 && d < len(runes) {
		switch runes[d] {
		case '.', '、', ')':
			return true
		}
	}
	return false
}

// firstLineOf 多行文本的首行（行内去首尾空白）。
func firstLineOf(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// lastLineOf 多行文本的末行（行内去首尾空白）。
func lastLineOf(s string) string {
	if i := strings.LastIndexAny(s, "\r\n"); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(s)
}

func tableRowStart(s string) bool { return strings.HasPrefix(strings.TrimSpace(s), "|") }

func tableRowEnd(s string) bool {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return len(lines) > 0 && tableRowStart(lines[len(lines)-1])
}

func sameFileBytes(a, b string) bool {
	sa, err1 := os.Stat(a)
	sb, err2 := os.Stat(b)
	if err1 != nil || err2 != nil || sa.Size() != sb.Size() {
		return false
	}
	da, err1 := os.ReadFile(a)
	db, err2 := os.ReadFile(b)
	return err1 == nil && err2 == nil && string(da) == string(db)
}

// cleanupStaleResults 清理 parseDir 顶层历史 result-*（保留 keep），
// 与 mineru 包内 purgeStale 同语义：分片合并发布走管线侧同名逻辑。
func cleanupStaleResults(parseDir, keep string) {
	entries, err := os.ReadDir(parseDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "result-") {
			continue
		}
		full := filepath.Join(parseDir, e.Name())
		if full == keep {
			continue
		}
		_ = os.RemoveAll(full)
	}
}
