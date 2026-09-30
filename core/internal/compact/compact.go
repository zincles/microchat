// Package compact：**摘要的唯一入口** —— 把一段对话收成一条摘要。
//
// 机制与策略两层（口径见 `AGENTS.md`「摘要 / 压缩的设计」与「后台任务 / 压缩 / 能力」）：
//
//	机制 = 给它一段（两端用 message id 指）→ 拼材料 → 叫 compact 能力 → 过五条验证 → 单事务落库；
//	策略 = "从第一条没被覆盖的消息起，取最老的 N 个**已闭合块**"（手动按钮的语义）。
//
// 四条纪律：
//
//  1. **只往 `summaries` 插行**：`messages` 上唯一允许被压缩改的是那一段的 `summary_id`
//     —— 绝不插 / 改 / 删正文（正文是存档）；
//  2. **不许覆盖已压缩的区间**：区间里有消息已经挂在摘要上 ⇒ 报错（不在老区间上盖第二层）；
//  3. **取料在锁里、调用在锁外、落库再进锁**：绝不跨网络调用持锁（`store` 那把锁是全局的）；
//  4. **失败不阻塞**：失败就把这一次报失败（带原因），会话一个字节都不动；**重试由调用方决定**，
//     这里自己绝不重试。
//
// 谁也**不许**绕过这里自己拼摘要请求。
package compact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"microchat/internal/abilities"
	"microchat/internal/blocks"
	"microchat/internal/config"
	"microchat/internal/model"
	"microchat/internal/providers"
	"microchat/internal/state"
	"microchat/internal/statelang"
	"microchat/internal/store"
	"microchat/internal/task"
	"microchat/internal/turn"
)

// materialHeader：材料那一段的抬头（让模型一眼知道下面是什么）。
const materialHeader = "【要压缩的这段对话】\n"

// stateHeader：材料后面附的那份状态的抬头 —— **口气定死**：程序事实、仅供参考、不要写进梗概。
//
// 它是 `at_idx` 那条链算出来的（**当前的**生效提示词打底 + 正文 fold 到区间末 ⇒ **不是快照**）：
// 摘要本身**不存状态**（状态是端点的属性，存进去就是第二个真相来源），但"这一段结束时是什么样"
// 对写梗概有用 ⇒ 作为**程序事实**摆在材料里，并明说别抄进梗概。
const stateHeader = "【程序·状态（截至这段末尾；程序事实，仅供参考，不要写进梗概）】\n"

// dummyReply：本地假上游（`kind: dummy`）吐的那句摘要 —— 确定性、不联网，一眼认得出是假的。
const dummyReply = "（测试用假摘要）"

// compactTimeout：**压缩这一趟子调用的上限** —— 60 秒。
//
// 与 title 的 10s 是同一个理由，只是压缩的材料大、给得宽些：渠道的 `timeouts.total_seconds`
// 缺省 300s，上游"接了不回"时这一趟会一直挂着，`turn` 的压缩状态就一直是 `running`
// （界面转圈停不下来）。到点即撤 ⇒ 这一次报失败（带原因）、会话一个字节都不动（失败不阻塞）。
//
// 取 60s 而不是沿用渠道默认：压缩是**后台**作业，卡住的是"这一趟"而不是某一轮的可见性；
// 但后台作业也不该无限期占着 `turn` 的那一档状态。**只加给辅助调用**（compact / title）。
const compactTimeout = 60 * time.Second

// 压不动 / 压不了时的口径：**明说**，不静默降级（口径见 `AGENTS.md`：只有 Compact，没有"丢"）。

// Request：一次压缩要压哪儿 —— **两种给法二选一**。
//
//	Blocks：按策略取最老的 N 个已闭合块（0 = 用 `config.json` 的 `compact_blocks`）；
//	Begin/End：直接点名一段区间（两端都是 **message id**）。
type Request struct {
	Blocks int
	Begin  string
	End    string
}

// ByRange：这次是按区间来的（不是按块数）。
func (r Request) ByRange() bool { return r.Begin != "" || r.End != "" }

// Result：一次压缩的结局（`-debug compact` 直接打它）。
type Result struct {
	SessionID      string          `json:"session_id"`
	BeginMessageID string          `json:"begin_message_id"`
	EndMessageID   string          `json:"end_message_id"`
	Blocks         int             `json:"blocks"`
	Messages       int             `json:"messages"`
	SummaryID      string          `json:"summary_id"`
	PromptVersion  int64           `json:"prompt_version"`
	Text           string          `json:"text"`
	Provider       string          `json:"provider"`
	Model          string          `json:"model"`
	Usage          json.RawMessage `json:"usage"`
}

