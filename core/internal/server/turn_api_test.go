package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"microchat/internal/chat"
	"microchat/internal/compact"
	"microchat/internal/config"
	"microchat/internal/model"
	"microchat/internal/store"
	"microchat/internal/task"
	"microchat/internal/turn"
)

// newTestServer：一台测试用服务（进程内的 turn / task 登记表各一份，走真的 chat / compact 两层）。
func newTestServer(st *store.Store, cfg config.Config, paths config.Paths) *Server {
	turns, tasks := turn.NewRegistry(), task.NewRegistry()
	return New(st, cfg, paths, chat.New(st, paths, turns, tasks), compact.New(st, paths, turns, tasks))
}

// dummySandbox：一条会话 + dummy 渠道（**确定性、不联网** —— 验收与测试都靠它）。
type dummySandbox struct {
	server    *Server
	store     *store.Store
	sessionID string
	path      string
}

func newDummySandbox(t *testing.T) *dummySandbox {
	t.Helper()
	dir := t.TempDir()
	// dummy 渠道照着开箱默认（`config.DefaultConfig` 的 defaults 就是 dummy/dummy）
	if err := config.SaveJSON(dir+"/providers.json", mustJSON(t, `{"providers":[{"id":"dummy","kind":"dummy"}]}`)); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir + "/microchat.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	session, err := st.CreateSession("dummy", "dummy", "")
	if err != nil {
		t.Fatal(err)
	}
	box := &dummySandbox{
		store: st, sessionID: session.ID,
		path: "/api/v1/sessions/" + session.ID,
	}
	box.server = newTestServer(st, config.DefaultConfig(), config.Paths{ConfigDir: dir, DataDir: dir})
	return box
}

