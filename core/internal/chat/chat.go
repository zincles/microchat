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
	"strings"
	"time"

	"microchat/internal/config"
	"microchat/internal/model"
	"microchat/internal/providers"
	"microchat/internal/state"
	"microchat/internal/store"
	"microchat/internal/task"
	"microchat/internal/title"
	"microchat/internal/turn"
)

// Service：一轮生成的唯一实现处。
type Service struct {
	Store *store.Store
	Turns *turn.Registry
	Tasks *task.Registry
	Paths config.Paths
	// Titles：起标题（**只在标题还空着时**自动一次）—— 由 main / 直操模式接上；
	// 没接（比如只跑一轮生成的单测）就只是"没人起名"，不影响这一轮。
	Titles *title.Service
}

func New(st *store.Store, paths config.Paths, turns *turn.Registry, tasks *task.Registry) *Service {
	return &Service{Store: st, Turns: turns, Tasks: tasks, Paths: paths}
}

// 本地假上游的两句话术（都**不联网**）。dummy 那句照旧版逐字 —— 联调与验收靠它认得出。
const (
	dummyReply    = "（测试用空模型）"
	fallbackReply = "未配置模型"
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
	// 两个 id 一口气铸完**排序后**发：用户那句必须在回复前面（线性会话的顺序就是 id）
	ids, err := store.MintOrderedIDs(2)
	if err != nil {
		return Accepted{}, err
	}
	userID, replyID := ids[0], ids[1]

	// **唯一的并发闸门**：同会话已经在跑 ⇒ 直接拒（不排队）
	token, err := s.Turns.Begin(session.ID, replyID)
	if err != nil {
		return Accepted{}, err
	}

	user, err := s.Store.InsertMessage(model.Message{
		ID: userID, SessionID: session.ID, Role: model.RoleUser, Content: content,
	})
	if err != nil {
		s.Turns.Stop(session.ID) // 登记了却写不进去 ⇒ 摘掉，别留下一个假的"在跑"
		return Accepted{}, err
	}
	// 起标题**不在这儿**：它是 title 能力的事（`title.Service.Auto`，在拿到回复之后起）——
	// 这里只落用户那句（用户的话不该丢，换个模型接着聊）。
	// 出站**此刻定稿**（受理时捕获的上文）：后台不再重算，期间别人改了库也不影响这一轮
	outgoing, reasoningByID, err := s.outgoingFor(session)
	if err != nil {
		s.Turns.Stop(session.ID)
		return Accepted{}, err
	}
	chosen := providers.SelectBackend(session.Provider, session.Model, providersConfig)
	go s.run(session, replyID, token, chosen, outgoing, reasoningByID, time.Now())
	return Accepted{
		User:    &user,
		Backend: chosen.Name,
		Turn:    statusView(s.Turns.Status(session.ID)),
	}, nil
}

