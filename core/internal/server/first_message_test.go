package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"microchat/internal/config"
	"microchat/internal/providers"
)

// 首段对话（2026-10-11 用户定）：配了 `first_message` 的 agent 建新会话 ⇒ 第一条 assistant 消息（idx 1）；
// Copy 不重复；草稿预演与真发装配一致（预演 = 真发）。
func TestFirstMessageOnCreate(t *testing.T) {
	box := newDummySandboxWith(t, true)
	agentsJSON := `{"agents":[{"id":"带首段","name":"带首段","system_prompt":"你是助手。","first_message":"你好，我是带首段。"}]}`
	if err := config.SaveJSON(box.server.paths.ConfigDir+"/agents.json", mustAgents(t, agentsJSON)); err != nil {
		t.Fatal(err)
	}

	// ① 建会话 ⇒ 首段落库：role=assistant、idx=1、正文原样
	made := authCall(box.server, "POST", "/api/v1/sessions", `{"agent_id":"带首段"}`, testSysToken)
	if made.Code != http.StatusCreated {
		t.Fatalf("建会话 = %d（%s）", made.Code, made.Body.String())
	}
	sessionID := decodeBody[struct {
		ID string `json:"id"`
	}](t, made).ID
	messages := decodeBody[[]struct {
		ID      string `json:"id"`
		Idx     int    `json:"idx"`
		Role    string `json:"role"`
		Content string `json:"content"`
	}](t, authCall(box.server, "GET", "/api/v1/sessions/"+sessionID+"/messages", "", testSysToken))
	if len(messages) != 1 || messages[0].Role != "assistant" || messages[0].Idx != 1 || messages[0].Content != "你好，我是带首段。" {
		t.Fatalf("首段该是唯一一条（assistant、idx 1、原样）：%+v", messages)
	}

	// ② (c) 预演（已存在会话）：首段在库里 ⇒ wire = system + assistant(首段) + user
	wire := draftOrSessionWire(t, box, "/api/v1/sessions/"+sessionID+"/outgoing", `{"content":"第一句"}`)
	if len(wire) != 3 || wire[1]["role"] != "assistant" || wire[1]["content"] != "你好，我是带首段。" || wire[2]["role"] != "user" {
		t.Fatalf("(c) 预演该带库里的首段：%+v", wire)
	}

	// ③ 草稿预演（会话还不存在）：**模拟**首段 —— 与真发那条装配一致
	wire = draftOrSessionWire(t, box, "/api/v1/outgoing", `{"agent_id":"带首段","content":"草稿第一句"}`)
	if len(wire) != 3 || wire[1]["role"] != "assistant" || wire[1]["content"] != "你好，我是带首段。" || wire[2]["content"] != "草稿第一句" {
		t.Fatalf("草稿预演该模拟首段：%+v", wire)
	}

	// ④ Copy 不重复：复制出来的会话消息数与原会话一致
	copied := authCall(box.server, "POST", "/api/v1/sessions/"+sessionID+"/copy", "", testSysToken)
	if copied.Code != http.StatusCreated {
		t.Fatalf("Copy = %d（%s）", copied.Code, copied.Body.String())
	}
	copiedID := decodeBody[struct {
		ID string `json:"id"`
	}](t, copied).ID
	copiedMessages := decodeBody[[]struct {
		Role string `json:"role"`
	}](t, authCall(box.server, "GET", "/api/v1/sessions/"+copiedID+"/messages", "", testSysToken))
	if len(copiedMessages) != 1 {
		t.Fatalf("Copy 不该重复插首段：%+v", copiedMessages)
	}

	// ⑤ agent 没配首段 ⇒ 新会话就是空的（不无中生有）
	if err := config.SaveJSON(box.server.paths.ConfigDir+"/agents.json", mustAgents(t, `{"agents":[{"id":"无首段","name":"无首段","system_prompt":"你是助手。"}]}`)); err != nil {
		t.Fatal(err)
	}
	plain := authCall(box.server, "POST", "/api/v1/sessions", `{"agent_id":"无首段"}`, testSysToken)
	plainID := decodeBody[struct {
		ID string `json:"id"`
	}](t, plain).ID
	plainMessages := decodeBody[[]json.RawMessage](t, authCall(box.server, "GET", "/api/v1/sessions/"+plainID+"/messages", "", testSysToken))
	if len(plainMessages) != 0 {
		t.Fatalf("没配首段的 agent 不该插消息：%+v", plainMessages)
	}
}

// PATCH agent 的 first_message 三态：nil = 不动、`""` = 清掉、非空 = 写上。
func TestUpdateAgentFirstMessage(t *testing.T) {
	box := newDummySandboxWith(t, true)
	made := authCall(box.server, "POST", "/api/v1/agents", `{"name":"首段测试","system_prompt":"x","first_message":"开场白"}`, testSysToken)
	if made.Code != http.StatusCreated {
		t.Fatalf("建 agent = %d（%s）", made.Code, made.Body.String())
	}
	agentID := decodeBody[struct {
		ID           string `json:"id"`
		FirstMessage string `json:"first_message"`
	}](t, made)
	if agentID.FirstMessage != "开场白" {
		t.Fatalf("建时该收 first_message：%+v", agentID)
	}
	// nil = 不动（只改名）
	renamed := decodeBody[struct {
		FirstMessage string `json:"first_message"`
	}](t, authCall(box.server, "PATCH", "/api/v1/agents/"+agentID.ID, `{"name":"首段测试2"}`, testSysToken))
	if renamed.FirstMessage != "开场白" {
		t.Fatalf("没给 first_message 该不动：%+v", renamed)
	}
	// "" = 清掉
	cleared := decodeBody[struct {
		FirstMessage string `json:"first_message"`
	}](t, authCall(box.server, "PATCH", "/api/v1/agents/"+agentID.ID, `{"first_message":""}`, testSysToken))
	if cleared.FirstMessage != "" {
		t.Fatalf("空串该清掉：%+v", cleared)
	}
}

// draftOrSessionWire：打一发预演（草稿或会话），回 wire 体里的 messages。
func draftOrSessionWire(t *testing.T, box *dummySandbox, path, body string) []map[string]any {
	t.Helper()
	recorder := authCall(box.server, "POST", path, body, testSysToken)
	if recorder.Code != http.StatusOK {
		t.Fatalf("%s = %d（%s）", path, recorder.Code, recorder.Body.String())
	}
	var payload providers.LastPayload
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	return wireBody(t, payload)
}
