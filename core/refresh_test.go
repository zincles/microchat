package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"microchat/internal/config"
	"microchat/internal/providers"
	"microchat/internal/store"
	"microchat/internal/task"
)

// modelsStub：假上游 —— `GET /models` 固定回一份（**不联网**）。`seen` 收下每次请求的 UA
// （服务器级身份那条规则要在启动这条路上也生效）。
func modelsStub(t *testing.T, status int, body string) (*httptest.Server, *[]string) {
	t.Helper()
	seen := &[]string{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		*seen = append(*seen, r.Header.Get("User-Agent"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(upstream.Close)
	return upstream, seen
}

// **启动那条路**（`refreshModels`：main.go 里启动时与 `-debug refresh-models` 共用的那一段）：
//
//   - 每个**真去刷**的渠道各自挂号一个 `refresh_models` 的 Task（后台活要挂号）；
//   - 跳过的（dummy / 要去外网又没配 key）连号都不挂；
//   - **一个渠道失败不影响别的**：它那个 Task 记"失败"，后面的照跑、照落库；
//   - 服务器级默认身份（`server.identity`）也在这条路上生效。
func TestRefreshModelsSkipsRegistersAndIsolatesFailures(t *testing.T) {
	for _, name := range []string{"OPENCODE_API_KEY", "OPENAI_API_KEY", "OPENROUTER_API_KEY"} {
		t.Setenv(name, "")
	}
	dir := t.TempDir()
	st, err := store.Open(dir + "/microchat.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	healthy, healthyUA := modelsStub(t, 200, `{"data":[{"id":"m1","owned_by":"上游"},{"id":"m2"}]}`)
	broken, _ := modelsStub(t, 500, `{"error":"boom"}`)

	// 身份：服务器级默认 = microchat ⇒ 渠道自己没写就跟着它（与 HTTP 那条路同一处规则）
	cfg := config.DefaultConfig()
	cfg.Server.Identity = string(providers.IdentityMicrochat)

	list := []config.Provider{
		{ID: "dummy", Kind: "dummy"},
		{ID: "cloud", Kind: "opencode-go"}, // 没配 key ⇒ 跳过（别拿 401 刷屏）
		{ID: "healthy", Kind: "opencode-go", APIKey: "sk-x", BaseURL: healthy.URL},
		{ID: "broken", Kind: "openai-compat", BaseURL: broken.URL},
	}
	tasks := task.NewRegistry()
	outcomes := refreshModels(st, tasks, cfg, list)

	if len(outcomes) != len(list) {
		t.Fatalf("一个渠道一行：%+v", outcomes)
	}
	// 跳过的两个：没去刷、也没挂号
	if outcomes[0].Skipped == "" || outcomes[1].Skipped != "没配 key" {
		t.Fatalf("跳过规则不对：%+v", outcomes[:2])
	}
	// 成功的那个：真拉到了两个模型
	if outcomes[2].Models != 2 || outcomes[2].Skipped != "" || outcomes[2].Error != "" {
		t.Fatalf("该真刷到 2 个：%+v", outcomes[2])
	}
	// 失败的那个：照实记；**排在它后面的照跑**（这里它就在最后 ⇒ 换成"别的没受影响"来验）
	if outcomes[3].Error == "" {
		t.Fatalf("500 该报错：%+v", outcomes[3])
	}
	rows, err := st.ListModels("healthy")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("发现该落库：%+v", rows)
	}
	if rows, _ := st.ListModels("broken"); len(rows) != 0 {
		t.Fatalf("失败的那个不该落库：%+v", rows)
	}
	// 身份那条规则在启动这条路上也生效（渠道没写 identity ⇒ 跟随服务器）
	for _, agent := range *healthyUA {
		if agent != "microchat/0.1.0" {
			t.Fatalf("UA = %q（该跟随服务器级身份 microchat）", agent)
		}
	}

	// 挂号台账：只有真去刷的两个渠道各一个 Task，且**没有僵尸条目**（不是进行中/中断）
	board := tasks.Board()
	if len(board.Tasks) != 2 {
		t.Fatalf("该只挂两个（跳过的不挂）：%+v", board.Tasks)
	}
	outcomesOfTasks := map[string]task.State{}
	for _, record := range board.Tasks {
		if record.Kind != task.KindRefreshModels {
			t.Fatalf("种类 = %q", record.Kind)
		}
		if record.Running() || record.Outcome == task.StateInterrupted {
			t.Fatalf("不许留下没收尾的条目：%+v", record)
		}
		outcomesOfTasks[record.Title] = record.Outcome
	}
	if outcomesOfTasks["刷新模型 · healthy"] != task.StateSucceeded {
		t.Fatalf("成功的那个该记「成功」：%+v", outcomesOfTasks)
	}
	if outcomesOfTasks["刷新模型 · broken"] != task.StateFailed {
		t.Fatalf("失败的那个该记「失败」：%+v", outcomesOfTasks)
	}
}