// run：后台那半程 —— **整段拿到才 INSERT**（用受理时定好的 id）。
func (s *Service) run(session model.Session, replyID string, token uint64, chosen providers.Backend,
	outgoing []state.Outgoing, reasoningByID map[string]string, started time.Time) {
	sessionID := session.ID
	// 挂号：前台这一轮也是任务（会调模型的作业必带会话 id ⇒ Begin 自己会断言）
	guard := s.Tasks.Begin(task.KindTurn, &sessionID, "生成 · 会话 "+shortID(sessionID))
	defer guard.Interrupted() // 忘了收 / panic ⇒ 记成"中断"，绝不留僵尸条目

	ctx, cancel := context.WithCancel(context.Background())
	s.Turns.Attach(sessionID, token, cancel)
	defer cancel()

	var firstTextAt time.Time
	// 配置 → 线上形状只有这一条路（`FromConfig`）：手搓字面量漏一个字段就是静默失效
	wire := providers.FromConfig(chosen.Provider)
	result, err := providers.NewClient(wire).Chat(ctx, wire, providers.Request{
		Model:     session.Model,
		Messages:  wireMessages(outgoing, reasoningByID),
		SessionID: sessionID,
	}, localReply(chosen), func(delta providers.Delta) {
		if delta.Text != "" {
			if firstTextAt.IsZero() {
				firstTextAt = time.Now()
			}
			s.Turns.AppendContent(sessionID, token, delta.Text)
		}
		if delta.Reasoning != "" {
			s.Turns.AppendReasoning(sessionID, token, delta.Reasoning)
		}
	})
	if err != nil {
		if !s.Turns.IsMine(sessionID, token) {
			// 被按停 / 被顶掉：**正常操作**，不是失败；库里本来就没有东西要收拾
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
	message := s.assistantMessage(session, replyID, token, chosen, result, firstTextAt, started)
	if _, err := s.Store.InsertMessage(message); err != nil {
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
}

// assistantMessage：把这一轮的产出拼成要落库的那一行。
//
// 思考**留不留档**由渠道的 `store_reasoning` 决定：它只服务动画与回看，不参与任何计算；
// 关掉它时连 `reasoning_ms` 一起不写（没有"这个思考"可言）。
func (s *Service) assistantMessage(session model.Session, replyID string, token uint64, chosen providers.Backend,
	result providers.Result, firstTextAt time.Time, started time.Time) model.Message {
	durationMS := time.Since(started).Milliseconds()
	message := model.Message{
		ID: replyID, SessionID: session.ID, Role: model.RoleAssistant,
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

// assemble：历史（+ 可选的"还没进库的那一句"）→ 真会发出去的东西。
//
// **真发（`outgoingFor`）与预演（`OutgoingWithPending`）共用这一处** —— 各写一遍必然漂移，
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
	// 待发那句也要有个号：它就是"**下一条**"（现有最大 + 1）。序号只有一处算（`store.IndexMessages`）
	// ⇒ 这里整段重编一遍（对已编过号的那些是幂等的），而不是在别处再写一遍"下一个是多少"。
	store.IndexMessages(messages)
	summaries, err := s.Store.ListSummaries(session.ID)
	if err != nil {
		return nil, nil, err
	}
	prompt, source := s.EffectiveSystemPrompt(session)
	tables := state.FromSources(session.ID, prompt, source, messages).Tables
	return state.BuildOutgoing(prompt, messages, summaries, tables), messages, nil
}

// outgoingFor：受理那一刻把"真会发出去的东西"定稿。
//
// 拼装**只有一条路**（`state.BuildOutgoing`，经 `assemble`）；这里额外做的是把 assistant 消息当时的
// **思考**按 id 收成一张表 —— 那是回传上游用的 replay metadata（Pi 的原话：不回传，多轮推理就断了），
// 正文一个字都不动。
func (s *Service) outgoingFor(session model.Session) ([]state.Outgoing, map[string]string, error) {
	outgoing, messages, err := s.assemble(session, nil)
	if err != nil {
		return nil, nil, err
	}
	reasoningByID := map[string]string{}
	for _, message := range messages {
		if message.Reasoning != "" {
			reasoningByID[message.ID] = message.Reasoning
		}
	}
	return outgoing, reasoningByID, nil
}

// OutgoingWithPending：**把待发的那一句追加进去之后**，真会发出去的东西（`POST /outgoing`）。
//
// **只算不写**：不落库、不动任何状态。与真发那一轮（`outgoingFor`）共用同一段装配（`assemble`）——
// 真发时是"先 INSERT 再 ListMessages（那句就在里面）"，这里是"ListMessages 之后把那句追在末尾"，
// 落到 `FromSources` / `BuildOutgoing` 眼里是**同一回事** ✓（拼法没有第二种）。
//
// 为什么必须重新走一遍装配（而不是"(b) + 一条消息"）：待发那句里可能带 `<state>` 块 ⇒
// 它会改变**注入系统提示词的那张状态表** ⇒ 连第一条 system 都不一样。
//
// 那条 `pending` 消息的 id 是**预测值**（真发时另铸一个）：给出去只为让这一项与 (b) 里那些
// message 项**逐字段同形状**；它不指向库里的任何东西，别拿它去查消息 —— 所以那一项**带 `pending: true`**，
// 客户端一眼看得出"这条还没进库"（同形状，但不会被误会成能查的 id）。
func (s *Service) OutgoingWithPending(session model.Session, content string) ([]state.Outgoing, error) {
	ids, err := store.MintOrderedIDs(1)
	if err != nil {
		return nil, err
	}
	pending := model.Message{ID: ids[0], SessionID: session.ID, Role: model.RoleUser, Content: content}
	outgoing, _, err := s.assemble(session, []model.Message{pending})
	if err != nil {
		return nil, err
	}
	for index := range outgoing {
		if outgoing[index].MessageID != nil && *outgoing[index].MessageID == pending.ID {
			outgoing[index].Pending = true
		}
	}
	return outgoing, nil
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
