// Package mineru 封装 MinerU v4 精准解析云 API。
//
// 流程：申请批量上传链接 → PUT 上传 PDF → 轮询批次结果 →
// 下载 zip 解压，得到 full.md + images/。
package mineru

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	baseURL     = "https://mineru.net/api/v4"
	pollEvery   = 5 * time.Second
	pollTimeout = 20 * time.Minute

	// MinerU 云端单文件限制（官方策略：≤200 页 / ≤200MB）。解析上传
	// 与管线超限预检共用同一套常量，避免两处漂移。
	MaxPages = 200
	MaxBytes = 200 << 20
	// 单个 batch 的条目上限（官方策略 ≤200 个）。
	MaxFilesPerBatch = 200

	// 单条目 / 整包解压上限：防 zip 炸弹写满磁盘。上限按实际解压
	// 字节数计（LimitReader 计数），不信任 zip 头声明的大小。
	maxZipEntry = 200 << 20
	maxZipTotal = 500 << 20
)

// Result 是一次解析的产物。
type Result struct {
	// MDPath Markdown 文件绝对路径
	MDPath string
}

type apiResp struct {
	Code    *int   `json:"code"`
	MsgCode string `json:"msgCode"`
	Msg     string `json:"msg"`
	Data    struct {
		BatchID       string   `json:"batch_id"`
		FileURLs      []string `json:"file_urls"`
		ExtractResult []struct {
			State      string `json:"state"`
			FullZipURL string `json:"full_zip_url"`
			ErrMsg     string `json:"err_msg"`
		} `json:"extract_result"`
	} `json:"data"`
	Success *bool `json:"success"`
}

// friendlyCode 把 MinerU 错误码翻译为可读文本。响应形态有两种：
// 认证类 {"msgCode":"A0202","msg":"...","success":false} 与
// 业务类 {"code":-60018,"msg":"..."}，都覆盖。
func friendlyCode(r apiResp) error {
	switch r.MsgCode {
	case "A0202":
		return errors.New("MinerU Token 无效（请到 mineru.net「API 管理」页重新生成并核对）")
	case "A0211":
		return errors.New("MinerU Token 已过期，请重新生成")
	}
	if r.Success != nil && !*r.Success {
		return fmt.Errorf("MinerU 接口错误 %s: %s", r.MsgCode, r.Msg)
	}
	if r.Code == nil {
		return &APIError{Message: "MinerU 返回了无法识别的状态（缺少 code）"}
	}
	switch *r.Code {
	case 0:
		return nil
	case -60018:
		return errors.New("MinerU 今日任务量已达上限，请明天再试")
	case -60019:
		return errors.New("MinerU HTML 解析额度不足")
	default:
		return fmt.Errorf("MinerU 接口错误 code=%d msg=%s", *r.Code, "请检查服务任务状态")
	}
}

// ParsePDF 全流程解析一个 PDF，workDir 为本次任务的工作目录。
// modelVersion: "vlm"（精准，默认）或 "pipeline"（快速）。
// language: 源文档 OCR 语言代码，空值默认 "en"。
// onState 回调用于向 UI 报进度（state: pending/running/done…）。
func ParsePDF(ctx context.Context, token, pdfPath, modelVersion, language, workDir string,
	onState func(state string)) (*Result, error) {
	return parsePDF(ctx, token, pdfPath, modelVersion, language, workDir, onState, baseURL, pollEvery, pollTimeout)
}

