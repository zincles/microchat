package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"microchat/internal/config"
	"microchat/internal/store"
)

// 规则只影响派生画像，不写 models 行；/models 与渠道详情都读同一份。
func TestModelRulesApplyToModelViews(t *testing.T) {
	server, st := newProvidersServer(t, `{"providers":[{"id":"xai","vendor":"xai"}]}`)
	if _, _, _, err := st.RefreshDiscovered("xai", []store.Discovered{{UpstreamID: "grok-4.7"}}, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}

	recorder := call(server, "GET", "/api/v1/models", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /models = %d：%s", recorder.Code, recorder.Body.String())
	}
	var items []ModelListItem
	if err := json.Unmarshal(recorder.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Brand != "xAI" || items[0].Icon != "xai" {
		t.Fatalf("默认 grok 规则没生效：%+v", items)
	}
	if items[0].ThinkingLevels == nil {
		t.Fatal("thinking_levels 不该缺（fallback 是七档）")
	}

	// 渠道详情里的模型视图必须是同一份派生结果。
	recorder = call(server, "GET", "/api/v1/providers", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /providers = %d：%s", recorder.Code, recorder.Body.String())
	}
	var providers []ProviderView
	if err := json.Unmarshal(recorder.Body.Bytes(), &providers); err != nil {
		t.Fatal(err)
	}
	if len(providers) != 1 || len(providers[0].Models) != 1 || providers[0].Models[0].Icon != "xai" {
		t.Fatalf("渠道模型视图没带派生画像：%+v", providers)
	}

	// 覆写：后命中的规则覆盖品牌，并把 thinking_levels 整组替换。
	body := `{"model_define_rules":[
		{"id":"xai","match_contains":"grok","brand":"xAI","icon":"xai","thinking_levels":["low","high"]},
		{"id":"xai-final","match_contains":"grok","is_final":true,"brand":"Grok!"}
	]}`
	recorder = call(server, "PUT", "/api/v1/config/model-rules", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PUT rules = %d：%s", recorder.Code, recorder.Body.String())
	}
	recorder = call(server, "GET", "/api/v1/models", "")
	if err := json.Unmarshal(recorder.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if items[0].Brand != "Grok!" || len(items[0].ThinkingLevels) != 2 || items[0].MatchedRules[1] != "xai-final" {
		t.Fatalf("PUT 后画像 = %+v", items[0])
	}

	// 坏规则：422，且不落盘（原来的规则还在）。
	recorder = call(server, "PUT", "/api/v1/config/model-rules", `{"model_define_rules":[{"match_contains":"x"}]}`)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("坏规则该 422，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	recorder = call(server, "GET", "/api/v1/config/model-rules", "")
	var rules config.ModelRulesConfig
	if err := json.Unmarshal(recorder.Body.Bytes(), &rules); err != nil {
		t.Fatal(err)
	}
	if len(rules.Rules) != 2 || rules.Rules[1].ID != "xai-final" {
		t.Fatalf("坏规则不该覆盖旧规则：%+v", rules)
	}
}
