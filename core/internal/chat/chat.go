// Package chat：**一轮生成怎么跑**。
//
// 形状（照 `AGENTS.md` 的「一轮生成（202 + 轮询）」）：
//
//	受理（同步）：铸 id → 登记 turn（**唯一的并发闸门**）→ 落用户消息 → 出站定稿
//	生成（后台）：调 `providers` → 增量喂 `turn`（只服务动画）→ **整段拿到才 INSERT** → 起标题
//
// 一条铁律：**整段拿到才 INSERT** —— 停止 / 失败 / 被杀都不留半条，也没有"清理占位"要维护；
// 落库用的 id 是**受理那一刻**就算好、并发给客户端的那个。
//
// 三份状态各管一段（不许互为镜像）：`task` = 身份与生死，`turn` = 流细节（几个字、怎么停），
// 这里 = 一轮怎么跑。
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"microchat/internal/config"
	"microchat/internal/macro"
	"microchat/internal/model"
	"microchat/internal/providers"
	"microchat/internal/state"
	"microchat/internal/statelang"
	"microchat/internal/store"
	"microchat/internal/task"
	"microchat/internal/tavern"
	"microchat/internal/title"
	"microchat/internal/turn"
)

// Service：一轮生成的唯一实现处。
// Compactor：自动压缩要的"压 N 块"那一口 —— **消费方定义接口**（与 reroll 那三个一个套路），
// `compact.Service` 在 main.go 里适配（各包不互相 import ⇒ 不报环）。nil = 没接 ⇒ 自动压缩不跑。
type Compactor interface {
	Compact(ctx context.Context, session model.Session, blocks int) error
}

type Service struct {
	Store *store.Store
	Turns *turn.Registry
	Tasks *task.Registry
	Paths config.Paths
	// Titles：起标题（**只在标题还空着时**自动一次）—— 由 main / 直操模式接上；
	// 没接（比如只跑一轮生成的单测）就只是"没人起名"，不影响这一轮。
	Titles *title.Service
	// Candidates：重摇模式的候选列表（`reroll.Service` 实现它）。**新消息一到就清空消息重摇的候选** ——
	// 候选说的只是"当前路径尾条"的替代版本，一旦往下走了，它们就不再指向那条尾巴了
	// （message 保留当前这版 ✓，候选全删 ✓）。**摘要重摇的候选不受影响** —— 摘要盖的区间已经定了，
	// 再往下说一句也不改变"那条根摘要"（`ClearMessages` 只清消息那一类）。
	//
	// 类型是接口（在下面定义）：`chat` 与 `reroll` 于是**互不依赖** —— 装配与候选各归各的包，
	// 只在这一处点一下。没接（单测 / 直操模式）就是"没人维护候选"，不影响这一轮。
	Candidates CandidateOwner
	// Compactor：自动压缩的那一口（见上面接口）—— main.go 里接上；没接（单测 / 直操模式）
	// 就是"没人自动压"，手动 `/compact` 照旧。
	Compactor Compactor
	// FlowTrace 的最近一次图执行（进程内；重启即失，和 turn/task 一样）。
	flowTraceMu sync.Mutex
	flowTraces  map[string]FlowRunTrace
}

// CandidateOwner：候选列表的持有者（唯一实现是 `reroll.Service`）。
type CandidateOwner interface {
	ClearMessages(sessionID string)
}

func New(st *store.Store, paths config.Paths, turns *turn.Registry, tasks *task.Registry) *Service {
	return &Service{Store: st, Turns: turns, Tasks: tasks, Paths: paths}
}

// 本地假上游的两句话术（都**不联网**）。dummy 那句照旧版逐字 —— 联调与验收靠它认得出。
const (
	dummyReply    = "（测试用空模型）"
	fallbackReply = "未配置模型"
	// 一次用户输入最多几轮客户端工具调用（不含最终 assistant）。
	maxToolRounds = 4
)

// localReply：这一发要不要走本地假上游、吐哪句话（非空 ⇒ 不联网）。
//
// 选后端（哪条渠道来答）**规则只有一处**：`providers.SelectBackend`；这里只把它的结论
// 翻成本地话术 —— 真上游的 `Name` 是渠道的 kind，翻不出话术（回空串 = 真发）。
func localReply(chosen providers.Backend) string {
	switch chosen.Name {
	case providers.BackendDummy:
		return dummyReply
	case providers.BackendFallback:
		return fallbackReply
	}
	return ""
}

// Accepted：受理回执（`POST /sessions/{session_id}/messages` 的 **202** 体）。
//
// **不含回复正文**：回复还在生成。客户端拿 `turn.message_id` 指认它（那会儿还没进库），
// 再按 `turn.phase` 轮询到 `idle` / `error`。
type Accepted struct {
	User    *model.Message   `json:"user,omitempty"`
	Backend string           `json:"backend"`
	Turn    model.TurnStatus `json:"turn"`
}