// Error：带 code 的失败 —— `code` 与 HTTP 错误体同一套词（client 按 code 分支，别匹配文案）。
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

// ErrorCode：给 server / `-debug` 这两条出口读的（它们各自翻成 HTTP 状态码 / 打印）。
func (e *Error) ErrorCode() string { return e.Code }

// invalid：请求不合规矩（HTTP 层 400）。
func invalid(format string, args ...any) error {
	return &Error{Code: "invalid", Message: fmt.Sprintf(format, args...)}
}

// Service：压缩的唯一实现处。
type Service struct {
	Store *store.Store
	Paths config.Paths
	Turns *turn.Registry
	Tasks *task.Registry
}

func New(st *store.Store, paths config.Paths, turns *turn.Registry, tasks *task.Registry) *Service {
	return &Service{Store: st, Paths: paths, Turns: turns, Tasks: tasks}
}

// ── 两种入口 ────────────────────────────────────────────────────────────────

// Start：**受理**一次压缩（先校验、再挂号，回 202 那一份状态）。
//
// 校验（能力开关、区间、后端能不能发）全在**返回 202 之前**做完 ⇒ 这些错当场回给调用方；
// 之后才是后台那半程（调上游 + 落库），失败落在 `turn` 的压缩状态里（`state: error` + 原因）。
func (s *Service) Start(session model.Session, req Request) (turn.CompactStatus, error) {
	prepared, err := s.prepare(session, req)
	if err != nil {
		return turn.CompactStatus{}, err
	}
	status, err := s.Turns.BeginCompact(session.ID, prepared.requested, time.Now().UnixMilli())
	if err != nil {
		return turn.CompactStatus{}, busy()
	}
	go func() {
		// 辅助调用只给这一趟短超时（理由见 `compactTimeout`）：到点即撤 ⇒ 这一次报失败、会话不动。
		ctx, cancel := context.WithTimeout(context.Background(), compactTimeout)
		defer cancel()
		_, runErr := s.execute(ctx, prepared)
		s.finish(session.ID, runErr)
	}()
	return *status, nil
}

// Compact：**同步跑完一次**（直操模式用它）—— 校验 → 挂号 → 调 → 落库 → 收尾，全在这一趟里。
func (s *Service) Compact(ctx context.Context, session model.Session, req Request) (Result, error) {
	prepared, err := s.prepare(session, req)
	if err != nil {
		return Result{}, err
	}
	if _, err := s.Turns.BeginCompact(session.ID, prepared.requested, time.Now().UnixMilli()); err != nil {
		return Result{}, busy()
	}
	result, err := s.execute(ctx, prepared)
	s.finish(session.ID, err)
	return result, err
}

// finish：把这一次的结局写进 `turn` 的压缩状态（界面看到的"压缩中 / 压好了 / 失败了"）。
func (s *Service) finish(sessionID string, runErr error) {
	if runErr == nil {
		return // 成功那条路由 `execute` 自己收（它手里有 summary_id 与块数）
	}
	s.Turns.FinishCompact(sessionID, "error", "", runErr.Error(), 0)
}

// busy：同一个会话已经有一次压缩在跑（**不排队**：排队会让"点了按钮却什么都没发生"难以解释）。
func busy() error {
	return &Error{Code: "conflict", Message: "这个会话已经有一次压缩在跑了，等它跑完"}
}

// ── 取料：能力 + 区间（都在"调用之前"做完）────────────────────────────────────

// prepared：受理时就把料取好、把闸过掉的那一份。
//
// **取料在锁里、调用在锁外、落库再进锁** ⇒ 这一段（消息快照 + 生效模板 + 后端）拿着走，
// 调上游期间不再碰库；真落库时由 `store.RecordSummary` 在事务里再核一遍区间。
type prepared struct {
	session  model.Session
	channel  config.Provider // 生效渠道（能力的 provider 覆盖优先，否则会话的）
	model    string          // 生效模型
	template string          // 生效模板（进 prompt_version）
	// local：本地假上游要吐的那句（非空 ⇒ 不联网，dummy 渠道走它）。
	local string
	span  span
	// stateSection：材料里附的那份"算到区间末的状态"（渲染好的文本；空 = 一个变量都没有 ⇒ 不附）。
	// 取料时就算好（那时手里有消息快照与生效提示词），调用期间不再碰库。
	stateSection string
	// requested：面板上"请求压几个块"（区间入口 = 区间里实际几个块）。
	requested int
}

