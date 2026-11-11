package server

import (
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
