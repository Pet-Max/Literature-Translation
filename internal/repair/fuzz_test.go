package repair

import (
	"strings"
	"testing"
)

// Calibrate 模糊测试——任意字节混合下不 panic、不泄漏 NUL、严格幂等。
func FuzzCalibrate(f *testing.F) {
	f.Add("The diferent `env` ofmatrix nodesK = 8 $k_{n  n'}$ \x00", "The different env of matrix nodes K = 8 k_{n->n'}")
	f.Add("```awk\n`a\n```\n<table><tr><td>x</td></tr></table>", "x")
	f.Add("$$\n$$\n|00i \\right.\\left.", "|00⟩")
	f.Fuzz(func(t *testing.T, md, orig string) {
		res1, err := Calibrate(md, orig, both)
		if err != nil {
			t.Fatalf("Calibrate 返回错误: %v", err)
		}
		if strings.ContainsRune(res1.Text, 0) {
			t.Fatalf("Calibrate 输出泄漏了 NUL: %q", res1.Text)
		}
		res2, err := Calibrate(res1.Text, orig, both)
		if err != nil {
			t.Fatalf("二次 Calibrate 出错: %v", err)
		}
		if res2.Text != res1.Text {
			t.Fatalf("FuzzCalibrate 破坏幂等性:\n  in:   %q\n  out1: %q\n  out2: %q", md, res1.Text, res2.Text)
		}
	})
}
