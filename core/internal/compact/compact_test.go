package compact

// 压缩的五条验证，一条条钉住（**dummy 渠道**：确定性、不联网）：
// ① 剔 `<state>`；② 非空；③ 区间自洽；④ 不许覆盖已压缩的区间；⑤ 落库带上 prompt_version / usage。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"microchat/internal/abilities"
	"microchat/internal/config"
	"microchat/internal/model"
	"microchat/internal/state"
	"microchat/internal/store"
	"microchat/internal/task"
	"microchat/internal/turn"
)

// harness：一条会话 + dummy 渠道 + 一个进程内的压缩服务。
type harness struct {
	service   *Service
	store     *store.Store
	paths     config.Paths
	session   model.Session
	sessionID string
	turns     *turn.Registry
	tasks     *task.Registry
}

func newHarness(t *testing.T, agents config.AgentsConfig) *harness {
	t.Helper()
	dir := t.TempDir()
	paths := config.Paths{ConfigDir: dir, DataDir: dir}
	// dummy 渠道：不联网，压缩会走本地假摘要
	if err := config.SaveJSON(dir+"/providers.json",
		mustJSON(t, `{"providers":[{"id":"dummy","kind":"dummy"}]}`)); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveJSON(dir+"/agents.json", agents); err != nil {
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
	agentID := session.AgentID
	if len(agents.Agents) > 0 {
		agentID = agents.Agents[0].ID
		if err := st.SetSessionAgent(session.ID, agentID); err != nil {
			t.Fatal(err)
		}
		session.AgentID = agentID
	}
	turns, tasks := turn.NewRegistry(), task.NewRegistry()
	return &harness{
		service: New(st, paths, turns, tasks), store: st, paths: paths,
		session: session, sessionID: session.ID, turns: turns, tasks: tasks,
	}
}

// turn：落一轮对话（用户 + 助手，顺序就是 id）—— 一个块。
//
// 用户那半句里带一个 `<state>` 块：压缩的材料必须把它剔掉（验证 ①）。
func (h *harness) turn(t *testing.T, text string) {
	t.Helper()
	ids, err := store.MintOrderedIDs(2)
	if err != nil {
		t.Fatal(err)
	}
	for index, message := range []model.Message{
		{ID: ids[0], SessionID: h.sessionID, Role: model.RoleUser,
			Content: text + "\n<state>第几轮 = " + ids[0][:4] + "</state>"},
		{ID: ids[1], SessionID: h.sessionID, Role: model.RoleAssistant, Content: "收到：" + text},
	} {
		if _, err := h.store.InsertMessage(message); err != nil {
			t.Fatalf("第 %d 条落库失败：%v", index, err)
		}
	}
}

func (h *harness) messages(t *testing.T) []model.Message {
	t.Helper()
	messages, err := h.store.ListMessages(h.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	return messages
}

func (h *harness) summaries(t *testing.T) []model.Summary {
	t.Helper()
	summaries, err := h.store.ListSummaries(h.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	return summaries
}

func mustJSON(t *testing.T, raw string) any {
	t.Helper()
	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatal(err)
	}
	return parsed
}

// 走的完整条路：三块 → 压最老的两块 ⇒ summaries 一行（区间 / 块数 / prompt_version / usage）
// + 那一段消息的 `summary_id` 指过去 + 装配里变成**一条** source=summary。
func TestCompactWritesOneSummaryAndPointsTheSpan(t *testing.T) {
	h := newHarness(t, config.AgentsConfig{})
	h.turn(t, "第一句")
	h.turn(t, "第二句")
	h.turn(t, "第三句") // 最后一块是开着的（永不压）

	messages := h.messages(t)
	result, err := h.service.Compact(context.Background(), h.session, Request{Blocks: 2})
	if err != nil {
		t.Fatal(err)
	}
	if result.Blocks != 2 || result.Messages != 4 {
		t.Fatalf("压的该是最老的两块（4 条消息）：%+v", result)
	}
	if result.BeginMessageID != messages[0].ID || result.EndMessageID != messages[3].ID {
		t.Fatalf("区间 = %s..%s，想要 %s..%s", result.BeginMessageID, result.EndMessageID,
			messages[0].ID, messages[3].ID)
	}
	if result.PromptVersion != PromptVersion(DefaultTemplate) || result.PromptVersion == 0 {
		t.Fatalf("prompt_version = %d（想要默认模板的指纹）", result.PromptVersion)
	}
	if !strings.Contains(result.Text, dummyReply) {
		t.Fatalf("dummy 渠道该回本地假摘要：%q", result.Text)
	}

	rows := h.summaries(t)
	if len(rows) != 1 {
		t.Fatalf("该只插一行摘要：%+v", rows)
	}
	row := rows[0]
	if row.SourceKind != model.SourceMessages || row.Blocks != 2 || row.Dirty {
		t.Fatalf("摘要那一行 = %+v", row)
	}
	if row.BeginMessageID == nil || *row.BeginMessageID != messages[0].ID ||
		row.EndMessageID == nil || *row.EndMessageID != messages[3].ID {
		t.Fatalf("区间没写对：%+v", row)
	}
	if len(row.SourceIDs) != 4 || row.Provider != "dummy" || row.Model != "dummy" || row.Tokens <= 0 {
		t.Fatalf("source_ids / 渠道 / tokens = %+v", row)
	}
	// 那一段消息的指针指过去；**最后那块一个字都没动**
	after := h.messages(t)
	for index := range 4 {
		if after[index].SummaryID == nil || *after[index].SummaryID != row.ID {
			t.Fatalf("第 %d 条该被这一份摘要盖住：%+v", index, after[index].SummaryID)
		}
		if after[index].Content != messages[index].Content {
			t.Fatalf("第 %d 条的正文被改了（压缩只许改 summary_id）：%q", index, after[index].Content)
		}
	}
	if after[4].SummaryID != nil {
		t.Fatal("最后那块（开着的）不该被盖")
	}

	// 装配：被压的那一段变成**一条** source=summary
	outgoing := state.BuildOutgoing("", after, rows, nil)
	summaries := 0
	for _, item := range outgoing {
		if item.Source == "summary" {
			summaries++
			if item.SummaryID == nil || *item.SummaryID != row.ID || item.Blocks == nil || *item.Blocks != 2 {
				t.Fatalf("装配出来的摘要那条 = %+v", item)
			}
		}
	}
	if summaries != 1 {
		t.Fatalf("装配里该正好一条摘要：%+v", outgoing)
	}
}

// ④ 不许覆盖已压缩的区间：再压一次同一段 ⇒ **报错**（不是又插一行）。
func TestCompactRefusesToCoverACompactedRange(t *testing.T) {
	h := newHarness(t, config.AgentsConfig{})
	h.turn(t, "第一句")
	h.turn(t, "第二句")
	h.turn(t, "第三句")

	first, err := h.service.Compact(context.Background(), h.session, Request{Blocks: 2})
	if err != nil {
		t.Fatal(err)
	}
	// 按区间来（同一段）⇒ 报错
	_, err = h.service.Compact(context.Background(), h.session,
		Request{Begin: first.BeginMessageID, End: first.EndMessageID})
	if err == nil || !strings.Contains(err.Error(), "已压缩") {
		t.Fatalf("覆盖已压缩的区间该报错：%v", err)
	}
	// 按块数来（最老的 N 块）⇒ 从第一条没被覆盖的起算，压的是**第三块**（开着的）⇒ 没得压
	_, err = h.service.Compact(context.Background(), h.session, Request{Blocks: 1})
	if err == nil {
		t.Fatal("只剩一个开着的块时该报错")
	}
	if len(h.summaries(t)) != 1 {
		t.Fatal("失败的那两次不许留下任何摘要行")
	}
}

// ③ 区间自洽：两端必须都在这条会话里、begin ≤ end。
func TestCompactRangeMustBeSelfConsistent(t *testing.T) {
	h := newHarness(t, config.AgentsConfig{})
	h.turn(t, "第一句")
	h.turn(t, "第二句")
	messages := h.messages(t)

	cases := map[string]Request{
		"一端不在本会话":        {Begin: messages[0].ID, End: "01a00000-0000-7000-8000-000000000000"},
		"begin 在 end 之后": {Begin: messages[2].ID, End: messages[0].ID},
		"只给一端":           {Begin: messages[0].ID},
	}
	for name, request := range cases {
		if _, err := h.service.Compact(context.Background(), h.session, request); err == nil {
			t.Fatalf("%s：该报错", name)
		}
	}
	if len(h.summaries(t)) != 0 {
		t.Fatal("失败不许落库")
	}
}

// 块不被劈开 + 最后那个开着的块永不压。
func TestCompactRangeMustAlignToWholeBlocks(t *testing.T) {
	h := newHarness(t, config.AgentsConfig{})
	h.turn(t, "第一句")
	h.turn(t, "第二句")
	h.turn(t, "第三句")
	messages := h.messages(t)

	// 从中间那条起（不是块首）⇒ 报错
	if _, err := h.service.Compact(context.Background(), h.session,
		Request{Begin: messages[1].ID, End: messages[3].ID}); err == nil ||
		!strings.Contains(err.Error(), "块") {
		t.Fatalf("劈开块该报错：%v", err)
	}
	// 一直盖到最后那条（开着的块）⇒ 报错
	if _, err := h.service.Compact(context.Background(), h.session,
		Request{Begin: messages[0].ID, End: messages[len(messages)-1].ID}); err == nil {
		t.Fatal("开着的块永不压")
	}
	// 整块对齐 ⇒ 通过
	if _, err := h.service.Compact(context.Background(), h.session,
		Request{Begin: messages[0].ID, End: messages[1].ID}); err != nil {
		t.Fatalf("整块对齐该通过：%v", err)
	}
}

// ⑤ 材料：那一段的 `<state>` 块**不发给上游**；模板进 system。
func TestMaterialStripsStateAndSendsTemplate(t *testing.T) {
	var body []byte
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"一段前情提要"}}],` +
			`"usage":{"prompt_tokens":120,"completion_tokens":8,"total_tokens":128}}`))
	}))
	defer stub.Close()

	h := newHarness(t, config.AgentsConfig{})
	if err := config.SaveJSON(h.paths.Config("providers.json"), mustJSON(t,
		`{"providers":[{"id":"stub","kind":"openai-compat","base_url":"`+stub.URL+`","identity":"bare"}]}`)); err != nil {
		t.Fatal(err)
	}
	h.session.Provider, h.session.Model = "stub", "m"
	h.turn(t, "第一句")
	h.turn(t, "第二句")

	result, err := h.service.Compact(context.Background(), h.session, Request{Blocks: 1})
	if err != nil {
		t.Fatal(err)
	}
	sent := string(body)
	if strings.Contains(sent, "<state>") || strings.Contains(sent, "第几轮") {
		t.Fatalf("材料里的 <state> 块该被剔掉：%s", sent)
	}
	if !strings.Contains(sent, "第一句") || !strings.Contains(sent, "助手：收到：第一句") {
		t.Fatalf("材料该是这一段正文：%s", sent)
	}
	// 模板在 system 里（改模板 ⇒ prompt_version 跟着变）
	if !strings.Contains(sent, "前情提要") {
		t.Fatalf("模板该作为 system 发出去：%s", sent)
	}
	// ⑤ usage 落进那一行
	rows := h.summaries(t)
	if len(rows) != 1 || !strings.Contains(string(rows[0].Usage), `"completion_tokens":8`) {
		t.Fatalf("usage 该落进摘要那一行：%+v", rows)
	}
	if result.PromptVersion != PromptVersion(DefaultTemplate) {
		t.Fatalf("prompt_version = %d", result.PromptVersion)
	}
	// 换个模板 ⇒ 指纹就变（"改一个字就变"）
	agent := config.Agent{ID: h.session.AgentID, Abilities: map[string]config.AbilityToggle{
		abilities.Compact: {Prompt: new("另一套模板")},
	}}
	if err := config.SaveJSON(h.paths.Config("agents.json"), config.AgentsConfig{Agents: []config.Agent{agent}}); err != nil {
		t.Fatal(err)
	}
	h.turn(t, "第三句")
	if _, err := h.service.Compact(context.Background(), h.session, Request{Blocks: 1}); err != nil {
		t.Fatal(err)
	}
	rows = h.summaries(t)
	if rows[1].PromptVersion == rows[0].PromptVersion || rows[1].PromptVersion != PromptVersion("另一套模板") {
		t.Fatalf("换模板该换指纹：%d vs %d", rows[0].PromptVersion, rows[1].PromptVersion)
	}
}

// ①② 剔 `<state>` 之后是空的 ⇒ **不写**（宁可什么都没有，也别塞一条空摘要）。
func TestEmptySummaryIsNotWritten(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"<state>季节 = 初冬</state>"}}]}`))
	}))
	defer stub.Close()

	h := newHarness(t, config.AgentsConfig{})
	if err := config.SaveJSON(h.paths.Config("providers.json"), mustJSON(t,
		`{"providers":[{"id":"stub","kind":"openai-compat","base_url":"`+stub.URL+`","identity":"bare"}]}`)); err != nil {
		t.Fatal(err)
	}
	h.session.Provider, h.session.Model = "stub", "m"
	h.turn(t, "第一句")
	h.turn(t, "第二句")

	_, err := h.service.Compact(context.Background(), h.session, Request{Blocks: 1})
	var coded *Error
	if !errors.As(err, &coded) || coded.Code != "upstream" {
		t.Fatalf("该报 upstream 的错：%v", err)
	}
	if len(h.summaries(t)) != 0 {
		t.Fatal("空摘要不许落库")
	}
	for _, message := range h.messages(t) {
		if message.SummaryID != nil {
			t.Fatal("失败时一条消息都不许被盖")
		}
	}
}

