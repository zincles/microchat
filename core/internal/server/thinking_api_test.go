package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"microchat/internal/config"
	"microchat/internal/providers"
	"microchat/internal/store"
)

// 思考档位是 Session 级字段（2026-11-06）：建会话、PATCH、Copy、草稿预演与真发都必须带上。
func TestSessionThinkingLevel(t *testing.T) {
	box := newDummySandbox(t)
	if err := config.SaveJSON(box.server.paths.ConfigDir+"/providers.json", mustJSON(t, `{"providers":[
		{"id":"dummy","vendor":"dummy"},
		{"id":"deepseek","vendor":"deepseek","base_url":"https://api.deepseek.com"}
	]}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := box.store.RefreshDiscovered("deepseek", []store.Discovered{{UpstreamID: "deepseek-flash"}}, 1); err != nil {
		t.Fatal(err)
	}
	models := decodeBody[[]struct {
		UpstreamID     string   `json:"upstream_id"`
		ThinkingLevels []string `json:"thinking_levels"`
	}](t, authCall(box.server, "GET", "/api/v1/models", "", testSysToken))
	if len(models) != 1 || len(models[0].ThinkingLevels) != 7 {
		t.Fatalf("/models 没带思考能力：%+v", models)
	}

	// 建会话：显式 max 要落库并随响应回来。
	made := authCall(box.server, "POST", "/api/v1/sessions",
		`{"provider":"deepseek","model":"deepseek-flash","thinking_level":"max"}`, testSysToken)
	if made.Code != http.StatusCreated {
		t.Fatalf("建会话 = %d（%s）", made.Code, made.Body.String())
	}
	session := decodeBody[struct {
		ID            string `json:"id"`
		ThinkingLevel string `json:"thinking_level"`
	}](t, made)
	if session.ThinkingLevel != "max" {
		t.Fatalf("thinking_level = %q，想要 max", session.ThinkingLevel)
	}

	// Copy 要原样带走（它是会话的一部分，不是模型临时状态）。
	copied := authCall(box.server, "POST", "/api/v1/sessions/"+session.ID+"/copy", "", testSysToken)
	copiedSession := decodeBody[struct {
		ID            string `json:"id"`
		ThinkingLevel string `json:"thinking_level"`
	}](t, copied)
	if copiedSession.ThinkingLevel != "max" {
		t.Fatalf("Copy 丢了思考档位：%+v", copiedSession)
	}

	// PATCH：改值、清回默认、非法值拒绝。
	patched := authCall(box.server, "PATCH", "/api/v1/sessions/"+session.ID, `{"thinking_level":"low"}`, testSysToken)
	if patched.Code != http.StatusOK || decodeBody[struct {
		ThinkingLevel string `json:"thinking_level"`
	}](t, patched).ThinkingLevel != "low" {
		t.Fatalf("PATCH low = %d（%s）", patched.Code, patched.Body.String())
	}
	cleared := authCall(box.server, "PATCH", "/api/v1/sessions/"+session.ID, `{"thinking_level":""}`, testSysToken)
	if cleared.Code != http.StatusOK || decodeBody[struct {
		ThinkingLevel string `json:"thinking_level"`
	}](t, cleared).ThinkingLevel != "" {
		t.Fatalf("PATCH 清空 = %d（%s）", cleared.Code, cleared.Body.String())
	}
	if bad := authCall(box.server, "PATCH", "/api/v1/sessions/"+session.ID, `{"thinking_level":"ultra"}`, testSysToken); bad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("非法档位该 422，得到 %d（%s）", bad.Code, bad.Body.String())
	}

	// 草稿预演：选中的档位必须进请求体（预演 = 真发）。
	preview := authCall(box.server, "POST", "/api/v1/outgoing",
		`{"provider":"deepseek","model":"deepseek-flash","thinking_level":"medium","content":"你好"}`, testSysToken)
	if preview.Code != http.StatusOK {
		t.Fatalf("草稿预演 = %d（%s）", preview.Code, preview.Body.String())
	}
	payload := decodeBody[providers.LastPayload](t, preview)
	var body map[string]any
	if err := json.Unmarshal(payload.Body, &body); err != nil {
		t.Fatal(err)
	}
	thinking, _ := body["thinking"].(map[string]any)
	if thinking["type"] != "enabled" || body["reasoning_effort"] != "high" {
		t.Fatalf("草稿预演体没带思考映射：%+v", body)
	}

	// 会话预演同一支笔：把 PATCH 回 low，body 应该是 low（不是旧值）。
	if err := box.store.SetSessionThinking(session.ID, "low"); err != nil {
		t.Fatal(err)
	}
	preview = authCall(box.server, "POST", "/api/v1/sessions/"+session.ID+"/outgoing", `{"content":"再说一句"}`, testSysToken)
	if preview.Code != http.StatusOK {
		t.Fatalf("会话预演 = %d（%s）", preview.Code, preview.Body.String())
	}
	payload = decodeBody[providers.LastPayload](t, preview)
	body = map[string]any{}
	if err := json.Unmarshal(payload.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["reasoning_effort"] != "low" {
		t.Fatalf("会话预演没读会话档位：%+v", body)
	}
}
