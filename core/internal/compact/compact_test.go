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
// 钉住：父的 SourceKind=summary、自己无父、Blocks=子和、两个孩子都指向它、Dirty 传播、
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
	if _, err := h.store.UpdateMessage(h.sessionID, messages[0].ID, "第一句（改过）"); err != nil {
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
	rows := h.summaries(t)
	parent := findSummary(t, rows, merged.SummaryID)
	if parent.SourceKind != model.SourceSummaries {
		t.Fatalf("父的 source_kind 该是 summary：%+v", parent)
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
		if item.Source != "summary" {
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