// Accept：受理一轮 —— 同步做完"落用户消息 + 登记 + 出站定稿"，然后把生成丢给后台。
//
// 顺序是刻意的：**登记（并发闸门）在写用户消息之前**（挤不进来就 409，且一个字节都还没写）；
// 用户消息在后端失败时也留在库里（用户的话不该丢，换个模型接着聊）。
func (s *Service) Accept(session model.Session, content string) (Accepted, error) {
	_, _, providersConfig := s.files()
	// 预留多条 assistant 消息的 id：用户那句在最前、最终回复在最后；中间留给工具轮。
	ids, err := store.MintOrderedIDs(2 + maxToolRounds)
	if err != nil {
		return Accepted{}, err
	}
	userID, replyID := ids[0], ids[len(ids)-1]
	assistantIDs := ids[1 : len(ids)-1]

	// **唯一的并发闸门**：同会话已经在跑 ⇒ 直接拒（不排队）
	token, err := s.Turns.Begin(session.ID, replyID)
	if err != nil {
		return Accepted{}, err
	}

	storedContent := statelang.Scan(content).Cleaned
	user, err := s.Store.InsertMessage(model.Message{
		// 用户 `<state>` 注入一律剔除再入库（只 system prompt 与 assistant 的块算数）。
		ID: userID, SessionID: session.ID, Role: model.RoleUser, RunID: userID,
		Content: storedContent,
	})
	if err != nil {
		s.Turns.Stop(session.ID) // 登记了却写不进去 ⇒ 摘掉，别留下一个假的"在跑"
		return Accepted{}, err
	}
	// **一发出新消息 ⇒ 消息重摇的候选全删**（用户口径：候选只属于"当前这条尾巴"）——
	// message 保留当前这版（候选从没进过库，删掉它们一个字节都不用回退）；摘要重摇的候选不受影响。
	if s.Candidates != nil {
		s.Candidates.ClearMessages(session.ID)
	}
	// 起标题**不在这儿**：它是 title 能力的事（`title.Service.Auto`，在拿到回复之后起）——
	// 这里只落用户那句（用户的话不该丢，换个模型接着聊）。
	accepted, err := s.launch(session, replyID, token, providersConfig, assistantIDs, userID, storedContent)
	if err != nil {
		return Accepted{}, err
	}
	accepted.User = &user
	return accepted, nil
}

// Resend：重发历史 —— **不落新消息**，拿现有历史再跑一轮生成（尾条 user 没回复 / 尾条 assistant 想再要一版）。
//
// 与 Accept 共用 `launch`（出站定稿 + 选后端 + 丢后台）：差别只有"落不落用户那句"。
// 空会话（0 条消息）⇒ InvalidError（没历史发什么，system prompt 自己不算一轮）。
func (s *Service) Resend(session model.Session) (Accepted, error) {
	_, _, providersConfig := s.files()
	messages, err := s.Store.ListMessages(session.ID)
	if err != nil {
		return Accepted{}, err
	}
	if len(messages) == 0 {
		return Accepted{}, store.InvalidError("空会话没有历史可重发：先说一句话")
	}
	ids, err := store.MintOrderedIDs(1 + maxToolRounds)
	if err != nil {
		return Accepted{}, err
	}
	replyID := ids[len(ids)-1]
	assistantIDs := ids[:len(ids)-1]
	// **唯一的并发闸门**：同会话已经在跑 ⇒ 直接拒（不排队，与 Accept 同一把）
	token, err := s.Turns.Begin(session.ID, replyID)
	if err != nil {
		return Accepted{}, err
	}
	lastUser := ""
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == model.RoleUser {
			lastUser = statelang.Scan(messages[index].Content).Cleaned
			break
		}
	}
	return s.launch(session, replyID, token, providersConfig, assistantIDs, replyID, lastUser)
}

// launch：出站定稿 + 选后端 + 丢给后台跑。调用方已登记（Begin 过了）⇒ 失败摘登记。
func (s *Service) launch(session model.Session, replyID string, token uint64, providersConfig config.ProvidersConfig,
	assistantIDs []string, runID, input string) (Accepted, error) {
	// FlowAgent 不经过普通 Chat 的单请求装配：图自己决定每个 LLM 节点的 messages。
	if agent, ok := s.flowAgentOf(session); ok {
		chosen := providers.SelectBackend(session.Provider, session.Model, providersConfig)
		go s.runFlow(session, replyID, token, runID, input, agent, providersConfig, "")
		return Accepted{Backend: chosen.Name, Turn: statusView(s.Turns.Status(session.ID))}, nil
	}
	// 出站**此刻定稿**（受理时捕获的上文）：后台不再重算，期间别人改了库也不影响这一轮
	outgoing, reasoningByID, err := s.outgoingFor(session)
	if err != nil {
		s.Turns.Stop(session.ID)
		return Accepted{}, err
	}
	chosen := providers.SelectBackend(session.Provider, session.Model, providersConfig)
	// 发之前查表定这次说什么话：`model_route` 有行且 api=="openai-responses" ⇒
	// response；没有行时用渠道配置的 protocol 兜底。只覆盖进 wire.Protocol（别改 session、别落库）。
	// 查表键是**渠道 id**（`chosen.Provider.ID`）：`model_route` 是按渠道刷新的，
	// 同一个 vendor 配两条渠道时各有各的表 —— 拿 vendor 查会串台。
	// compact/title/reroll-message 走同一条查表（各调 Complete 之前），见三包对应调用点。
	if model := session.Model; chosen.Name != providers.BackendFallback && chosen.Name != providers.BackendDummy {
		chosen.Provider.Protocol = string(providers.ResolveProtocolFor(s.Store.ModelRoute, chosen.Provider.ID, model, providers.Protocol(chosen.Provider.Protocol)))
	}
	go s.run(session, replyID, token, chosen, outgoing, reasoningByID, time.Now(), assistantIDs, runID)
	return Accepted{
		Backend: chosen.Name,
		Turn:    statusView(s.Turns.Status(session.ID)),
	}, nil
}

