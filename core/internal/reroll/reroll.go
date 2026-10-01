// Package reroll：**重摇模式** —— 把当前路径上最后那条 assistant 回复重新摇几版，挑一版定下来。
//
// 口径（`DEFINE.md`「重摇（Re-roll）」是唯一权威）：
//
//   - **只重摇尾条，且它必须是 assistant** —— 尾条是 user ⇒ 那是"重发"，不是重摇（**明说拒绝**）；
//   - 进模式 = **立刻摇一次**：列表于是有两条 —— 原文占 `idx = 1`、摇出来的占 `idx = 2`；
//   - 候选只在**内存**里（不建表、不写库、Copy 不带走）；**一发出新消息 ⇒ 全删**；
//     删那条消息 / 切会话 / **进程重启** ⇒ 自然消失（重启后库里只剩你选中的那一版）；
//   - 切换（选中另一版）= **重建库里那条 Message**：`message_id` **绝对不变**，正文与
//     usage / duration_ms / reasoning / reasoning_ms 一起换（复用"改正文"那条路 ⇒ 摘要照旧标 `dirty`）；
//   - `idx` 是**位次不是身份**：删掉第 k 版 ⇒ 后方统一 -1，再把"当前那条指向的位置"那版 apply 回去
//     （删的是最后一条 ⇒ 前移后那个号不存在 ⇒ **clamp 到新的最后一条**）；
//   - 只剩一版 ⇒ **退出模式 + 清列表**（**不 apply、也不回原文**：message 保留当前这版）；
//   - 与生成**共用同一把闸**（`turn` 登记表）：重摇期间不许发新消息（409），生成期间不许重摇。
//
// **不保护原文，只保护原文的结构体**（用户口径）：重摇的前提就是你对原文不满意了，
// 所以原文没有任何特殊待遇 —— 它只是"第一版候选"（好处是切回来的时候连正文都能换回去，
// 而库里的那条 `Message` 自始至终只有一个 UUID）。
package reroll

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"microchat/internal/config"
	"microchat/internal/model"
	"microchat/internal/providers"
	"microchat/internal/statelang"
	"microchat/internal/store"
	"microchat/internal/task"
	"microchat/internal/turn"
)

// previewChars：界面预览留几个**字符**（中文按字节截会切出残字 ⇒ 用 `model.TitleFrom`）。
const previewChars = 40

// dummyReply：本地假上游（`kind: dummy`）吐的那句候选 —— 确定性、不联网，一眼认得出是假的。
//
// 与一轮对话那句**刻意不同**（`（测试用空模型·重摇）`）：dummy 下两版得能分得出来，
// 否则"切了没切"在验收里看不出来。
const dummyReply = "（测试用空模型·重摇）"

// TargetKind：重摇模式**摇的是什么** —— 一类是那条 assistant 回复（消息级），一类是一条摘要在（摘要级）。
//
// 一个会话同一时刻只有一个重摇模式（一份候选列表，带 kind）：进另一种模式 / 换一条摘要目标时，
// 正在摇 ⇒ conflict，没在摇 ⇒ 清掉旧的、进新的。
type TargetKind string

const (
	TargetMessage TargetKind = "message"
	TargetSummary TargetKind = "summary"
)

// RerolledMessage：**一版候选** —— 它**没有自己的 UUID**（它不是一条消息，要指认它就用 `idx` 这个位次）。
//
// 带上那次生成的附带信息（usage / 耗时 / 思考），切换时**一起换上去** ——
// 于是"选中的那一版"在库里与一次正常生成没有任何区别。
//
// `Tokens` 只有摘要模式用（`ReplaceSummaryText` 要它）；消息模式不用（消息那一版不带 token 数）。
type RerolledMessage struct {
	SessionID       string          `json:"session_id"`
	TargetMessageID string          `json:"target_message_id"`
	RawText         string          `json:"raw_text"`
	Idx             int             `json:"idx"`
	Tokens          int             `json:"tokens,omitempty"`
	Usage           json.RawMessage `json:"usage,omitempty"`
	DurationMS      *int64          `json:"duration_ms,omitempty"`
	Reasoning       string          `json:"reasoning,omitempty"`
	ReasoningMS     *int64          `json:"reasoning_ms,omitempty"`
	At              int64           `json:"at"`
}

// Item：给界面看的一版（**不吐全文** —— 预览几十个字就够）。
type Item struct {
	Idx     int    `json:"idx"`
	Preview string `json:"preview"`
	At      int64  `json:"at"`
	// Pending：这一版还在摇（还没有正文）。
	Pending bool `json:"pending"`
	// Current：这一版就是**现在库里那条 Message 的正文**（界面上高亮它）。
	Current bool `json:"current"`
}

// State：`GET /sessions/{session_id}/reroll` 的响应 —— 界面看的就是它：
// 共几版、当前第几版、有没有在摇（以及摇了多久）、每一版的预览。
type State struct {
	Active     bool       `json:"active"`
	TargetKind TargetKind `json:"target_kind,omitempty"`
	// TargetMessageID / TargetSummaryID：这次摇的是哪条（消息模式给前者，摘要模式给后者；另一个留空）。
	TargetMessageID string `json:"target_message_id,omitempty"`
	TargetSummaryID string `json:"target_summary_id,omitempty"`
	// FromIdx / ToIdx：摘要模式 —— 目标（根）盖住的那段消息区间（1-based）；消息模式不给。
	FromIdx    *int `json:"from_idx,omitempty"`
	ToIdx      *int `json:"to_idx,omitempty"`
	Count      int  `json:"count"`
	CurrentIdx int  `json:"current_idx"`
	// Running / ElapsedMS / Error：那次重摇的活状态（从 `turn` 那一档读，**不另存一份**，两类共用）。
	Running   bool   `json:"running"`
	ElapsedMS int64  `json:"elapsed_ms"`
	Error     string `json:"error,omitempty"`
	Items     []Item `json:"items"`
}

