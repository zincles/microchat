package registry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"microchat/internal/config"
	"microchat/internal/store"
)

func errFakeSnapshot() error { return errors.New("fake snapshot down") }

// 快照 fixture：opencode 节里 a 走 responses、b 走 completions；opencode-go 节里 g 走 responses。
const zenSnapshotFixture = `{
	"opencode": {"models": {
		"a": {"provider": {"npm": "@ai-sdk/openai"}},
		"b": {"provider": {"npm": "other"}}
	}},
	"opencode-go": {"models": {
		"g": {"provider": {"npm": "@ai-sdk/openai"}}
	}}
}`

// stubModelsDev：把 modelsDevFetch 劫到 fixture（只为测打标的取数分支，不联网）。
func stubModelsDev(t *testing.T, fixture string) {
	t.Helper()
	routes, err := ParseModelsDev([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	old := modelsDevFetch
	modelsDevFetch = func() (map[string]map[string]string, error) { return routes, nil }
	t.Cleanup(func() { modelsDevFetch = old })
}

// fake /models + 快照现算 ⇒ a 带 zen_protocol，b 不动（b 在快照里是 completions）。
func TestProbeTagsZenProtocol(t *testing.T) {
	clearKeyEnv(t)
	stubModelsDev(t, zenSnapshotFixture)
	upstream := stubUpstream(t, 200,
		`{"data":[{"id":"a","object":"model","created":1},{"id":"b","object":"model","created":1}]}`)
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
		t.Fatalf("a 该带标：%v", err)
	}
	if tagged["zen_protocol"] != "openai-responses" {
		t.Fatalf("标 = %v", tagged)
	}
	if len(result.Models[1].UpstreamParams) != 0 {
		t.Fatalf("b 不该动：%s", result.Models[1].UpstreamParams)
	}
}

// 非 Zen vendor ⇒ 不打标；已有 UpstreamParams 并进去不覆盖；快照取不到 ⇒ 不打标不报错。
func TestWithZenProtocolVendorAndMerge(t *testing.T) {
	// openai vendor：原样返回
	plain := []store.Discovered{{UpstreamID: "a"}}
	got := withZenProtocol(config.Provider{ID: "z", Vendor: "openai"}, plain)
	if len(got[0].UpstreamParams) != 0 {
		t.Fatalf("非 Zen vendor 不该打标：%s", got[0].UpstreamParams)
	}
	// opencode vendor：已有 JSON 保留，只加键
	stubModelsDev(t, zenSnapshotFixture)
	existing := []store.Discovered{{
		UpstreamID:     "a",
		UpstreamParams: json.RawMessage(`{"supported":["a"]}`),
	}}
	got = withZenProtocol(config.Provider{ID: "z", Vendor: "opencode"}, existing)
	var merged map[string]any
	if err := json.Unmarshal(got[0].UpstreamParams, &merged); err != nil {
		t.Fatal(err)
	}
	if merged["zen_protocol"] != "openai-responses" {
		t.Fatalf("标没并进去：%v", merged)
	}
	if supported, _ := merged["supported"].([]any); len(supported) != 1 {
		t.Fatalf("已有的键被覆盖了：%v", merged)
	}
	// opencode-go 看自己那一节
	got = withZenProtocol(config.Provider{ID: "z", Vendor: "opencode-go"},
		[]store.Discovered{{UpstreamID: "g"}})
	var goTagged map[string]any
	if err := json.Unmarshal(got[0].UpstreamParams, &goTagged); err != nil {
		t.Fatalf("g 该带标：%v", err)
	}
	if goTagged["zen_protocol"] != "openai-responses" {
		t.Fatalf("标 = %v", goTagged)
	}
	// 快照取不到 ⇒ 不打标、不报错（刷新本身不受影响）
	old := modelsDevFetch
	modelsDevFetch = func() (map[string]map[string]string, error) { return nil, errFakeSnapshot() }
	t.Cleanup(func() { modelsDevFetch = old })
	got = withZenProtocol(config.Provider{ID: "z", Vendor: "opencode"},
		[]store.Discovered{{UpstreamID: "a"}})
	if len(got[0].UpstreamParams) != 0 {
		t.Fatalf("快照坏了就不该打标：%s", got[0].UpstreamParams)
	}
}

// 打标能一路落库：RefreshDiscovered 把 zen_protocol 写进 upstream_params 列。
func TestZenTagSurvivesRefresh(t *testing.T) {
	clearKeyEnv(t)
	stubModelsDev(t, zenSnapshotFixture)
	st := openStore(t)
	upstream := stubUpstream(t, 200, `{"data":[{"id":"a"}]}`)
	provider := config.Provider{ID: "z", Vendor: "opencode", BaseURL: upstream.URL}
	if _, err := RefreshOne(provider, st); err != nil {
		t.Fatal(err)
	}
	row, err := st.GetModel("z", "a")
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
	if params["zen_protocol"] != "openai-responses" {
		t.Fatalf("标没落库：%v", params)
	}
}

// RefreshRoutes：只认聚合站 —— 建表 + 快照节落库；非聚合站记错；快照失败不清空旧行。
func TestRefreshRoutesWritesTable(t *testing.T) {
	clearKeyEnv(t)
	stubModelsDev(t, zenSnapshotFixture)
	st := openStore(t)
	client := &http.Client{Timeout: 5 * time.Second}
	ctx := context.Background()
	list := []config.Provider{
		{ID: "zg", Vendor: "opencode-go"},
		{ID: "plain", Vendor: "openai"},
	}
	outcomes := RefreshRoutes(ctx, st, list, client)
	if len(outcomes) != 2 {
		t.Fatalf("一家一行：%+v", outcomes)
	}
	// 注意：RefreshRoutes 拉的是真快照（stubModelsDev 只劫打标那条路），行数看线上是什么就是什么
	if outcomes[0].Error != "" || outcomes[0].Models == 0 {
		t.Fatalf("聚合站该落行：%+v", outcomes[0])
	}
	if api, ok := st.ModelRoute("zg", "g"); !ok || api != "openai-responses" {
		t.Fatalf("路由表 = %q,%v", api, ok)
	}
	if outcomes[1].Error == "" {
		t.Fatalf("非聚合站该记错：%+v", outcomes[1])
	}
	// 快照整份挂掉 ⇒ 旧行留着（宁可用旧表，别拿空表断路）
	old := modelsDevFetch
	modelsDevFetch = func() (map[string]map[string]string, error) { return nil, errFakeSnapshot() }
	t.Cleanup(func() { modelsDevFetch = old })
	outcomes = RefreshRoutes(ctx, st, list, client)
	if outcomes[0].Error == "" {
		t.Fatalf("快照挂了该记错：%+v", outcomes[0])
	}
	if api, ok := st.ModelRoute("zg", "g"); !ok || api != "openai-responses" {
		t.Fatalf("旧行该留着：%q,%v", api, ok)
	}
}
