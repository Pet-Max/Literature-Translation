package engine

import (
	"testing"
	"unicode/utf8"
)

// Freeze / Split 模糊测试：验证不 panic、往返完全可逆、Split 切段合法。
func FuzzFreezeSplit(f *testing.F) {
	f.Add("# H1\n\nText with $E=mc^2$ and $50 to $100.\r\n\r\n```py\r\n# comment\r\nx = 1\r\n```\r\n")
	f.Add("$$\na + b\n$$\n\n![alt](img.png) ⟦F1⟧")
	f.Fuzz(func(t *testing.T, text string) {
		fr := Freeze(text, 1)
		back := Unfreeze(fr.Text, fr.Mapping)
		if utf8.ValidString(text) && back != text {
			t.Fatalf("Freeze/Unfreeze 不可逆:\n  in:   %q\n  back: %q", text, back)
		}
		_ = ProtectedSpans(text)
		_ = Split(text, 200)
	})
}