// Accepted：`POST /sessions/{session_id}/reroll` 的 **202** 回执。
//
// 带 `target_message_id`（消息模式：这次摇的是哪条）/ `target_summary_id`（摘要模式：哪条摘要）
// 与 `task_id`（这次上游请求的 id，日志/面板里对得上）；`state` 就是 `GET .../reroll` 的那一份
// （进模式那一刻：原文 + 正在摇的那一版）。
type Accepted struct {
	TargetMessageID string `json:"target_message_id,omitempty"`
	TargetSummaryID string `json:"target_summary_id,omitempty"`
	TaskID          string `json:"task_id,omitempty"`
	State           State  `json:"state"`
}

// Error：带 code 的失败 —— `code` 与 HTTP 错误体同一套词（客户端按 code 分支，别匹配文案）。
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

// ErrorCode：给 server / `-debug` 这两条出口读的。
func (e *Error) ErrorCode() string { return e.Code }

func invalid(format string, args ...any) error {
	return &Error{Code: "invalid", Message: fmt.Sprintf(format, args...)}
}

func notFound(format string, args ...any) error {
	return &Error{Code: "not_found", Message: fmt.Sprintf(format, args...)}
}

func conflict(format string, args ...any) error {
	return &Error{Code: "conflict", Message: fmt.Sprintf(format, args...)}
}

// Assembler：把"历史减去被重摇的那条回复"装配成这次要发的消息。
//
// **唯一实现是 `chat.Service`**（`RerollMessages`）—— 重摇不许自己拼一份装配：
// 与一轮对话共用那条路（`assembleMessages`），否则"重摇发出去的东西"与"真发出去的东西"迟早漂移。
// 接口在这儿定义 ⇒ `reroll` 与 `chat` 互不依赖（谁也不用 import 谁）。
type Assembler interface {
	RerollMessages(session model.Session) ([]providers.ChatMessage, error)
}

// GeneratedSummary：一次"只生成、不落库"的摘要重摇产出（**消费方定义的形状**）。
//
// 唯一实现由 `main` 用适配器接上 `compact.Service`（`RegenerateSummary`）：摘要不许绕过 compact
// 自己拼请求，所以这里只认"给定摘要 id，重跑一次，把产出交回来"这一句话。
type GeneratedSummary struct {
	Text          string
	Tokens        int
	Provider      string
	Model         string
	PromptVersion int64
	Usage         json.RawMessage
}

// SummaryGenerator：摘要重摇那一趟要的生成器（接口在这儿定义 ⇒ `reroll` 不 import `compact`）。
type SummaryGenerator interface {
	RegenerateSummary(ctx context.Context, session model.Session, summaryID string) (GeneratedSummary, error)
}

// Service：重摇模式的唯一实现处。
type Service struct {
	Store  *store.Store
	Paths  config.Paths
	Turns  *turn.Registry
	Tasks  *task.Registry
	Roller Assembler
	// Summaries：摘要模式的生成器（消息模式不用它）。没接 ⇒ 摘要重摇那一趟报 internal。
	Summaries SummaryGenerator

	// mu：候选列表的门（进程内的事实 ⇒ 只有这一处登账）。
	mu    sync.Mutex
	lists map[string]*list
}

// list：一个会话的候选列表 —— **最多一个 target**（同一个会话同一时刻只有一个重摇模式）。
type list struct {
	sessionID string
	// kind：这个列表摇的是什么（消息级 / 摘要级）。
	kind TargetKind
	// targetID：消息模式 —— 摇的是哪条消息（= 尾条）。
	targetID string
	// summaryID / fromIdx / toIdx：摘要模式 —— 目标（根）摘要 id 与它盖的区间（1-based）。
	summaryID string
	fromIdx   int
	toIdx     int
	items     []RerolledMessage
	// versions：摘要模式专用 —— 与 items **同位次平行**的那一版的生成参数
	// （`ReplaceSummaryText` 要的 Provider/Model/PromptVersion 不在 `RerolledMessage` 里）。
	versions []GeneratedSummary
	// current：现在 apply 的是第几版（位次）。
	current int
	// pendingToken：有一版正在摇 ⇒ 那次重摇的令牌（0 = 没有在摇）。
	//
	// 记**令牌**而不是位次：位次会被"删掉前面那几版"改掉，令牌不会
	// （前面删了一版，摇出来那一版落在新的最后一位上）。
	pendingToken uint64
	// pendingIdx：在摇的那一版此刻占第几位（只有展示与"删不得"判定用它）。
	pendingIdx int
}

func New(st *store.Store, paths config.Paths, turns *turn.Registry, tasks *task.Registry, roller Assembler) *Service {
	return &Service{Store: st, Paths: paths, Turns: turns, Tasks: tasks, Roller: roller,
		lists: map[string]*list{}}
}

// ── 进入模式（= 立刻摇一次）────────────────────────────────────────────────

