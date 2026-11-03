package mineru

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

type APIError struct {
	Status    int
	Temporary bool
	Message   string
}

func (e *APIError) Error() string { return e.Message }

func retryable(err error) bool {
	var api *APIError
	if errors.As(err, &api) {
		return api.Temporary
	}
	var network net.Error
	return errors.As(err, &network)
}

func wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func decodeResponse(resp *http.Response, out *apiResp) (string, error) {
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", err
	}
	parsed := json.Unmarshal(b, out) == nil
	if parsed && out.MsgCode == "A0211" {
		return "", &APIError{Status: resp.StatusCode, Message: "MinerU Token 已过期，请重新生成"}
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 || (parsed && out.MsgCode == "A0202") {
		return "", &APIError{Status: resp.StatusCode, Message: "MinerU 认证失败，请核对 Token 或账号权限"}
	}
	if resp.StatusCode == 429 {
		return "", &APIError{Status: 429, Temporary: true, Message: "MinerU 服务限流，请稍后重试"}
	}
	if resp.StatusCode >= 500 {
		return "", &APIError{Status: resp.StatusCode, Temporary: true, Message: fmt.Sprintf("MinerU 服务故障（HTTP %d）", resp.StatusCode)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &APIError{Status: resp.StatusCode, Message: fmt.Sprintf("MinerU 请求被拒绝（HTTP %d）", resp.StatusCode)}
	}
	if !parsed {
		return "", &APIError{Message: "MinerU 响应不是有效 JSON 对象"}
	}
	if err = friendlyCode(*out); err != nil {
		return "", &APIError{Message: err.Error()}
	}
	return string(b), nil
}