func parsePDF(ctx context.Context, token, pdfPath, modelVersion, language, workDir string, onState func(string), apiBase string, interval, timeout time.Duration) (*Result, error) {
	if strings.TrimSpace(language) == "" {
		language = "en"
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if token == "" {
		return nil, errors.New("MinerU Token 未配置")
	}
	data, err := os.ReadFile(pdfPath)
	if err != nil {
		return nil, fmt.Errorf("读取 PDF 失败: %w", err)
	}
	if len(data) > MaxBytes {
		return nil, fmt.Errorf("PDF 超过 MinerU %dMB 上限", MaxBytes>>20)
	}

	// 1. 申请上传链接
	applyBody := map[string]any{
		"enable_formula": true,
		"enable_table":   true,
		"model_version":  modelVersion,
		"language":       language,
		"files": []map[string]any{{
			"name":    filepath.Base(pdfPath),
			"is_ocr":  false, // 自动判断，扫描件 MinerU 会自行 OCR
			"data_id": time.Now().Format("20060102150405"),
		}},
	}
	var apply apiResp
	// 单请求超时：无超时的 http.DefaultClient 遇到黑洞连接（对端不响应）
	// 会让任务永久卡死——外层 ctx 无 deadline，轮询的 20 分钟上限只在
	// 两次请求之间检查，救不了挂在单次请求里的情况。
	applyCtx, cancelApply := context.WithTimeout(ctx, 30*time.Second)
	_, err = postJSON(applyCtx, token, apiBase+"/file-urls/batch", applyBody, &apply)
	cancelApply()
	if err != nil {
		return nil, err
	}
	if err := friendlyCode(apply); err != nil {
		return nil, err
	}
	if len(apply.Data.FileURLs) == 0 || apply.Data.BatchID == "" {
		return nil, fmt.Errorf("MinerU 未返回有效的批次编号和上传链接")
	}

	// 2. PUT 上传（预签名 URL：不要设置 Content-Type 等额外头，
	// 与 MinerU 官方客户端行为一致，避免签名不匹配 403）。
	// 大文件上传给足 10 分钟（200MB 慢链路 + 黑洞连接兜底）。
	putCtx, cancelPut := context.WithTimeout(ctx, 10*time.Minute)
	defer cancelPut()
	req, err := http.NewRequestWithContext(putCtx, http.MethodPut,
		apply.Data.FileURLs[0], bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("上传 PDF 到 MinerU 失败: %w", err)
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		resp.Body.Close()
		return nil, fmt.Errorf("上传 PDF 失败 HTTP %d: %s", resp.StatusCode, string(body))
	}
	resp.Body.Close()

	// 3. 轮询批次结果
	deadline := time.Now().Add(timeout)
	consecutiveFailures := 0
	var zipURL string
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if time.Now().After(deadline) {
			return nil, errors.New("MinerU 解析超时（20 分钟），可稍后重试")
		}
		var pr apiResp
		url := apiBase + "/extract-results/batch/" + apply.Data.BatchID
		pollCtx, cancelPoll := context.WithTimeout(ctx, 30*time.Second)
		err := getJSON(pollCtx, token, url, &pr)
		cancelPoll()
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			consecutiveFailures++
			if !retryable(err) || consecutiveFailures >= 5 {
				return nil, err
			}
			if err = wait(ctx, interval); err != nil {
				return nil, err
			}
			continue
		}
		consecutiveFailures = 0
		if err := friendlyCode(pr); err != nil {
			return nil, err
		}
		if len(pr.Data.ExtractResult) == 0 {
			return nil, &APIError{Message: "MinerU 批次响应缺少任务结果"}
		}
		if len(pr.Data.ExtractResult) > 0 {
			r := pr.Data.ExtractResult[0]
			if onState != nil {
				onState(r.State)
			}
			switch r.State {
			case "done":
				zipURL = r.FullZipURL
				if zipURL == "" {
					return nil, &APIError{Message: "MinerU 完成响应缺少结果地址"}
				}
			case "pending", "running", "waiting-file", "converting":
			case "failed":
				msg := r.ErrMsg
				if msg == "" {
					msg = "（未给出原因，常见于文件损坏或页数超限）"
				}
				return nil, fmt.Errorf("MinerU 解析失败: %s", msg)
			default:
				return nil, &APIError{Message: "MinerU 返回未知任务状态"}
			}
		}
		if zipURL != "" {
			break
		}
		if err := wait(ctx, interval); err != nil {
			return nil, err
		}
	}

	// Extract and validate only this response, then publish on the same volume.
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(workDir, ".parse-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	if err := downloadZip(ctx, zipURL, stage); err != nil {
		return nil, err
	}
	result, err := locate(stage)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(stage, result.MDPath)
	if err != nil {
		return nil, err
	}
	published := filepath.Join(workDir, "result-"+strings.TrimPrefix(filepath.Base(stage), ".parse-"))
	if err := os.Rename(stage, published); err != nil {
		return nil, err
	}
	purgeStale(workDir, published)
	return &Result{MDPath: filepath.Join(published, rel)}, nil
}