// run：后台那半程 —— **整段拿到才 INSERT**（用受理时定好的 id）。
func (s *Service) run(session model.Session, replyID string, token uint64, chosen providers.Backend,
	outgoing []state.Outgoing, reasoningByID map[string]string, started time.Time, assistantIDs []string, runID string) {
	sessionID := session.ID
	// 挂号：前台这一轮也是任务（会调模型的作业必带会话 id ⇒ Begin 自己会断言）
	guard := s.Tasks.Begin(task.KindTurn, &sessionID, "生成 · 会话 "+shortID(sessionID))
	defer guard.Interrupted() // 忘了收 / panic ⇒ 记成"中断"，绝不留僵尸条目
	// 任务面板上的按钮走的也是这条（与 `POST /sessions/{id}/stop` 同一个口 —— 两处一个动作，别分家）。
	guard.Cancellable(func() { s.Turns.Stop(sessionID) })

	ctx, cancel := context.WithCancel(context.Background())
	s.Turns.Attach(sessionID, token, cancel)
	defer cancel()

	var firstTextAt time.Time
	var partialText, partialReasoning strings.Builder
	// 配置 → 线上形状只有这一条路（`FromConfig`）：手搓字面量漏一个字段就是静默失效
	wire := providers.FromConfig(chosen.Provider)
	client := providers.NewClient(wire)
	tools := s.availableTools(session, chosen)
	chatMessages := wireMessages(outgoing, reasoningByID)
	toolOrder := []string{}
	toolEvents := map[string]providers.ToolEvent{}
	assistantMessages := []model.Message{}
	nextAssistant := 0
	var result providers.Result
	var err error
	for round := 0; round <= maxToolRounds; round++ {
		if round > 0 {
			s.Turns.NextStep(sessionID, token)
		}
		result, err = client.Chat(ctx, wire, providers.Request{
			Model:         session.Model,
			Messages:      chatMessages,
			SessionID:     sessionID,
			ThinkingLevel: session.ThinkingLevel,
			Tools:         tools,
			Override:      session.RequestOverrides,
		}, localReply(chosen), func(delta providers.Delta) {
			if delta.Tool != nil {
				s.Turns.SetTool(sessionID, token, delta.Tool.Kind, delta.Tool.State)
				if delta.Tool.ItemID != "" {
					event, exists := toolEvents[delta.Tool.ItemID]
					if !exists {
						toolOrder = append(toolOrder, delta.Tool.ItemID)
						event = *delta.Tool
					} else {
						if delta.Tool.Kind != "" {
							event.Kind = delta.Tool.Kind
						}
						if delta.Tool.State != "" {
							event.State = delta.Tool.State
						}
						if delta.Tool.Query != "" {
							event.Query = delta.Tool.Query
						}
						if len(delta.Tool.Queries) > 0 {
							event.Queries = append([]string{}, delta.Tool.Queries...)
						}
						if len(delta.Tool.Sources) > 0 {
							event.Sources = append([]providers.ToolSource{}, delta.Tool.Sources...)
						}
					}
					toolEvents[delta.Tool.ItemID] = event
				}
				if len(delta.Tool.Sources) > 0 {
					sources := make([]turn.Source, 0, len(delta.Tool.Sources))
					for _, source := range delta.Tool.Sources {
						sources = append(sources, turn.Source{Type: source.Type, URL: source.URL, Title: source.Title})
					}
					s.Turns.AddSources(sessionID, token, sources)
				}
			}
			if delta.Text != "" {
				if firstTextAt.IsZero() {
					firstTextAt = time.Now()
				}
				partialText.WriteString(delta.Text)
				s.Turns.AppendContent(sessionID, token, delta.Text)
			}
			if delta.Reasoning != "" {
				partialReasoning.WriteString(delta.Reasoning)
				s.Turns.AppendReasoning(sessionID, token, delta.Reasoning)
			}
		})
		if err != nil {
			break
		}
		if len(result.ToolCalls) == 0 {
			break
		}
		chatMessages = append(chatMessages, providers.ChatMessage{
			Role: "assistant", Content: result.Text, Reasoning: result.Reasoning,
			ToolCalls: toolCallsJSON(result.ToolCalls),
		})
		roundTools := make([]model.MessageTool, 0, len(result.ToolCalls))
		for _, call := range result.ToolCalls {
			s.Turns.SetTool(sessionID, token, call.Name, "in_progress")
			content, sources, toolErr := runLocalTool(ctx, chosen.Provider, call)
			status := "done"
			if toolErr != nil {
				status = "failed"
				errJSON, _ := json.Marshal(map[string]string{"error": toolErr.Error()})
				content = string(errJSON)
			}
			s.Turns.SetTool(sessionID, token, call.Name, status)
			if len(sources) > 0 {
				turnSources := make([]turn.Source, 0, len(sources))
				for _, source := range sources {
					turnSources = append(turnSources, turn.Source{Type: source.Type, URL: source.URL, Title: source.Title})
				}
				s.Turns.AddSources(sessionID, token, turnSources)
			}
			roundTools = append(roundTools, model.MessageTool{
				Source: "client", ToolName: call.Name, ItemID: call.ID,
				Arguments: call.Arguments, Result: content, Sources: sources, Status: status,
				Queries: localToolQueries(call),
			})
			chatMessages = append(chatMessages, providers.ChatMessage{
				Role: "tool", Content: content, ToolCallID: call.ID, Name: call.Name,
			})
		}
		if nextAssistant >= len(assistantIDs) {
			err = fmt.Errorf("assistant 消息 id 预留不足")
			break
		}
		roundMessage := s.roundMessage(
			session, assistantIDs[nextAssistant], chosen, result, roundTools, toolNames(tools), runID)
		if _, err := s.Store.InsertMessageWithTools(roundMessage, roundMessage.Tools); err != nil {
			s.Turns.Finish(sessionID, token, err.Error())
			guard.Fail(err.Error())
			return
		}
		nextAssistant++
		if round == maxToolRounds {
			err = fmt.Errorf("工具调用超过 %d 轮", maxToolRounds)
			break
		}
	}
	if err != nil {
		if !s.Turns.IsMine(sessionID, token) {
			// 被按停 / 被顶掉：已完成的中途 tool-call 消息已经落库（保留），
			// 当前这条以 canceled 落一条空/半截消息 —— 取消不是"整轮消失"。
			canceled := model.Message{
				ID: replyID, SessionID: sessionID, Role: model.RoleAssistant,
				Kind: model.MessageKindChat, Status: model.MessageStatusCanceled, RunID: runID,
				Content: partialText.String(), DurationMS: ptrInt64(time.Since(started).Milliseconds()),
			}
			if chosen.StoresReasoning() && strings.TrimSpace(partialReasoning.String()) != "" {
				canceled.Reasoning = partialReasoning.String()
			}
			_, _ = s.Store.InsertMessageWithTools(canceled, nil)
			guard.Cancel()
			return
		}
		s.Turns.Finish(sessionID, token, err.Error()) // 进 error 态：界面看得见原因
		guard.Fail(err.Error())
		return
	}
	if !s.Turns.IsMine(sessionID, token) {
		// 令牌对不上 ⇒ 这一轮已经被停掉：结果**直接丢掉**，不许写库
		guard.Cancel()
		return
	}
	kind := model.MessageKindChat
	messageTools := []model.MessageTool{}
	for _, itemID := range toolOrder {
		event := toolEvents[itemID]
		sources := make([]model.MessageToolSource, 0, len(event.Sources))
		for _, source := range event.Sources {
			sources = append(sources, model.MessageToolSource{Type: source.Type, URL: source.URL, Title: source.Title})
		}
		messageTools = append(messageTools, model.MessageTool{
			Source: "server", ToolName: event.Kind, ItemID: event.ItemID,
			Queries: event.Queries, Sources: sources, Status: "done",
		})
	}
	if len(messageTools) > 0 {
		kind = model.MessageKindToolRun
	}
	message := s.assistantMessage(session, replyID, token, chosen, result, firstTextAt, started, kind, toolNames(tools), messageTools, runID)
	assistantMessages = append(assistantMessages, message)
	if _, err := s.Store.InsertMessagesWithTools(assistantMessages); err != nil {
		s.Turns.Finish(sessionID, token, err.Error())
		guard.Fail(err.Error())
		return
	}
	// **拿到回复之后**：起标题（只在标题还空着时一次）。放在 `Finish` **之前**是刻意的 ——
	// 客户端看到 `idle`（据此重拉列表）时名字已经落库，一次刷新就拿到；直操模式同理。
	// 失败 / 关掉只影响名字（`Auto` 自己退回首句截断兜底并记日志），**不改这一轮的结果**。
	if s.Titles != nil {
		s.Titles.Auto(session)
	}
	s.Turns.Finish(sessionID, token, "") // 成功 ⇒ idle（客户端据此一次性重拉消息）
	guard.Succeed()
	// 自动压缩（2026-10-11 用户定）：**翻 idle 之后**再看 —— 压缩是好几个上游调用，
	// 别让界面为它多转圈；它自己挂号（compact 的 Task），在"后台任务"行里看得见。
	if s.Compactor != nil {
		go s.MaybeAutoCompact(session)
	}
}