// roll：一次重摇在受理时就把料取齐的那一份（**两类目标共用**）。
type roll struct {
	kind     TargetKind
	session  model.Session
	targetID string // 消息模式：摇的是哪条消息（摘要模式留空）
	// summaryID：摘要模式：目标（根）摘要 id（消息模式留空）。
	summaryID string
	token     uint64
	guard     *task.Guard
	channel   config.Provider // 真发的渠道（dummy 除外；摘要模式不用 —— compact 自己选）
	model     string
	local     string // 非空 ⇒ 本地假上游，不联网
	// storesReasoning：这个渠道要不要把"思考"留档（与一轮生成同一个判据：`store_reasoning`）。
	storesReasoning bool
}

// Enter：**进入重摇模式并立刻发起一次重摇**（202 那一套：受理即回，候选在后台摇）。
//
// 已经在模式里（且没有在摇）⇒ **再摇一版**（列表往后追一版 —— 界面上那个分母就是这么来的）。
// 校验（尾条必须是 assistant、闸门、模型可用）全在**返回之前**做完 ⇒ 这些错当场回给调用方。
func (s *Service) Enter(session model.Session) (Accepted, error) {
	prepared, err := s.prepare(session)
	if err != nil {
		return Accepted{}, err
	}
	go func() { s.roll(context.Background(), prepared) }()
	return Accepted{
		TargetMessageID: prepared.targetID, TaskID: prepared.guard.ID(),
		State: s.State(session.ID, TargetMessage),
	}, nil
}

// EnterSync：**同步摇一次** —— 与 `Enter` 同一个底（`prepare` + `roll`，只有"要不要丢给后台"这一线之差）。
//
// 测试用它：不靠 sleep 就能确定地看到"摇完"的样子（dummy 那一发是瞬时的，但测试不该赌时序）。
// 线上那条路是 `Enter`（受理即回 **202**，候选在后台摇 —— 界面靠 `/reroll` 状态轮询它）。
func (s *Service) EnterSync(ctx context.Context, session model.Session) (Accepted, error) {
	prepared, err := s.prepare(session)
	if err != nil {
		return Accepted{}, err
	}
	s.roll(ctx, prepared)
	return Accepted{
		TargetMessageID: prepared.targetID, TaskID: prepared.guard.ID(),
		State: s.State(session.ID, TargetMessage),
	}, nil
}

// EnterSummary：**进入摘要重摇模式并立刻摇一版** —— 与 `Enter` 平行，只是目标换成
// `idx` 指的那条消息**所在的根摘要**（同树任意 idx 指向同一个目标）。
//
// 校验（idx 解析、未被摘要盖住、闸门、模式替换冲突）全在**返回之前**做完 ⇒ 这些错当场回给调用方；
// **模型 / 能力可用性不在这儿预检** —— compact 的生成失败落进 `turn` 的 error 那一档（走现有失败路径）。
func (s *Service) EnterSummary(session model.Session, idx int) (Accepted, error) {
	prepared, err := s.prepareSummary(session, idx)
	if err != nil {
		return Accepted{}, err
	}
	go func() { s.roll(context.Background(), prepared) }()
	return Accepted{
		TargetSummaryID: prepared.summaryID, TaskID: prepared.guard.ID(),
		State: s.State(session.ID, TargetSummary),
	}, nil
}

// EnterSummarySync：**同步摇一版摘要** —— 与 `EnterSummary` 同一个底，测试用（不赌后台时序）。
func (s *Service) EnterSummarySync(ctx context.Context, session model.Session, idx int) (Accepted, error) {
	prepared, err := s.prepareSummary(session, idx)
	if err != nil {
		return Accepted{}, err
	}
	s.roll(ctx, prepared)
	return Accepted{
		TargetSummaryID: prepared.summaryID, TaskID: prepared.guard.ID(),
		State: s.State(session.ID, TargetSummary),
	}, nil
}

// prepare：受理那一半 —— 校验尾条、过闸门、挂号、把原文那一版先占住第一位。
func (s *Service) prepare(session model.Session) (*roll, error) {
	// 换模式 / 再摇一版之前先看：这个会话是不是已经有一趟在摇（有就当场说清在摇什么）
	if err := s.busyConflict(session.ID); err != nil {
		return nil, err
	}
	_, _, providersConfig, err := config.Load(s.Paths)
	if err != nil {
		// 读不出来就说读不出来：静默按默认跑会让"配了别的渠道"的人对着结果猜
		return nil, &Error{Code: "internal", Message: "读配置失败：" + err.Error()}
	}
	messages, err := s.Store.ListMessages(session.ID)
	if err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		return nil, invalid("这条会话还没有消息，没有什么可重摇的")
	}
	tail := messages[len(messages)-1]
	if tail.Role != model.RoleAssistant {
		// 明说：那不是重摇，是重发（尾条是用户消息 ⇒ 你要的是"再答一次那句"）
		return nil, invalid("重摇只针对最后那条 assistant 回复；这条会话的尾条是用户消息" +
			"（那是重发，不是重摇）—— 先让它答完再摇，或者换个说法重发")
	}

	// 生效渠道 / 模型：重摇是**对话型调用** ⇒ 就用会话自己的（与一轮对话同一条口径）
	backend := providers.SelectBackend(session.Provider, session.Model, providersConfig)
	local := ""
	switch backend.Name {
	case providers.BackendFallback:
		return nil, invalid("这条会话没有可用的模型（渠道 %q 不在 providers.json 里，或模型名为空）："+
			"重摇要真调一次上游 —— 先配好渠道，或把这条会话切到 dummy", session.Provider)
	case providers.BackendDummy:
		local = dummyReply
	}

	// **与生成共用同一把闸**：生成在跑、或上一次重摇还没摇完 ⇒ 409（不排队）
	token, err := s.Turns.BeginReroll(session.ID)
	if err != nil {
		return nil, conflict("这个会话还在生成中（或上一次重摇还没摇完）：等它跑完，或先按停止")
	}
	sessionID := session.ID
	guard := s.Tasks.Begin(task.KindReroll, &sessionID, "重摇 · 会话 "+shortID(sessionID))
	if err := s.seed(tail, token); err != nil {
		guard.Fail(err.Error())
		s.Turns.FinishReroll(session.ID, token, err.Error())
		return nil, err
	}
	return &roll{
		kind: TargetMessage, session: session, targetID: tail.ID, token: token, guard: guard,
		channel: backend.Provider, model: session.Model, local: local,
		storesReasoning: backend.StoresReasoning(),
	}, nil
}

