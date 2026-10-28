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

	// 单条目 / 整包解压上限：防 zip 炸弹写满磁盘。上限按实际解压
	// 字节数计（LimitReader 计数），不信任 zip 头声明的大小。
	maxZipEntry = 200 << 20
	maxZipTotal = 500 << 20
)

// Result 是一次解析的产物。
type Result struct {
	// Dir 解压后的工作目录（内含 full.md 与 images/）
	Dir string
	// MDPath Markdown 文件绝对路径
	MDPath string
	// Pages 解析页数（尽力而为，取不到为 0）
	Pages int
}

type apiResp struct {
	Code    int    `json:"code"`
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
	switch r.Code {
	case 0:
		return nil
	case -60018:
		return errors.New("MinerU 今日任务量已达上限，请明天再试")
	case -60019:
		return errors.New("MinerU HTML 解析额度不足")
	default:
		return fmt.Errorf("MinerU 接口错误 code=%d msg=%s", r.Code, r.Msg)
	}
}

// ParsePDF 全流程解析一个 PDF，workDir 为本次任务的工作目录。
// modelVersion: "vlm"（精准，默认）或 "pipeline"（快速）。
// onState 回调用于向 UI 报进度（state: pending/running/done…）。
func ParsePDF(ctx context.Context, token, pdfPath, modelVersion, workDir string,
	onState func(state string)) (*Result, error) {
	if token == "" {
		return nil, errors.New("MinerU Token 未配置")
	}
	data, err := os.ReadFile(pdfPath)
	if err != nil {
		return nil, fmt.Errorf("读取 PDF 失败: %w", err)
	}
	if len(data) > 200*1024*1024 {
		return nil, errors.New("PDF 超过 MinerU 200MB 上限")
	}

	// 1. 申请上传链接
	applyBody := map[string]any{
		"enable_formula": true,
		"enable_table":   true,
		"model_version":  modelVersion,
		"language":       "en",
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
	rawApply, err := postJSON(applyCtx, token, baseURL+"/file-urls/batch", applyBody, &apply)
	cancelApply()
	if err != nil {
		return nil, err
	}
	if err := friendlyCode(apply); err != nil {
		return nil, err
	}
	if len(apply.Data.FileURLs) == 0 {
		return nil, fmt.Errorf("MinerU 未返回上传链接，原始响应：%s", rawApply)
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
	deadline := time.Now().Add(pollTimeout)
	var zipURL string
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if time.Now().After(deadline) {
			return nil, errors.New("MinerU 解析超时（20 分钟），可稍后重试")
		}
		var pr apiResp
		url := baseURL + "/extract-results/batch/" + apply.Data.BatchID
		pollCtx, cancelPoll := context.WithTimeout(ctx, 30*time.Second)
		err := getJSON(pollCtx, token, url, &pr)
		cancelPoll()
		if err != nil {
			time.Sleep(pollEvery) // 瞬时网络问题：跳过本次，继续轮询
			continue
		}
		if err := friendlyCode(pr); err != nil {
			return nil, err
		}
		if len(pr.Data.ExtractResult) > 0 {
			r := pr.Data.ExtractResult[0]
			if onState != nil {
				onState(r.State)
			}
			switch r.State {
			case "done":
				zipURL = r.FullZipURL
			case "failed":
				msg := r.ErrMsg
				if msg == "" {
					msg = "（未给出原因，常见于文件损坏或页数超限）"
				}
				return nil, fmt.Errorf("MinerU 解析失败: %s", msg)
			}
		}
		if zipURL != "" {
			break
		}
		time.Sleep(pollEvery)
	}

	// 4. 下载并解压 zip
	if err := downloadZip(ctx, zipURL, workDir); err != nil {
		return nil, err
	}
	return locate(workDir)
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
		n, err := io.Copy(out, io.LimitReader(rc, maxZipEntry+1))
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

// locate 在解压目录中找 Markdown：优先 full.md，否则取最大的 .md。
func locate(dir string) (*Result, error) {
	var mdPath, anyPath string
	var anySize int64
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if strings.HasSuffix(strings.ToLower(info.Name()), ".md") {
			if strings.EqualFold(info.Name(), "full.md") {
				mdPath = p // full.md 优先，直接锁定
			} else if anyPath == "" || info.Size() > anySize {
				anyPath, anySize = p, info.Size()
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if mdPath == "" {
		mdPath = anyPath
	}
	if mdPath == "" {
		return nil, errors.New("解析结果 zip 中没有 Markdown 文件")
	}
	return &Result{Dir: dir, MDPath: mdPath}, nil
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
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if uerr := json.Unmarshal(raw, out); uerr != nil {
		return string(raw), fmt.Errorf("MinerU 响应解析失败 HTTP %d: %s", resp.StatusCode, string(raw[:min(len(raw), 300)]))
	}
	return string(raw), nil
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
	return json.NewDecoder(resp.Body).Decode(out)
}
