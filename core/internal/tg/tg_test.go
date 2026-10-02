package tg

import (
	"strings"
	"testing"
)

// 切段按 rune（中文不断半个字）；4096 内不切。
func TestSplitMessage(t *testing.T) {
	if got := splitMessage("短"); len(got) != 1 || got[0] != "短" {
		t.Fatalf("短的不该切：%q", got)
	}
	long := strings.Repeat("长", 5000)
	parts := splitMessage(long)
	if len(parts) != 2 {
		t.Fatalf("5000 字该切两段：%d 段", len(parts))
	}
	joined := strings.Join(parts, "")
	if joined != long {
		t.Fatal("切完拼回去该一字不差")
	}
	for _, part := range parts {
		if len([]rune(part)) > tgMaxLen {
			t.Fatalf("一段不许超 %d：%d", tgMaxLen, len([]rune(part)))
		}
	}
	exact := strings.Repeat("x", tgMaxLen)
	if got := splitMessage(exact); len(got) != 1 {
		t.Fatalf("正好 4096 不该切：%d 段", len(got))
	}
}
