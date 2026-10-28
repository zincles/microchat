package server

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"microchat/internal/abilities"
	"microchat/internal/config"
)

// ── 能力也能拼段（2026-10-28 用户定）────────────────────────────────────────
//
// 用户的原话：「我希望"用于Compact的提示词段"也可以被**自由组合**。」
//
// 于是 `abilities.compact` 上多了一个 `segments`：它和 agent 的 `segments` 共用同一个段库、
// 同一条拼装规则（`state.JoinPrompt`）、同一处校验。压缩那一发发出去的 system 消息
// 于是 = 模板（`prompt` 覆盖 ?: 代码里的默认）+ 逐段 `"\n\n"` 拼接。
//
// 这里看的正是**载荷**（不是某个内部函数）：用户点"看载荷"看到的那条 system 消息里，
// 段必须真的在里面，且顺序 = 列表顺序。

// wireParts：解那一发请求体，回（system 模板，user 材料）。
func (p payloadPreviewShape) wireParts(t *testing.T) (string, string) {
	t.Helper()
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(p.Payload.Body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) != 2 || body.Messages[0].Role != "system" || body.Messages[1].Role != "user" {
		t.Fatalf("压缩那一发该正好两条（system 模板 + user 材料），得到 %+v", body.Messages)
	}
	return body.Messages[0].Content, body.Messages[1].Content
}

// writeAgents：把一份 agents.json 落到这个沙箱的配置目录（压缩每次都现读配置 ⇒ 立刻生效）。
//
// ⚠ `SaveJSON(path, value)` 是"**编码**这个值再写"，传 `[]byte` 会写成一串 base64（或 JSON 字符串），
// 读回来就是 `cannot unmarshal string into Go value of type config.AgentsConfig` —— 踩过。
// 所以要先把文本解成结构再交回去。
func (b *dummySandbox) writeAgents(t *testing.T, doc string) {
	t.Helper()
	var parsed config.AgentsConfig
	if err := json.Unmarshal([]byte(doc), &parsed); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(b.server.paths.Config("agents.json")), "agents.json")
	if err := config.SaveJSON(path, parsed); err != nil {
		t.Fatal(err)
	}
}

// 能力的段追加在模板后面，顺序 = 列表顺序（用倒序造数据，顺序才证得出来）。
func TestCompactTemplateAppendsAbilitySegments(t *testing.T) {
	box := newDummySandbox(t)
	box.writeAgents(t, `{
		"default_agent": "default",
		"segments": [
			{"id": "终章", "text": "【终章】收尾要短。"},
			{"id": "风格", "text": "【风格】用第二人称。"}
		],
		"agents": [{"id": "default", "name": "默认", "system_prompt": "你是助手。",
			"abilities": {"compact": {"segments": ["终章", "风格"]}}}]
	}`)
	box.seedTurn(t, "第一句")
	box.seedTurn(t, "第二句")
	box.seedTurn(t, "第三句")

	system, material := box.previewPayload(t, `{"blocks":2}`).wireParts(t)

	first := strings.Index(system, "【终章】")
	second := strings.Index(system, "【风格】")
	if first < 0 || second < 0 {
		t.Fatalf("压缩模板里没有拼上能力的段：%q", system)
	}
	if first > second {
		t.Fatalf("段的顺序该跟列表一致（终章 在 风格 之前），实际 %d > %d", first, second)
	}
	// 段间空行（`"\n\n"`）——与 agent 提示词同一条规则
	if !strings.Contains(system, "【终章】收尾要短。\n\n【风格】用第二人称。") {
		t.Fatalf("段之间该正好隔一个空行：%q", system)
	}
	// 段在**模板之后**（不是插在模板前面）
	if strings.Index(system, "【终章】") < len(system)/4 {
		t.Fatalf("段该追加在模板后面，而不是贴在开头：%q", system)
	}
	if !strings.Contains(material, "第一句") {
		t.Fatalf("材料该照旧是那段对话：%q", material)
	}
}

// 没配 segments ⇒ 模板与从前**一字不差**（老 agents.json 一个字都不用改）。
func TestCompactTemplateWithoutSegmentsIsUnchanged(t *testing.T) {
	box := newDummySandbox(t)
	box.seedTurn(t, "第一句")
	box.seedTurn(t, "第二句")
	box.seedTurn(t, "第三句")

	system, _ := box.previewPayload(t, `{"blocks":2}`).wireParts(t)
	if want := abilities.DefaultTemplate(abilities.Compact); system != want {
		t.Fatalf("没配段时压缩模板该就是代码里的默认（%d 字），得到 %d 字：%q…", len(want), len(system), firstRunes(system, 60))
	}
}

// 空 id / 库里没有的段：写入时就拒（`PATCH /agents` 走 `ValidateSegments`）。
//
// 口径与 agent 的段一致：**写了不生效是最难查的一类问题**。
func TestAgentWriteRejectsUnknownAbilitySegment(t *testing.T) {
	box := newDummySandbox(t)
	box.writeAgents(t, `{"default_agent": "default", "segments": [{"id": "在库", "text": "x"}],
		"agents": [{"id": "default", "name": "默认"}]}`)

	body := `{"abilities": {"compact": {"segments": ["不在库"]}}}`
	recorder := call(box.server, "PATCH", "/api/v1/agents/default", body)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("引用不存在的段该 400，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	if message := recorder.Body.String(); !strings.Contains(message, "不在库") {
		t.Fatalf("错误里该点名那段：%s", message)
	}

	// 改成一个**在库的**段 ⇒ 200，且 GET 回得来（JSON 键 `segments` 别写错 —— 写错就静默丢失）
	okBody := `{"abilities": {"compact": {"segments": ["在库"]}}}`
	if recorder := call(box.server, "PATCH", "/api/v1/agents/default", okBody); recorder.Code != http.StatusOK {
		t.Fatalf("在库的段该 200，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	got := call(box.server, "GET", "/api/v1/agents", "")
	if !strings.Contains(got.Body.String(), `"segments":["在库"]`) {
		t.Fatalf("GET /agents 回不来能力的段：%s", got.Body.String())
	}
}

func firstRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
