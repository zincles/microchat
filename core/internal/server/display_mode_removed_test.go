package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"microchat/internal/config"
)

// 显示形态成唯一之后的兼容性（2026-10-27 用户定：只保留"你发气泡、AI 回 markdown"）。
//
// `display_mode` 这个字段整个删了（`config.Agent` 上没有它、`CreateAgentReq` / `UpdateAgentReq`
// 也不再收）。这条钉的是**老文件不能炸**：已经在用的 `agents.json` 里还写着
// `"display_mode": "roleplay"`（或 "chat"），读进来必须照常工作 ——
// 它现在就是一个不认识的键（与当年 `params` / `prompt_order` 同一条口径：不报错，
// 下次整份重写时自己没了）。
func TestLegacyAgentsFileWithDisplayModeStillLoads(t *testing.T) {
	box := newDummySandboxWith(t, true)

	// 直接写一份"老"配置：display_mode 混在里面
	legacy := `{
	  "default_agent": "老的",
	  "agents": [
	    {"id":"老的","name":"老 Agent","system_prompt":"你是主持人。",
	     "display_mode":"roleplay","prepend_state":true}
	  ]
	}`
	cfgDir := filepath.Dir(box.server.paths.Config("agents.json"))
	path := filepath.Join(cfgDir, "agents.json")
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	// ① 读得动：`GET /agents` 列出它、名字与提示词原样
	got := decodeBody[struct {
		DefaultAgent string `json:"default_agent"`
		Agents       []struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			SystemPrompt string `json:"system_prompt"`
			PrependState bool   `json:"prepend_state"`
			DisplayMode  string `json:"display_mode"`
		} `json:"agents"`
	}](t, authCall(box.server, "GET", "/api/v1/agents", "", testSysToken))

	if got.DefaultAgent != "老的" {
		t.Fatalf("default_agent 该是「老的」，得到 %q", got.DefaultAgent)
	}
	var found bool
	for _, a := range got.Agents {
		if a.ID != "老的" {
			continue
		}
		found = true
		if a.Name != "老 Agent" || a.SystemPrompt != "你是主持人。" || !a.PrependState {
			t.Fatalf("老配置读歪了：%+v", a)
		}
		// 视图里**不再回**这个字段（结构体都没了，编码出来自然是空串）
		if a.DisplayMode != "" {
			t.Fatalf("响应里不该再有 display_mode，得到 %q", a.DisplayMode)
		}
	}
	if !found {
		t.Fatal("老 agent 没被读出来")
	}

	// ② 改别的字段照样能存（不是"读得动但写不动"）
	if got := authCall(box.server, "PATCH", "/api/v1/agents/老的", `{"name":"新名字"}`, testSysToken); got.Code != 200 {
		t.Fatalf("改老 agent = %d（%s）", got.Code, got.Body.String())
	}

	// ③ 存完再读：改名生效 —— 且 `display_mode` 已经**自然消失**（整体重写写的是结构体）
	_, agentsConfig, _, err := config.Load(box.server.paths)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, item := range raw["agents"].([]any) {
		if _, ok := item.(map[string]any)["display_mode"]; ok {
			t.Fatal("重写之后文件里不该再有 display_mode（它已不是结构体的字段）")
		}
	}
	if len(agentsConfig.Agents) != 1 || agentsConfig.Agents[0].Name != "新名字" {
		t.Fatalf("改名没落盘：%+v", agentsConfig.Agents)
	}
}

// 老客户端还发 `display_mode` ⇒ **忽略**（不报错）。接口不该因为一个过时的键把保存打回去。
func TestCreateAgentIgnoresUnknownDisplayModeField(t *testing.T) {
	box := newDummySandboxWith(t, true)

	recorder := authCall(box.server, "POST", "/api/v1/agents",
		`{"name":"新助手","system_prompt":"你好。","display_mode":"roleplay"}`, testSysToken)
	if recorder.Code != 201 {
		t.Fatalf("带过时的 display_mode 也该建得出来，得到 %d（%s）", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); len(body) > 0 {
		var view map[string]any
		if err := json.Unmarshal([]byte(body), &view); err != nil {
			t.Fatal(err)
		}
		if _, ok := view["display_mode"]; ok {
			t.Fatal("视图里不该回 display_mode")
		}
	}

	// PATCH 同样：带上它也不该 400
	agents := decodeBody[struct {
		Agents []struct {
			ID string `json:"id"`
		} `json:"agents"`
	}](t, authCall(box.server, "GET", "/api/v1/agents", "", testSysToken))
	var target string
	for _, a := range agents.Agents {
		if a.ID != "default" { // 内置默认那个不动，挑新建的
			target = a.ID
		}
	}
	if target == "" {
		t.Fatal("没找到新建的 agent")
	}
	if got := authCall(box.server, "PATCH", "/api/v1/agents/"+target, `{"display_mode":"chat"}`, testSysToken); got.Code != 200 {
		t.Fatalf("PATCH 带过时的 display_mode 该被忽略，得到 %d（%s）", got.Code, got.Body.String())
	}
}