// ParseFilesBatch 一次批次解析多个 PDF（探针窗口与片解析共用）：宽容
// 模式——条目级失败不中止批次，逐条目回报结果与错误（探针调用方视
// 条目失败为整体降级；片解析调用方对失败片单独重试）。服务端并行
// 解析，墙钟时间 = 最慢片而非各片之和。language 空值默认 "en"。
// onState 按条目序号回报状态。
func ParseFilesBatch(ctx context.Context, token string, pdfPaths []string, modelVersion, language, workDir string,
	onState func(idx int, state string)) ([]*Result, []error, error) {
	return parseFilesBatch(ctx, token, pdfPaths, modelVersion, language, workDir, onState,
		baseURL, pollEvery, pollTimeout, true)
}

// parseFilesBatch 批次解析公共实现。tolerant=false（探针）时条目失败
// 直接中止批次；tolerant=true（片解析）时逐条目记入 itemErrs，其余
// 条目照常解析发布。全局性失败（申请/上传/网络轮询故障）两种模式
// 都整批中止。language 空值默认 "en"。
func parseFilesBatch(ctx context.Context, token string, pdfPaths []string, modelVersion, language, workDir string,
	onState func(idx int, state string), apiBase string, interval, timeout time.Duration, tolerant bool) ([]*Result, []error, error) {
	if strings.TrimSpace(language) == "" {
		language = "en"
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if token == "" {
		return nil, nil, errors.New("MinerU Token 未配置")
	}
	if len(pdfPaths) == 0 {
		return nil, nil, errors.New("探针批次为空")
	}
	// 全部读入内存：探针窗口仅数页，体量可忽略；与 parsePDF 的整读一致。
	datas := make([][]byte, len(pdfPaths))
	files := make([]map[string]any, len(pdfPaths))
	ts := time.Now().Format("20060102150405")
	for i, p := range pdfPaths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, nil, fmt.Errorf("读取 %s 失败: %w", p, err)
		}
		if len(data) > MaxBytes {
			return nil, nil, fmt.Errorf("%s 超过 MinerU %dMB 上限", filepath.Base(p), MaxBytes>>20)
		}
		datas[i] = data
		files[i] = map[string]any{
			"name":    filepath.Base(p),
			"is_ocr":  false, // 自动判断，扫描件 MinerU 会自行 OCR
			"data_id": fmt.Sprintf("%s-%d", ts, i),
		}
	}

	applyBody := map[string]any{
		"enable_formula": true,
		"enable_table":   true,
		"model_version":  modelVersion,
		"language":       language,
		"files":          files,
	}
	var apply apiResp
	applyCtx, cancelApply := context.WithTimeout(ctx, 30*time.Second)
	_, err := postJSON(applyCtx, token, apiBase+"/file-urls/batch", applyBody, &apply)
	cancelApply()
	if err != nil {
		return nil, nil, err
	}
	if err := friendlyCode(apply); err != nil {
		return nil, nil, err
	}
	if len(apply.Data.FileURLs) != len(pdfPaths) || apply.Data.BatchID == "" {
		return nil, nil, fmt.Errorf("MinerU 未返回与请求数一致的批次上传链接（%d/%d）",
			len(apply.Data.FileURLs), len(pdfPaths))
	}

	// 逐条目上传（预签名 URL 不带额外头，与官方客户端行为一致）
	for i, putURL := range apply.Data.FileURLs {
		putCtx, cancelPut := context.WithTimeout(ctx, 10*time.Minute)
		req, err := http.NewRequestWithContext(putCtx, http.MethodPut, putURL, bytes.NewReader(datas[i]))
		if err != nil {
			cancelPut()
			return nil, nil, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancelPut()
			return nil, nil, fmt.Errorf("上传第 %d 个探针窗口失败: %w", i+1, err)
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
		resp.Body.Close()
		cancelPut()
		if resp.StatusCode != 200 {
			return nil, nil, fmt.Errorf("上传第 %d 个探针窗口失败 HTTP %d", i+1, resp.StatusCode)
		}
	}

	// 统一轮询：全部条目 done 才继续；任一 failed 即失败
	deadline := time.Now().Add(timeout)
	consecutiveFailures := 0
	zipURLs := make([]string, len(pdfPaths))
	itemErrs := make([]error, len(pdfPaths))
	settled := make([]bool, len(pdfPaths))
	for {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		if time.Now().After(deadline) {
			return nil, nil, errors.New("MinerU 探针解析超时（20 分钟）")
		}
		var pr apiResp
		pollCtx, cancelPoll := context.WithTimeout(ctx, 30*time.Second)
		err := getJSON(pollCtx, token, apiBase+"/extract-results/batch/"+apply.Data.BatchID, &pr)
		cancelPoll()
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			consecutiveFailures++
			if !retryable(err) || consecutiveFailures >= 5 {
				return nil, nil, err
			}
			if err = wait(ctx, interval); err != nil {
				return nil, nil, err
			}
			continue
		}
		consecutiveFailures = 0
		if err := friendlyCode(pr); err != nil {
			return nil, nil, err
		}
		if len(pr.Data.ExtractResult) != len(pdfPaths) {
			return nil, nil, &APIError{Message: "MinerU 批次响应条目数与请求不符"}
		}
		// 宽容模式：条目级失败记入 itemErrs 不中止批次；strict 模式
		// 立即中止（探针语义：任一窗口失败即整体降级）
		itemFailed := make([]error, len(pdfPaths))
		allSettled := true
		for i, r := range pr.Data.ExtractResult {
			if onState != nil {
				onState(i, r.State)
			}
			if settled[i] {
				continue
			}
			switch r.State {
			case "done":
				if r.FullZipURL == "" {
					itemFailed[i] = &APIError{Message: fmt.Sprintf("MinerU 条目 %d 完成响应缺少结果地址", i+1)}
					settled[i] = true
				} else {
					zipURLs[i] = r.FullZipURL
					settled[i] = true
				}
			case "failed":
				msg := r.ErrMsg
				if msg == "" {
					msg = "（未给出原因）"
				}
				itemFailed[i] = fmt.Errorf("MinerU 条目 %d 解析失败: %s", i+1, msg)
				settled[i] = true
			default:
				allSettled = false
			}
		}
		// 条目级失败入账：宽容模式继续等其余条目；strict 模式立即中止
		for i, e := range itemFailed {
			if e == nil {
				continue
			}
			itemErrs[i] = e
			if !tolerant {
				return nil, nil, e
			}
		}
		for i := range pdfPaths {
			if !settled[i] {
				allSettled = false
				break
			}
		}
		if allSettled {
			break
		}
		if err := wait(ctx, interval); err != nil {
			return nil, nil, err
		}
	}

	// 逐条目解包发布（与 parsePDF 相同的 stage → publish 模式）；失败
	// 条目跳过（itemErrs 已记录），由调用方决定重试/降级
	results := make([]*Result, len(pdfPaths))
	for i := range pdfPaths {
		if itemErrs[i] != nil {
			continue
		}
		if err := os.MkdirAll(workDir, 0o755); err != nil {
			return nil, nil, err
		}
		stage, err := os.MkdirTemp(workDir, fmt.Sprintf(".probe-%d-*", i+1))
		if err != nil {
			return nil, nil, err
		}
		if err := downloadZip(ctx, zipURLs[i], stage); err != nil {
			os.RemoveAll(stage)
			return nil, nil, err
		}
		result, err := locate(stage)
		if err != nil {
			os.RemoveAll(stage)
			return nil, nil, err
		}
		rel, err := filepath.Rel(stage, result.MDPath)
		if err != nil {
			os.RemoveAll(stage)
			return nil, nil, err
		}
		published := filepath.Join(workDir, fmt.Sprintf("probe-%d-%s",
			i+1, strings.TrimPrefix(filepath.Base(stage), ".probe-")))
		if err := os.Rename(stage, published); err != nil {
			os.RemoveAll(stage)
			return nil, nil, err
		}
		results[i] = &Result{MDPath: filepath.Join(published, rel)}
	}
	return results, itemErrs, nil
}