// seed：把这次的位次占下来；第一次进模式时**原文先占第一位**。
//
// 原文那一版记的是它此刻的正文与附带信息 ⇒ "切回原文"就是把它 apply 回去
// （**不是**原样不动地"保护"—— 原文没有任何特殊待遇，它只是第一版）。
func (s *Service) seed(tail model.Message, token uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.lists[tail.SessionID]
	if entry == nil || entry.kind != TargetMessage || entry.targetID != tail.ID {
		// 新的一次重摇（上一次的候选说的不是这条尾巴了、或上一次摇的是另一类目标 ⇒ 直接换掉）
		entry = &list{
			sessionID: tail.SessionID, kind: TargetMessage, targetID: tail.ID, current: 1,
			items: []RerolledMessage{{
				SessionID: tail.SessionID, TargetMessageID: tail.ID,
				RawText: tail.Content, Idx: 1,
				Usage: tail.Usage, DurationMS: tail.DurationMS,
				Reasoning: tail.Reasoning, ReasoningMS: tail.ReasoningMS,
				At: time.Now().UnixMilli(),
			}},
		}
		s.lists[tail.SessionID] = entry
	}
	if entry.pendingToken != 0 {
		// 闸门挡着，正常到不了这儿；真到了就明说（别默默覆盖掉正在摇的那一版）
		return conflict("这个会话已经有一版在摇了：等它出来，或先按停止")
	}
	entry.pendingToken = token
	entry.pendingIdx = len(entry.items) + 1
	return nil
}

// busyConflict：这个会话已经有一趟在摇 ⇒ 说清**摇的是什么**（换模式 / 换目标被它挡住）。
//
// 只认候选列表里那个"正在摇"的位次；`turn` 那一道闸门另外再挡一次（手工占闸、或列表没登账时）。
func (s *Service) busyConflict(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.lists[sessionID]
	if entry == nil || entry.pendingToken == 0 {
		return nil
	}
	if entry.kind == TargetSummary {
		return conflict("这个会话正在重摇一条摘要（还没摇完）：等它出来，或先按停止")
	}
	return conflict("这个会话正在重摇最后那一条回复（还没摇完）：等它出来，或先按停止")
}

// prepareSummary：受理那一半（摘要版）—— 解析目标、过闸门、挂号、把目标当前那一版先占住第一位。
func (s *Service) prepareSummary(session model.Session, idx int) (*roll, error) {
	if err := s.busyConflict(session.ID); err != nil {
		return nil, err
	}
	messages, err := s.Store.ListMessages(session.ID)
	if err != nil {
		return nil, err
	}
	if idx < 1 || idx > len(messages) {
		return nil, invalid("第 %d 条不存在（这条会话只有 %d 条）", idx, len(messages))
	}
	target, fromIdx, toIdx, err := s.resolveRoot(session.ID, messages, messages[idx-1])
	if err != nil {
		return nil, err
	}
	// **与生成共用同一把闸**：生成在跑、或上一次重摇还没摇完 ⇒ 409（不排队）
	token, err := s.Turns.BeginReroll(session.ID)
	if err != nil {
		return nil, conflict("这个会话还在生成中（或上一次重摇还没摇完）：等它跑完，或先按停止")
	}
	sessionID := session.ID
	guard := s.Tasks.Begin(task.KindReroll, &sessionID, "重摇摘要 · 会话 "+shortID(sessionID))
	if err := s.seedSummary(session, target, fromIdx, toIdx, token); err != nil {
		guard.Fail(err.Error())
		s.Turns.FinishReroll(session.ID, token, err.Error())
		return nil, err
	}
	return &roll{
		kind: TargetSummary, session: session, summaryID: target.ID, token: token, guard: guard,
	}, nil
}

