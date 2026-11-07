package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"literature-translation/internal/config"
)

type ConnectionResult struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

func (a *App) TestLLMWithConfig(baseURL, apiKey string) ConnectionResult {
	models, err := a.FetchModelsWithConfig(baseURL, apiKey)
	if err != nil {
		return ConnectionResult{Status: "error", Message: err.Error()}
	}
	return ConnectionResult{Status: "ok", Message: fmt.Sprintf("模型列表接口连通 · %d 个模型", len(models))}
}

func (a *App) TestMinerUWithConfig(token string) ConnectionResult {
	c := config.Default()
	c.MinerU.Token = token
	c.Normalize()
	if c.MinerU.Token == "" {
		return ConnectionResult{Status: "error", Message: "请先填写 API Token"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://mineru.net/api/v4/extract-results/batch/conn-test", nil)
	req.Header.Set("Authorization", "Bearer "+c.MinerU.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ConnectionResult{Status: "error", Message: "暂时无法连接，请检查网络后重试"}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ConnectionResult{Status: "error", Message: "响应读取失败，请重试"}
	}
	return classifyMinerU(resp.StatusCode, body)
}

// conn-test 探针只验证认证与连通，不提交真实解析任务；成功态文案保留"待验证"限定。
func classifyMinerU(status int, body []byte) ConnectionResult {
	result := func(s, m string) ConnectionResult { return ConnectionResult{Status: s, Message: m} }
	var p struct {
		Code    json.RawMessage `json:"code"`
		MsgCode string          `json:"msgCode"`
		Success *bool           `json:"success"`
	}
	parsed := json.Unmarshal(body, &p) == nil
	if parsed && p.MsgCode == "A0211" {
		return result("error", "Token 已过期，请更新")
	}
	if status == 401 || status == 403 || (parsed && p.MsgCode == "A0202") {
		return result("error", "Token 认证失败，请检查凭据或账号权限")
	}
	if status == 429 {
		return result("error", "服务限流，请稍后重试")
	}
	if status >= 500 {
		return result("error", "MinerU 服务异常，请稍后重试")
	}
	if status < 200 || status >= 300 {
		return result("warning", "已收到响应，暂时无法确认服务状态")
	}
	if !parsed {
		return result("warning", "已收到响应，暂时无法确认服务状态")
	}
	var code int
	if json.Unmarshal(p.Code, &code) == nil && string(p.Code) != "null" && code == -60012 {
		return result("ok", "Token 有效，服务连通 · 解析功能待验证")
	}
	bad, ok := parseConnTestCode(p.Code)
	if bad {
		return result("error", "Token 认证失败，请检查凭据或账号权限")
	}
	if ok && (p.Success == nil || *p.Success) && p.MsgCode == "" {
		return result("ok", "Token 有效，服务连通 · 解析功能待验证")
	}
	return result("warning", "已收到响应，暂时无法确认服务状态")
}