// localToolQueries：给界面卡片显示的一小段检索词/URL（完整参数仍留 Arguments）。
func localToolQueries(call providers.ToolCall) []string {
	var args struct {
		Query string `json:"query"`
		URL   string `json:"url"`
	}
	if json.Unmarshal([]byte(call.Arguments), &args) != nil {
		return nil
	}
	switch call.Name {
	case "web_search":
		if strings.TrimSpace(args.Query) != "" {
			return []string{strings.TrimSpace(args.Query)}
		}
	case "web_fetch":
		if strings.TrimSpace(args.URL) != "" {
			return []string{strings.TrimSpace(args.URL)}
		}
	}
	return nil
}

// toolCallsJSON：把客户端 function calls 拼成 assistant 消息的 wire 形状。
func toolCallsJSON(calls []providers.ToolCall) json.RawMessage {
	out := make([]map[string]any, 0, len(calls))
	for _, call := range calls {
		out = append(out, map[string]any{
			"id": call.ID, "type": "function",
			"function": map[string]any{"name": call.Name, "arguments": call.Arguments},
		})
	}
	raw, _ := json.Marshal(out)
	return raw
}

// roundMessage：客户端工具循环里的**中间 assistant 消息**（通常只有 tool_calls，
// 正文可能为空）；它和最终回复同属一个 run，工具记录挂在它自己身上。
func (s *Service) roundMessage(session model.Session, id string, chosen providers.Backend,
	result providers.Result, tools []model.MessageTool, snapshot []string, runID string) model.Message {
	message := model.Message{
		ID: id, SessionID: session.ID, Role: model.RoleAssistant,
		Kind: model.MessageKindToolRun, Status: model.MessageStatusDone, RunID: runID,
		ToolsSnapshot: snapshot, Tools: tools, Content: result.Text,
		Usage: result.Usage,
	}
	if chosen.StoresReasoning() && strings.TrimSpace(result.Reasoning) != "" {
		message.Reasoning = result.Reasoning
	}
	return message
}

