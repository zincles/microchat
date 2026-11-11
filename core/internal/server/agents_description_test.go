package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

// Description 只是列表里的一句人话：能建、能改、能清空，且不参与提示词。
func TestAgentDescriptionRoundTrip(t *testing.T) {
	box := newDummySandboxWith(t, true)
	rec := authCall(box.server, "POST", "/api/v1/agents", `{"name":"描述测试","description":"负责旁白与 NPC"}`, testSysToken)
	if rec.Code != http.StatusCreated {
		t.Fatalf("建 agent = %d（%s）", rec.Code, rec.Body.String())
	}
	made := decodeBody[struct {
		ID          string `json:"id"`
		Description string `json:"description"`
	}](t, rec)
	if made.Description != "负责旁白与 NPC" {
		t.Fatalf("description 没落库：%+v", made)
	}
	if rec := authCall(box.server, "PATCH", "/api/v1/agents/"+made.ID, `{"description":"新的说明"}`, testSysToken); rec.Code != http.StatusOK {
		t.Fatalf("改 description = %d（%s）", rec.Code, rec.Body.String())
	}
	if rec := authCall(box.server, "PATCH", "/api/v1/agents/"+made.ID, `{"description":""}`, testSysToken); rec.Code != http.StatusOK {
		t.Fatalf("清 description = %d（%s）", rec.Code, rec.Body.String())
	}
	list := decodeBody[struct {
		Agents []struct {
			ID          string `json:"id"`
			Description string `json:"description"`
		} `json:"agents"`
	}](t, authCall(box.server, "GET", "/api/v1/agents", "", testSysToken))
	for _, agent := range list.Agents {
		if agent.ID == made.ID {
			if agent.Description != "" {
				t.Fatalf("description 没清掉：%+v", agent)
			}
			return
		}
	}
	t.Fatal("找不到刚建的 agent")
}

// FlowAgent 的 kind/flow 由编辑器原样搬运；后端暂不解释节点表。
func TestFlowAgentKindAndGraphRoundTrip(t *testing.T) {
	box := newDummySandboxWith(t, true)
	body := `{"name":"流程图","kind":"flow","flow":{"version":1,"nodes":[{"id":"in","type":"input","position":{"x":1,"y":2},"data":{}}],"edges":[]}}`
	rec := authCall(box.server, "POST", "/api/v1/agents", body, testSysToken)
	if rec.Code != http.StatusCreated {
		t.Fatalf("建 FlowAgent = %d（%s）", rec.Code, rec.Body.String())
	}
	made := decodeBody[struct {
		ID   string          `json:"id"`
		Kind string          `json:"kind"`
		Flow json.RawMessage `json:"flow"`
	}](t, rec)
	if made.Kind != "flow" || len(made.Flow) == 0 {
		t.Fatalf("kind/flow 没落库：%+v", made)
	}
	list := decodeBody[struct {
		Agents []struct {
			ID   string          `json:"id"`
			Kind string          `json:"kind"`
			Flow json.RawMessage `json:"flow"`
		} `json:"agents"`
	}](t, authCall(box.server, "GET", "/api/v1/agents", "", testSysToken))
	for _, agent := range list.Agents {
		if agent.ID == made.ID {
			if agent.Kind != "flow" || len(agent.Flow) == 0 {
				t.Fatalf("读回的 kind/flow 不对：%+v", agent)
			}
			return
		}
	}
	t.Fatal("找不到刚建的 FlowAgent")
}

func TestAgentRejectsUnknownKind(t *testing.T) {
	box := newDummySandboxWith(t, true)
	rec := authCall(box.server, "POST", "/api/v1/agents", `{"name":"坏类型","kind":"graph"}`, testSysToken)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未知 kind 该 400，得到 %d（%s）", rec.Code, rec.Body.String())
	}
}

// FlowAgent 的测试发送：走最小图 input -> agent(default) -> output，返回每步 I/O。
func TestFlowAgentTestSend(t *testing.T) {
	box := newDummySandboxWith(t, true)
	flow := `{"version":1,"nodes":[
	  {"id":"in","type":"input","position":{"x":0,"y":0},"data":{}},
	  {"id":"a","type":"agent","position":{"x":1,"y":0},"data":{"agent_id":"default"}},
	  {"id":"out","type":"output","position":{"x":2,"y":0},"data":{}}
	],"edges":[{"id":"e1","source":"in","target":"a"},{"id":"e2","source":"a","target":"out"}]}`
	body, _ := json.Marshal(map[string]any{"name": "测试图", "kind": "flow", "flow": json.RawMessage(flow)})
	rec := authCall(box.server, "POST", "/api/v1/agents", string(body), testSysToken)
	if rec.Code != http.StatusCreated {
		t.Fatalf("建 FlowAgent = %d（%s）", rec.Code, rec.Body.String())
	}
	made := decodeBody[struct {
		ID string `json:"id"`
	}](t, rec)
	rec = authCall(box.server, "POST", "/api/v1/agents/"+made.ID+"/flow/test", `{"message":"你好"}`, testSysToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("测试发送 = %d（%s）", rec.Code, rec.Body.String())
	}
	got := decodeBody[struct {
		Output string `json:"output"`
		Steps  []struct {
			ID     string   `json:"id"`
			Inputs []string `json:"inputs"`
			Output string   `json:"output"`
		} `json:"steps"`
	}](t, rec)
	if len(got.Steps) != 3 || got.Steps[0].Output != "你好" || got.Output != got.Steps[2].Output {
		t.Fatalf("测试步骤不对：%+v", got)
	}
	if len(got.Steps[1].Inputs) != 1 || got.Steps[1].Inputs[0] != "你好" {
		t.Fatalf("agent 输入没接上：%+v", got.Steps[1])
	}
}