// resolveRoot：把"第 idx 条消息"解析成**它的根摘要**（没有父的那条）+ 那条根盖的区间（1-based）。
//
// 同树任意 idx 指向同一个目标：沿 `summary_id → parent_summary_id` 上溯到根。
// 未覆盖 / 链断 / 摘要找不着 ⇒ invalid（说清是哪一步断的）。
func (s *Service) resolveRoot(sessionID string, messages []model.Message, message model.Message) (model.Summary, int, int, error) {
	if message.SummaryID == nil {
		return model.Summary{}, 0, 0,
			invalid("第 %d 条还没被摘要盖住，没有可重摇的摘要", message.Idx)
	}
	summaries, err := s.Store.ListSummaries(sessionID)
	if err != nil {
		return model.Summary{}, 0, 0, err
	}
	byID := make(map[string]model.Summary, len(summaries))
	for _, summary := range summaries {
		byID[summary.ID] = summary
	}
	current, ok := byID[*message.SummaryID]
	if !ok {
		return model.Summary{}, 0, 0,
			invalid("第 %d 条盖着的那条摘要找不着了（多半被删了）：先让它重新压一遍", message.Idx)
	}
	for current.ParentSummaryID != nil {
		parent, ok := byID[*current.ParentSummaryID]
		if !ok {
			return model.Summary{}, 0, 0,
				invalid("第 %d 条那条摘要的上一级找不着了（父子链断了）：先让它重新压一遍", message.Idx)
		}
		current = parent
	}
	// 区间：拿根的起止消息在当前消息表里定位（1-based）。
	byMessageID := make(map[string]int, len(messages))
	for _, item := range messages {
		byMessageID[item.ID] = item.Idx
	}
	fromIdx, toIdx := 0, 0
	if current.BeginMessageID != nil {
		fromIdx = byMessageID[*current.BeginMessageID]
	}
	if current.EndMessageID != nil {
		toIdx = byMessageID[*current.EndMessageID]
	}
	return current, fromIdx, toIdx, nil
}

// seedSummary：摘要模式那一半 —— 把这次的位次占下来；第一次进模式时**目标摘要当前那一版先占第一位**。
//
// 原文那一版记的是目标摘要**此刻**的 text / usage / tokens ⇒ "切回原文"就是把它换回去。
func (s *Service) seedSummary(session model.Session, target model.Summary, fromIdx, toIdx int, token uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.lists[session.ID]
	if entry == nil || entry.kind != TargetSummary || entry.summaryID != target.ID {
		// 换模式 / 换目标 ⇒ 清掉旧的、进新的
		seed := GeneratedSummary{
			Text: target.Text, Tokens: int(target.Tokens), Provider: target.Provider,
			Model: target.Model, PromptVersion: target.PromptVersion, Usage: target.Usage,
		}
		entry = &list{
			sessionID: session.ID, kind: TargetSummary, summaryID: target.ID,
			fromIdx: fromIdx, toIdx: toIdx, current: 1,
			items: []RerolledMessage{{
				SessionID: session.ID, RawText: target.Text, Tokens: int(target.Tokens),
				Usage: target.Usage, Idx: 1, At: time.Now().UnixMilli(),
			}},
			versions: []GeneratedSummary{seed},
		}
		s.lists[session.ID] = entry
	}
	if entry.pendingToken != 0 {
		// 闸门挡着，正常到不了这儿；真到了就明说（别默默覆盖掉正在摇的那一版）
		return conflict("这个会话已经有一版在摇了：等它出来，或先按停止")
	}
	entry.pendingToken = token
	entry.pendingIdx = len(entry.items) + 1
	return nil
}

// roll：后台那半程 —— 两类共用外壳（挂 ctx、收尾），消息模式走装配 + 上游，摘要模式走生成器。
//
// 走 `Complete`（非流式）而不是 `Chat`：候选**没有人在一个字一个字地看**（界面只画「重摇中… 耗时」），
// 而这一趟是"整段拿结果"的后台子调用 —— 与压缩、起标题同一条脾气。代价是**没有"第一段正文"那一刻** ⇒
// 新摇出来的那一版 `reasoning_ms` 是空的（那格说的是"受理 → 第一段正文"，没有那一刻就不能编）。
func (s *Service) roll(ctx context.Context, prepared *roll) {
	defer prepared.guard.Interrupted() // 忘了收 / panic ⇒ 记成"中断"，绝不留僵尸条目
	sessionID := prepared.session.ID

	ctx, cancel := context.WithCancel(ctx)
	s.Turns.Attach(sessionID, prepared.token, cancel)
	defer cancel()

	if prepared.kind == TargetSummary {
		s.rollSummary(ctx, prepared)
		return
	}

	messages, err := s.Roller.RerollMessages(prepared.session)
	if err != nil {
		s.abort(prepared, err)
		return
	}
	started := time.Now()
	wire := providers.FromConfig(prepared.channel)
	result, err := providers.NewClient(wire).Complete(ctx, wire, providers.Request{
		Model:     prepared.model,
		Messages:  messages,
		SessionID: sessionID, // 骑同一个会话 id（网关按它路由、前缀缓存也认它）
	}, prepared.local)
	if err != nil {
		s.abort(prepared, err)
		return
	}

	durationMS := time.Since(started).Milliseconds()
	candidate := RerolledMessage{
		SessionID: sessionID, TargetMessageID: prepared.targetID,
		RawText: result.Text, Usage: result.Usage, DurationMS: &durationMS,
		At: time.Now().UnixMilli(),
	}
	// 思考：与一轮生成同一条规矩 —— 渠道要留档才留（`store_reasoning`）
	if prepared.storesReasoning {
		candidate.Reasoning = result.Reasoning
	}
	if !s.Turns.IsMine(sessionID, prepared.token) {
		// 被按停 / 被顶掉：结果**丢掉**（候选也不留），别把它写到别人的账上
		prepared.guard.Cancel()
		s.dropPending(prepared)
		return
	}
	if !s.land(prepared, candidate, nil) {
		// 列表已经清了（退出模式 / 新消息）⇒ 这一版没人要
		prepared.guard.Cancel()
		s.Turns.FinishReroll(sessionID, prepared.token, "")
		return
	}
	prepared.guard.Succeed()
	s.Turns.FinishReroll(sessionID, prepared.token, "")
}

