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
	"strconv"
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
	session, err := st.CreateSession("dummy", "dummy", "", "")
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
// + 那一段消息的 `summary_id` 指过去 + 装配里变成**一条** type=summary。
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
	if result.BeginMessageID != messages[0].ID || result.EndMessageID != messages[3].ID {
		t.Fatalf("区间 = %s..%s，想要 %s..%s", result.BeginMessageID, result.EndMessageID,
			messages[0].ID, messages[3].ID)
	}
	if result.FromIdx != 1 || result.ToIdx != 4 || result.Merged {
		t.Fatalf("结局该带区间 1–4 且不是合并：%+v", result)
	}
	if result.PromptVersion != abilities.PromptVersion(abilities.DefaultTemplate(abilities.Compact)) || result.PromptVersion == 0 {
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
	if row.Type != model.TypeMessages || row.Blocks != 2 || row.Dirty {
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

	// 装配：被压的那一段变成**一条** type=summary
	outgoing := state.BuildOutgoing("", after, rows, nil)
	summaries := 0
	for _, item := range outgoing {
		if item.Type == "summary" {
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

	_, err := h.service.Compact(context.Background(), h.session, Request{Blocks: 2})
	if err != nil {
		t.Fatal(err)
	}
	// 按区间来（同一段，现在整段被那一条摘要盖住）⇒ 合并级但只有一条孩子 ⇒ 报错
	_, err = h.service.Compact(context.Background(), h.session,
		Request{BeginIdx: 1, EndIdx: 4})
	if err == nil {
		t.Fatal("这一段已经全被一条摘要盖住：并不了（至少要两条同层）")
	}
	// 按块数来：底层只剩开着的那块 ⇒ 摘要层只有一条顶层 ⇒ 凑不齐 2 坨 ⇒ 无可压缩
	_, err = h.service.Compact(context.Background(), h.session, Request{Blocks: 2})
	if err == nil || !strings.Contains(err.Error(), "无可压缩") {
		t.Fatalf("顶层只有一条时该报无可压缩：%v", err)
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

	cases := map[string]Request{
		"越界":             {BeginIdx: 1, EndIdx: 99},
		"begin 在 end 之后": {BeginIdx: 3, EndIdx: 1},
		"只给一端":           {BeginIdx: 1},
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

	// 从中间那条起（不是块首）⇒ 报错
	if _, err := h.service.Compact(context.Background(), h.session,
		Request{BeginIdx: 2, EndIdx: 4}); err == nil ||
		!strings.Contains(err.Error(), "块") {
		t.Fatalf("劈开块该报错：%v", err)
	}
	// 一直盖到最后那条（开着的块）⇒ 报错
	if _, err := h.service.Compact(context.Background(), h.session,
		Request{BeginIdx: 1, EndIdx: 6}); err == nil {
		t.Fatal("开着的块永不压")
	}
	// 整块对齐 ⇒ 通过
	if _, err := h.service.Compact(context.Background(), h.session,
		Request{BeginIdx: 1, EndIdx: 2}); err != nil {
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
	if strings.Contains(sent, "<state>") {
		t.Fatalf("材料里的 <state> 块该被剔掉：%s", sent)
	}
	// 对话那一段（附状态之前）里不许留下状态键的原文 —— 状态只以"程序事实"那一段的渲染形式出现
	// （抬头里那个换行在 JSON 里被转义了 ⇒ 拿不带换行的那段去切）
	dialogue, _, _ := strings.Cut(sent, strings.TrimSpace(stateHeader))
	if strings.Contains(dialogue, "第几轮") {
		t.Fatalf("对话材料里不该留状态键：%s", dialogue)
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
	if result.PromptVersion != abilities.PromptVersion(abilities.DefaultTemplate(abilities.Compact)) {
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
	if rows[1].PromptVersion == rows[0].PromptVersion || rows[1].PromptVersion != abilities.PromptVersion("另一套模板") {
		t.Fatalf("换模板该换指纹：%d vs %d", rows[0].PromptVersion, rows[1].PromptVersion)
	}
	// **agent 上覆盖的 prompt 就是发出去的那一段**（不是默认模板）—— 覆盖真的生效，不是摆设
	sent = string(body)
	if !strings.Contains(sent, "另一套模板") || strings.Contains(sent, "前情提要") {
		t.Fatalf("覆盖后的模板该进 system、默认的不该再出现：%s", sent)
	}
}

// 材料里附的那份状态：**算到区间末**（`at_idx` = 区间最后一条），而不是"整个会话此刻的样子"。
//
// 证伪点：把"算到区间末"换成"算到会话最后一条"，第二轮之后写的变量就会出现在**第一段**的材料里 ⇒ 这里红。
// 顺带钉住口气：抬头写清"程序事实，仅供参考，不要写进梗概"。
func TestMaterialCarriesTheStateAtTheRangeEnd(t *testing.T) {
	var body []byte
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"一段前情提要"}}]}`))
	}))
	defer stub.Close()

	h := newHarness(t, config.AgentsConfig{})
	if err := config.SaveJSON(h.paths.Config("providers.json"), mustJSON(t,
		`{"providers":[{"id":"stub","kind":"openai-compat","base_url":"`+stub.URL+`","identity":"bare"}]}`)); err != nil {
		t.Fatal(err)
	}
	h.session.Provider, h.session.Model = "stub", "m"
	// 三轮，每轮改写同一个键（`轮次`）—— 材料里看到几，就说明"算到了第几条"
	//（不借 harness.turn：它那个值是消息 id 的前 4 位，同一毫秒铸出来的会一模一样，证伪不了）
	for round := 1; round <= 3; round++ {
		ids, err := store.MintOrderedIDs(2)
		if err != nil {
			t.Fatal(err)
		}
		for _, message := range []model.Message{
			{ID: ids[0], SessionID: h.sessionID, Role: model.RoleUser,
				Content: "第" + strconv.Itoa(round) + "句\n<state>轮次 = " + strconv.Itoa(round) + "</state>"},
			{ID: ids[1], SessionID: h.sessionID, Role: model.RoleAssistant, Content: "收到：" + strconv.Itoa(round)},
		} {
			if _, err := h.store.InsertMessage(message); err != nil {
				t.Fatal(err)
			}
		}
	}

	// 压最老的一块（第 1、2 条）⇒ 区间末是第 2 条：材料里该是 `轮次 = 1`
	if _, err := h.service.Compact(context.Background(), h.session, Request{Blocks: 1}); err != nil {
		t.Fatal(err)
	}
	sent := string(body)
	if !strings.Contains(sent, "不要写进梗概") || !strings.Contains(sent, "仅供参考") {
		t.Fatalf("材料里那份状态的抬头该写清口气：%s", sent)
	}
	if !strings.Contains(sent, "当前变量:") || !strings.Contains(sent, "轮次 = 1") {
		t.Fatalf("材料里该附上「算到区间末」的那张变量表：%s", sent)
	}
	for _, later := range []string{"轮次 = 2", "轮次 = 3"} {
		if strings.Contains(sent, later) {
			t.Fatalf("区间**之后**才写的变量不该跑进这一段的材料（%s）：%s", later, sent)
		}
	}
	if !strings.Contains(sent, "第1句") || strings.Contains(sent, "第3句") {
		t.Fatalf("该压的是第 1 条那一段（且只有它）：%s", sent)
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

// findSummary：按 id 在这一串摘要里找（断言用）。
func findSummary(t *testing.T, rows []model.Summary, id string) model.Summary {
	t.Helper()
	for _, row := range rows {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("摘要 %s 不在：%+v", id, rows)
	return model.Summary{}
}

// 合并级：三块会话先按块压出**两条同层摘要**，再用 idx 区间并成一条父。
//
// 钉住：父的 Type=summary、自己无父、Blocks=子和、两个孩子都指向它、Dirty 传播、
// Begin/End=并集两端；且**消息指针保持指孩子**（升格是装配侧的事，合并绝不改 messages）。
func TestCompactMergesSameLevelSummaries(t *testing.T) {
	h := newHarness(t, config.AgentsConfig{})
	h.turn(t, "第一句")
	h.turn(t, "第二句")
	h.turn(t, "第三句") // 最后一块开着（永不压）

	first, err := h.service.Compact(context.Background(), h.session, Request{Blocks: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.service.Compact(context.Background(), h.session, Request{Blocks: 1})
	if err != nil {
		t.Fatal(err)
	}
	messages := h.messages(t)
	if first.BeginMessageID != messages[0].ID || first.EndMessageID != messages[1].ID ||
		second.BeginMessageID != messages[2].ID || second.EndMessageID != messages[3].ID {
		t.Fatalf("两条孩子该各盖一块（第 1–2、3–4 条）：%+v / %+v", first, second)
	}

	// 改一条孩子覆盖的消息 ⇒ 那条摘要（含祖先）标 dirty；并出来后父该跟着脏（传播）
	if _, err := h.store.UpdateMessage(h.sessionID, messages[0].ID, store.MessageEdit{Content: new("第一句（改过）")}); err != nil {
		t.Fatal(err)
	}
	if !findSummary(t, h.summaries(t), first.SummaryID).Dirty {
		t.Fatal("改被覆盖的消息该把那条摘要标脏")
	}

	merged, err := h.service.Compact(context.Background(), h.session, Request{BeginIdx: 1, EndIdx: 4})
	if err != nil {
		t.Fatal(err)
	}
	if merged.SummaryID == first.SummaryID || merged.SummaryID == second.SummaryID {
		t.Fatal("合并该另铸一条父摘要")
	}
	if merged.FromIdx != 1 || merged.ToIdx != 4 || !merged.Merged {
		t.Fatalf("合并的结局该带区间 1–4 且标合并：%+v", merged)
	}
	rows := h.summaries(t)
	parent := findSummary(t, rows, merged.SummaryID)
	if parent.Type != model.TypeSummaries {
		t.Fatalf("父的 type 该是 summary：%+v", parent)
	}
	if parent.ParentSummaryID != nil {
		t.Fatalf("父自己该是顶层：%+v", parent.ParentSummaryID)
	}
	if parent.Blocks != 2 { // 两条各 1 块 ⇒ 子和
		t.Fatalf("父的 blocks 该是子和（1+1）：%d", parent.Blocks)
	}
	if !parent.Dirty {
		t.Fatal("孩子脏了 ⇒ 父该脏（dirty 传播）")
	}
	if parent.BeginMessageID == nil || *parent.BeginMessageID != messages[0].ID ||
		parent.EndMessageID == nil || *parent.EndMessageID != messages[3].ID {
		t.Fatalf("父的两端该是并集（第 1 条 → 第 4 条）：%+v", parent)
	}
	if len(parent.SourceIDs) != 2 || parent.SourceIDs[0] != first.SummaryID || parent.SourceIDs[1] != second.SummaryID {
		t.Fatalf("父的 source_ids 该是按区间排序的孩子：%+v", parent.SourceIDs)
	}
	for _, childID := range []string{first.SummaryID, second.SummaryID} {
		child := findSummary(t, rows, childID)
		if child.ParentSummaryID == nil || *child.ParentSummaryID != merged.SummaryID {
			t.Fatalf("孩子 %s 该指向父：%+v", childID, child.ParentSummaryID)
		}
	}
	// 消息指针保持指孩子（合并绝不改 messages）
	after := h.messages(t)
	if after[0].SummaryID == nil || *after[0].SummaryID != first.SummaryID ||
		after[2].SummaryID == nil || *after[2].SummaryID != second.SummaryID {
		t.Fatal("合并级不许动 messages.summary_id")
	}

	// 装配：这一段变成**一条更粗的**（父），末块照旧逐条
	outgoing := state.BuildOutgoing("", after, rows, nil)
	summaries := 0
	for _, item := range outgoing {
		if item.Type != "summary" {
			continue
		}
		summaries++
		if item.SummaryID == nil || *item.SummaryID != merged.SummaryID {
			t.Fatalf("装配该取最粗的父：%+v", item)
		}
	}
	if summaries != 1 {
		t.Fatalf("装配里该正好一条摘要：%+v", outgoing)
	}
}

// 合并级的四条负例：混合（洞）/ 只盖一条摘要 / 不同层 / 最后开着块 —— 都该报错、都不落库。
func TestCompactMergeRefusals(t *testing.T) {
	h := newHarness(t, config.AgentsConfig{})
	for _, text := range []string{"第一句", "第二句", "第三句", "第四句"} {
		h.turn(t, text)
	}
	// 四块：已闭合 [1,2][3,4][5,6]，开着 [7,8]
	if _, err := h.service.Compact(context.Background(), h.session, Request{Blocks: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.Compact(context.Background(), h.session, Request{Blocks: 1}); err != nil {
		t.Fatal(err)
	}

	refuse := func(name string, request Request, want string) {
		t.Helper()
		_, err := h.service.Compact(context.Background(), h.session, request)
		if err == nil {
			t.Fatalf("%s：该报错", name)
		}
		if want != "" && !strings.Contains(err.Error(), want) {
			t.Fatalf("%s：错误里该说清 %q：%v", name, want, err)
		}
	}
	// 混合（洞）：第 5 条还是新消息
	refuse("混合", Request{BeginIdx: 1, EndIdx: 5}, "洞")
	// 只盖一条摘要
	refuse("只盖一条", Request{BeginIdx: 1, EndIdx: 2}, "两条同层")
	// 最后那个开着的块
	refuse("开着块", Request{BeginIdx: 7, EndIdx: 8}, "开着的块")

	// 先并出一条 level-2 的父（第 1–4 条），再压出一条 level-1 的兄弟（第 5–6 条）
	if _, err := h.service.Compact(context.Background(), h.session, Request{BeginIdx: 1, EndIdx: 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.Compact(context.Background(), h.session, Request{Blocks: 1}); err != nil {
		t.Fatal(err)
	}
	// 不同层：level-2 的父 + level-1 的弟 并在一起
	refuse("不同层", Request{BeginIdx: 1, EndIdx: 6}, "同一层")

	if rows := h.summaries(t); len(rows) != 4 { // 两条 level-1 + 一条 level-2 + 一条 level-1，失败的都没落
		t.Fatalf("失败不许落库：%+v", rows)
	}
}

// 重摇的三条闸：目标不存在 / 有父（不是根）/ 区间两端缺失 —— 都该报错，且一个字节都不许落库。
func TestRegenerateSummaryRefusals(t *testing.T) {
	h := newHarness(t, config.AgentsConfig{})
	h.turn(t, "第一句")
	h.turn(t, "第二句")
	h.turn(t, "第三句")

	first, err := h.service.Compact(context.Background(), h.session, Request{Blocks: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.Compact(context.Background(), h.session, Request{Blocks: 1}); err != nil {
		t.Fatal(err)
	}
	parent, err := h.service.Compact(context.Background(), h.session, Request{BeginIdx: 1, EndIdx: 4})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// 不存在：这条会话里没有这个 id
	if _, err := h.service.RegenerateSummary(ctx, h.session, "查无此摘要"); err == nil {
		t.Fatal("目标不在本会话：该报错")
	}
	// 有父：孩子不是根（重摇只换顶层摘要的正文）
	if _, err := h.service.RegenerateSummary(ctx, h.session, first.SummaryID); err == nil {
		t.Fatal("有父的摘要不该被重摇")
	}
	// 反例的对照：顶层那一条（父）该能重摇
	if _, err := h.service.RegenerateSummary(ctx, h.session, parent.SummaryID); err != nil {
		t.Fatalf("顶层摘要该能重摇：%v", err)
	}

	// 区间两端缺失：造一条**顶层**摘要，两端为空（老数据）—— 材料没法重建
	//（`RecordSummary` 只管落库、不校验区间两端，这里正是要造出"老数据"那一格）
	open := h.messages(t)[4:]
	before := len(h.summaries(t))
	staleID := "00000000-0000-7000-8000-0000000000ff"
	if err := h.store.RecordSummary(model.Summary{
		ID: staleID, SessionID: h.sessionID, Type: model.TypeMessages, Text: "老数据",
	}, []string{open[0].ID, open[1].ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.RegenerateSummary(ctx, h.session, staleID); err == nil {
		t.Fatal("区间两端缺失的摘要：该报错")
	}
	if rows := h.summaries(t); len(rows) != before+1 { // 只多了刚造的那一行
		t.Fatalf("重摇不落库：%+v", rows)
	}
	if after := h.messages(t); after[4].SummaryID == nil || *after[4].SummaryID != staleID {
		t.Fatalf("重摇不许动 messages：%+v", after[4].SummaryID)
	}
}

// 消息级根：按那段消息的**当前正文**重跑一次 ⇒ 正常回文本，且 summaries 行数不变（不落库）。
func TestRegenerateSummaryMessageRootWritesNothing(t *testing.T) {
	h := newHarness(t, config.AgentsConfig{})
	h.turn(t, "第一句")
	h.turn(t, "第二句")
	h.turn(t, "第三句")

	original, err := h.service.Compact(context.Background(), h.session, Request{Blocks: 2})
	if err != nil {
		t.Fatal(err)
	}
	rows := h.summaries(t)
	messages := h.messages(t)

	generated, err := h.service.RegenerateSummary(context.Background(), h.session, original.SummaryID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(generated.Text, dummyReply) {
		t.Fatalf("dummy 渠道该回本地假摘要：%q", generated.Text)
	}
	if generated.Tokens <= 0 || generated.Provider != "dummy" || generated.Model != "dummy" {
		t.Fatalf("产出 = %+v", generated)
	}
	if generated.PromptVersion != abilities.PromptVersion(abilities.DefaultTemplate(abilities.Compact)) {
		t.Fatalf("prompt_version = %d（想要默认模板的指纹）", generated.PromptVersion)
	}
	// 不落库：行数不变、原来那一行一个字都没改、消息的指针也没动
	after := h.summaries(t)
	if len(after) != len(rows) {
		t.Fatalf("重摇不落库：%d → %d 行", len(rows), len(after))
	}
	if after[0].Text != rows[0].Text || after[0].Blocks != rows[0].Blocks {
		t.Fatalf("原来那一行不该被改：%+v → %+v", rows[0], after[0])
	}
	for index, message := range h.messages(t) {
		if (message.SummaryID == nil) != (messages[index].SummaryID == nil) {
			t.Fatalf("重摇不许动 messages：第 %d 条", index)
		}
		if message.SummaryID != nil && *message.SummaryID != *messages[index].SummaryID {
			t.Fatalf("重摇不许动 messages：第 %d 条", index)
		}
	}
}

// 合并级根：材料是孩子的**轻抬头 + 正文**（不是对话原文）—— httptest 假上游捕获请求体断言。
//
// 照 `TestMaterialStripsStateAndSendsTemplate` 的写法。
func TestRegenerateSummaryMergeRootKeepsChildHeaders(t *testing.T) {
	var body []byte
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"重摇出来的一段前情提要"}}],` +
			`"usage":{"prompt_tokens":80,"completion_tokens":6,"total_tokens":86}}`))
	}))
	defer stub.Close()

	h := newHarness(t, config.AgentsConfig{})
	h.turn(t, "第一句")
	h.turn(t, "第二句")
	h.turn(t, "第三句")

	if _, err := h.service.Compact(context.Background(), h.session, Request{Blocks: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.Compact(context.Background(), h.session, Request{Blocks: 1}); err != nil {
		t.Fatal(err)
	}
	parent, err := h.service.Compact(context.Background(), h.session, Request{BeginIdx: 1, EndIdx: 4})
	if err != nil {
		t.Fatal(err)
	}

	// 重摇走 openai-compat 假上游（与 `prepare` 同一套解析：能力没覆盖 ⇒ 骑会话的渠道 / 模型）
	if err := config.SaveJSON(h.paths.Config("providers.json"), mustJSON(t,
		`{"providers":[{"id":"stub","kind":"openai-compat","base_url":"`+stub.URL+`","identity":"bare"}]}`)); err != nil {
		t.Fatal(err)
	}
	h.session.Provider, h.session.Model = "stub", "m"

	rows := h.summaries(t)
	generated, err := h.service.RegenerateSummary(context.Background(), h.session, parent.SummaryID)
	if err != nil {
		t.Fatal(err)
	}
	if generated.Text != "重摇出来的一段前情提要" ||
		!strings.Contains(string(generated.Usage), `"completion_tokens":6`) {
		t.Fatalf("产出该带上游回的正文与 usage：%+v", generated)
	}
	if generated.Provider != "stub" || generated.Model != "m" {
		t.Fatalf("生效渠道 / 模型 = %s / %s", generated.Provider, generated.Model)
	}

	sent := string(body)
	// 材料是各段孩子的**轻抬头 + 正文**（不是那段对话原文）
	for _, want := range []string{
		"【第 1–2 条（1 块）】",
		"【第 3–4 条（1 块）】",
		dummyReply, // 两条孩子当初就是 dummy 压出来的正文
		"不是对话原文",   // mergeHeader 把口气说清
	} {
		if !strings.Contains(sent, want) {
			t.Fatalf("合并级的材料里该有 %q：%s", want, sent)
		}
	}
	if strings.Contains(sent, "第一句") || strings.Contains(sent, "<state>") {
		t.Fatalf("合并级材料不该发对话原文 / 状态块：%s", sent)
	}

	// 不落库：行数不变
	if after := h.summaries(t); len(after) != len(rows) {
		t.Fatalf("重摇不落库：%d → %d 行", len(rows), len(after))
	}
}

// 策略抬头两跳：A B C D E F G 压成 AB/CD/EF+G → 再压 2 块 ⇒ 并 AB+CD（AD）；
// 再压 ⇒ EF 是一坨、G 不是坨 ⇒ 凑不齐 2 坨 ⇒ 报无可压缩。
func TestStrategyMergesTopLevelSummariesPyramid(t *testing.T) {
	h := newHarness(t, config.AgentsConfig{})
	for _, text := range []string{"A", "B", "C", "D", "E", "F", "G"} {
		h.turn(t, text)
	}
	ctx := context.Background()
	// 七块：已闭合 [1,2]…[11,12]，开着 [13,14]。先压成 AB/CD/EF+G。
	ab, err := h.service.Compact(ctx, h.session, Request{Blocks: 2})
	if err != nil {
		t.Fatal(err)
	}
	cd, err := h.service.Compact(ctx, h.session, Request{Blocks: 2})
	if err != nil {
		t.Fatal(err)
	}
	ef, err := h.service.Compact(ctx, h.session, Request{Blocks: 2})
	if err != nil {
		t.Fatal(err)
	}
	if ab.FromIdx != 1 || ab.ToIdx != 4 || ab.Merged {
		t.Fatalf("AB 该是消息级 1–4：%+v", ab)
	}
	if ef.FromIdx != 9 || ef.ToIdx != 12 || ef.Merged {
		t.Fatalf("EF 该是消息级 9–12：%+v", ef)
	}
	messages := h.messages(t)
	tops := map[string]bool{}
	for _, row := range h.summaries(t) {
		if row.ParentSummaryID == nil {
			tops[row.ID] = true
		}
	}
	for _, id := range []string{ab.SummaryID, cd.SummaryID, ef.SummaryID} {
		if !tops[id] {
			t.Fatalf("孩子 %s 该是顶层：%v", id, tops)
		}
	}

	// 第一跳：底层只剩开着的那块 ⇒ 并最老的连续两坨 AB+CD
	ad, err := h.service.Compact(ctx, h.session, Request{Blocks: 2})
	if err != nil {
		t.Fatal(err)
	}
	if ad.FromIdx != 1 || ad.ToIdx != 8 || !ad.Merged {
		t.Fatalf("AD 该是合并级 1–8：%+v", ad)
	}
	rows := h.summaries(t)
	parent := findSummary(t, rows, ad.SummaryID)
	if len(parent.SourceIDs) != 2 || parent.SourceIDs[0] != ab.SummaryID || parent.SourceIDs[1] != cd.SummaryID {
		t.Fatalf("AD 的 source_ids 该是 AB+CD：%+v", parent.SourceIDs)
	}
	if parent.BeginMessageID == nil || *parent.BeginMessageID != messages[0].ID ||
		parent.EndMessageID == nil || *parent.EndMessageID != messages[7].ID {
		t.Fatalf("AD 的两端该是第 1 条 → 第 8 条：%+v", parent)
	}
	for _, childID := range []string{ab.SummaryID, cd.SummaryID} {
		child := findSummary(t, rows, childID)
		if child.ParentSummaryID == nil || *child.ParentSummaryID != ad.SummaryID {
			t.Fatalf("孩子 %s 该指向 AD：%+v", childID, child.ParentSummaryID)
		}
	}
	got := h.turns.Status(h.sessionID).Compact
	if got == nil || got.State != "done" || got.FromIdx != 1 || got.ToIdx != 8 || !got.Merged || got.Compacted != int(parent.Blocks) {
		t.Fatalf("turn 收尾该带区间 1–8 与合并位：%+v", got)
	}

	// 第二跳：底层只剩开着的那块（G 在上面，不整块可压）⇒ 看摘要层。
	// 顶层剩下 AD（1–8）+ EF（9–12）：相邻但跨层（AD 是 level-2、EF 是 level-1）⇒ 不凑；
	// G 只是开着块上的一条单消息、不是坨 ⇒ 凑不齐 2 坨 ⇒ 无可压缩。
	before := len(h.summaries(t))
	if _, err := h.service.Compact(ctx, h.session, Request{Blocks: 2}); err == nil ||
		!strings.Contains(err.Error(), "无可压缩") {
		t.Fatalf("跨层 + G 不是坨 ⇒ 该报无可压缩：%v", err)
	}
	if after := len(h.summaries(t)); after != before {
		t.Fatalf("失败不许落库：%d → %d", before, after)
	}
}

// 查表那一行：model_route 有 responses 行 ⇒ 这一发走 /responses，正文从 output_text 来；
// 无行 ⇒ 还是 /chat/completions（零行为变化）。
func TestCompactRoutesByModelRouteTable(t *testing.T) {
	var path string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(path, "/responses") {
			_, _ = w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"一段前情提要"}]}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"一段前情提要"}}]}`))
	}))
	defer stub.Close()

	providersJSON := mustJSON(t, `{"providers":[{"id":"stub","kind":"openai-compat","base_url":"`+stub.URL+`","identity":"bare"}]}`)

	// 有行 ⇒ /responses。
	h := newHarness(t, config.AgentsConfig{})
	if err := config.SaveJSON(h.paths.Config("providers.json"), providersJSON); err != nil {
		t.Fatal(err)
	}
	if err := h.store.ReplaceModelRoutes("stub", map[string]string{"m": "openai-responses"}, 7); err != nil {
		t.Fatal(err)
	}
	h.session.Provider, h.session.Model = "stub", "m"
	h.turn(t, "第一句")
	h.turn(t, "第二句")
	result, err := h.service.Compact(context.Background(), h.session, Request{Blocks: 1})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/responses" {
		t.Fatalf("有 responses 行该走 /responses：path = %q", path)
	}
	if !strings.Contains(result.Text, "一段前情提要") {
		t.Fatalf("正文该从 output_text 来：%q", result.Text)
	}

	// 无行 ⇒ 还是 /chat/completions。
	g := newHarness(t, config.AgentsConfig{})
	if err := config.SaveJSON(g.paths.Config("providers.json"), providersJSON); err != nil {
		t.Fatal(err)
	}
	g.session.Provider, g.session.Model = "stub", "m"
	g.turn(t, "第一句")
	g.turn(t, "第二句")
	if _, err := g.service.Compact(context.Background(), g.session, Request{Blocks: 1}); err != nil {
		t.Fatal(err)
	}
	if path != "/chat/completions" {
		t.Fatalf("无行该还是 /chat/completions：path = %q", path)
	}
}
