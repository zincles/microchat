package server

import (
	"net/url"
	"strings"
	"testing"
)

// 提示词段（system prompt segment，用户定 2026-10-11）：CRUD、引用守卫、拼装落到 /prompt。
//
// 一条链走完：建段（重名 409 / 空 id 400）→ 建 agent 挂段（未知引用 400）→ /prompt 拼装 →
// 段改名（引用一起搬）→ 整段替换 → 删除守卫（还被引用 400）→ 摘掉引用后能删。
func TestSegmentsCRUDAndAssembly(t *testing.T) {
	box := newDummySandboxWith(t, true) // true = 服务已启用（root 有口令），否则全被 setup 闸挡住

	// ① 建段
	if got := authCall(box.server, "POST", "/api/v1/segments", `{"id":"katex","text":"数学用 $…$ 写。"}`, testSysToken); got.Code != 201 {
		t.Fatalf("建段 = %d（%s）", got.Code, got.Body.String())
	}
	if got := authCall(box.server, "POST", "/api/v1/segments", `{"id":"katex","text":"x"}`, testSysToken); got.Code != 409 {
		t.Fatalf("重名该 409，得到 %d", got.Code)
	}
	if got := authCall(box.server, "POST", "/api/v1/segments", `{"id":"  ","text":"x"}`, testSysToken); got.Code != 400 {
		t.Fatalf("空 id 该 400，得到 %d", got.Code)
	}
	authCall(box.server, "POST", "/api/v1/segments", `{"id":"mcp","text":"需要联网时调用搜索工具。"}`, testSysToken)

	// ② 段库随 `GET /agents` 一起给：**内置在前（带 builtin 标记）+ 用户段在后**
	agents := decodeBody[struct {
		Segments []struct {
			ID      string `json:"id"`
			Text    string `json:"text"`
			Builtin bool   `json:"builtin"`
		} `json:"segments"`
	}](t, authCall(box.server, "GET", "/api/v1/agents", "", testSysToken))
	if len(agents.Segments) != 6 || !agents.Segments[0].Builtin || agents.Segments[0].ID != "StateLang 语法" {
		t.Fatalf("段库该是「四条内置 + 两条用户段」：%+v", agents.Segments)
	}
	if agents.Segments[4].ID != "katex" || agents.Segments[4].Builtin || agents.Segments[5].ID != "mcp" {
		t.Fatalf("用户段不该带 builtin 标记：%+v", agents.Segments[4:])
	}
	// 内置**只读**：删 / 改都拒、id 保留（要改走"复制为自定义"）
	if got := authCall(box.server, "DELETE", "/api/v1/segments/"+url.PathEscape("StateLang 语法"), "", testSysToken); got.Code != 400 {
		t.Fatalf("删内置段该 400，得到 %d（%s）", got.Code, got.Body.String())
	}
	if got := authCall(box.server, "PATCH", "/api/v1/segments/"+url.PathEscape("StateLang 语法"), `{"text":"x"}`, testSysToken); got.Code != 400 {
		t.Fatalf("改内置段该 400，得到 %d", got.Code)
	}
	if got := authCall(box.server, "POST", "/api/v1/segments", `{"id":"StateLang 语法","text":"x"}`, testSysToken); got.Code != 409 {
		t.Fatalf("与内置撞 id 该 409，得到 %d（%s）", got.Code, got.Body.String())
	}

	// ③ 建 agent：未知引用当场 400；合法引用 201
	if got := authCall(box.server, "POST", "/api/v1/agents", `{"name":"坏","segments":["nope"]}`, testSysToken); got.Code != 400 {
		t.Fatalf("未知引用该 400，得到 %d（%s）", got.Code, got.Body.String())
	}
	made := authCall(box.server, "POST", "/api/v1/agents", `{"name":"段测","system_prompt":"你是助手。","segments":["katex","mcp"]}`, testSysToken)
	if made.Code != 201 {
		t.Fatalf("建 agent = %d（%s）", made.Code, made.Body.String())
	}
	agentID := decodeBody[struct {
		ID string `json:"id"`
	}](t, made).ID

	// ④ 拼装落到 /prompt：把会话切到这个 agent
	if got := authCall(box.server, "PATCH", box.path, `{"agent_id":"`+agentID+`"}`, testSysToken); got.Code != 200 {
		t.Fatalf("切 agent = %d（%s）", got.Code, got.Body.String())
	}
	prompt := decodeBody[struct {
		Text string `json:"text"`
		Type string `json:"type"`
	}](t, authCall(box.server, "GET", box.path+"/prompt", "", testSysToken))
	want := "你是助手。\n\n数学用 $…$ 写。\n\n需要联网时调用搜索工具。"
	if prompt.Text != want || prompt.Type != "agent" {
		t.Fatalf("/prompt 拼装 = (%q, %q)，要 (%q, agent)", prompt.Text, prompt.Type, want)
	}

	// ⑤ 段改名：所有 agent 的引用一起搬
	if got := authCall(box.server, "PATCH", "/api/v1/segments/katex", `{"new_id":"katex2"}`, testSysToken); got.Code != 200 {
		t.Fatalf("段改名 = %d（%s）", got.Code, got.Body.String())
	}
	after := decodeBody[struct {
		Agents []struct {
			Segments []string `json:"segments"`
		} `json:"agents"`
	}](t, authCall(box.server, "GET", "/api/v1/agents", "", testSysToken))
	found := false
	for _, a := range after.Agents {
		if len(a.Segments) == 2 && a.Segments[0] == "katex2" {
			found = true
		}
	}
	if !found {
		t.Fatalf("改名该把引用一起搬：%+v", after.Agents)
	}

	// ⑥ 整段替换（`[]` = 清空也是整段替换的一种）
	if got := authCall(box.server, "PATCH", "/api/v1/agents/"+agentID, `{"segments":["mcp"]}`, testSysToken); got.Code != 200 {
		t.Fatalf("整段替换 = %d（%s）", got.Code, got.Body.String())
	}
	prompt = decodeBody[struct {
		Text string `json:"text"`
		Type string `json:"type"`
	}](t, authCall(box.server, "GET", box.path+"/prompt", "", testSysToken))
	if want := "你是助手。\n\n需要联网时调用搜索工具。"; prompt.Text != want {
		t.Fatalf("替换后的拼装 = %q，要 %q", prompt.Text, want)
	}

	// ⑦ 内置段可以被 agent 引用，且拼进 /prompt
	if got := authCall(box.server, "PATCH", "/api/v1/agents/"+agentID, `{"segments":["StateLang 语法"]}`, testSysToken); got.Code != 200 {
		t.Fatalf("引用内置段该 200：%d（%s）", got.Code, got.Body.String())
	}
	prompt = decodeBody[struct {
		Text string `json:"text"`
		Type string `json:"type"`
	}](t, authCall(box.server, "GET", box.path+"/prompt", "", testSysToken))
	if !strings.Contains(prompt.Text, "StateLang 语法规范") || prompt.Type != "agent" {
		t.Fatalf("内置段该拼进 /prompt：%q", prompt.Text)
	}
	if got := authCall(box.server, "PATCH", "/api/v1/agents/"+agentID, `{"segments":["mcp"]}`, testSysToken); got.Code != 200 {
		t.Fatalf("切回用户段 = %d", got.Code)
	}

	// ⑧ 删除守卫：katex2 已无人引用 ⇒ 能删；mcp 还被引用 ⇒ 400 且点名
	if got := authCall(box.server, "DELETE", "/api/v1/segments/katex2", "", testSysToken); got.Code != 204 {
		t.Fatalf("无引用的段该能删：%d（%s）", got.Code, got.Body.String())
	}
	del := authCall(box.server, "DELETE", "/api/v1/segments/mcp", "", testSysToken)
	if del.Code != 400 || !strings.Contains(del.Body.String(), "段测") {
		t.Fatalf("被引用的段该 400 且点名引用者：%d（%s）", del.Code, del.Body.String())
	}
	if got := authCall(box.server, "PATCH", "/api/v1/agents/"+agentID, `{"segments":[]}`, testSysToken); got.Code != 200 {
		t.Fatalf("清空引用 = %d", got.Code)
	}
	if got := authCall(box.server, "DELETE", "/api/v1/segments/mcp", "", testSysToken); got.Code != 204 {
		t.Fatalf("摘掉引用后该能删：%d（%s）", got.Code, got.Body.String())
	}
}
