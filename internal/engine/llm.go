// llm.go — OpenAI 兼容 chat/completions 客户端。
//
// 只使用通用请求体
// （model/messages/temperature），非流式，不依赖任何厂商私有参数，
// 保证对各类兼容服务的最大适配面。网络层重试（429/5xx/超时，指数
// 退避）只在这里做；质量重试（校验失败回喂）由 engine 循环负责，
// 两层互不混淆。
//
// 请求禁止自动跟随重定向，使用 CheckRedirect 显式控制；
// 200 但 body 异常（非 JSON/非对象/空内容）与其它 4xx 一样立即报错
// 不重试。
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// LLMError 调用失败（可读文本，可直接进日志/界面）。
type LLMError struct{ Msg string }

func (e *LLMError) Error() string { return e.Msg }

// ChatMessage 一条对话消息。
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content *string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// LLMClient OpenAI 兼容补全客户端。
type LLMClient struct {
	url         string
	apiKey      string
	model       string
	maxAttempts int
	client      *http.Client
}

// NewLLMClient 构造客户端。timeoutSec 为单次请求超时秒数；
// maxAttempts 为网络层最大尝试次数。
func NewLLMClient(baseURL, apiKey, model string, timeoutSec float64, maxAttempts int) *LLMClient {
	return &LLMClient{
		url:         strings.TrimRight(baseURL, "/") + "/chat/completions",
		apiKey:      apiKey,
		model:       model,
		maxAttempts: maxAttempts,
		client: &http.Client{
			Timeout: time.Duration(timeoutSec * float64(time.Second)),
			// 不跟随重定向：3xx 按普通响应处理
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// truncChars 按字符数截断。
func truncChars(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// CompleteContext 一次补全。网络层自动重试（ctx 取消时立即返回）；仍失败
// 返回 *LLMError。history 为可选的追加消息（质量重试时带上次的 assistant
// 译文与纠错指令，让模型看到自己上一次的输出）。非流式。
func (c *LLMClient) CompleteContext(ctx context.Context, system, user string, temperature float64, history []ChatMessage) (string, error) {
	messages := make([]ChatMessage, 0, 2+len(history))
	messages = append(messages, ChatMessage{Role: "system", Content: system})
	messages = append(messages, ChatMessage{Role: "user", Content: user})
	messages = append(messages, history...)
	body, err := json.Marshal(chatRequest{
		Model:       c.model,
		Messages:    messages,
		Temperature: temperature,
	})
	if err != nil {
		return "", &LLMError{Msg: "网络错误: " + err.Error()}
	}

	lastErr := ""
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		content, retryable, err := c.once(ctx, body)
		if err == nil {
			return content, nil
		}
		if !retryable {
			return "", err
		}
		lastErr = err.Error()
		if attempt < c.maxAttempts {
			d := time.Duration(1<<(attempt-1)) * time.Second // 1,2,4,8…封顶 8s
			if d > 8*time.Second {
				d = 8 * time.Second
			}
			if err := waitContext(ctx, d); err != nil {
				return "", err
			}
		}
	}
	return "", &LLMError{Msg: fmt.Sprintf("重试 %d 次后仍失败，最后错误：%s", c.maxAttempts, lastErr)}
}

// once 单次请求。retryable=true 表示值得退避重试（网络错误/429/5xx），
// err 即此刻的最后错误文案。
func (c *LLMClient) once(ctx context.Context, body []byte) (content string, retryable bool, err error) {
	req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if reqErr != nil {
		return "", false, &LLMError{Msg: "网络错误: " + reqErr.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, doErr := c.client.Do(req)
	if doErr != nil {
		return "", true, &LLMError{Msg: "网络错误: " + doErr.Error()}
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		// 响应体读取中断（网络抖动/连接重置）：按网络错误处理走重试，
		// 保留原始读取错误，不将读取失败误判为「非 JSON」
		return "", true, &LLMError{Msg: "网络错误: " + readErr.Error()}
	}
	text := string(respBody)

	if resp.StatusCode == http.StatusOK {
		if !json.Valid(respBody) {
			// 200 但 body 非 JSON：多为模型 API 地址配错（如打到了网页）
			return "", false, &LLMError{Msg: "返回了非 JSON 内容（检查模型 API 地址是否正确）: " + truncChars(text, 120)}
		}
		trimmed := bytes.TrimLeft(respBody, " \t\r\n")
		if len(trimmed) == 0 || trimmed[0] != '{' {
			// 合法 JSON 但非对象（如顶层数组）：显式转为可读文本。
			// 引号风格与历史版本文案略有差异，仅出现在配错地址时
			return "", false, &LLMError{Msg: "返回了非预期格式（应为 JSON 对象）: " + truncChars(text, 120)}
		}
		var data chatResponse
		if json.Unmarshal(respBody, &data) != nil {
			return "", false, &LLMError{Msg: "返回了非预期格式（应为 JSON 对象）: " + truncChars(text, 120)}
		}
		var content *string
		if len(data.Choices) > 0 {
			content = data.Choices[0].Message.Content
		}
		if content == nil || strings.TrimSpace(*content) == "" {
			return "", false, &LLMError{Msg: "模型返回了空内容"}
		}
		return strings.TrimSpace(*content), false, nil
	}
	switch resp.StatusCode {
	case http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return "", true, &LLMError{Msg: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncChars(text, 200))}
	}
	// 其余（鉴权失败、模型名错误等）重试无意义，直接抛
	return "", false, &LLMError{Msg: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncChars(text, 300))}
}

// Close 释放空闲连接。
func (c *LLMClient) Close() { c.client.CloseIdleConnections() }

func waitContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