// assistantMessage：把这一轮的产出拼成要落库的那一行。
//
// 思考**留不留档**由渠道的 `store_reasoning` 决定：它只服务动画与回看，不参与任何计算；
// 关掉它时连 `reasoning_ms` 一起不写（没有"这个思考"可言）。
func (s *Service) assistantMessage(session model.Session, replyID string, token uint64, chosen providers.Backend,
	result providers.Result, firstTextAt time.Time, started time.Time, kind model.MessageKind, tools []string,
	messageTools []model.MessageTool, runID string) model.Message {
	durationMS := time.Since(started).Milliseconds()
	message := model.Message{
		ID: replyID, SessionID: session.ID, Role: model.RoleAssistant,
		Kind: kind, Status: model.MessageStatusDone, RunID: runID, ToolsSnapshot: tools,
		Tools:   messageTools,
		Content: result.Text, DurationMS: &durationMS, Usage: result.Usage,
	}
	if chosen.StoresReasoning() {
		if reasoning := s.Turns.ThinkingOf(session.ID); strings.TrimSpace(reasoning) != "" {
			message.Reasoning = reasoning
			// 思考用时 = 受理 → 第一段正文（没有正文就没有"思考用时"可言）
			if !firstTextAt.IsZero() {
				ms := firstTextAt.Sub(started).Milliseconds()
				message.ReasoningMS = &ms
			}
		}
	}
	return message
}

func ptrInt64(value int64) *int64 { return &value }

// toolNames：请求里工具声明的名字（server 工具用 type，function 工具用 name），
// 存进消息的 tools_snapshot —— 记录"生成时可用什么"，不表示实际调用。
func toolNames(tools []providers.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		name := tool.Type
		if tool.Function != nil && tool.Function.Name != "" {
			name = tool.Function.Name
		}
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

// assemble：历史（+ 可选的"还没进库的那一句"）→ 真会发出去的东西。
//
// **真发（`outgoingFor`）与预演（`PreviewWire`）共用这一处** —— 各写一遍必然漂移，
// 而漂移之后预演说的就不再是"真发会发什么"（接口存在的全部理由就是这句话）。
// 装配本身仍只有一条路：世界状态现演（`state.FromSources`）+ 出站拼装（`state.BuildOutgoing`）。
//
// `pending` 那一项**追加在历史末尾**，就是真发时"用户那句刚落库、`ListMessages` 里排在最后"的位置。
// 它缺 `created_at`：那个字段只进 `/state` 的操作流水（"哪句话带来的状态"），不参与出站装配。
func (s *Service) assemble(session model.Session, pending []model.Message) ([]state.Outgoing, []model.Message, error) {
	messages, err := s.Store.ListMessages(session.ID)
	if err != nil {
		return nil, nil, err
	}
	messages = append(messages, pending...)
	return s.assembleMessages(session, messages)
}

// assembleMessages：**装配的唯一实现**（历史已经是最终那份清单）—— 状态现演 + 出站拼装。
//
// 序号在这儿整段重编一遍（`store.IndexMessages`：对已编过号的那些是幂等的）——
// **"下一个序号是多少"也只有这一处算**，别在别处再写一遍。
func (s *Service) assembleMessages(session model.Session, messages []model.Message) ([]state.Outgoing, []model.Message, error) {
	store.IndexMessages(messages)
	summaries, err := s.Store.ListSummaries(session.ID)
	if err != nil {
		return nil, nil, err
	}
	prompt, _ := s.EffectiveSystemPrompt(session)
	entries, presetActive := s.promptEntries(session, messages)
	if presetActive {
		// 预设是完整的提示词方案：Agent 的 base/段不参与；会话自己的 system_prompt 仍最高。
		prompt = strings.TrimSpace(session.SystemPrompt)
	}
	tables := state.FromSources(session.ID, messages).Tables
	return state.BuildOutgoingWithPromptEntries(prompt, messages, summaries, tables, s.prependState(session), entries), messages, nil
}

// promptEntries：当前会话的提示词计划。
//
// Agent 绑定了 preset_id ⇒ 每次现读原始 JSON → tavern.Parse（不复制条目；预设是唯一来源）。
// 没有绑定 ⇒ 用手工/高级 PromptEntries（空 = 老行为）。
// 第二个返回值 = 预设是否生效；调用方据此决定 Agent base/段要不要让位。
func (s *Service) promptEntries(session model.Session, messages []model.Message) ([]config.PromptEntry, bool) {
	_, agents, _ := s.files()
	agent, ok := agents.Get(session.AgentID)
	if !ok {
		return nil, false
	}
	if strings.TrimSpace(agent.PresetID) == "" {
		return agent.PromptEntries, false
	}
	preset, data, err := s.Store.GetTavernPreset(agent.PresetID)
	if err != nil || preset == nil {
		log.Printf("会话 %s 的 Agent 预设 %q 读不到（回落 Agent 提示词）：%v", shortID(session.ID), agent.PresetID, err)
		return agent.PromptEntries, false
	}
	// 真正进模型的不是原始 entries，而是执行 setvar/getvar/trim/注释后留下的那 24 条。
	// lastUserMessage 用这一轮的最后一条 user（pending 已包含），于是 User_Input 段也是活的。
	var lastUser *string
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == model.RoleUser {
			text := strings.TrimSpace(statelang.Scan(messages[index].Content).Cleaned)
			lastUser = &text
			break
		}
	}
	extracted, err := tavern.Extract(data, tavern.ExtractOptions{
		Values: s.MacroValues(session), LastUserMessage: lastUser,
	})
	if err != nil {
		log.Printf("会话 %s 的 Agent 预设 %q 求值失败（回落 Agent 提示词）：%v", shortID(session.ID), agent.PresetID, err)
		return agent.PromptEntries, false
	}
	entries := make([]config.PromptEntry, 0, len(extracted.Entries))
	for _, entry := range extracted.Entries {
		entries = append(entries, config.PromptEntry{
			ID: entry.Identifier, Name: entry.Name, Content: entry.Content, Role: entry.Role,
			Position: entry.Position, Depth: entry.Depth, Order: entry.Order, SystemPrompt: entry.SystemPrompt,
		})
	}
	return entries, true
}