// rollSummary：摘要模式那半程 —— 借 `SummaryGenerator`（= compact 的"只生成、不落库"半程）重跑一次，
// 产出落进候选（**不进库**；apply 那一刻才换）。
//
// 上游错、compact 的拒绝（材料不对 / 产出空）都由生成器原样报回 ⇒ 走 `abort`（摘占位、error 落 turn），
// 与消息模式同一条失败路径。
func (s *Service) rollSummary(ctx context.Context, prepared *roll) {
	sessionID := prepared.session.ID
	if s.Summaries == nil {
		s.abort(prepared, &Error{Code: "internal", Message: "摘要重摇没接生成器（Summaries 没接上）"})
		return
	}
	generated, err := s.Summaries.RegenerateSummary(ctx, prepared.session, prepared.summaryID)
	if err != nil {
		s.abort(prepared, err)
		return
	}
	candidate := RerolledMessage{
		SessionID: sessionID, RawText: generated.Text, Tokens: generated.Tokens,
		Usage: generated.Usage, At: time.Now().UnixMilli(),
	}
	if !s.Turns.IsMine(sessionID, prepared.token) {
		// 被按停 / 被顶掉：结果**丢掉**（候选也不留）
		prepared.guard.Cancel()
		s.dropPending(prepared)
		return
	}
	if !s.land(prepared, candidate, &generated) {
		prepared.guard.Cancel()
		s.Turns.FinishReroll(sessionID, prepared.token, "")
		return
	}
	prepared.guard.Succeed()
	s.Turns.FinishReroll(sessionID, prepared.token, "")
}

// abort：这一趟没摇成 —— 摘掉那个占位的位次，并把原因写进 `turn` 那一档。
//
// 只剩原文一版时顺手退出模式（`dropPending` 干这件事）：一版没什么可挑的。
func (s *Service) abort(prepared *roll, cause error) {
	mine := s.Turns.IsMine(prepared.session.ID, prepared.token)
	s.dropPending(prepared)
	if mine {
		s.Turns.FinishReroll(prepared.session.ID, prepared.token, cause.Error())
		prepared.guard.Fail(cause.Error())
		return
	}
	prepared.guard.Cancel() // 被按停：正常操作，不是失败
}

// land：把摇出来的这一版落在**当时**那个位次上（列表已经被清掉/换掉 ⇒ 回 false，这一版丢掉）。
//
// `version` 只有摘要模式给（`Provider/Model/PromptVersion` 得与 items 同位次平行地留一份）。
func (s *Service) land(prepared *roll, candidate RerolledMessage, version *GeneratedSummary) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.lists[prepared.session.ID]
	if entry == nil || entry.pendingToken != prepared.token {
		return false
	}
	candidate.Idx = entry.pendingIdx
	candidate.TargetMessageID = entry.targetID
	entry.items = append(entry.items, candidate)
	if version != nil {
		entry.versions = append(entry.versions, *version)
	}
	entry.pendingToken, entry.pendingIdx = 0, 0
	return true
}

// dropPending：摘掉"正在摇"那个位次；**只剩原文一版 ⇒ 退出模式并清列表**（不 apply ✗）。
func (s *Service) dropPending(prepared *roll) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.lists[prepared.session.ID]
	if entry == nil || entry.pendingToken != prepared.token {
		return
	}
	entry.pendingToken, entry.pendingIdx = 0, 0
	if len(entry.items) <= 1 {
		delete(s.lists, prepared.session.ID)
	}
}

// ── 位次上的操作（切换 / 删除 / 退出）──────────────────────────────────────

// Switch：把第 `idx` 版 apply 回去（消息模式 ⇒ 就地换那条 Message，UUID 不变；摘要模式 ⇒ 就地换那条摘要）。
//
// 先按 kind 查：另一个 kind 正活着 = 本模式没进 ⇒ not_found（与"没进模式"同一套措辞）。
func (s *Service) Switch(sessionID string, kind TargetKind, idx int) (State, error) {
	s.dropIfStale(sessionID) // 目标没了 / 换了 ⇒ 先自愈（口径见 DEFINE.md）
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.lists[sessionID]
	if entry == nil || entry.kind != kind {
		return State{}, notFound("这个会话没在重摇模式里（先 `/reroll` 摇一版）")
	}
	if idx < 1 || idx > len(entry.items) {
		return State{}, invalid("位次 %d 越界：现在共 %d 版（在摇的那一版还不算）", idx, len(entry.items))
	}
	if entry.pendingToken != 0 && idx == entry.pendingIdx {
		return State{}, conflict("第 %d 版还在摇，还没得切（等它出来）", idx)
	}
	if kind == TargetSummary {
		if err := s.applySummary(entry, idx); err != nil {
			return State{}, err
		}
	} else if err := s.apply(entry.items[idx-1]); err != nil {
		return State{}, err
	}
	entry.current = idx
	return s.stateLocked(entry), nil
}

