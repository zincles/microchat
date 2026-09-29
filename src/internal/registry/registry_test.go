package registry

import "testing"

// 三级回退的第三级（照旧版逐字一致）。
func TestPrettify(t *testing.T) {
	cases := map[string]string{
		"deepseek-v4-flash":  "Deepseek V4 Flash",
		"gpt-5":              "Gpt 5",
		"provider/some-name": "Some Name",
		"x":                  "X",
	}
	for in, want := range cases {
		if got := Prettify(in); got != want {
			t.Fatalf("Prettify(%q) = %q，想要 %q", in, got, want)
		}
	}
}

// 显示名三级回退：用户覆盖 → 上游名 → prettify。
func TestDisplayName(t *testing.T) {
	name := "自定义"
	upstream := "上游名"
	if got := DisplayName(&name, &upstream, "deepseek-v4-flash"); got != "自定义" {
		t.Fatalf("用户覆盖该赢：%q", got)
	}
	if got := DisplayName(nil, &upstream, "deepseek-v4-flash"); got != "上游名" {
		t.Fatalf("上游名该赢过 prettify：%q", got)
	}
	if got := DisplayName(nil, nil, "deepseek-v4-flash"); got != "Deepseek V4 Flash" {
		t.Fatalf("回落到 prettify：%q", got)
	}
	blank := "  "
	if got := DisplayName(&blank, &upstream, "deepseek-v4-flash"); got != "上游名" {
		t.Fatalf("空白覆盖视同没覆盖：%q", got)
	}
}

// 上下文口径：覆盖 > 上游发现 > 配置兜底。
func TestContextLen(t *testing.T) {
	override, discovered := int64(200000), int64(131072)
	if got := ContextLen(&override, &discovered, 40000); got != 200000 {
		t.Fatalf("覆盖该赢：%d", got)
	}
	if got := ContextLen(nil, &discovered, 40000); got != 131072 {
		t.Fatalf("发现该赢过兜底：%d", got)
	}
	if got := ContextLen(nil, nil, 40000); got != 40000 {
		t.Fatalf("兜底：%d", got)
	}
}
