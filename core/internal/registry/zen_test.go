package registry

import (
	"encoding/json"
	"testing"

	"microchat/internal/config"
	"microchat/internal/store"
)

// fake /models 回 Zen 形状 ⇒ muse-spark-1.3 带 zen_protocol，deepseek-v4.1-flash 不动。
func TestProbeTagsZenProtocol(t *testing.T) {
	clearKeyEnv(t)
	upstream := stubUpstream(t, 200,
		`{"data":[{"id":"muse-spark-1.3","object":"model","created":1,"owned_by":"zen"},{"id":"deepseek-v4.1-flash","object":"model","created":1,"owned_by":"zen"}]}`)
	provider := config.Provider{ID: "z", Vendor: "opencode", BaseURL: upstream.URL}
	result, err := Probe(provider)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Models) != 2 {
		t.Fatalf("%+v", result.Models)
	}
	var tagged map[string]any
	if err := json.Unmarshal(result.Models[0].UpstreamParams, &tagged); err != nil {
		t.Fatalf("muse-spark-1.3 该带标：%v", err)
	}
	if tagged["zen_protocol"] != "openai-response" {
		t.Fatalf("标 = %v", tagged)
	}
	if len(result.Models[1].UpstreamParams) != 0 {
		t.Fatalf("deepseek-v4.1-flash 不该动：%s", result.Models[1].UpstreamParams)
	}
}

// 非 Zen vendor ⇒ 不打标；已有 UpstreamParams 并进去不覆盖。
func TestWithZenProtocolVendorAndMerge(t *testing.T) {
	// openai vendor：原样返回
	plain := []store.Discovered{{UpstreamID: "muse-spark-1.3"}}
	got := withZenProtocol(config.Provider{ID: "z", Vendor: "openai"}, plain)
	if len(got[0].UpstreamParams) != 0 {
		t.Fatalf("非 Zen vendor 不该打标：%s", got[0].UpstreamParams)
	}
	// opencode vendor：已有 JSON 保留，只加键
	existing := []store.Discovered{{
		UpstreamID:     "muse-spark-1.3",
		UpstreamParams: json.RawMessage(`{"supported":["a"]}`),
	}}
	got = withZenProtocol(config.Provider{ID: "z", Vendor: "opencode"}, existing)
	var merged map[string]any
	if err := json.Unmarshal(got[0].UpstreamParams, &merged); err != nil {
		t.Fatal(err)
	}
	if merged["zen_protocol"] != "openai-response" {
		t.Fatalf("标没并进去：%v", merged)
	}
	if supported, _ := merged["supported"].([]any); len(supported) != 1 {
		t.Fatalf("已有的键被覆盖了：%v", merged)
	}
}

// 打标能一路落库：RefreshDiscovered 把 zen_protocol 写进 upstream_params 列。
func TestZenTagSurvivesRefresh(t *testing.T) {
	clearKeyEnv(t)
	st := openStore(t)
	upstream := stubUpstream(t, 200, `{"data":[{"id":"muse-spark-1.3"}]}`)
	provider := config.Provider{ID: "z", Vendor: "opencode", BaseURL: upstream.URL}
	if _, err := RefreshOne(provider, st); err != nil {
		t.Fatal(err)
	}
	row, err := st.GetModel("z", "muse-spark-1.3")
	if err != nil {
		t.Fatal(err)
	}
	if row == nil {
		t.Fatal("模型没落库")
	}
	var params map[string]any
	if err := json.Unmarshal(row.UpstreamParams, &params); err != nil {
		t.Fatalf("upstream_params 不是 JSON：%s", row.UpstreamParams)
	}
	if params["zen_protocol"] != "openai-response" {
		t.Fatalf("标没落库：%v", params)
	}
}