// Delete：删掉第 `idx` 版 —— 后方位次统一 -1，再把"当前那条指向的位置"那版 apply 回去
// （删的是最后一条 ⇒ 前移后那个号不存在 ⇒ **clamp 到新的最后一条**）。
//
// **删到只剩一版 ⇒ 退出模式 + 清列表**，这次**不 apply** ✗ —— 目标保留当前这版
// （口径：不存在"保护原文"，只存在"保护原文的结构体"）。
func (s *Service) Delete(sessionID string, kind TargetKind, idx int) (State, error) {
	s.dropIfStale(sessionID)
	s.mu.Lock()
	state, err, abort := s.deleteLocked(sessionID, kind, idx)
	s.mu.Unlock()
	if abort {
		// 列表已经清了，但那一趟还在摇 ⇒ 别让它白跑（结果回来时没人认领，丢掉了）
		s.Turns.StopReroll(sessionID)
	}
	return state, err
}

// deleteLocked：删的实底（**调用方持锁**）；第三个返回值 = "顺手把在摇的那一趟也按停"。
func (s *Service) deleteLocked(sessionID string, kind TargetKind, idx int) (State, error, bool) {
	entry := s.lists[sessionID]
	if entry == nil || entry.kind != kind {
		return State{}, notFound("这个会话没在重摇模式里（先 `/reroll` 摇一版）"), false
	}
	if idx < 1 || idx > len(entry.items) {
		return State{}, invalid("位次 %d 越界：现在共 %d 版", idx, len(entry.items)), false
	}
	if entry.pendingToken != 0 && idx == entry.pendingIdx {
		return State{}, conflict("第 %d 版还在摇，删不了（等它出来，或先按停止）", idx), false
	}
	entry.items = append(entry.items[:idx-1], entry.items[idx:]...)
	if entry.versions != nil { // 摘要模式：versions 与 items 同位次平行
		entry.versions = append(entry.versions[:idx-1], entry.versions[idx:]...)
	}
	for index := range entry.items { // 位次不是身份：后方统一 -1
		entry.items[index].Idx = index + 1
	}
	// 当前那条的位次：被删的它在前面 ⇒ 一起 -1（还是同一版）；在后面/就是它 ⇒ 号不变
	if entry.current > idx {
		entry.current--
	}
	// 在摇的那一版也跟着前移（它不在这份 `items` 里，但它也占一个位次）
	if entry.pendingToken != 0 && entry.pendingIdx > idx {
		entry.pendingIdx--
	}
	if len(entry.items) <= 1 {
		// 只剩一版 ⇒ 退出重摇模式 + 清列表（**不 apply**：目标保留当前这版）
		abort := entry.pendingToken != 0
		delete(s.lists, sessionID)
		return State{}, nil, abort
	}
	if entry.current > len(entry.items) { // 删的是最后一条 ⇒ clamp
		entry.current = len(entry.items)
	}
	if kind == TargetSummary {
		if err := s.applySummary(entry, entry.current); err != nil {
			return State{}, err, false
		}
	} else if err := s.apply(entry.items[entry.current-1]); err != nil {
		return State{}, err, false
	}
	return s.stateLocked(entry), nil, false
}

// Clear：**退出某一个重摇模式**（`/reroll off` / `DELETE .../reroll`）+ 清列表；在摇的那一趟顺手按停。
//
// 退出**不 apply、也不回原文** ✗ —— 目标保留当前这版（它就是用户最后选中的那一版）。
// 只清**本 kind** 的那份列表：另一个 kind 正活着 = 本模式没进 ⇒ 什么都不做（不去碰它）。
//
// `chat` 在"新消息一到"时调的是 `ClearMessages`（= 只清消息重摇的候选；摘要重摇不受影响）。
func (s *Service) Clear(sessionID string, kind TargetKind) {
	s.mu.Lock()
	entry := s.lists[sessionID]
	if entry == nil || entry.kind != kind {
		s.mu.Unlock()
		return
	}
	delete(s.lists, sessionID)
	s.mu.Unlock()
	// 别让上游白跑完（结果回来时列表已经空了 ⇒ `land` 回 false ⇒ 丢掉）
	s.Turns.StopReroll(sessionID)
}

// ClearMessages：`chat` 的钩子 —— **新消息一到 ⇒ 消息重摇的候选全清**；摘要重摇不受影响。
//
// 语义就是 `Clear(sessionID, TargetMessage)`：候选只属于"当前那条尾巴"。
func (s *Service) ClearMessages(sessionID string) {
	s.Clear(sessionID, TargetMessage)
}

// State：给界面看的那一份（本 kind 没进模式 ⇒ `active: false`）。
func (s *Service) State(sessionID string, kind TargetKind) State {
	s.dropIfStale(sessionID) // 目标没了 / 换了 / 被并走了 ⇒ 候选自然消失（口径见 DEFINE.md）
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.lists[sessionID]
	if entry == nil || entry.kind != kind {
		return State{Active: false}
	}
	return s.stateLocked(entry)
}

// dropIfStale：候选说的那个目标**还是不是可摇的那个**？
//
// 消息模式：被 `/cut` 删掉、或这条会话已经换了尾巴（新消息那条路由 `chat` 直接清，这里是兜底）。
// 摘要模式：目标不在、或**已经有父**（被并走了）⇒ 候选自然消失；**新消息不清摘要模式**。
func (s *Service) dropIfStale(sessionID string) {
	s.mu.Lock()
	entry := s.lists[sessionID]
	var kind TargetKind
	var messageID, summaryID string
	if entry != nil {
		kind, messageID, summaryID = entry.kind, entry.targetID, entry.summaryID
	}
	s.mu.Unlock()
	if entry == nil {
		return
	}
	if kind == TargetSummary {
		if s.summaryStillRoot(sessionID, summaryID) {
			return
		}
		s.Clear(sessionID, kind)
		return
	}
	last, err := s.Store.LastMessageID(sessionID)
	if err != nil || last == messageID {
		return
	}
	s.Clear(sessionID, kind)
}