// span：这一次压的区间 —— 下标（含）+ 块数 + 那一批消息的快照。
type span struct {
	Begin    int
	End      int
	Blocks   int
	Messages []model.Message
}

func (s *Service) prepare(session model.Session, req Request) (*prepared, error) {
	chatConfig, agents, providersConfig, err := config.Load(s.Paths)
	if err != nil {
		// 读不出来就说读不出来：能力开关的默认是"全开"，静默按默认跑会让"配了要关"的人以为关上了
		return nil, &Error{Code: "internal", Message: "读配置失败：" + err.Error()}
	}
	agent, _ := agents.Resolve(session.AgentID)
	setting := abilities.Resolve(agent, abilities.Compact)
	if !setting.Enabled {
		// **明确拒绝**，不是偷偷降级（`AGENTS.md`：关掉"总结"就该做不了压缩）
		return nil, invalid("会话 %s 的 agent（%s）关掉了 compact 能力（abilities.compact.enabled = false）："+
			"压缩不做就是不做，不会降级执行", shortID(session.ID), agentLabel(agent))
	}

	messages, err := s.Store.ListMessages(session.ID)
	if err != nil {
		return nil, err
	}
	resolved, err := resolveSpan(messages, req, chatConfig.CompactBlocks)
	if err != nil {
		return nil, err
	}

	// 生效渠道 / 模型：能力上填了就用它，没填就骑会话的（辅助调用本来就要同一个会话）
	providerID, modelID := session.Provider, session.Model
	if setting.Provider != "" {
		providerID = setting.Provider
	}
	if setting.Model != "" {
		modelID = setting.Model
	}
	backend := providers.SelectBackend(providerID, modelID, providersConfig)
	if backend.Name == providers.BackendFallback {
		return nil, invalid("这条会话没有可用的模型（渠道 %q 不在 providers.json 里，或模型名为空）："+
			"压缩要真调一次上游 —— 先配好渠道，或把这条会话切到 dummy", providerID)
	}
	local := ""
	if backend.Name == providers.BackendDummy {
		local = dummyReply
	}
	return &prepared{
		session: session, channel: backend.Provider, model: modelID,
		// 生效模板只有一处算式（abilities.Template）：agent 覆盖 ?: 代码里的默认
		template: abilities.Template(setting, abilities.Compact),
		local:    local,
		span:     resolved,
		// 材料里附的那份状态：**at_idx 那条链**（`state.StateAt`）—— 底子是**当前**的生效提示词、
		// 正文 fold 到区间末（`End` 是下标 ⇒ 序号 = End+1）。取料在锁里，所以在这儿算。
		stateSection: stateSectionAt(session, agents, messages, resolved.End+1),
		requested:    requestedBlocks(req, resolved, chatConfig.CompactBlocks),
	}, nil
}

// stateSectionAt：材料里那份状态的文本（**算到 `at` 条为止**）—— 没有变量可报就回空串。
//
// 拿的是 `at_idx` 同一条链（`state.StateAt` ⇒ `FromSources`），渲染也走唯一的 `state.RenderTable`
// ⇒ 与出站注入的那张表长得一模一样。空（一个变量都没有）⇒ 不附：材料里多一段空话纯属噪音。
func stateSectionAt(session model.Session, agents config.AgentsConfig, messages []model.Message, at int) string {
	// 底子用**当前**的生效提示词（三级解析只有一处：`state.ResolveSystemPrompt`）
	systemPrompt, source := state.ResolveSystemPrompt(session, agents)
	tables := state.StateAt(session.ID, systemPrompt, source, messages, at).Tables
	rendered, ok := state.RenderTable(tables)
	if !ok {
		return ""
	}
	return rendered
}

// requestedBlocks：面板上那个"压 N 个块"（区间入口就是区间里实际几块）。
func requestedBlocks(req Request, resolved span, defaultBlocks int) int {
	if req.ByRange() {
		return resolved.Blocks
	}
	if req.Blocks > 0 {
		return req.Blocks
	}
	return defaultBlocks
}

// ── 区间：两种给法 → 同一份 span ───────────────────────────────────────────

