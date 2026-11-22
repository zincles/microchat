package server

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"microchat/internal/config"
	"microchat/internal/store"
)

func contains(list []string, want string) bool { return slices.Contains(list, want) }

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
	if len(items) != 1 || items[0].Brand != "xAI" || items[0].Icon != "grok" {
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
	if len(providers) != 1 || len(providers[0].Models) != 1 || providers[0].Models[0].Icon != "grok" {
		t.Fatalf("渠道模型视图没带派生画像：%+v", providers)
	}

	// 覆写：后命中的规则覆盖品牌，并把 thinking_levels 整组替换。
	body := `{"model_define_rules":[
		{"id":"xai","match_contains":"grok","brand":"xAI","icon":"grok","thinking_levels":["low","high"]},
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
	if items[0].Brand != "Grok!" || len(items[0].ThinkingLevels) != 2 ||
		!contains(items[0].MatchedRules, "global:xai-final") || !contains(items[0].MatchedRules, "global:grok") {
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

// 系统内置规则：GET 分列、可停用；停用后不再参与画像；用户规则不能复用内置 id。
func TestBuiltinModelRulesViewAndDisable(t *testing.T) {
	server, st := newProvidersServer(t, `{"providers":[{"id":"xai","vendor":"xai"}]}`)
	if _, _, _, err := st.RefreshDiscovered("xai", []store.Discovered{{UpstreamID: "grok-4.7"}}, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	recorder := call(server, "GET", "/api/v1/config/model-rules", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET rules = %d：%s", recorder.Code, recorder.Body.String())
	}
	var view config.ModelRulesView
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.BuiltinModelRules) == 0 || !view.BuiltinModelRules[0].Builtin {
		t.Fatalf("没有系统内置规则：%+v", view)
	}
	if len(view.ModelDefineRules) != 0 {
		t.Fatalf("内置规则不该出现在用户规则里：%+v", view.ModelDefineRules)
	}

	body := `{"model_define_rules":[],"disabled_builtin_model_rules":["xai-grok-4-native"]}`
	recorder = call(server, "PUT", "/api/v1/config/model-rules", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PUT disable = %d：%s", recorder.Code, recorder.Body.String())
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	disabled := false
	for _, rule := range view.BuiltinModelRules {
		if rule.ID == "xai-grok-4-native" {
			disabled = rule.Disabled
		}
	}
	if !disabled {
		t.Fatalf("停用状态没写回视图：%+v", view.BuiltinModelRules)
	}
	recorder = call(server, "GET", "/api/v1/models", "")
	var items []ModelListItem
	if err := json.Unmarshal(recorder.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || len(items[0].ServerTools) != 0 {
		t.Fatalf("停用内置 grok 规则后仍有工具：%+v", items)
	}

	// 用户规则复用内置 id / 未知停用 id 都 422。
	recorder = call(server, "PUT", "/api/v1/config/model-rules",
		`{"model_define_rules":[{"id":"xai-grok-4-native","match_contains":"grok","server_tools":["x_search"]}]}`)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("复用内置 id 该 422，得到 %d", recorder.Code)
	}
	recorder = call(server, "PUT", "/api/v1/config/model-rules",
		`{"model_define_rules":[],"disabled_builtin_model_rules":["nope"]}`)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("未知停用 id 该 422，得到 %d", recorder.Code)
	}
}

// 渠道规则：存在 providers.json 里，应用在全局之后，优先覆盖；视图要能回读。
func TestProviderRulesOverrideGlobal(t *testing.T) {
	server, st := newProvidersServer(t, `{"providers":[{"id":"xai","vendor":"xai","model_define_rules":[
		{"id":"custom","match_contains":"grok","brand":"渠道自定义","icon":"grok"}
	]}]}`)
	if _, _, _, err := st.RefreshDiscovered("xai", []store.Discovered{{UpstreamID: "grok-4.7"}}, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}

	recorder := call(server, "GET", "/api/v1/providers", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /providers = %d：%s", recorder.Code, recorder.Body.String())
	}
	var providers []ProviderView
	if err := json.Unmarshal(recorder.Body.Bytes(), &providers); err != nil {
		t.Fatal(err)
	}
	if len(providers) != 1 || len(providers[0].ModelDefineRules) != 1 {
		t.Fatalf("渠道规则没回读：%+v", providers)
	}

	recorder = call(server, "GET", "/api/v1/models", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /models = %d：%s", recorder.Code, recorder.Body.String())
	}
	var items []ModelListItem
	if err := json.Unmarshal(recorder.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Brand != "渠道自定义" || items[0].Icon != "grok" {
		t.Fatalf("渠道规则没覆盖全局：%+v", items)
	}
}

// 渠道规则通过 PATCH 写入：数组替换；坏规则 422。
func TestProviderRulesPatchAndValidation(t *testing.T) {
	server, _ := newProvidersServer(t, `{"providers":[{"id":"xai","vendor":"xai"}]}`)
	recorder := call(server, "PATCH", "/api/v1/providers/xai", `{"model_define_rules":[
		{"id":"local","match_contains":"grok","brand":"Local","is_final":false}
	]}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PATCH = %d：%s", recorder.Code, recorder.Body.String())
	}
	var view ProviderView
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.ModelDefineRules) != 1 || view.ModelDefineRules[0].ID != "local" {
		t.Fatalf("渠道规则没写进视图：%+v", view.ModelDefineRules)
	}

	// 空定义是坏规则，不能落盘
	recorder = call(server, "PATCH", "/api/v1/providers/xai", `{"model_define_rules":[{"match_contains":"grok"}]}`)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("坏规则该 422，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	recorder = call(server, "GET", "/api/v1/providers", "")
	var providers []ProviderView
	if err := json.Unmarshal(recorder.Body.Bytes(), &providers); err != nil {
		t.Fatal(err)
	}
	if len(providers) != 1 || len(providers[0].ModelDefineRules) != 1 {
		t.Fatalf("坏规则不该覆盖旧值：%+v", providers)
	}
}
