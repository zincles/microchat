package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"microchat/internal/config"
	"microchat/internal/model"
	"microchat/internal/store"
	"microchat/internal/turn"
)

// ── 压缩：`POST /sessions/{session_id}/compact` ────────────────────────────

// seedTurn：往这条会话里直接落一轮（用户 + 助手）—— 顺序就是 id。
//
// 不走 `POST /messages`：那条路要等 dummy 的"一个字一个字吐"（验收用得上，测试里纯属等）。
func (b *dummySandbox) seedTurn(t *testing.T, text string) {
	t.Helper()
	ids, err := store.MintOrderedIDs(2)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []model.Message{
		{ID: ids[0], SessionID: b.sessionID, Role: model.RoleUser, Content: text},
		{ID: ids[1], SessionID: b.sessionID, Role: model.RoleAssistant, Content: "收到：" + text},
	} {
		if _, err := b.store.InsertMessage(message); err != nil {
			t.Fatal(err)
		}
	}
}

// compactStatus：`GET .../status` 里那一档压缩状态（**只有这一处**说了算）。
func (b *dummySandbox) compactStatus(t *testing.T) *turn.CompactStatus {
	t.Helper()
	recorder := call(b.server, "GET", b.path+"/status", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("读状态该 200，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	var status struct {
		Compact *turn.CompactStatus `json:"compact"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	return status.Compact
}

// waitCompact：等压缩跑完（后台那一半程）—— 失败就报出原因。
func (b *dummySandbox) waitCompact(t *testing.T) turn.CompactStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		status := b.compactStatus(t)
		if status != nil && status.State != "running" {
			if status.State != "done" {
				t.Fatalf("压缩该成功：%+v", status)
			}
			return *status
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("等不到压缩跑完")
	return turn.CompactStatus{}
}

// 走完整条路：三块 → `{"blocks":2}` ⇒ **202** + running ⇒ 跑完 ⇒ 装配里那一段变成一条摘要。
func TestCompactRouteAcceptsAndMarksTheSpan(t *testing.T) {
	box := newDummySandbox(t)
	box.seedTurn(t, "第一句")
	box.seedTurn(t, "第二句")
	box.seedTurn(t, "第三句")

	recorder := call(box.server, "POST", box.path+"/compact", `{"blocks":2}`)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("受理该 202，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	var accepted turn.CompactStatus
	if err := json.Unmarshal(recorder.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.State != "running" || accepted.Blocks != 2 {
		t.Fatalf("202 那一份 = %+v", accepted)
	}
	done := box.waitCompact(t)
	if done.SummaryID == nil || done.Compacted != 2 {
		t.Fatalf("跑完那一份 = %+v", done)
	}

	// 装配：被压的那一段变成**一条** source=summary（检查压缩效果只能靠这个）
	recorder = call(box.server, "GET", box.path+"/outgoing", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("%d：%s", recorder.Code, recorder.Body.String())
	}
	var outgoing []struct {
		Source    string  `json:"source"`
		SummaryID *string `json:"summary_id"`
		Blocks    *int64  `json:"blocks"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &outgoing); err != nil {
		t.Fatal(err)
	}
	summaries := []int{}
	for index, item := range outgoing {
		if item.Source == "summary" {
			summaries = append(summaries, index)
		}
	}
	if len(summaries) != 1 {
		t.Fatalf("装配里该正好一条摘要：%+v", outgoing)
	}
	last := outgoing[summaries[0]]
	if last.SummaryID == nil || *last.SummaryID != *done.SummaryID || last.Blocks == nil || *last.Blocks != 2 {
		t.Fatalf("摘要那条 = %+v", last)
	}

	// 那一段消息的指针指过去；最后那块（开着的）没被动
	messages := box.messages(t)
	for index := range 4 {
		if messages[index].SummaryID == nil || *messages[index].SummaryID != *done.SummaryID {
			t.Fatalf("第 %d 条该被这一份摘要盖住：%+v", index, messages[index].SummaryID)
		}
	}
	if messages[4].SummaryID != nil || messages[5].SummaryID != nil {
		t.Fatal("最后那块（开着的）永不压")
	}

	// 区间自洽：再压同一段 ⇒ 400（不许覆盖已压缩的区间）
	recorder = call(box.server, "POST", box.path+"/compact", `{"begin_idx":1,"end_idx":4}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("覆盖已压缩的区间该 400，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
}

// 请求体的规矩：两种给法只能给一种、区间要两端都给；会话不存在 ⇒ 404。
func TestCompactRouteValidatesItsBody(t *testing.T) {
	box := newDummySandbox(t)
	box.seedTurn(t, "第一句")
	box.seedTurn(t, "第二句")

	for name, body := range map[string]string{
		"两种给法都给了": `{"blocks":1,"begin_idx":1,"end_idx":2}`,
		"区间只给首端":  `{"begin_idx":1}`,
		"区间只给尾端":  `{"end_idx":2}`,
	} {
		if recorder := call(box.server, "POST", box.path+"/compact", body); recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s 该 400，得到 %d：%s", name, recorder.Code, recorder.Body.String())
		}
	}
	if recorder := call(box.server, "POST", box.path+"/compact", `{"blocks":1}`); recorder.Code != http.StatusAccepted {
		t.Fatalf("正常的一发该 202：%d %s", recorder.Code, recorder.Body.String())
	}
	box.waitCompact(t)

	// 会话不存在 ⇒ 404（不猜、也不回 202）
	recorder := call(box.server, "POST", "/api/v1/sessions/查无此会话/compact", `{}`)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("会话不存在该 404，得到 %d", recorder.Code)
	}
}

// 能力开关（`agents.json` 上每个 agent 的 `abilities`）：
//   - 未知的能力 id ⇒ 400（写了不生效比报错更坏）；
//   - 关掉 `compact` ⇒ 压缩**明确拒绝**（不是静默成功、也不是降级）。
func TestCompactRouteRefusesDisabledAbility(t *testing.T) {
	box := newDummySandbox(t)
	box.seedTurn(t, "第一句")
	box.seedTurn(t, "第二句")
	box.seedTurn(t, "第三句")

	if recorder := call(box.server, "POST", "/api/v1/agents",
		`{"name":"打错的","abilities":{"compactt":{"enabled":true}}}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("未知的能力 id 该 400，得到 %d：%s", recorder.Code, recorder.Body.String())
	}

	recorder := call(box.server, "POST", "/api/v1/agents",
		`{"name":"闭嘴的","system_prompt":"x","abilities":{"compact":{"enabled":false}}}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("建 agent 该 201，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	var agent config.Agent
	if err := json.Unmarshal(recorder.Body.Bytes(), &agent); err != nil {
		t.Fatal(err)
	}
	if recorder := call(box.server, "PATCH", "/api/v1/sessions/"+box.sessionID,
		`{"agent_id":"`+agent.ID+`"}`); recorder.Code != http.StatusOK {
		t.Fatalf("把会话切到这个 agent 该 200，得到 %d：%s", recorder.Code, recorder.Body.String())
	}

	recorder = call(box.server, "POST", box.path+"/compact", `{"blocks":2}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("关掉能力该明确拒绝（400），得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "abilities.compact.enabled") {
		t.Fatalf("错误里要说清是哪一格关的：%s", recorder.Body.String())
	}
	if summaries, err := box.store.ListSummaries(box.sessionID); err != nil || len(summaries) != 0 {
		t.Fatalf("拒绝就是拒绝：一个字节都不许落（%v / %+v）", err, summaries)
	}
}

// 同一个会话同时只许一次压缩：在跑 ⇒ **409**（不排队）。
func TestCompactRouteConflictsWhileRunning(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	letGo := func() { once.Do(func() { close(release) }) }
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // 卡住这一发，让"压缩中"真的看得见
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"慢摘要"}}]}`))
	}))
	defer stub.Close()
	defer letGo()

	box := newDummySandbox(t)
	box.seedTurn(t, "第一句")
	box.seedTurn(t, "第二句")
	box.seedTurn(t, "第三句")
	if recorder := call(box.server, "POST", "/api/v1/providers",
		`{"id":"slow","vendor":"openai-compat","base_url":"`+stub.URL+`"}`); recorder.Code != http.StatusCreated {
		t.Fatalf("建渠道该 201：%d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := call(box.server, "PATCH", "/api/v1/sessions/"+box.sessionID,
		`{"provider":"slow","model":"m"}`); recorder.Code != http.StatusOK {
		t.Fatalf("切渠道该 200：%d %s", recorder.Code, recorder.Body.String())
	}

	if recorder := call(box.server, "POST", box.path+"/compact", `{"blocks":2}`); recorder.Code != http.StatusAccepted {
		t.Fatalf("受理该 202，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	recorder := call(box.server, "POST", box.path+"/compact", `{"blocks":1}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("在跑的时候该 409，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	letGo()
	if done := box.waitCompact(t); done.SummaryID == nil {
		t.Fatalf("跑完那一份 = %+v", done)
	}
}
