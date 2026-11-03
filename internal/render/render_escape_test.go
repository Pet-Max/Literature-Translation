// render_escape_test.go — \$ 转义哨兵：冻结区内的 \$ 必须还原为字面 $，
// 不得以 NUL+"ESC"+NUL 形态残留在导出 HTML（历史转义缺陷的回归锁）。
package render

import (
	"strings"
	"testing"
)

func TestBuildEscapedDollarNoNUL(t *testing.T) {
	dollar := `\$` // 字面的反斜杠+美元符
	md := "代码：\n\n```\n价格 " + dollar + "5\n```\n\n公式 $a" + dollar + "b$ 结束。"
	out := Build(md, Options{Title: "t"})
	if strings.ContainsRune(out, '\x00') {
		t.Fatalf("输出残留 NUL/ESC 标记:\n%s", out)
	}
	if !strings.Contains(out, "$5") {
		t.Fatalf("代码块内的 $ 丢失:\n%s", out)
	}
	if !strings.Contains(out, "a"+dollar+"b") {
		t.Fatalf("公式内的 \\$ 丢失（data-tex 应含 a\\$b）:\n%s", out)
	}
}