// 能力关掉 ⇒ **明确拒绝**（不是静默成功、也不是降级）；会话与库都不动。
func TestDisabledAbilityIsRefusedLoudly(t *testing.T) {
	agents := config.AgentsConfig{Agents: []config.Agent{{
		ID: "闭嘴的", Name: "闭嘴的", SystemPrompt: "x",
		Abilities: map[string]config.AbilityToggle{abilities.Compact: {Enabled: new(false)}},
	}}}
	h := newHarness(t, agents)
	h.turn(t, "第一句")
	h.turn(t, "第二句")

	_, err := h.service.Compact(context.Background(), h.session, Request{Blocks: 1})
	var coded *Error
	if !errors.As(err, &coded) || coded.Code != "invalid" {
		t.Fatalf("关掉能力该明确拒绝（invalid）：%v", err)
	}
	if !strings.Contains(err.Error(), "abilities.compact.enabled") {
		t.Fatalf("错误里要说清是哪一格：%v", err)
	}
	if len(h.summaries(t)) != 0 {
		t.Fatal("拒绝就是拒绝：一个字节都不许落")
	}
}

// 一个会话同时只许一次压缩：在跑 ⇒ conflict（**不排队**）。
func TestSecondCompactWhileRunningConflicts(t *testing.T) {
	h := newHarness(t, config.AgentsConfig{})
	h.turn(t, "第一句")
	h.turn(t, "第二句")
	h.turn(t, "第三句")
	if _, err := h.turns.BeginCompact(h.sessionID, 1, 1); err != nil {
		t.Fatal(err)
	}
	_, err := h.service.Compact(context.Background(), h.session, Request{Blocks: 1})
	var coded *Error
	if !errors.As(err, &coded) || coded.Code != "conflict" {
		t.Fatalf("在跑的时候该回 conflict：%v", err)
	}
}

// 受理那条路：202 那一份状态（running）先回，跑完的结局落在压缩状态里。
func TestStartReturnsRunningAndFinishesWithDone(t *testing.T) {
	h := newHarness(t, config.AgentsConfig{})
	h.turn(t, "第一句")
	h.turn(t, "第二句")
	h.turn(t, "第三句")

	status, err := h.service.Start(h.session, Request{Blocks: 2})
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "running" || status.Blocks != 2 {
		t.Fatalf("202 那一份 = %+v", status)
	}
	deadline := 5 * time.Second
	for start := time.Now(); time.Since(start) < deadline; {
		got := h.turns.Status(h.sessionID).Compact
		if got != nil && got.State != "running" {
			if got.State != "done" || got.SummaryID == nil || got.Compacted != 2 {
				t.Fatalf("跑完该是 done：%+v", got)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("等不到压缩跑完")
}
