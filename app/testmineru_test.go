// testmineru_test.go — conn-test 响应判据的全矩阵哨兵：
// 标量/数组/null/缺失/垃圾逐形态验证 parseConnTestCode 的判定，
// 锁死「401/403→无效、200/0→有效、子串误判（1401）、成功数组误报」四类回归。
package app

import (
	"encoding/json"
	"testing"
)

func parse(t *testing.T, raw string) (bad, ok bool) {
	t.Helper()
	return parseConnTestCode(json.RawMessage(raw))
}

func TestParseConnTestCode(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantBad bool
		wantOK  bool
	}{
		{"标量200", `200`, false, true},
		{"标量0", `0`, false, true},
		{"标量401", `401`, true, false},
		{"标量403", `403`, true, false},
		{"标量1401不受子串误判", `1401`, false, false},
		{"数组成功", `[200,0]`, false, true},
		{"数组拒绝", `[401,403]`, true, false},
		{"数组成功不误报异常", `[200]`, false, true},
		{"混合数组无效优先", `[401,200]`, true, true}, // 双旗标并存：调用方 switch 先查 bad → 判无效
		{"数组垃圾元素", `["x"]`, false, false},
		{"null视为有效", `null`, false, true},
		{"缺失视为有效", ``, false, true},
		{"垃圾文本", `"abc"`, false, false},
		{"垃圾文本含401子串不误判", `"140189"`, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bad, ok := parse(t, c.raw)
			if bad != c.wantBad || ok != c.wantOK {
				t.Fatalf("got (bad=%v, ok=%v), want (bad=%v, ok=%v)", bad, ok, c.wantBad, c.wantOK)
			}
		})
	}
}
