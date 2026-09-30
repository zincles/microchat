package registry

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"microchat/internal/config"
	"microchat/internal/store"
)

// stubUpstream：假上游 —— `GET /models` 回固定那份列表（**不联网**；照 server 那批测试的脾气）。
//
// `status` 给非 200 就是"渠道坏了"那条路（刷新要照实报错，且**不影响别的渠道**）。
func stubUpstream(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(upstream.Close)
	return upstream
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/microchat.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// 环境变量会给渠道补密钥（`ApplyPreset`）⇒ 判定跳过时先把它清干净，测试才确定。
func clearKeyEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"OPENCODE_API_KEY", "OPENAI_API_KEY", "OPENROUTER_API_KEY"} {
		t.Setenv(name, "")
	}
}

// **核心不变量**：刷新只写发现列 —— 用户覆盖（display_name / context_override）在刷新后**原样还在**，
// 而"这次没见到的删行"照旧（列表 = 上游当前那份）。
func TestRefreshOneTouchesOnlyDiscoveryColumns(t *testing.T) {
	clearKeyEnv(t)
	st := openStore(t)
	upstream := stubUpstream(t, 200,
		`{"data":[{"id":"a","owned_by":"上游","context_length":100},{"id":"b"}]}`)

	// 先铺一份旧状态：a / b 已经发现过，外加一条"上游已经没有了"的旧行（这次刷新该把它删掉）
	if _, _, _, err := st.RefreshDiscovered("x", []store.Discovered{
		{UpstreamID: "a"}, {UpstreamID: "b"}, {UpstreamID: "gone"},
	}, 1); err != nil {
		t.Fatal(err)
	}
	// 用户给 a 改过名与上下文（用户列 —— 刷新永不动）
	name, override := "我的名字", int64(200000)
	if err := st.SetModelOverride("x", "a", &name, &override, nil); err != nil {
		t.Fatal(err)
	}

	provider := config.Provider{ID: "x", Kind: "openai", BaseURL: upstream.URL}
	outcome, err := RefreshOne(provider, st)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Provider != "x" || outcome.Models != 2 || outcome.Removed != 1 {
		t.Fatalf("结果 = %+v", outcome)
	}

	// 再刷一次（幂等）：用户列还是原样
	if _, err := RefreshOne(provider, st); err != nil {
		t.Fatal(err)
	}
	rows, err := st.ListModels("x")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("该只剩上游那两个：%+v", rows)
	}
	found := false
	for _, row := range rows {
		if row.UpstreamID != "a" {
			continue
		}
		found = true
		if row.DisplayName == nil || *row.DisplayName != "我的名字" {
			t.Fatalf("用户列被刷新动了：%+v", row)
		}
		if row.ContextOverride == nil || *row.ContextOverride != 200000 {
			t.Fatalf("用户列（上下文覆盖）被刷新动了：%+v", row)
		}
		if row.OwnedBy == nil || *row.OwnedBy != "上游" {
			t.Fatalf("发现列该被写上：%+v", row)
		}
	}
	if !found {
		t.Fatalf("发现没落库：%+v", rows)
	}
	// "这次没见到的删行"那条也照旧（a 改名了不该被删，gone 该被删）
	for _, row := range rows {
		if row.UpstreamID == "gone" {
			t.Fatalf("没见到的旧行该被删：%+v", rows)
		}
	}
}

// 失败照实报错（HTTP 那边回 502 upstream；启动那条路只 log）。
func TestRefreshOneReportsUpstreamError(t *testing.T) {
	clearKeyEnv(t)
	st := openStore(t)
	upstream := stubUpstream(t, 401, `{"error":"unauthorized"}`)
	provider := config.Provider{ID: "x", Kind: "openai", BaseURL: upstream.URL, APIKey: "sk-bad"}
	if _, err := RefreshOne(provider, st); err == nil {
		t.Fatal("401 该报错")
	}
	if rows, _ := st.ListModels("x"); len(rows) != 0 {
		t.Fatalf("失败了不该落库：%+v", rows)
	}
}