// prependState：这个会话的 Agent 开了 `prepend_state` 才往用户句首贴 `<current_state>`。
// 缺省关（只有 RP 这类 Agent 需要；`nil` = 没写 = 关）。
func (s *Service) prependState(session model.Session) bool {
	_, agents, _ := s.files()
	agent, ok := agents.Get(session.AgentID)
	return ok && agent.PrependState != nil && *agent.PrependState
}

// RerollMessages：**重摇那一趟**要发给上游的消息 —— 把最后那条 assistant 回复**摘掉**之后，
// 与一轮对话**同一份装配**（`assembleMessages`）。
//
// 为什么不另写一份拼装：重摇不是另一个功能，它就是"把最后那条回复重来一遍"——
// 世界状态现演（`FromSources`）与出站拼装（`BuildOutgoing`）一个字都不该不同。
// 唯一的差别是**那条回复不在历史里**（它正文里的 `<state>` 块当然也跟着不算数 ——
// 世界状态本来就跟着候选变，这正是想要的效果）。
//
// 摘掉的**永远是最后一条**（重摇只针对"当前路径上的最后一条 assistant 消息"）；
// 尾条不是 assistant ⇒ 报错（那是"重发"，不是重摇）。
func (s *Service) RerollMessages(session model.Session) ([]providers.ChatMessage, error) {
	messages, err := s.Store.ListMessages(session.ID)
	if err != nil {
		return nil, err
	}
	if len(messages) == 0 || messages[len(messages)-1].Role != model.RoleAssistant {
		return nil, errors.New("这条会话的最后一条不是 assistant 回复，没有什么可重摇的")
	}
	outgoing, kept, err := s.assembleMessages(session, messages[:len(messages)-1])
	if err != nil {
		return nil, err
	}
	return wireMessages(outgoing, s.reasoningTable(kept)), nil
}

// outgoingFor：受理那一刻把"真会发出去的东西"定稿。
//
// 拼装**只有一条路**（`state.BuildOutgoing`，经 `assemble`）；这里额外做的是按需把 assistant 消息当时的
// **思考**按 id 收成一张表 —— 那是回传上游用的 replay metadata（Pi 的原话：不回传，多轮推理就断了），
// 正文一个字都不动。收不收由 `chat.replay_reasoning` 定（缺省关：省上下文；思考仍落库可看）。
func (s *Service) outgoingFor(session model.Session) ([]state.Outgoing, map[string]string, error) {
	outgoing, messages, err := s.assemble(session, nil)
	if err != nil {
		return nil, nil, err
	}
	return s.renderMacros(session, outgoing), s.reasoningTable(messages), nil
}

// MacroValues：当前会话可用的**只读替换宏**的值（user_name / char_name / model …）。
//
// 与 `<state>` 完全分开：宏没有 setter、没有作用域、没有历史；只在这里供出站/预览查表。
func (s *Service) MacroValues(session model.Session) map[string]string {
	prefs, err := config.LoadPrefs(s.Paths)
	if err != nil {
		prefs = config.DefaultPrefs()
	}
	_, agents, _ := s.files()
	agentName := ""
	if agent, ok := agents.Get(session.AgentID); ok {
		agentName = agent.Name
		if strings.TrimSpace(agentName) == "" {
			agentName = agent.ID
		}
	}
	return macro.Values(prefs.UserName, agentName, session.Model)
}

// renderMacros：**只在这里**（以及 ContextUsage）把出站文本里的宏换成值。
// 存库、接口回包、前端编辑框都保持 `{{...}}` 原文 —— 这是“原文进库”的唯一口径。
func (s *Service) renderMacros(session model.Session, outgoing []state.Outgoing) []state.Outgoing {
	values := s.MacroValues(session)
	for index := range outgoing {
		outgoing[index].Content = macro.Render(outgoing[index].Content, values)
	}
	return outgoing
}

// reasoningTable：思考回传表。`chat.replay_reasoning` 开才收，否则空表（`wireMessages` 贴不上思考）。
// 每次现读配置（与 `files()` 同口径，不缓存）⇒ 改完即时生效，不用重启。
func (s *Service) reasoningTable(messages []model.Message) map[string]string {
	reasoningByID := map[string]string{}
	chatConfig, _, _, err := config.Load(s.Paths)
	if err == nil && chatConfig.ReplayReasoning != nil && *chatConfig.ReplayReasoning {
		for _, message := range messages {
			if message.Reasoning != "" {
				reasoningByID[message.ID] = message.Reasoning
			}
		}
	}
	return reasoningByID
}

