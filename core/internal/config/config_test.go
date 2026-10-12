package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// Endpoints 只透传：JSON 能进能出，校验在 providers 侧（这里不断言行为，只钉住"字段还在"）。
func TestProviderEndpointsPassthrough(t *testing.T) {
	raw := `{"id":"go","vendor":"opencode-go","endpoints":{"openai-response":"https://x.invalid/r"}}`
	var provider Provider
	if err := json.Unmarshal([]byte(raw), &provider); err != nil {
		t.Fatal(err)
	}
	if provider.Endpoints["openai-response"] != "https://x.invalid/r" {
		t.Fatalf("%+v", provider.Endpoints)
	}
	out, err := json.Marshal(provider)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	endpoints, _ := back["endpoints"].(map[string]any)
	if endpoints["openai-response"] != "https://x.invalid/r" {
		t.Fatalf("透传丢了：%s", out)
	}
}

// 提示词段（system prompt segment）：段库与引用的一致性 —— 写入与启动读取同一个口径。
func TestValidateSegments(t *testing.T) {
	ok := AgentsConfig{
		Segments: []Segment{{ID: "katex", Text: "数学用 $…$ 写。"}},
		Agents:   []Agent{{ID: "a", Segments: []string{"katex"}}},
	}
	if err := ok.ValidateSegments(); err != nil {
		t.Fatalf("合法的该过：%v", err)
	}
	for _, probe := range []struct {
		name string
		cfg  AgentsConfig
	}{
		{"空 id", AgentsConfig{Segments: []Segment{{ID: "  "}}}},
		{"重复 id", AgentsConfig{Segments: []Segment{{ID: "a"}, {ID: "a"}}}},
		{"引用不存在", AgentsConfig{Agents: []Agent{{ID: "x", Segments: []string{"nope"}}}}},
	} {
		if err := probe.cfg.ValidateSegments(); err == nil {
			t.Fatalf("%s 该报错", probe.name)
		}
	}
}

// 内置段（2026-10-11 用户定）：正文住 `segments/*.md`（go:embed 进二进制）；id 保留、只读；
// 引用内置合法；SegmentByID 找得到。`current_state` 那条已删（用户定）。
func TestBuiltinSegmentsAreReserved(t *testing.T) {
	builtins := BuiltinSegments()
	if len(builtins) != 4 {
		t.Fatalf("内置段该四条（2026-10-11 迁移）：%+v", builtins)
	}
	if _, ok := BuiltinSegmentByID("current_state"); ok {
		t.Fatal("current_state 已删（2026-10-11 用户定）")
	}
	first := builtins[0]
	if first.ID != "StateLang 语法" || first.Text == "" {
		t.Fatalf("首条内置该是 StateLang 语法 且正文非空：%+v", first)
	}
	// 正文现读自嵌入文件（逐字）
	if !strings.Contains(first.Text, "StateLang 语法规范") {
		t.Fatalf("嵌入正文没读到？%q", first.Text[:40])
	}
	// 用户段撞内置 id ⇒ 校验拒（写入与启动读取同一个口径）
	if err := (AgentsConfig{Segments: []Segment{{ID: first.ID, Text: "x"}}}).ValidateSegments(); err == nil {
		t.Fatal("撞内置 id 该报错")
	}
	// 引用内置 ⇒ 合法
	if err := (AgentsConfig{Agents: []Agent{{ID: "a", Segments: []string{first.ID}}}}).ValidateSegments(); err != nil {
		t.Fatalf("引用内置该过：%v", err)
	}
	// SegmentByID 找得到内置（拼装靠它）
	if seg, found := (AgentsConfig{}).SegmentByID(first.ID); !found || seg.Text == "" {
		t.Fatal("SegmentByID 该找得到内置")
	}
}

// 自动压缩：**缺省 = 关**（未经验证，2026-10-11 用户定）与块数缺省（3）。
func TestAutoCompactDefaults(t *testing.T) {
	if DefaultCompactBlocks != 3 {
		t.Fatalf("缺省块数该是 3：%d", DefaultCompactBlocks)
	}
	if (ChatConfig{}).AutoCompact {
		t.Fatal("缺字段该算关（这套未经验证，默认不开）")
	}
	if !(ChatConfig{AutoCompact: true}).AutoCompact {
		t.Fatal("显式 true 该算开")
	}
}