// 跳过规则：dummy 不联网；**要去外网又没配 key** 的跳过（省一屏 401）；
// 本机 / 内网端点（ollama、LM Studio、假上游）没配 key 也照刷。
func TestSkip(t *testing.T) {
	clearKeyEnv(t)
	cases := []struct {
		name     string
		provider config.Provider
		want     string
	}{
		{"dummy 不联网", config.Provider{ID: "d", Kind: "dummy"}, "dummy（不联网）"},
		{"外网但没配 key（预设补上公网端点）", config.Provider{ID: "o", Kind: "openai"}, "没配 key"},
		{"opencode-go 没配 key", config.Provider{ID: "g", Kind: "opencode-go"}, "没配 key"},
		{"外网且配了 key", config.Provider{ID: "o", Kind: "openai", APIKey: "sk-x"}, ""},
		{"本机假上游（回环 IP）没配 key", config.Provider{ID: "s", Kind: "openai", BaseURL: "http://127.0.0.1:8080/v1"}, ""},
		{"本机假上游（localhost）没配 key", config.Provider{ID: "s", Kind: "openai-compat", BaseURL: "http://localhost:8080/v1"}, ""},
		{"内网端点没配 key", config.Provider{ID: "l", Kind: "openai-compat", BaseURL: "http://192.168.1.5:8080/v1"}, ""},
		{"ollama 预设（本机、无密钥概念）", config.Provider{ID: "ol", Kind: "ollama"}, ""},
	}
	for _, item := range cases {
		if got := Skip(item.provider); got != item.want {
			t.Fatalf("%s：Skip = %q，想要 %q", item.name, got, item.want)
		}
	}
}

// 环境变量里的密钥也算"配了 key"（`ApplyPreset` 会去那儿找）—— 别把能刷的渠道跳过。
func TestSkipSeesKeyFromEnv(t *testing.T) {
	t.Setenv("OPENCODE_API_KEY", "sk-from-env")
	if got := Skip(config.Provider{ID: "g", Kind: "opencode-go"}); got != "" {
		t.Fatalf("环境变量里配了密钥就该刷：%q", got)
	}
}

// 整批的编排：**跳过的不进 refresh**、**一个失败不影响别的**、顺序与配置一致、牌子以配置为准。
func TestRefreshAllSkipsAndIsolatesFailures(t *testing.T) {
	clearKeyEnv(t)
	list := []config.Provider{
		{ID: "dummy", Kind: "dummy"},
		{ID: "cloud", Kind: "opencode-go"},                                     // 没配 key ⇒ 跳过
		{ID: "local", Kind: "openai", BaseURL: "http://127.0.0.1:1/v1"},        // 本机 ⇒ 刷
		{ID: "broken", Kind: "openai-compat", BaseURL: "http://10.0.0.2:1/v1"}, // 内网 ⇒ 刷（这趟失败）
	}
	called := []string{}
	outcomes := RefreshAll(list, func(provider config.Provider) Outcome {
		called = append(called, provider.ID)
		if provider.ID == "broken" {
			return Outcome{Error: "上游不给"}
		}
		return Outcome{Models: 3, Removed: 1}
	})

	if len(outcomes) != 4 {
		t.Fatalf("一个渠道一行：%+v", outcomes)
	}
	wantCalled := []string{"local", "broken"}
	if len(called) != 2 || called[0] != wantCalled[0] || called[1] != wantCalled[1] {
		t.Fatalf("跳过的不该进 refresh（顺序也要照配置）：%v", called)
	}
	if outcomes[0].Provider != "dummy" || outcomes[0].Skipped == "" || outcomes[0].Models != 0 {
		t.Fatalf("dummy 该被跳过：%+v", outcomes[0])
	}
	if outcomes[1].Provider != "cloud" || outcomes[1].Skipped != "没配 key" {
		t.Fatalf("没配 key 该被跳过：%+v", outcomes[1])
	}
	if outcomes[2].Skipped != "" || outcomes[2].Models != 3 || outcomes[2].Removed != 1 {
		t.Fatalf("本机那个该真刷：%+v", outcomes[2])
	}
	if outcomes[3].Error != "上游不给" || outcomes[3].Models != 0 {
		t.Fatalf("失败要照实记：%+v", outcomes[3])
	}
}