// PreviewWire："把这句发出去"会拼出的**真请求** —— 同一段装配 + 同一段 Build，不发。
//
// 与真发（`Accept` → `outgoingFor` → `run` → `Client.Chat` → `Build`）共用每一段：
// `assemble`（含 pending 追尾）→ `wireMessages`（role/content + 思考回填）→
// 选后端 + `ResolveProtocol` 查表 → `providers.Build`。`Build` 里 `SessionID` 必填的
// 断言同样生效（缺了在这里就炸，不会等发出去被上游拒）。
//
// 回的是 `providers.LastPayload` 同形状（method/url/headers/体；密钥打码）——
// 与 (a) `GET /debug/last-payload` 同一支笔（`Snapshot`），两份对得上才叫调试可靠。
func (s *Service) PreviewWire(session model.Session, content string) (providers.LastPayload, error) {
	_, agents, providersConfig := s.files()
	// 合成消息的 id 只用来**定序**（`IndexMessages` 按 id 排）：`preview-1` < `preview-2`。
	pending := []model.Message{{ID: "preview-2", SessionID: session.ID, Role: model.RoleUser, Content: content}}
	// 草稿（这条会话**还不存在**）：`POST /sessions` 会把 agent 的"首段对话"插成第一条 assistant ——
	// 预演必须把它算进去，不然"预演 = 真发"这条不变量当场破（真发那条会多出一条 assistant）。
	// 已存在的会话不用模拟：首段已经在库里了。
	if existing, err := s.Store.GetSession(session.ID); err == nil && existing == nil {
		if first := agents.FirstMessageOf(session.AgentID); first != "" {
			greeting := model.Message{ID: "preview-1", SessionID: session.ID, Role: model.RoleAssistant, Content: first}
			pending = append([]model.Message{greeting}, pending...)
		}
	}
	outgoing, messages, err := s.assemble(session, pending)
	if err != nil {
		return providers.LastPayload{}, err
	}
	return s.buildWire(session, s.renderMacros(session, outgoing), s.reasoningTable(messages), providersConfig)
}

// PreviewHistoryWire："空输入点发送"会发出去的真请求 —— 现有历史、不追新句。
// 与 `Resend → launch → outgoingFor` 同一段装配（`assemble(session, nil)`），只算不写。
// 空会话（0 条消息）⇒ 与 Resend 同错（InvalidError），前端右栏保持空话。
func (s *Service) PreviewHistoryWire(session model.Session) (providers.LastPayload, error) {
	messages, err := s.Store.ListMessages(session.ID)
	if err != nil {
		return providers.LastPayload{}, err
	}
	if len(messages) == 0 {
		return providers.LastPayload{}, store.InvalidError("空会话没有历史可重发：先说一句话")
	}
	_, _, providersConfig := s.files()
	outgoing, reasoningByID, err := s.outgoingFor(session)
	if err != nil {
		return providers.LastPayload{}, err
	}
	return s.buildWire(session, outgoing, reasoningByID, providersConfig)
}

// buildWire：出站 → 选后端 → Build → 快照。PreviewWire 与 PreviewHistoryWire 共用。
func (s *Service) buildWire(session model.Session, outgoing []state.Outgoing, reasoningByID map[string]string, providersConfig config.ProvidersConfig) (providers.LastPayload, error) {
	chosen := providers.SelectBackend(session.Provider, session.Model, providersConfig)
	if model := session.Model; chosen.Name != providers.BackendFallback && chosen.Name != providers.BackendDummy {
		chosen.Provider.Protocol = string(providers.ResolveProtocolFor(s.Store.ModelRoute, chosen.Provider.ID, model, providers.Protocol(chosen.Provider.Protocol)))
	}
	wire := providers.FromConfig(chosen.Provider)
	request, err := providers.Build(wire, providers.Request{
		Model:         session.Model,
		Messages:      wireMessages(outgoing, reasoningByID),
		SessionID:     session.ID,
		ThinkingLevel: session.ThinkingLevel,
		Tools:         s.availableTools(session, chosen),
		Override:      session.RequestOverrides,
		Stream:        true,
	})
	if err != nil {
		return providers.LastPayload{}, err
	}
	payload, err := providers.Snapshot(request, time.Now())
	if err != nil {
		return providers.LastPayload{}, err
	}
	payload.Annotations = messageAnnotations(outgoing)
	return payload, nil
}

// messageAnnotations：把 relative prompt / absolute injection 标在 body.messages 的对应下标上。
func messageAnnotations(outgoing []state.Outgoing) []providers.MessageAnnotation {
	out := make([]providers.MessageAnnotation, 0)
	for index, item := range outgoing {
		if item.Type != "prompt" && item.Type != "injection" {
			continue
		}
		annotation := providers.MessageAnnotation{Index: index, Type: item.Type, PromptID: item.PromptID}
		if item.Depth != nil {
			depth := *item.Depth
			annotation.Depth = &depth
		}
		if item.Order != nil {
			order := *item.Order
			annotation.Order = &order
		}
		out = append(out, annotation)
	}
	return out
}