// resolveSpan：把两种给法归一成一段（**闸都在这里**：区间自洽 + 不许覆盖已压缩的区间）。
func resolveSpan(messages []model.Message, req Request, defaultBlocks int) (span, error) {
	if len(messages) == 0 {
		return span{}, invalid("这条会话还没有消息，没有可压的")
	}
	all := blocks.Split(messages)
	if req.ByRange() {
		return rangeSpan(messages, all, req)
	}
	return strategySpan(messages, blocks.Closed(all), req.Blocks, defaultBlocks)
}

// strategySpan：策略 —— "从第一条没被覆盖的消息起，取最老的 N 个已闭合块"。
func strategySpan(messages []model.Message, closed []blocks.Block, requested, defaultBlocks int) (span, error) {
	if requested < 0 {
		return span{}, invalid("blocks 要正整数（不给我就用配置的 compact_blocks 默认值）")
	}
	count := requested
	if count == 0 {
		count = defaultBlocks
	}
	if count <= 0 {
		return span{}, invalid("要压几个块得说得出来（配置里的 compact_blocks 也是 0）")
	}
	if len(closed) == 0 {
		// 最后那个开着的块永不压 ⇒ 刚聊了一轮时一块都压不了
		return span{}, invalid("还没有已闭合的块可压（最后那个开着的块永不压：再聊一轮）")
	}
	start := firstUncovered(messages)
	if start < 0 {
		return span{}, invalid("整条会话都已经被摘要盖住了：没有可压的")
	}
	taken := make([]blocks.Block, 0, count)
	for _, block := range closed {
		if block.End < start {
			continue // 早就压过的那几块（在第一条没被覆盖的消息之前）
		}
		if firstCovered(messages, block.Begin, block.End) >= 0 {
			continue // 碎的覆盖：跳过 —— 不许覆盖已压缩的区间
		}
		taken = append(taken, block)
		if len(taken) == count {
			break
		}
	}
	if len(taken) == 0 {
		return span{}, invalid("没有可压的已闭合块（剩下的要么是开着的那块，要么已经被摘要盖住了）")
	}
	return makeSpan(messages, taken[0].Begin, taken[len(taken)-1].End, len(taken)), nil
}

// rangeSpan：另一条入口 —— 直接点名一段区间（两端都是 message id）。
//
// 三条闸：两端都在这条会话里、begin ≤ end、**块不被劈开**（两端正好落在块的边界上）。
// 再加一条"最后那个开着的块永不压"与"区间里不许含已压缩的文本"。
func rangeSpan(messages []model.Message, all []blocks.Block, req Request) (span, error) {
	if req.Begin == "" || req.End == "" {
		return span{}, invalid("按区间压要两端都给：begin_message_id + end_message_id")
	}
	begin, end := indexOf(messages, req.Begin), indexOf(messages, req.End)
	if begin < 0 || end < 0 {
		missing := req.Begin
		if begin >= 0 {
			missing = req.End
		}
		return span{}, invalid("这个区间的一端不在这条会话里：%s", missing)
	}
	if begin > end {
		return span{}, invalid("区间反了：begin 在 end 之后")
	}
	first, last := -1, -1
	for index, block := range all {
		if block.Begin == begin {
			first = index
		}
		if block.End == end {
			last = index
		}
	}
	if first < 0 {
		return span{}, invalid("区间的开头不是一个块的开头（块 = 在 assistant → user 交界处切的一段，块不被劈开）")
	}
	if last < 0 {
		return span{}, invalid("区间的结尾不是一个块的结尾（同一条规矩：块不被劈开）")
	}
	if last == len(all)-1 {
		return span{}, invalid("最后那个开着的块永不压（对话还在往下长）：等它接上下一轮")
	}
	if err := ensureUncovered(messages, begin, end); err != nil {
		return span{}, err
	}
	return makeSpan(messages, begin, end, last-first+1), nil
}

// makeSpan：切出这一段（**消息是快照** —— 调用期间别人改库不影响这一趟的判断）。
func makeSpan(messages []model.Message, begin, end, count int) span {
	snapshot := make([]model.Message, end-begin+1)
	copy(snapshot, messages[begin:end+1])
	return span{Begin: begin, End: end, Blocks: count, Messages: snapshot}
}

// firstUncovered：第一条**还没被摘要盖住**的消息的下标（全被盖住 ⇒ -1）。
func firstUncovered(messages []model.Message) int {
	for index := range messages {
		if messages[index].SummaryID == nil {
			return index
		}
	}
	return -1
}

