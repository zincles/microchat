package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"microchat/internal/abilities"
	"microchat/internal/config"
)

// `judge` 退役之后的兼容性（2026-10-28 用户定：「暂时砍掉 agent 的 judge 功能」）。
//
// 它从前是三个能力之一（`title` / `compact` / `judge`），但流程一直没写、也没有模板 ——
// 界面上就是一块点了没反应的开关。现在它从 `abilities.definitions` 里拿掉，
// 与 `display_mode` 同一条处理口径：**老文件不能炸**。
//
// 现网那份 `data/config/agents.json` 就有四个 agent 写着 `"judge": {…}` ——
// 要是把它当"写错的 id"报，启动时 `abilities.Validate` 会直接让服务起不来。
func TestLegacyAgentsFileWithJudgeStillLoads(t *testing.T) {
	box := newDummySandboxWith(t, true)

	// 直接写一份"老"配置：judge 混在里面（还带着开关与覆盖，证明我们不是只认键名）
	legacy := `{
	  "default_agent": "老的",
	  "agents": [
	    {"id":"老的","name":"老 Agent","system_prompt":"你是主持人。",
	     "abilities":{"judge":{"enabled":false,"provider":"dummy","model":"m1"},
	                   "compact":{"enabled":true}}}
	  ]
	}`
	path := filepath.Join(filepath.Dir(box.server.paths.Config("agents.json")), "agents.json")
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	// ① 读得动：`GET /agents` 列出它，compact 的开关原样，**不再有 judge 这块能力**
	got := decodeBody[struct {
		Agents []struct {
			ID        string                             `json:"id"`
			Abilities map[string]config.AbilityToggle    `json:"abilities"`
		} `json:"agents"`
	}](t, authCall(box.server, "GET", "/api/v1/agents", "", testSysToken))

	var found bool
	for _, agent := range got.Agents {
		if agent.ID != "老的" {
			continue
		}
		found = true
		if toggle, ok := agent.Abilities["compact"]; !ok || toggle.Enabled == nil || !*toggle.Enabled {
			t.Fatalf("compact 的开关读歪了：%+v", agent.Abilities)
		}
	}
	if !found {
		t.Fatal("老 agent 没被读出来")
	}

	// ② 改别的字段照样能存（不是"读得动但写不动"）
	if got := authCall(box.server, "PATCH", "/api/v1/agents/老的", `{"name":"新名字"}`, testSysToken); got.Code != 200 {
		t.Fatalf("改老 agent = %d（%s）", got.Code, got.Body.String())
	}

	// ③ 存完再读：改名生效 —— 且 `judge` 已经**自然消失**（abilities 是整段替换，
	//    前端只发在册的两个能力 ⇒ 退役的那个跟着没掉）
	_, agentsConfig, _, err := config.Load(box.server.paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(agentsConfig.Agents) != 1 || agentsConfig.Agents[0].Name != "新名字" {
		t.Fatalf("改名没落盘：%+v", agentsConfig.Agents)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, item := range raw["agents"].([]any) {
		abilitiesOf, _ := item.(map[string]any)["abilities"].(map[string]any)
		if _, ok := abilitiesOf["judge"]; ok {
			t.Fatal("整份重写之后文件里不该再有 judge（它已不是在册能力）")
		}
	}
}

// 退役 ≠ 什么都不查：写错一个 id 仍要 400（否则"配了不生效"这类问题又回来了）。
func TestAgentWriteStillRejectsUnknownAbilityID(t *testing.T) {
	box := newDummySandboxWith(t, true)

	recorder := authCall(box.server, "POST", "/api/v1/agents",
		`{"name":"新的","abilities":{"judgee":{"enabled":true}}}`, testSysToken)
	if recorder.Code != 400 {
		t.Fatalf("写错的能力 id 该 400，得到 %d（%s）", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); !containsAll(body, "judgee", "title / compact") {
		t.Fatalf("错误该点名那个 id 与现在认哪些：%s", body)
	}
}

// 在册能力就是两个 —— 这条钉的是"退役"这个事实本身，防止哪天被悄悄加回去。
func TestAbilityRegistryOnlyTitleAndCompact(t *testing.T) {
	if ids := abilities.IDs(); len(ids) != 2 || ids[0] != abilities.Title || ids[1] != abilities.Compact {
		t.Fatalf("在册能力该是 [title compact]，得到 %v", ids)
	}
	if abilities.Valid(abilities.Judge) {
		t.Fatal("judge 已退役：Valid 该回 false")
	}
}
