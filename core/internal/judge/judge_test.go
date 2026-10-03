package judge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"microchat/internal/config"
	"microchat/internal/model"
	"microchat/internal/providers"
	"microchat/internal/store"
	"microchat/internal/task"
)

// 往返：URL verbatim、体只有 model/state/questions、answers 照解析。
func TestEvaluateRoundTrip(t *testing.T) {
	state := map[string]any{"turn": 3}
	questions := map[string]any{"q1": "追吗"}
	var gotMethod, gotURL string
	var gotBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotURL = r.Method, r.URL.String()
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"answers":{"q1":"追"},"usage":{"tokens":7},"model":"jev-1"}`))
	}))
	t.Cleanup(upstream.Close)

	provider := providers.Provider{
		ID: "jev", Vendor: providers.VendorTypesafe,
		Protocol: providers.ProtocolSystemOne, BaseURL: upstream.URL + "/systemone?token=abc",
	}
	answers, err := Evaluate(context.Background(), provider, "jev-1", state, questions)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("方法 = %q", gotMethod)
	}
	if gotURL != "/systemone?token=abc" {
		t.Fatalf("URL 该 verbatim：%q", gotURL)
	}
	if gotBody["model"] != "jev-1" || len(gotBody) != 3 {
		t.Fatalf("体该只有三个键：%v", gotBody)
	}
	if answers["q1"] != "追" {
		t.Fatalf("answers = %+v", answers)
	}
}

// answers 不是 object ⇒ 报错；非 2xx ⇒ 报错；失败不重试（一次请求只打一发）。
func TestEvaluateRejectsBadShape(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"answers":[1,2]}`))
	}))
	t.Cleanup(upstream.Close)
	provider := providers.Provider{
		ID: "jev", Vendor: providers.VendorTypesafe,
		Protocol: providers.ProtocolSystemOne, BaseURL: upstream.URL,
	}
	if _, err := Evaluate(context.Background(), provider, "m", nil, nil); err == nil ||
		!strings.Contains(err.Error(), "answers") {
		t.Fatalf("answers 不是 object 该报错：%v", err)
	}
	if calls != 1 {
		t.Fatalf("失败不重试：打了 %d 发", calls)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "上游炸了", http.StatusBadGateway)
	}))
	t.Cleanup(bad.Close)
	provider.BaseURL = bad.URL
	if _, err := Evaluate(context.Background(), provider, "m", nil, nil); err == nil ||
		!strings.Contains(err.Error(), "502") {
		t.Fatalf("非 2xx 该报错：%v", err)
	}
}

// KindJudgement 挂号：有会话过、无会话 panic（needsSession 有它）。
func TestJudgementNeedsSession(t *testing.T) {
	registry := task.NewRegistry()
	session := "c1"
	registry.Begin(task.KindJudgement, &session, "判断 · 会话 c1").Succeed()
	if registry.Running() != 0 {
		t.Fatal("收尾后不该还在跑")
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("judgement 缺会话 id 该当场炸")
			}
		}()
		registry.Begin(task.KindJudgement, nil, "缺会话")
	}()
	if task.KindJudgement.Label() != "判断" {
		t.Fatalf("标签 = %q", task.KindJudgement.Label())
	}
}

// JudgeFor：开关 + 选渠道 + 挂号 —— 关掉明确拒绝；没渠道说清楚；通了挂号收尾。
func TestJudgeForRespectsSwitchAndChannel(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"answers":{"intent":{"choice":"attack"}}}`))
	}))
	t.Cleanup(upstream.Close)
	write("config.json", `{}`)
	write("providers.json", `{"providers":[{"id":"jev1","vendor":"custom","protocol":"systemone","base_url":"`+upstream.URL+`/x"}]}`)
	write("agents.json", `{"agents":[]}`)
	st, err := store.Open(filepath.Join(dir, "microchat.db"))
	if err != nil {
		t.Fatal(err)
	}
	tasks := task.NewRegistry()
	service := New(st, config.Paths{DataDir: dir, ConfigDir: dir}, tasks)
	session := model.Session{ID: "c1", Provider: "jev1", Model: "jev-latest", AgentID: "default"}

	answers, err := service.JudgeFor(context.Background(), session,
		map[string]any{"text": "玩家拔刀"}, map[string]any{"intent": "打吗"})
	if err != nil {
		t.Fatal(err)
	}
	if answers["intent"] == nil {
		t.Fatalf("answers = %+v", answers)
	}
	if tasks.Running() != 0 {
		t.Fatal("收尾后不该还在跑")
	}

	// 能力关掉 ⇒ 明确拒绝（连上游都不碰）
	write("agents.json", `{"agents":[{"id":"off","abilities":{"judge":{"enabled":false}}}]}`)
	session.AgentID = "off"
	if _, err := service.JudgeFor(context.Background(), session, nil, nil); err == nil {
		t.Fatal("关掉 judge 该明确拒绝")
	}
	if tasks.Running() != 0 {
		t.Fatal("拒绝的不该留挂号")
	}

	// 渠道不在 ⇒ 说清楚
	session.AgentID = "default"
	session.Provider = "没有这条"
	if _, err := service.JudgeFor(context.Background(), session, nil, nil); err == nil {
		t.Fatal("没渠道该说清楚")
	}
}