// purgeStale 发布成功后清理工作目录里历史解析产物与崩溃残留：本次发布的
// result-* 保留，其余 result-*（旧解析）与 .parse-*（中断残留）删除。
// 任务队列串行 + CLI 单跑，同一 workDir 无并发发布；删除失败不影响本次结果。
func purgeStale(workDir, keep string) {
	entries, err := os.ReadDir(workDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || filepath.Join(workDir, name) == keep {
			continue
		}
		if strings.HasPrefix(name, "result-") || strings.HasPrefix(name, ".parse-") {
			_ = os.RemoveAll(filepath.Join(workDir, name))
		}
	}
}

// downloadZip 下载结果压缩包到临时文件后解压到 dir。
func downloadZip(ctx context.Context, url, dir string) error {
	// 下载给足 10 分钟（结果包可能含整篇文献的图片）。
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("下载解析结果失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("下载解析结果失败 HTTP %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp("", "mineru-*.zip")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return fmt.Errorf("保存解析结果失败: %w", err)
	}
	tmp.Close()

	zr, err := zip.OpenReader(tmpPath)
	if err != nil {
		return fmt.Errorf("解析结果不是有效 zip: %w", err)
	}
	defer zr.Close()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var totalWritten int64
	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			continue
		}
		name := filepath.Clean(f.Name)
		dst := filepath.Join(dir, name)
		// 防 zip 路径穿越：解压目标必须落在 dir 内。用路径关系判定
		// （Rel 结果不含 .. 前缀），而不是字符串包含 ".."——后者会
		// 误伤 v1..2.md 这类合法文件名，让产物静默缺失。
		rel, rerr := filepath.Rel(dir, dst)
		if rerr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(dst)
		if err != nil {
			rc.Close()
			return err
		}
		// 解压限额按实际字节数计，不信任 zip 头声明的大小
		n, err := io.Copy(out, contextReader{ctx, io.LimitReader(rc, maxZipEntry+1)})
		out.Close()
		rc.Close()
		if err != nil {
			return err
		}
		if n > maxZipEntry {
			return fmt.Errorf("zip 条目 %s 解压超过 %d MB 上限", name, maxZipEntry>>20)
		}
		totalWritten += n
		if totalWritten > maxZipTotal {
			return fmt.Errorf("zip 解压总量超过 %d MB 上限，疑似异常压缩包", maxZipTotal>>20)
		}
	}
	return nil
}

// locate accepts one full.md, or one unambiguous Markdown document.
func locate(dir string) (*Result, error) {
	var full, all []string
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if strings.HasSuffix(strings.ToLower(info.Name()), ".md") {
			all = append(all, p)
			if strings.EqualFold(info.Name(), "full.md") {
				full = append(full, p)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, errors.New("解析结果 zip 中没有 Markdown 文件")
	}
	if len(full) == 1 {
		return &Result{MDPath: full[0]}, nil
	}
	if len(full) == 0 && len(all) == 1 {
		return &Result{MDPath: all[0]}, nil
	}
	return nil, errors.New("解析结果包含多个可能的正文，无法确定 Markdown 文件")
}

func postJSON(ctx context.Context, token, url string, body any, out *apiResp) (string, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(buf)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求 MinerU 失败（检查网络）: %w", err)
	}
	defer resp.Body.Close()
	return decodeResponse(resp, out)
}

func getJSON(ctx context.Context, token, url string, out *apiResp) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("请求 MinerU 失败（检查网络）: %w", err)
	}
	defer resp.Body.Close()
	_, err = decodeResponse(resp, out)
	return err
}

// Check cancellation while decompressing large entries, not only between files.
type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