// availableTools：这一轮请求体顶层要带的工具声明。
//
// 只从**模型规则派生画像**取 `server_tools`（provider 侧执行），并扣掉会话里手动关掉的；
// 服务端工具是 Responses 的已知能力，不依赖 `allow_tools`。`local_tools` 目前只是配置候选，
// 还没有工具注册表/执行器 ⇒ 不发，等定义落地后再在这里接 function tool 声明。
func (s *Service) availableTools(session model.Session, chosen providers.Backend) []providers.Tool {
	if chosen.Name == providers.BackendFallback || chosen.Name == providers.BackendDummy {
		return nil
	}
	// DeepSeek 的 Responses web_search 只支持 v4-pro，而且只给 queries 不给 sources；
	// flash 会忽略它并把工具调用当正文吐出来。统一不走服务端工具，搜索走客户端 Search API。
	if chosen.Provider.Vendor == "deepseek" {
		disabled := make(map[string]bool, len(session.NativeToolsDisabled))
		for _, id := range session.NativeToolsDisabled {
			disabled[id] = true
		}
		out := deepseekLocalTools()
		kept := out[:0]
		for _, tool := range out {
			name := tool.Type
			if tool.Function != nil {
				name = tool.Function.Name
			}
			if !disabled[name] {
				kept = append(kept, tool)
			}
		}
		return kept
	}
	// 服务端工具只有 OpenAI Response 协议能表达。
	// xAI 实测：/chat/completions 对 web_search/x_search 直接 422（只认 function/live_search），
	// live_search 又已 410 废弃；/responses 才认 Agent Tools。
	if providers.Protocol(chosen.Provider.Protocol) != providers.ProtocolOpenAIResponse {
		return nil
	}
	cfg, err := config.LoadConfig(s.Paths)
	if err != nil {
		return nil
	}
	profile := cfg.ModelProfileWithRulesFor(
		chosen.Provider.ID, session.Model, chosen.Provider.Protocol,
		config.ModelProfile{}, chosen.Provider.ModelDefineRules)
	disabled := make(map[string]bool, len(session.NativeToolsDisabled))
	for _, id := range session.NativeToolsDisabled {
		disabled[id] = true
	}
	ids := make([]string, 0, len(profile.ServerTools))
	for _, id := range profile.ServerTools {
		if !disabled[id] {
			ids = append(ids, id)
		}
	}
	return providers.ServerTools(ids)
}

// wireMessages：出站消息 → 上游形状（正文照抄，只把思考贴回 assistant 那几条）。
func wireMessages(outgoing []state.Outgoing, reasoningByID map[string]string) []providers.ChatMessage {
	messages := make([]providers.ChatMessage, 0, len(outgoing))
	for _, item := range outgoing {
		message := providers.ChatMessage{Role: string(item.Role), Content: item.Content}
		if item.Role == state.RoleAssistant && item.MessageID != nil {
			message.Reasoning = reasoningByID[*item.MessageID]
		}
		messages = append(messages, message)
	}
	return messages
}

// EffectiveSystemPrompt：生效的系统提示词 —— 会话级覆盖优先，否则用 agent 的，再不然用内置默认那句。
//
// **这就是真会发给模型的那份**：世界状态底子、出站消息、界面显示都用它 ——
// "生效提示词由一处解析"那条规矩的落点就是这里（第二项是它的来源：底子算哪一层由它决定）。
// 解析本身在 `state.ResolveSystemPrompt`（唯一实现，**永不返回空串**）；这里只是把现读的
// agent 配置喂给它，没有第二份算法。
func (s *Service) EffectiveSystemPrompt(session model.Session) (string, state.PromptSource) {
	// 绑定预设时，Agent 的 base/段不参与；会话自己的显式 system_prompt 仍最高。
	if _, presetActive := s.promptEntries(session, nil); presetActive {
		if own := strings.TrimSpace(session.SystemPrompt); own != "" {
			return own, state.PromptFromSession
		}
		return "", state.PromptFromPreset
	}
	_, agents, _ := s.files()
	return state.ResolveSystemPrompt(session, agents)
}

// Status：这一轮现在的状态（进程内的事实；没登记过就是 `idle`）。
func (s *Service) Status(sessionID string) turn.Status { return s.Turns.Status(sessionID) }

// StatusView：会话列表那一栏的形状（`GET /sessions` 每项里的 `turn`）。
func (s *Service) StatusView(sessionID string) model.TurnStatus {
	return statusView(s.Turns.Status(sessionID))
}

// statusView：登记表的状态 → 接口上的形状（`GET /sessions` 那一栏要它）。
func statusView(status turn.Status) model.TurnStatus {
	return model.TurnStatus{
		Phase: model.TurnPhase(status.Phase), MessageID: status.MessageID,
		ElapsedMS: status.ElapsedMS, Chars: status.Chars,
		ThinkingChars: status.ThinkingChars, Error: status.Error,
	}
}

// StreamSince：游标读（正文与思考各一条游标）—— 转发给登记表，server 只跟这一层说话。
func (s *Service) StreamSince(sessionID string, from, thinkFrom int) turn.StreamSlice {
	return s.Turns.StreamSince(sessionID, from, thinkFrom)
}

// Stop：按停这一轮（摘登记 + abort 后台任务）。**幂等**：没在跑也回 false。
func (s *Service) Stop(sessionID string) bool { return s.Turns.Stop(sessionID) }

// files：受理 / 生成时读一遍配置。
//
// 每次现读（不缓存）⇒ 改 `providers.json` / `agents.json` 立即生效，不用重启（与旧版一致）。
// 读坏了就退回代码里的默认值：配置文件本来就可缺失，别让一轮生成因此失败。
func (s *Service) files() (config.ChatConfig, config.AgentsConfig, config.ProvidersConfig) {
	chatConfig, agents, providersConfig, err := config.Load(s.Paths)
	if err != nil {
		return config.DefaultChat(), agents, providersConfig
	}
	return chatConfig, agents, providersConfig
}

// shortID：日志与面板上认得出是哪个就够。
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