// summaryStillRoot：那条摘要还在、且还没有父（仍是可重摇的根）。
//
// 读不出来就当作**还在**（别凭一次读失败清掉用户还没选完的候选）。
func (s *Service) summaryStillRoot(sessionID, summaryID string) bool {
	summaries, err := s.Store.ListSummaries(sessionID)
	if err != nil {
		return true
	}
	for _, summary := range summaries {
		if summary.ID == summaryID {
			return summary.ParentSummaryID == nil
		}
	}
	return false
}

// stateLocked：**调用方持锁** —— 位次 + 那一趟的活状态（`running` / `elapsed_ms` / `error` 从 `turn` 读）。
func (s *Service) stateLocked(entry *list) State {
	items := make([]Item, 0, len(entry.items)+1)
	for _, item := range entry.items {
		items = append(items, Item{
			Idx: item.Idx, Preview: previewOf(item.RawText), At: item.At,
			Current: item.Idx == entry.current,
		})
	}
	if entry.pendingToken != 0 {
		items = append(items, Item{Idx: entry.pendingIdx, Pending: true})
	}
	state := State{
		Active: true, TargetKind: entry.kind,
		TargetMessageID: entry.targetID, TargetSummaryID: entry.summaryID,
		Count: len(items), CurrentIdx: entry.current, Items: items,
	}
	if entry.kind == TargetSummary {
		// 摘要模式：目标（根）盖的那段消息区间（1-based）；老数据两端不全 ⇒ 不给这一格
		if entry.fromIdx > 0 {
			from := entry.fromIdx
			state.FromIdx = &from
		}
		if entry.toIdx > 0 {
			to := entry.toIdx
			state.ToIdx = &to
		}
	}
	if status := s.Turns.Status(entry.sessionID).Reroll; status != nil {
		state.Running = status.IsRunning()
		state.ElapsedMS = status.ElapsedMS
		state.Error = status.Error
	}
	return state
}

// apply：把一版候选**换回库里那条 Message** —— 复用"改正文"那条路（`store.ReplaceMessage`），
// 只是把附带信息一起换掉；`message_id` 不变 ⇒ 摘要照旧标 `dirty`、级联只归删除。
func (s *Service) apply(item RerolledMessage) error {
	_, err := s.Store.ReplaceMessage(item.SessionID, item.TargetMessageID, item.RawText, store.MessageMeta{
		Reasoning: item.Reasoning, ReasoningMS: item.ReasoningMS,
		DurationMS: item.DurationMS, Usage: item.Usage,
	})
	if err == nil {
		return nil
	}
	// 那条消息被删了（这期间 `/cut` 了）⇒ 说清楚，别回一个 500
	var invalidError store.InvalidError
	switch {
	case errors.Is(err, store.ErrNotFound):
		return notFound("要换的那条消息不见了（多半被删了）：退出重摇模式就好")
	case errors.As(err, &invalidError):
		return invalid("%s", invalidError.Error())
	}
	return err
}

// applySummary：把摘要模式的第 `idx` 版**换回库里那条摘要** —— `store.ReplaceSummaryText`（就地换正文），
// id / 覆盖区间 / parent / children 一概不动。
//
// `Dirty`：目标 `source_kind=message` ⇒ `false`（刚重摇出来就是干净的）；
// `source_kind=summary` ⇒ **任一孩子脏**（用 `ListSummaries` 现算 —— 父盖着被改过的内容）。
//
// store 报 `ErrNotFound`（那条摘要没了 / 已经被并走了 ⇒ 守卫不符）⇒ 转成 reroll 的 not_found。
func (s *Service) applySummary(entry *list, idx int) error {
	version := entry.versions[idx-1]
	dirty := false
	summaries, err := s.Store.ListSummaries(entry.sessionID)
	if err != nil {
		return err
	}
	for _, summary := range summaries {
		if summary.ID != entry.summaryID {
			continue
		}
		if summary.SourceKind == model.SourceSummaries {
			for _, child := range summaries {
				if child.ParentSummaryID != nil && *child.ParentSummaryID == summary.ID && child.Dirty {
					dirty = true
					break
				}
			}
		}
		break
	}
	err = s.Store.ReplaceSummaryText(model.Summary{
		ID: entry.summaryID, SessionID: entry.sessionID,
		Text: version.Text, Tokens: int64(version.Tokens),
		Provider: version.Provider, Model: version.Model,
		PromptVersion: version.PromptVersion, Usage: version.Usage,
		Dirty: dirty,
	})
	if err == nil {
		return nil
	}
	if errors.Is(err, store.ErrNotFound) {
		return notFound("那条摘要已经不在了（多半被删了，或被并走了）：退出重摇模式就好")
	}
	return err
}

// previewOf：一版候选的一行预览 —— 剔掉 `<state>` 块（那是标签，不是"话"）、压平换行、
// 按**字符**截断（中文按字节截会切出残字，算式只有一处：`model.TitleFrom`）。
func previewOf(text string) string {
	return model.TitleFrom(statelang.Scan(text).Cleaned, previewChars)
}

// shortID：日志与面板上认得出是哪个就够。
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