// firstCovered：这段里**第一条已经挂在摘要上**的消息的下标（没有 ⇒ -1）。
func firstCovered(messages []model.Message, begin, end int) int {
	for index := begin; index <= end; index++ {
		if messages[index].SummaryID != nil {
			return index
		}
	}
	return -1
}

// ensureUncovered：区间里**一条**已压缩的消息都不许有（口径见包注释第 2 条）。
func ensureUncovered(messages []model.Message, begin, end int) error {
	if index := firstCovered(messages, begin, end); index >= 0 {
		return invalid("区间里含已压缩的文本（消息 %s 已经挂在摘要上了）：压缩不许覆盖已压缩的区间",
			shortID(messages[index].ID))
	}
	return nil
}

// indexOf：按**完整 id** 找下标（找不到 ⇒ -1 —— 区间两端必须是精确的 message id）。
func indexOf(messages []model.Message, id string) int {
	for index := range messages {
		if messages[index].ID == id {
			return index
		}
	}
	return -1
}

// ── 调用 + 五条验证 + 落库 ─────────────────────────────────────────────────

// execute：后台/同步那半程 —— 挂号 → 拼材料 → 调上游 → 过闸 → 单事务落库 → 收尾。
//
// **失败不阻塞**：错一律原样返回（会话一个字节都不动），重试由调用方决定。
func (s *Service) execute(ctx context.Context, p *prepared) (Result, error) {
	sessionID := p.session.ID
	// 挂号：会调模型的作业必带会话 id ⇒ `Begin` 自己会断言
	guard := s.Tasks.Begin(task.KindCompact, &sessionID, "压缩 · 会话 "+shortID(sessionID))
	defer guard.Interrupted() // 忘了收 / panic ⇒ 记成"中断"，绝不留僵尸条目

	result, err := s.call(ctx, p)
	if err != nil {
		guard.Fail(err.Error())
		return Result{}, err
	}
	guard.Succeed()
	s.Turns.FinishCompact(sessionID, "done", result.SummaryID, "", result.Blocks)
	return result, nil
}

// call：真调一次 + 过五条验证 + 落库。
func (s *Service) call(ctx context.Context, p *prepared) (Result, error) {
	material := materialOf(p.span.Messages)
	if strings.TrimSpace(material) == "" {
		// ②之前的半步：**材料是空的就别发**（整段都是 `<state>` 块时就是这样）。
		// 注意判的是**对话正文**（不含下面附的那份状态）—— 状态不是"可压的内容"。
		return Result{}, invalid("这一段剔掉 <state> 之后没有正文可压（整段都是状态块）")
	}
	wire := providers.FromConfig(p.channel)
	result, err := providers.NewClient(wire).Complete(ctx, wire, providers.Request{
		Model: p.model,
		Messages: []providers.ChatMessage{
			{Role: "system", Content: p.template},
			{Role: "user", Content: materialHeader + withStateSection(material, p.stateSection)},
		},
		// 骑**同一个会话 id**（网关按它路由、缓存按前缀算；缺了会被上游拒）
		SessionID: p.session.ID,
		// 一次性的子调用：不写提示词缓存（照 Pi：Avoid cache writes for one-off summaries）
		CacheRetention: providers.CacheNone,
	}, p.local)
	if err != nil {
		return Result{}, &Error{Code: "upstream", Message: err.Error()}
	}

	// ① 剔 `<state>`：混进梗概里的状态块要拿掉（**只有 `statelang.Scan` 这一处**认得标签）
	text := strings.TrimSpace(statelang.Scan(result.Text).Cleaned)
	if text != strings.TrimSpace(result.Text) {
		// 上游把状态写进梗概了 ⇒ 记一句（文档口径：`summaries.text` 只写叙事，混进来会被剔掉并留个痕）
		log.Printf("压缩：会话 %s 的摘要里混了 <state> 块，已剔除", shortID(p.session.ID))
	}
	// ② 非空：剔完是空的 ⇒ **不写**（宁可这一趟什么都没有，也别塞一条空摘要）
	if text == "" {
		return Result{}, &Error{Code: "upstream",
			Message: "上游回来的那段剔掉 <state> 之后是空的：这一份不落库"}
	}

	// ③ 区间自洽 + ④ 不许覆盖已压缩的区间：落库时在**同一个事务里**再核一遍
	//    （调用上游那段时间里别人可能动过库 —— `RecordSummary` 两处重核就是干这个的）
	ids := make([]string, 0, len(p.span.Messages))
	for _, message := range p.span.Messages {
		ids = append(ids, message.ID)
	}
	summaryID, err := mintSummaryID()
	if err != nil {
		return Result{}, err
	}
	summary := model.Summary{
		ID: summaryID, SessionID: p.session.ID,
		SourceKind:     model.SourceMessages,
		BeginMessageID: &ids[0], EndMessageID: &ids[len(ids)-1],
		Text: text,
		// blocks = 覆盖了几个对话块（显示 + "≥N 块"判定；块本身不入库）
		Blocks: int64(p.span.Blocks),
		// tokens = 这条摘要正文的**估算**（上游 usage 可能没有；估算的位置与预算算法同一个口径）
		Tokens:    int64(s.estimateTokens(p, text)),
		SourceIDs: ids,
		Provider:  p.channel.ID, Model: p.model,
		// ⑤ 落库一并写 prompt_version（**生效模板**的指纹）与 usage
		PromptVersion: abilities.PromptVersion(p.template),
		Usage:         result.Usage,
		Dirty:         false, // 刚压出来就是干净的
		CreatedAt:     time.Now().UnixMilli(),
	}
	if err := s.Store.RecordSummary(summary, ids); err != nil {
		return Result{}, storeError(err)
	}
	return Result{
		SessionID: p.session.ID, BeginMessageID: ids[0], EndMessageID: ids[len(ids)-1],
		Blocks: p.span.Blocks, Messages: len(ids), SummaryID: summaryID,
		PromptVersion: summary.PromptVersion, Text: text,
		Provider: summary.Provider, Model: summary.Model, Usage: summary.Usage,
	}, nil
}