// send：发一句话，回 202 的回执。
func (b *dummySandbox) send(t *testing.T, content string) TurnAccepted {
	t.Helper()
	body, err := json.Marshal(map[string]string{"content": content})
	if err != nil {
		t.Fatal(err)
	}
	recorder := call(b.server, "POST", b.path+"/messages", string(body))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("发送该 202，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	var accepted TurnAccepted
	if err := json.Unmarshal(recorder.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	return accepted
}

// waitPhase：等到这一轮进到某一档（或超时）—— 生成在后台跑，测试必须等它。
func (b *dummySandbox) waitPhase(t *testing.T, sessionID string, phases ...string) turnPhase {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var status struct {
			Phase string `json:"phase"`
			Error string `json:"error"`
		}
		recorder := call(b.server, "GET", b.path+"/status", "")
		if recorder.Code == http.StatusOK {
			if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
				t.Fatal(err)
			}
			for _, phase := range phases {
				if status.Phase == phase {
					return turnPhase{Phase: status.Phase, Error: status.Error}
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等不到这些状态：%v", phases)
	return turnPhase{}
}

type turnPhase struct {
	Phase string
	Error string
}

type TurnAccepted = chat.Accepted

// messages：这条会话现在的消息列表（按 id 升序）。
func (b *dummySandbox) messages(t *testing.T) []model.Message {
	t.Helper()
	recorder := call(b.server, "GET", b.path+"/messages", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("%d：%s", recorder.Code, recorder.Body.String())
	}
	var messages []model.Message
	if err := json.Unmarshal(recorder.Body.Bytes(), &messages); err != nil {
		t.Fatal(err)
	}
	return messages
}

// 一轮生成走完整条路：202（受理时就算好回复 id）→ pending/streaming → 整段落库。
//
// 这是这一波的核心契约：**受理与生成分开**、`turn.message_id` 就是那条回复的 id、
// 消息列表里出现的助手消息**用的就是这个 id**。
func TestSendMessageRoundTrip(t *testing.T) {
	box := newDummySandbox(t)
	accepted := box.send(t, "第一句话，顺便起个标题")
	if accepted.Turn.MessageID == nil || *accepted.Turn.MessageID == "" {
		t.Fatalf("回执里该有这条回复的 id（受理时就算好）：%+v", accepted.Turn)
	}
	replyID := *accepted.Turn.MessageID
	if accepted.Backend != "dummy" || accepted.User == nil || accepted.User.Content != "第一句话，顺便起个标题" {
		t.Fatalf("回执 = %+v", accepted)
	}
	// 用户消息先落库（后端失败时用户的话也不该丢）
	if messages := box.messages(t); len(messages) != 1 || messages[0].Role != model.RoleUser {
		t.Fatalf("受理后该只有用户那一句：%+v", messages)
	}
	// 首句临时标题（config.chat.title_chars 截断）
	if session := box.serverSession(t); session.Title != "第一句话，顺便起个标题" {
		t.Fatalf("该用首句起个临时标题：%q", session.Title)
	}

	// 生成中：状态会走到 pending / streaming，游标读能看到增量
	phase := box.waitPhase(t, box.sessionID, "streaming", "idle")
	if phase.Phase == "streaming" {
		recorder := call(box.server, "GET", box.path+"/turn/text?from=0&think_from=0", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("%d：%s", recorder.Code, recorder.Body.String())
		}
		var slice struct {
			Text string `json:"text"`
			Done bool   `json:"done"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &slice); err != nil {
			t.Fatal(err)
		}
		if slice.Text == "" || slice.Done {
			t.Fatalf("生成中该有增量、且 done=false：%+v", slice)
		}
	}
	box.waitPhase(t, box.sessionID, "idle")

	// 整段落库：那条回复的 id **就是**受理时给的那个
	messages := box.messages(t)
	if len(messages) != 2 {
		t.Fatalf("该有一条回复了：%+v", messages)
	}
	reply := messages[1]
	if reply.ID != replyID {
		t.Fatalf("落库的回复 id 该是受理时那个：%s ≠ %s", reply.ID, replyID)
	}
	if reply.Role != model.RoleAssistant || reply.Content != "（测试用空模型）" {
		t.Fatalf("回复 = %+v", reply)
	}
	if reply.DurationMS == nil || *reply.DurationMS < 0 {
		t.Fatalf("该记下这一轮花了多久：%+v", reply)
	}
	// 顺序就是 id：用户那句在前
	if messages[0].ID >= messages[1].ID {
		t.Fatalf("线性会话的顺序就是 id，用户那句必须在前面：%s / %s", messages[0].ID, messages[1].ID)
	}
	// 结束后仍可补拉尾巴（游标读不消费，`done` 表示这轮结束）
	recorder := call(box.server, "GET", box.path+"/turn/text?from=0&think_from=0", "")
	var full struct {
		Text string `json:"text"`
		Done bool   `json:"done"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &full); err != nil {
		t.Fatal(err)
	}
	if full.Text != "（测试用空模型）" || !full.Done {
		t.Fatalf("结束后的补拉 = %+v", full)
	}
}

// serverSession：直接问后端要这条会话（列表里那一项：会话 + 这一轮的状态）。
func (b *dummySandbox) serverSession(t *testing.T) model.SessionView {
	t.Helper()
	recorder := call(b.server, "GET", "/api/v1/sessions", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("%d：%s", recorder.Code, recorder.Body.String())
	}
	var sessions []model.SessionView
	if err := json.Unmarshal(recorder.Body.Bytes(), &sessions); err != nil {
		t.Fatal(err)
	}
	for _, session := range sessions {
		if session.ID == b.sessionID {
			return session
		}
	}
	t.Fatalf("列表里没有这条会话：%s", recorder.Body.String())
	return model.SessionView{}
}

// 同会话在跑时再发 ⇒ **409**（不排队），而且一个字节都不该多出来。
func TestSendWhileRunningConflicts(t *testing.T) {
	box := newDummySandbox(t)
	box.send(t, "先来一句")
	box.waitPhase(t, box.sessionID, "pending")
	recorder := call(box.server, "POST", box.path+"/messages", `{"content":"插队的一句"}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("该 409：%d %s", recorder.Code, recorder.Body.String())
	}
	if !containsAll(recorder.Body.String(), `"code":"conflict"`) {
		t.Fatalf("错误体该按 code 分支：%s", recorder.Body.String())
	}
	// 400 的那条不算：库里只有第一句用户消息
	box.waitPhase(t, box.sessionID, "idle")
	if messages := box.messages(t); len(messages) != 2 {
		t.Fatalf("409 之后不该多出消息：%+v", messages)
	}
	call(box.server, "POST", box.path+"/stop", "")
}

// 中途按停 ⇒ **库里半条都没有**（生成中的回复不在库里）。
func TestStopLeavesNoHalfMessage(t *testing.T) {
	box := newDummySandbox(t)
	box.send(t, "这句会被打断")
	box.waitPhase(t, box.sessionID, "streaming")
	recorder := call(box.server, "POST", box.path+"/stop", "")
	if recorder.Code != http.StatusOK || recorder.Body.String() != `{"stopped":true}` {
		t.Fatalf("停止该回 {"+"\"stopped\":true}"+"：%d %s", recorder.Code, recorder.Body.String())
	}
	box.waitPhase(t, box.sessionID, "idle")
	messages := box.messages(t)
	if len(messages) != 1 || messages[0].Role != model.RoleUser {
		t.Fatalf("被打断的那一轮不许留下半条：%+v", messages)
	}
	// 幂等：没在跑也 200（false）
	again := call(box.server, "POST", box.path+"/stop", "")
	if again.Code != http.StatusOK || again.Body.String() != `{"stopped":false}` {
		t.Fatalf("再停一次该回 false：%d %s", again.Code, again.Body.String())
	}
	// 停完之后还能接着发（登记表已经摘干净，没有"僵尸在跑"）
	box.send(t, "接着聊")
	box.waitPhase(t, box.sessionID, "idle")
	if messages := box.messages(t); len(messages) != 3 {
		t.Fatalf("停完之后该能接着发：%+v", messages)
	}
}

// 上游失败 ⇒ 回复**不落库**、状态进 `error` 且**原因就在那儿**、task 的原因单开字段。
func TestFailureLeavesNothingAndReportsTheReason(t *testing.T) {
	box := newDummySandbox(t)
	// 把 dummy 换成一个指向死地址的渠道：连不上 ⇒ 上游错
	if err := config.SaveJSON(box.server.paths.ConfigDir+"/providers.json",
		mustJSON(t, `{"providers":[{"id":"dummy","kind":"dummy","base_url":"http://127.0.0.1:1/v1","client_ua_override":"x"}]}`)); err != nil {
		t.Fatal(err)
	}
	// dummy 渠道永远是"本地假上游"（不联网）⇒ 换一条真正会去连的死渠道
	if err := config.SaveJSON(box.server.paths.ConfigDir+"/providers.json",
		mustJSON(t, `{"providers":[{"id":"dead","kind":"openai-compat","base_url":"http://127.0.0.1:1/v1"}]}`)); err != nil {
		t.Fatal(err)
	}
	if err := box.store.SetSessionModel(box.sessionID, "dead", "some-model"); err != nil {
		t.Fatal(err)
	}
	box.send(t, "这一轮一定失败")
	phase := box.waitPhase(t, box.sessionID, "error")
	if phase.Error == "" {
		t.Fatal("失败该把原因报出来（status.error）")
	}
	if messages := box.messages(t); len(messages) != 1 {
		t.Fatalf("失败的那一轮不许留下半条：%+v", messages)
	}
}

// `/context` 只有数字，几条硬关系要成立（预算 = 上下文 − 输出预留；触发 = 预算 × 0.8）。
func TestSessionContextIsNumbersOnly(t *testing.T) {
	box := newDummySandbox(t)
	box.send(t, "一句话，让上下文有点东西")
	box.waitPhase(t, box.sessionID, "idle")
	recorder := call(box.server, "GET", box.path+"/context", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("%d：%s", recorder.Code, recorder.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	want := []string{"used_tokens", "budget_tokens", "trigger_tokens", "remaining_tokens",
		"ctx_len", "max_output", "ratio", "estimated", "last_prompt_tokens", "over_budget"}
	if len(raw) != len(want) {
		t.Fatalf("字段该正好是这十个：%v", raw)
	}
	for _, key := range want {
		if _, ok := raw[key]; !ok {
			t.Fatalf("缺 %s：%v", key, raw)
		}
	}
	var usage struct {
		UsedTokens      int  `json:"used_tokens"`
		BudgetTokens    int  `json:"budget_tokens"`
		TriggerTokens   int  `json:"trigger_tokens"`
		RemainingTokens int  `json:"remaining_tokens"`
		CtxLen          int  `json:"ctx_len"`
		MaxOutput       int  `json:"max_output"`
		Estimated       bool `json:"estimated"`
		OverBudget      bool `json:"over_budget"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &usage); err != nil {
		t.Fatal(err)
	}
	if usage.CtxLen != config.DefaultChat().ModelContextTokens {
		t.Fatalf("没有发现值 ⇒ 该用配置兜底：%+v", usage)
	}
	if usage.BudgetTokens != usage.CtxLen-usage.MaxOutput {
		t.Fatalf("预算 = 上下文 − 输出预留：%+v", usage)
	}
	if usage.TriggerTokens != usage.BudgetTokens*8/10 {
		t.Fatalf("触发阈值缺省 = 预算 × 0.8：%+v", usage)
	}
	if usage.RemainingTokens != usage.BudgetTokens-usage.UsedTokens {
		t.Fatalf("剩余 = 预算 − 占用：%+v", usage)
	}
	if usage.UsedTokens <= 0 || !usage.Estimated || usage.OverBudget {
		t.Fatalf("占用该是估算出来的正数、没超预算：%+v", usage)
	}
}

// 游标读的参数：写了但不是非负整数 ⇒ 422（**不静默当 0**）。
func TestTurnTextValidatesCursors(t *testing.T) {
	box := newDummySandbox(t)
	for _, query := range []string{"?from=-1", "?from=abc", "?think_from=x"} {
		recorder := call(box.server, "GET", box.path+"/turn/text"+query, "")
		if recorder.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s 该 422：%d %s", query, recorder.Code, recorder.Body.String())
		}
	}
	// 缺省 = 从头
	if recorder := call(box.server, "GET", box.path+"/turn/text", ""); recorder.Code != http.StatusOK {
		t.Fatalf("缺省该 200：%d %s", recorder.Code, recorder.Body.String())
	}
}

// 不存在的会话：状态 / 游标读 / 发送都 404；
// `GET .../messages` 例外（空列表是它既有的口径）；`stop` 也例外（它问的是"进程里有没有活在跑"）。
func TestTurnRoutesOnMissingSession(t *testing.T) {
	box := newDummySandbox(t)
	missing := "/api/v1/sessions/01a00000-0000-7000-8000-0000000000ff"
	for _, path := range []string{missing + "/status", missing + "/turn/text"} {
		if recorder := call(box.server, "GET", path, ""); recorder.Code != http.StatusNotFound {
			t.Fatalf("%s 该 404：%d %s", path, recorder.Code, recorder.Body.String())
		}
	}
	if recorder := call(box.server, "POST", missing+"/messages", `{"content":"喂"}`); recorder.Code != http.StatusNotFound {
		t.Fatalf("不存在的会话不该受理：%d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := call(box.server, "POST", missing+"/stop", ""); recorder.Code != http.StatusOK ||
		recorder.Body.String() != `{"stopped":false}` {
		t.Fatalf("stop 该幂等回 false：%d %s", recorder.Code, recorder.Body.String())
	}
}

// 正文必填：缺了 / 只有空白 ⇒ 422（空消息不是"发了一句空的"，是调用方写错了）。
func TestSendMessageRequiresContent(t *testing.T) {
	box := newDummySandbox(t)
	for _, body := range []string{`{}`, `{"content":"   "}`} {
		recorder := call(box.server, "POST", box.path+"/messages", body)
		if recorder.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s 该 422：%d %s", body, recorder.Code, recorder.Body.String())
		}
	}
}

// 会话列表里那一栏跟着走（左栏据此标"生成中"）。
func TestSessionListShowsTheRunningTurn(t *testing.T) {
	box := newDummySandbox(t)
	if view := box.serverSession(t); view.Turn.Phase != "idle" {
		t.Fatalf("没在跑该是 idle：%+v", view.Turn)
	}
	accepted := box.send(t, "看左栏")
	box.waitPhase(t, box.sessionID, "pending")
	view := box.serverSession(t)
	if view.Turn.Phase != "pending" && view.Turn.Phase != "streaming" {
		t.Fatalf("在跑时列表该报出来：%+v", view.Turn)
	}
	if view.Turn.MessageID == nil || *view.Turn.MessageID != *accepted.Turn.MessageID {
		t.Fatalf("列表里的 message_id 该是这一轮的：%+v", view.Turn)
	}
	box.waitPhase(t, box.sessionID, "idle")
}
