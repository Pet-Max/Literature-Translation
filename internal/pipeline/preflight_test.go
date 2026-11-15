package pipeline

import (
	"strings"
	"testing"
)

// TestOversizeIssue 超限预检文案：恰好压线不报，任一维度超限报对应
// 原因，双超合并为一条消息（用户一次看到全部原因）。
func TestOversizeIssue(t *testing.T) {
	MB := int64(1) << 20
	cases := []struct {
		name       string
		pages      int
		size       int64
		wantSubstr []string // 均须出现；空切片 = 未超限
	}{
		{"页数体积均未超", 190, 190 * MB, nil},
		{"恰好压线不报", 200, 200 * MB, nil},
		{"仅页数超", 201, 190 * MB, []string{"页数 201", "200 页"}},
		{"仅体积超", 190, 201 * MB, []string{"体积 201", "200 MB"}},
		{"双超合并一条", 201, 201 * MB, []string{"页数 201", "体积 201", "请拆分"}},
	}
	for _, c := range cases {
		got := oversizeIssue(c.pages, c.size)
		if len(c.wantSubstr) == 0 {
			if got != "" {
				t.Errorf("%s：未超限却有提示 %q", c.name, got)
			}
			continue
		}
		for _, sub := range c.wantSubstr {
			if !strings.Contains(got, sub) {
				t.Errorf("%s：提示 %q 缺少 %q", c.name, got, sub)
			}
		}
	}
}