// materialOf：把这一段正文拼成材料 —— **每条消息都过 `statelang.Scan(...).Cleaned`**
// （混进去的 `<state>` 块要剔掉；剔完全空的那条直接不要，别给模型留一行空话）。
//
// 思考（reasoning）不进材料：它只服务显示与回传上游，不是"说过的话"。
func materialOf(messages []model.Message) string {
	var builder strings.Builder
	for _, message := range messages {
		text := strings.TrimSpace(statelang.Scan(message.Content).Cleaned)
		if text == "" {
			continue
		}
		label := "用户"
		if message.Role == model.RoleAssistant {
			label = "助手"
		}
		builder.WriteString(label)
		builder.WriteString("：")
		builder.WriteString(text)
		builder.WriteString("\n\n")
	}
	return strings.TrimSpace(builder.String())
}

// withStateSection：把那份状态附在材料后面（`section` 为空 ⇒ 原样返回）。
//
// 抬头由 `stateHeader` 说清口气（程序事实、仅供参考、不要写进梗概）—— 梗概里不存状态这条规矩不变：
// 状态只是"写梗概时看得见的事实"，不进摘要、不进库。
func withStateSection(material, section string) string {
	if strings.TrimSpace(section) == "" {
		return material
	}
	return material + "\n\n" + stateHeader + section
}

// estimateTokens：这条摘要正文的估算 token 数（口径只有一处：`state.EstimateTokens`）。
func (s *Service) estimateTokens(p *prepared, text string) int {
	ratio := state.DefaultTokenizerRatio
	row, err := s.Store.GetModel(p.channel.ID, p.model)
	if err == nil && row != nil {
		ratio = state.TokenizerRatio(row.Tokenizer)
	}
	return state.EstimateTokens(text, ratio)
}

// mintSummaryID：摘要的 id（UUIDv7：与消息同一套铸法，顺序也有意义）。
func mintSummaryID() (string, error) {
	ids, err := store.MintOrderedIDs(1)
	if err != nil {
		return "", err
	}
	return ids[0], nil
}

// storeError：store 的两类错翻成带 code 的失败（与 HTTP 层的映射同一套词）。
func storeError(err error) error {
	var invalidError store.InvalidError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return &Error{Code: "not_found", Message: "区间里的消息不见了（被删过）：重新取一次"}
	case errors.As(err, &invalidError):
		return &Error{Code: "invalid", Message: invalidError.Error()}
	}
	return err
}

// agentLabel：报错里认得出是哪份人格。
func agentLabel(agent config.Agent) string {
	if agent.ID != "" {
		return agent.ID
	}
	return agent.Name
}

// shortID：日志与状态行里认得出是哪个就够。
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
