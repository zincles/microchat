// Package compact：**摘要的唯一入口** —— 把一段对话收成一条摘要。
//
// 机制与策略两层（口径见 `AGENTS.md`「摘要 / 压缩的设计」与「后台任务 / 压缩 / 能力」）：
//
//	机制 = 给它一段（**1-based 的消息序号区间**）→ 按覆盖情况分派（全未覆盖 ⇒ 消息级；全被同一批
//	        同层顶层摘要盖住 ⇒ 合并级）→ 拼材料 → 叫 compact 能力 → 过五条验证 → 单事务落库；
//	策略 = "从第一条没被覆盖的消息起，取最老的 N 个**已闭合块**"（手动按钮的语义）。
//
// 四条纪律：
//
//  1. **只往 `summaries` 插行**：`messages` 上唯一允许被压缩改的是那一段的 `summary_id`
//     —— 绝不插 / 改 / 删正文（正文是存档）；
//  2. **消息级不许覆盖已压缩的区间**：区间里只要有一条消息已经挂在摘要上，就不再走消息级
//     （要么整段都是同批同层摘要 ⇒ 合并级，要么有洞 ⇒ 报错，不在老区间上盖第二层）；
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
	"sort"
	"strconv"
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

// mergeHeader：合并级材料的抬头 —— 一句话说清"下面是各段梗概、不是对话原文"。
const mergeHeader = "【下面是各段已有梗概（不是对话原文）—— 请把它们并成一段更粗的前情提要】\n\n"

const stateBeforeHeader = "# 待压缩段落开始时的状态（程序事实，仅供参考，不要写进梗概）：\n"
const stateAfterHeader = "# 待压缩段落结束时的状态（程序事实，仅供参考，不要写进梗概）：\n"

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
//	BeginIdx/EndIdx：直接点名一段区间（**1-based 的消息序号**，与 `/messages?from_idx=&to_idx=`、
//	  `/state?at_idx=` 同一套词；`0` = 没给）。序号指的是消息 —— 这一段的成员是消息还是摘要，
//	  由覆盖情况在后端分派（全未覆盖 ⇒ 消息级；全被同一批同层顶层摘要盖住 ⇒ 合并级）。
type Request struct {
	Blocks   int
	BeginIdx int
	EndIdx   int
}

// ByRange：这次是按区间来的（不是按块数）。
func (r Request) ByRange() bool { return r.BeginIdx > 0 || r.EndIdx > 0 }

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
	// FromIdx/ToIdx：这一段的 1-based 消息序号区间（含）；Merged：是不是合并级
	//（Sources 非空 ⇒ 并的是摘要而不是消息）。
	FromIdx int  `json:"from_idx"`
	ToIdx   int  `json:"to_idx"`
	Merged  bool `json:"merged"`
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
	s.Turns.FinishCompact(sessionID, "error", "", runErr.Error(), 0, 0, 0, false)
}

// busy：同一个会话已经有一次压缩在跑（**不排队**：排队会让"点了按钮却什么都没发生"难以解释）。
func busy() error {
	return &Error{Code: "conflict", Message: "这个会话已经有一次压缩在跑了，等它跑完"}
}

// ── 重摇：按既有顶层摘要的原始材料重跑一次（**只生成、不落库**）────────────────

// Generated：一次"只生成、不落库"的压缩产出（摘要重摇用）。
type Generated struct {
	Text          string
	Tokens        int
	Provider      string
	Model         string
	PromptVersion int64
	Usage         json.RawMessage
}

// RegenerateSummary：按一条既有【顶层】摘要的**原始材料**重跑一次压缩（**不落库**）—— 摘要重摇用。
//
// 材料与当初那次压缩**同源**：`type=message`（DB 列名是历史：`source_kind`）⇒ 那段消息的**当前**正文；
// `type=summary` ⇒ 它的孩子摘要（按区间排序）。目标必须：在本会话、`parent_summary_id IS NULL`、
// 区间两端都在（口径见 `TODO.md` 第 8 条）。
//
// 生效渠道 / 模型 / 模板 / 本地假上游与 `prepare` 同一套；产出过同一套验证（剔 `<state>`、非空），
// 不行就报错（code 同一套词）；`Tokens` 与落库那条路同一个估算口径。
//
// **绝不写任何库**：`summaries` / `messages` 一个字节都不动；挂不挂号、动不动 turn 状态、
// 要不要接受这一版，全由调用方决定（这里只管"重跑一次、把产出交回去"）。
func (s *Service) RegenerateSummary(ctx context.Context, session model.Session, summaryID string) (Generated, error) {
	_, agents, providersConfig, err := config.Load(s.Paths)
	if err != nil {
		return Generated{}, &Error{Code: "internal", Message: "读配置失败：" + err.Error()}
	}
	agent, _ := agents.Resolve(session.AgentID)
	setting := abilities.Resolve(agent, abilities.Compact)
	if !setting.Enabled {
		// 与 `prepare` 同一条口径：关掉 compact 就该做不了压缩（重摇也是压缩那一趟）
		return Generated{}, invalid("会话 %s 的 agent（%s）关掉了 compact 能力（abilities.compact.enabled = false）："+
			"压缩不做就是不做，不会降级执行", shortID(session.ID), agentLabel(agent))
	}
	messages, err := s.Store.ListMessages(session.ID)
	if err != nil {
		return Generated{}, err
	}
	summaries, err := s.Store.ListSummaries(session.ID)
	if err != nil {
		return Generated{}, err
	}
	target, ok := summaryByID(summaries, summaryID)
	if !ok {
		return Generated{}, &Error{Code: "not_found",
			Message: fmt.Sprintf("摘要 %s 不在会话 %s 里：重新取一次", shortID(summaryID), shortID(session.ID))}
	}
	if target.ParentSummaryID != nil {
		// 重摇只针对**根**（没有父的那条）：有父的是合并的产物，得先动它的孩子
		return Generated{}, invalid("摘要 %s 不是顶层（它有父）：重摇只换顶层摘要的正文", shortID(summaryID))
	}
	resolved, err := regenerateSpan(target, messages, summaries)
	if err != nil {
		return Generated{}, err
	}
	effective, err := resolveEffective(session, setting, providersConfig)
	if err != nil {
		return Generated{}, err
	}
	p := &prepared{
		session: session, channel: effective.channel, model: effective.model,
		template: effective.template, local: effective.local,
		span: resolved,
		// 状态段：消息级 = 区间两端；合并级 = 第一个孩子的区间头与最后一个孩子的区间末。
		stateSection: stateSectionAt(session, agents, messages, resolved.End+1),
		stateBefore:  stateSectionAt(session, agents, messages, resolved.Begin),
	}
	generated, err := s.generateText(ctx, p)
	if err != nil {
		return Generated{}, err
	}
	return Generated{
		Text:          generated.text,
		Tokens:        s.estimateTokens(p, generated.text),
		Provider:      p.channel.ID,
		Model:         p.model,
		PromptVersion: abilities.PromptVersion(p.template),
		Usage:         generated.usage,
	}, nil
}

// regenerateSpan：按一条既有摘要的**原始材料**重建一份 span（只供重摇生成，不落库）。
//
// 消息级：成员是那段消息的**当前**正文；合并级：成员是它的孩子摘要（按区间排序），
// Messages 取孩子覆盖的那一段快照（给轻抬头与"区间末状态"用）。区间两端对不上 ⇒ 报错。
func regenerateSpan(target model.Summary, messages []model.Message, summaries []model.Summary) (span, error) {
	positions := make(map[string]int, len(messages))
	for index := range messages {
		positions[messages[index].ID] = index
	}
	beginID, endID := deref(target.BeginMessageID), deref(target.EndMessageID)
	begin, okBegin := positions[beginID]
	end, okEnd := positions[endID]
	if beginID == "" || endID == "" || !okBegin || !okEnd || begin > end {
		return span{}, invalid("摘要 %s 的区间对不上眼前这条会话（被删过 / 老数据）：重新取一次", shortID(target.ID))
	}
	if target.Type != model.TypeSummaries {
		return makeSpan(messages, begin, end, int(target.Blocks)), nil
	}
	// 合并级：孩子摘要按区间排序（`SourceIDs` 记的本来就是这个序，这里现排一遍求稳）
	byID := make(map[string]model.Summary, len(summaries))
	for index := range summaries {
		byID[summaries[index].ID] = summaries[index]
	}
	type childSpan struct {
		summary model.Summary
		begin   int
		end     int
	}
	children := make([]childSpan, 0, len(target.SourceIDs))
	for _, sourceID := range target.SourceIDs {
		source, ok := byID[sourceID]
		if !ok {
			return span{}, invalid("摘要 %s 的孩子 %s 找不着了（被删过 / 老数据）：重新取一次",
				shortID(target.ID), shortID(sourceID))
		}
		sourceBegin, sourceEnd, okRange := childRange(source, positions)
		if !okRange {
			return span{}, invalid("摘要 %s 的孩子 %s 的区间对不上眼前这条会话（被删过 / 老数据）：重新取一次",
				shortID(target.ID), shortID(source.ID))
		}
		children = append(children, childSpan{summary: source, begin: sourceBegin, end: sourceEnd})
	}
	if len(children) < 2 {
		return span{}, invalid("摘要 %s 是合并级的，但孩子不足两条（老数据）：重新取一次", shortID(target.ID))
	}
	sort.Slice(children, func(i, j int) bool { return children[i].begin < children[j].begin })
	firstBegin, lastEnd := children[0].begin, children[len(children)-1].end
	sources := make([]model.Summary, len(children))
	blocks := 0
	for index, child := range children {
		sources[index] = child.summary
		blocks += int(child.summary.Blocks)
	}
	snapshot := make([]model.Message, lastEnd-firstBegin+1)
	copy(snapshot, messages[firstBegin:lastEnd+1])
	return span{Begin: firstBegin, End: lastEnd, Blocks: blocks, Messages: snapshot, Sources: sources}, nil
}

// summaryByID：在这一串摘要里按 id 找（找不着 ⇒ false）。
func summaryByID(summaries []model.Summary, id string) (model.Summary, bool) {
	for _, summary := range summaries {
		if summary.ID == id {
			return summary, true
		}
	}
	return model.Summary{}, false
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
	// stateBefore：同上，算到区间**开头**（段首状态；与段末配对，模型看得见变化）。
	stateBefore string
	// requested：面板上"请求压几个块"（区间入口 = 区间里实际几个块）。
	requested int
}

// span：这一次压的区间 —— 下标（含）+ 块数 + 那一批消息的快照。
//
// Sources 非空 ⇒ 这一趟是**合并级**（成员是那批同层顶层摘要，不是消息）：材料与落库都走它，
// Messages 只是这段区间的快照（供"区间末状态"用）。
type span struct {
	Begin    int
	End      int
	Blocks   int
	Messages []model.Message
	Sources  []model.Summary
}

// effective：生效渠道 / 模型 / 模板 / 本地话术 —— 能力上填了就用它，没填就骑会话的
// （辅助调用本来就要同一个会话）。
type effective struct {
	channel  config.Provider
	model    string
	template string
	local    string
}

// resolveEffective：把"这次压缩用哪条渠道、哪个模型、哪份模板、要不要本地假上游"算出来。
//
// `prepare`（落库那条路）与 `RegenerateSummary`（重摇那条路）共用 ⇒ 口径与错误文案只有一份
// （模板的算式也只有一处：`abilities.Template`，agent 覆盖 ?: 代码里的默认）。
func resolveEffective(session model.Session, setting abilities.Setting, providersConfig config.ProvidersConfig) (effective, error) {
	providerID, modelID := session.Provider, session.Model
	if setting.Provider != "" {
		providerID = setting.Provider
	}
	if setting.Model != "" {
		modelID = setting.Model
	}
	backend := providers.SelectBackend(providerID, modelID, providersConfig)
	if backend.Name == providers.BackendFallback {
		return effective{}, invalid("这条会话没有可用的模型（渠道 %q 不在 providers.json 里，或模型名为空）："+
			"压缩要真调一次上游 —— 先配好渠道，或把这条会话切到 dummy", providerID)
	}
	local := ""
	if backend.Name == providers.BackendDummy {
		local = dummyReply
	}
	return effective{
		channel:  backend.Provider,
		model:    modelID,
		template: abilities.Template(setting, abilities.Compact),
		local:    local,
	}, nil
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
	// 摘要**无条件加载**（合并级要拿它算顶层祖先与层级；消息级用不上也照拿 —— 让 `resolveSpan`
	// 的签名只有一份，省得两条入口各带一半上下文）
	summaries, err := s.Store.ListSummaries(session.ID)
	if err != nil {
		return nil, err
	}
	resolved, err := resolveSpan(messages, summaries, req, chatConfig.CompactBlocks)
	if err != nil {
		return nil, err
	}

	effective, err := resolveEffective(session, setting, providersConfig)
	if err != nil {
		return nil, err
	}
	return &prepared{
		session: session, channel: effective.channel, model: effective.model,
		template: effective.template,
		local:    effective.local,
		span:     resolved,
		// 材料里附的两份状态：**at_idx 那条链**（`state.StateAt`）—— 底子是**当前**的生效提示词、
		// 正文 fold 到区间开头（`Begin` 是下标 ⇒ 序号 = Begin）与区间末（`End` 是下标 ⇒ 序号 = End+1）。
		// 取料在锁里，所以在这儿算。
		stateSection: stateSectionAt(session, agents, messages, resolved.End+1),
		stateBefore:  stateSectionAt(session, agents, messages, resolved.Begin),
		requested:    requestedBlocks(req, resolved, chatConfig.CompactBlocks),
	}, nil
}

// stateAtTables：`at` 条为止的状态表（`at_idx` 同一条链）。调用方决定渲染。
func stateAtTables(session model.Session, agents config.AgentsConfig, messages []model.Message, at int) state.Tables {
	systemPrompt, source := state.ResolveSystemPrompt(session, agents)
	return state.StateAt(session.ID, systemPrompt, source, messages, at).Tables
}

// stateSectionAt：材料里那份状态的 `<current_state>` 文本（**算到 `at` 条为止**）——
// 没有变量可报就回空串。渲染走 `renderCurrentState`（与出站注入同形）。
func stateSectionAt(session model.Session, agents config.AgentsConfig, messages []model.Message, at int) string {
	text, _ := state.RenderCurrentState(stateAtTables(session, agents, messages, at))
	return text
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

// resolveSpan：把两种给法归一成一段（**闸都在这里**）。
//
// 按块数 ⇒ 策略（先底层块、再顶层摘要）；按区间 ⇒ 看区间里的**覆盖情况**分派：
// 全未覆盖 ⇒ 消息级；全被**同一批同层顶层摘要**盖住 ⇒ 合并级；混合（有洞）⇒ 报错。
func resolveSpan(messages []model.Message, summaries []model.Summary, req Request, defaultBlocks int) (span, error) {
	if len(messages) == 0 {
		return span{}, invalid("这条会话还没有消息，没有可压的")
	}
	all := blocks.Split(messages)
	if req.ByRange() {
		return rangeSpan(messages, summaries, all, req)
	}
	return strategySpan(messages, summaries, all, req.Blocks, defaultBlocks)
}

// PreviewResult：压缩预览的结果 —— "压哪段、怎么压"（只算不动，不落库、不调上游、不挂号）。
type PreviewResult struct {
	FromIdx   int      `json:"from_idx"`
	ToIdx     int      `json:"to_idx"`
	Merged    bool     `json:"merged"`
	Blocks    int      `json:"blocks"`
	SourceIDs []string `json:"source_ids"`
	Action    string   `json:"action"`
}

// Preview：**只算不动**的压缩预览 —— 调同一套 `resolveSpan`，算完"压哪段、怎么压"。
//
// 只走"按块数"那条路（`blocks` 参数，块 = assistant→user 交界，一块 ≥2 条；区间入口自己就是答案，不需要预览）：
// 落库、调上游、挂号一概不碰 —— 与 deletion-preview 同一条"只算不动"规矩。
func Preview(messages []model.Message, summaries []model.Summary, blocks int, defaultBlocks int) (PreviewResult, error) {
	resolved, err := resolveSpan(messages, summaries, Request{Blocks: blocks}, defaultBlocks)
	if err != nil {
		return PreviewResult{}, err
	}
	from, to := resolved.Begin+1, resolved.End+1
	result := PreviewResult{
		FromIdx: from, ToIdx: to,
		Merged: len(resolved.Sources) > 0, Blocks: resolved.Blocks,
	}
	if result.Merged {
		ids := make([]string, 0, len(resolved.Sources))
		for _, source := range resolved.Sources {
			ids = append(ids, source.ID)
		}
		result.SourceIDs = ids
		result.Action = "并第" + strconv.Itoa(from) + "–" + strconv.Itoa(to) + "条那" +
			strconv.Itoa(len(ids)) + "坨"
	} else {
		result.Action = "压第" + strconv.Itoa(from) + "–" + strconv.Itoa(to) +
			"条（" + strconv.Itoa(resolved.Blocks) + "块）"
	}
	return result, nil
}

// strategySpan：策略 —— 按**块数** N 入口（块 = assistant→user 交界，一块 ≥2 条；
// `/compact 1` 是压 1 块不是 1 条），两层找。
//
// 先从 firstUncovered 起取 N 个已闭合未覆盖块 → 有就走（消息级，Sources 为空）；
// 没有（底层凑不够整块 / 全被盖住 / 只剩开着的那块）→ 摘要层（`strategyMergeSpan`）：
// 顶层摘要里从最老起取连续 N 个同层相邻的 → 并。
func strategySpan(messages []model.Message, summaries []model.Summary, all []blocks.Block, requested, defaultBlocks int) (span, error) {
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
	if start := firstUncovered(messages); start >= 0 {
		taken := make([]blocks.Block, 0, count)
		for _, block := range blocks.Closed(all) {
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
		if len(taken) > 0 {
			return makeSpan(messages, taken[0].Begin, taken[len(taken)-1].End, len(taken)), nil
		}
	}
	// 底层没有整块可压（要么全被盖住，要么只剩开着的那块）⇒ 往摘要层找
	return strategyMergeSpan(messages, summaries, all, count)
}

// strategyMergeSpan：策略的第二层 —— 顶层摘要里从最老起取连续 N 个同层相邻的，拼成合并级 span。
//
// "现成的"只有顶层（parent 为空；有爹的不算）；N 条必须在同一层找齐（跨层不凑），
// 区间首尾相接无洞，且不碰最后那个开着的块 —— 任一孩子区间碰到开块起点 ⇒ 跳过整组、往后找。
// 找齐就把那 N 条的并集区间交给 `mergeSpan`（四条闸与装配原样再过一遍，不自己重拼）；
// 找不齐 ⇒ 报"无可压缩"。
func strategyMergeSpan(messages []model.Message, summaries []model.Summary, all []blocks.Block, count int) (span, error) {
	none := func() (span, error) {
		return span{}, invalid("无可压缩：顶层摘要里凑不齐 %d 条同层相邻的"+
			"（要么不够 N 坨，要么跨层，要么会碰到最后那个开着的块）", count)
	}
	if count < 2 {
		return none() // 并至少要两坨（`mergeSpan` 第二条闸）：一条"并"不出东西
	}
	positions := make(map[string]int, len(messages))
	for index := range messages {
		positions[messages[index].ID] = index
	}
	type topSpan struct {
		summary model.Summary
		begin   int
		end     int
	}
	tops := make([]topSpan, 0, len(summaries))
	for _, summary := range summaries {
		if summary.ParentSummaryID != nil {
			continue // 有爹的不算"现成的"
		}
		begin, end, ok := childRange(summary, positions)
		if !ok {
			continue // 区间对不上眼前这条会话（被删过 / 老数据）：这坨没法排，跳过
		}
		tops = append(tops, topSpan{summary: summary, begin: begin, end: end})
	}
	sort.Slice(tops, func(i, j int) bool { return tops[i].begin < tops[j].begin })
	if len(tops) < count {
		return none()
	}
	childrenOf := make(map[string][]string, len(summaries))
	for index := range summaries {
		if parent := summaries[index].ParentSummaryID; parent != nil {
			childrenOf[*parent] = append(childrenOf[*parent], summaries[index].ID)
		}
	}
	memo := make(map[string]int, len(summaries))
	openBegin := -1
	if len(all) > 0 {
		openBegin = all[len(all)-1].Begin
	}
	for first := 0; first+count <= len(tops); first++ {
		window := tops[first : first+count]
		level := summaryLevel(window[0].summary.ID, childrenOf, memo)
		ok := true
		for index := 1; index < len(window); index++ {
			if window[index].begin != window[index-1].end+1 {
				ok = false // 有洞 / 没首尾相接
				break
			}
			if summaryLevel(window[index].summary.ID, childrenOf, memo) != level {
				ok = false // 跨层不凑
				break
			}
		}
		if ok && openBegin >= 0 && window[len(window)-1].end >= openBegin {
			ok = false // 碰到最后那个开着的块
		}
		if !ok {
			continue // 跳过整组、往后找
		}
		return mergeSpan(messages, summaries, all, window[0].begin, window[len(window)-1].end)
	}
	return none()
}

// rangeSpan：另一条入口 —— 直接点名一段区间（**1-based 的消息序号**，闭区间）。
//
// 四条闸：两端都给正数序号、都不越界、begin ≤ end；然后按覆盖情况分派：
//
//	全未覆盖（区间里没有消息带 summary_id）⇒ 消息级；
//	全被覆盖 ⇒ 合并级（是不是"同一批同层顶层摘要恰好铺满"由 `mergeSpan` 判）；
//	混合（有洞）⇒ 400（跨层不吃：一段已覆盖 + 一段新消息就是这种）。
func rangeSpan(messages []model.Message, summaries []model.Summary, all []blocks.Block, req Request) (span, error) {
	if req.BeginIdx <= 0 || req.EndIdx <= 0 {
		return span{}, invalid("按区间压要两端都给正数序号：begin_idx + end_idx（1-based，0 = 没给）")
	}
	if req.BeginIdx > len(messages) || req.EndIdx > len(messages) {
		return span{}, invalid("区间越界：这条会话只有 %d 条（begin_idx=%d, end_idx=%d）",
			len(messages), req.BeginIdx, req.EndIdx)
	}
	if req.BeginIdx > req.EndIdx {
		return span{}, invalid("区间反了：begin_idx（%d）在 end_idx（%d）之后", req.BeginIdx, req.EndIdx)
	}
	begin, end := req.BeginIdx-1, req.EndIdx-1
	covered := 0
	for index := begin; index <= end; index++ {
		if messages[index].SummaryID != nil {
			covered++
		}
	}
	switch {
	case covered == 0:
		return messageRangeSpan(messages, all, begin, end)
	case covered == end-begin+1:
		return mergeSpan(messages, summaries, all, begin, end)
	default:
		return span{}, invalid("区间里有洞：第 %d–%d 条里只有 %d 条已经被摘要盖住（另一半还是新消息）"+
			"—— 压缩只吃同一层，别把已覆盖的和新消息混在一段里",
			req.BeginIdx, req.EndIdx, covered)
	}
}

// messageRangeSpan：**消息级**的区间入口 —— 两端必须正好落在块的边界上（块不被劈开），
// 且不许碰最后那个开着的块。**路数一个字都不变，只把"两端 message id"换成"区间下标"。**
func messageRangeSpan(messages []model.Message, all []blocks.Block, begin, end int) (span, error) {
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

// mergeSpan：**合并级** —— 区间里每条消息都被盖住，且盖住它的**顶层摘要**恰好是一批
// 同层的兄弟、按位置首尾相接铺满整个区间。
//
// 四条闸（正确性，一条都不能省）：
//  1. 孩子全是**顶层**（沿 parent_summary_id 上溯到顶；单亲指针不能有两个爹）；
//  2. 至少两条；
//  3. 孩子区间恰好铺满区间（⊆ 且无洞、不劈开某条）；
//  4. 同层（level 现算，不入库）。
//
// 外加"不碰最后那个开着的块"（沿用消息级那条规矩与文案）。
func mergeSpan(messages []model.Message, summaries []model.Summary, all []blocks.Block, begin, end int) (span, error) {
	if len(all) > 0 && end >= all[len(all)-1].Begin {
		return span{}, invalid("最后那个开着的块永不压（对话还在往下长）：等它接上下一轮")
	}
	byID := make(map[string]*model.Summary, len(summaries))
	for index := range summaries {
		byID[summaries[index].ID] = &summaries[index]
	}
	// ① 收集区间里每个消息的**最顶层祖先**，按出现顺序去重
	seen := make(map[string]bool, 2)
	children := make([]model.Summary, 0, 2)
	for index := begin; index <= end; index++ {
		if messages[index].SummaryID == nil {
			return span{}, invalid("第 %d 条没被摘要盖住（这段不是清一色的摘要）", index+1)
		}
		top, ok := topAncestor(*messages[index].SummaryID, byID)
		if !ok {
			return span{}, invalid("第 %d 条挂的摘要在会话里找不着了：重新取一次", index+1)
		}
		if !seen[top.ID] {
			seen[top.ID] = true
			children = append(children, *top)
		}
	}
	if len(children) < 2 {
		return span{}, invalid("合并至少要两条同层摘要（这一段只盖着 %d 条）：拿不到就按消息级压", len(children))
	}
	// ③ 孩子区间 ⊆ 区间、按位置首尾相接恰好铺满（无洞、不劈开某条）
	positions := make(map[string]int, len(messages))
	for index := range messages {
		positions[messages[index].ID] = index
	}
	type childSpan struct {
		summary model.Summary
		begin   int
		end     int
	}
	spans := make([]childSpan, 0, len(children))
	for _, child := range children {
		childBegin, childEnd, ok := childRange(child, positions)
		if !ok {
			return span{}, invalid("子摘要 %s 的区间对不上眼前这条会话（被删过 / 老数据）：重新取一次", shortID(child.ID))
		}
		spans = append(spans, childSpan{summary: child, begin: childBegin, end: childEnd})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].begin < spans[j].begin })
	cursor := begin
	for _, child := range spans {
		if child.begin != cursor {
			return span{}, invalid("这一段没有被同层摘要恰好铺满（第 %d 条那儿空着 / 被劈开了）：按 idx 说清要并哪几条",
				cursor+1)
		}
		if child.end > end {
			return span{}, invalid("子摘要 %s 的范围超出了这个区间（不许劈开一条摘要）：把整段并进来", shortID(child.summary.ID))
		}
		cursor = child.end + 1
	}
	if cursor != end+1 {
		return span{}, invalid("这一段没有被同层摘要恰好铺满（第 %d 条那儿空着）：按 idx 说清要并哪几条", cursor+1)
	}
	// ④ 同层：level = 1 + max(孩子的 level)，无孩子 = 1（用这条会话的全部摘要现算）
	childrenOf := make(map[string][]string, len(summaries))
	for index := range summaries {
		if parent := summaries[index].ParentSummaryID; parent != nil {
			childrenOf[*parent] = append(childrenOf[*parent], summaries[index].ID)
		}
	}
	memo := make(map[string]int, len(summaries))
	level := summaryLevel(spans[0].summary.ID, childrenOf, memo)
	for _, child := range spans[1:] {
		if got := summaryLevel(child.summary.ID, childrenOf, memo); got != level {
			return span{}, invalid("要并的这几条不在同一层（合并只吃同层兄弟）：别把不同深度的并在一起")
		}
	}
	sources := make([]model.Summary, len(spans))
	totalBlocks := 0
	for index, child := range spans {
		sources[index] = child.summary
		totalBlocks += int(child.summary.Blocks)
	}
	snapshot := make([]model.Message, end-begin+1)
	copy(snapshot, messages[begin:end+1])
	return span{Begin: begin, End: end, Blocks: totalBlocks, Messages: snapshot, Sources: sources}, nil
}

// topAncestor：沿 `parent_summary_id` 上溯到**最顶层**（自己也可能是顶）。指不着 ⇒ false。
func topAncestor(summaryID string, byID map[string]*model.Summary) (*model.Summary, bool) {
	summary, ok := byID[summaryID]
	if !ok {
		return nil, false
	}
	for summary.ParentSummaryID != nil {
		parent, ok := byID[*summary.ParentSummaryID]
		if !ok {
			return nil, false
		}
		summary = parent
	}
	return summary, true
}

// childRange：这条摘要盖住的消息下标区间（闭区间）。两端缺一 / 指不着 ⇒ false。
func childRange(summary model.Summary, positions map[string]int) (int, int, bool) {
	if summary.BeginMessageID == nil || summary.EndMessageID == nil {
		return 0, 0, false
	}
	begin, okBegin := positions[*summary.BeginMessageID]
	end, okEnd := positions[*summary.EndMessageID]
	if !okBegin || !okEnd || begin > end {
		return 0, 0, false
	}
	return begin, end, true
}

// summaryLevel：摘要的层级 —— 叶子（没有孩子摘要）= 1，否则 1 + 孩子的最大层级。
// **现算、不入库**（层级是派生的：存它 = 第二个真相来源）。
func summaryLevel(summaryID string, childrenOf map[string][]string, memo map[string]int) int {
	if level, ok := memo[summaryID]; ok {
		return level
	}
	level := 1
	for _, childID := range childrenOf[summaryID] {
		if child := summaryLevel(childID, childrenOf, memo) + 1; child > level {
			level = child
		}
	}
	memo[summaryID] = level
	return level
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
	s.Turns.FinishCompact(sessionID, "done", result.SummaryID, "", result.Blocks,
		result.FromIdx, result.ToIdx, result.Merged)
	return result, nil
}

// generatedText：**只生成、不落库**的那半程的产出 —— 过完 ①②（剔 `<state>`、非空）之后的正文与 usage。
type generatedText struct {
	text  string
	usage json.RawMessage
}

// generateText：**只生成、不落库** —— 拼材料 → 调上游 → 剔 `<state>` → 非空校验，都在这里。
//
// `call()`（落库那条路）与 `RegenerateSummary`（重摇那条路）共用它：两者的分别只在
// "材料从哪来"（都装在同一份 `prepared` 里）与"生成之后干什么"（落库 / 只回文本）。
//
// 两处"不发 / 不写"的判断留在这里，口径只有一份：
//   - 材料剔完是空的 ⇒ 不发（整段都是 `<state>` 块时就是这样；判的是正文，不含附的那份状态）；
//   - 上游回来的剔完是空的 ⇒ 报 upstream（宁可这一趟什么都没有，也别塞一条空摘要）。
func (s *Service) generateText(ctx context.Context, p *prepared) (generatedText, error) {
	merge := len(p.span.Sources) > 0
	// 材料：消息级 = 这段对话正文；合并级 = 各段已有梗概（带轻抬头），外面套一句"这不是对话"。
	material := ""
	header := materialHeader
	if merge {
		material = mergeMaterial(p.span.Sources, messageIdxs(p.span.Messages))
		header = mergeHeader
	} else {
		material = materialOf(p.span.Messages)
	}
	if strings.TrimSpace(material) == "" {
		// ②之前的半步：**材料是空的就别发**（整段都是 `<state>` 块时就是这样）。
		// 注意判的是**正文**（不含下面附的那份状态）—— 状态不是"可压的内容"。
		return generatedText{}, invalid("这一段剔掉 <state> 之后没有正文可压（整段都是状态块）")
	}
	wire := providers.FromConfig(p.channel)
	// 与 chat.go Accept 同一条查表：有行且 api=="openai-responses" ⇒ 走 /responses，否则 chat 缺省。
	wire.Protocol = providers.ResolveProtocol(s.Store.ModelRoute, p.channel.ID, p.model)
	result, err := providers.NewClient(wire).Complete(ctx, wire, providers.Request{
		Model: p.model,
		Messages: []providers.ChatMessage{
			{Role: "system", Content: p.template},
			{Role: "user", Content: header + withStatePair(material, p.stateBefore, p.stateSection)},
		},
		// 骑**同一个会话 id**（网关按它路由、缓存按前缀算；缺了会被上游拒）
		SessionID: p.session.ID,
		// 一次性的子调用：不写提示词缓存（照 Pi：Avoid cache writes for one-off summaries）
		CacheRetention: providers.CacheNone,
	}, p.local)
	if err != nil {
		return generatedText{}, &Error{Code: "upstream", Message: err.Error()}
	}

	// ① 剔 `<state>`：混进梗概里的状态块要拿掉（**只有 `statelang.Scan` 这一处**认得标签）
	text := strings.TrimSpace(statelang.Scan(result.Text).Cleaned)
	if text != strings.TrimSpace(result.Text) {
		// 上游把状态写进梗概了 ⇒ 记一句（文档口径：`summaries.text` 只写叙事，混进来会被剔掉并留个痕）
		log.Printf("压缩：会话 %s 的摘要里混了 <state> 块，已剔除", shortID(p.session.ID))
	}
	// ② 非空：剔完是空的 ⇒ **不写**（宁可这一趟什么都没有，也别塞一条空摘要）
	if text == "" {
		return generatedText{}, &Error{Code: "upstream",
			Message: "上游回来的那段剔掉 <state> 之后是空的：这一份不落库"}
	}
	return generatedText{text: text, usage: result.Usage}, nil
}

// call：真调一次 + 过五条验证 + 落库。
func (s *Service) call(ctx context.Context, p *prepared) (Result, error) {
	merge := len(p.span.Sources) > 0
	generated, err := s.generateText(ctx, p)
	if err != nil {
		return Result{}, err
	}
	text := generated.text

	// 材料来源 → 落库那一份（成员是消息还是摘要）。
	sourceIDs := make([]string, 0, len(p.span.Messages))
	beginID, endID := "", ""
	summaryType := model.TypeMessages
	dirty := false
	if merge {
		for _, source := range p.span.Sources {
			sourceIDs = append(sourceIDs, source.ID)
			dirty = dirty || source.Dirty
		}
		// 并集两端 = 第一条孩子的 begin、最后一条孩子的 end（孩子已按区间排序）
		beginID = *p.span.Sources[0].BeginMessageID
		endID = *p.span.Sources[len(p.span.Sources)-1].EndMessageID
		summaryType = model.TypeSummaries
	} else {
		for _, message := range p.span.Messages {
			sourceIDs = append(sourceIDs, message.ID)
		}
		beginID = sourceIDs[0]
		endID = sourceIDs[len(sourceIDs)-1]
	}

	// ③ 区间自洽 + ④ 不许覆盖已压缩的区间：落库时在**同一个事务里**再核一遍
	//    （调用上游那段时间里别人可能动过库 —— `RecordSummary` 的守卫 UPDATE 就是干这个的）
	summaryID, err := mintSummaryID()
	if err != nil {
		return Result{}, err
	}
	summary := model.Summary{
		ID: summaryID, SessionID: p.session.ID,
		Type:           summaryType,
		BeginMessageID: &beginID, EndMessageID: &endID,
		Text: text,
		// blocks = 覆盖了几个对话块（显示 + "≥N 块"判定；块本身不入库）；合并级 = 子和
		Blocks: int64(p.span.Blocks),
		// tokens = 这条摘要正文的**估算**（上游 usage 可能没有；估算的位置与预算算法同一个口径）
		Tokens:    int64(s.estimateTokens(p, text)),
		SourceIDs: sourceIDs,
		Provider:  p.channel.ID, Model: p.model,
		// ⑤ 落库一并写 prompt_version（**生效模板**的指纹）与 usage
		PromptVersion: abilities.PromptVersion(p.template),
		Usage:         generated.usage,
		// 刚压出来就是干净的；合并级**传播**孩子的脏（父盖着被改过的内容）
		Dirty:     dirty,
		CreatedAt: time.Now().UnixMilli(),
	}
	if err := s.Store.RecordSummary(summary, sourceIDs); err != nil {
		return Result{}, storeError(err)
	}
	return Result{
		SessionID: p.session.ID, BeginMessageID: beginID, EndMessageID: endID,
		Blocks: p.span.Blocks, Messages: len(p.span.Messages), SummaryID: summaryID,
		PromptVersion: summary.PromptVersion, Text: text,
		Provider: summary.Provider, Model: summary.Model, Usage: summary.Usage,
		FromIdx: p.span.Begin + 1, ToIdx: p.span.End + 1, Merged: merge,
	}, nil
}

// messageIdxs：这段快照里 message id → 1-based 序号（摘要抬头里的"第 a–b 条"按它写）。
func messageIdxs(messages []model.Message) map[string]int {
	idxByID := make(map[string]int, len(messages))
	for _, message := range messages {
		if message.Idx > 0 {
			idxByID[message.ID] = message.Idx
		}
	}
	return idxByID
}

// mergeMaterial：合并级的材料 —— 每条子摘要正文前加一行轻抬头 `【第 a–b 条（N 块）】`，
// 段落间空行。a/b 用消息序号（缺了就不带抬头，别瞎说），N 用该孩子的块数。
func mergeMaterial(sources []model.Summary, idxByID map[string]int) string {
	var builder strings.Builder
	for _, source := range sources {
		text := strings.TrimSpace(source.Text)
		if text == "" {
			continue
		}
		builder.WriteString(mergeSourceHeader(source, idxByID))
		builder.WriteString(text)
		builder.WriteString("\n\n")
	}
	return strings.TrimSpace(builder.String())
}

// mergeSourceHeader：一条子摘要的轻抬头（区间序号缺了 ⇒ 只写块数，宁可不说也不瞎说）。
func mergeSourceHeader(source model.Summary, idxByID map[string]int) string {
	blocks := source.Blocks
	if blocks < 1 {
		blocks = 1
	}
	begin, beginOK := idxByID[deref(source.BeginMessageID)]
	end, endOK := idxByID[deref(source.EndMessageID)]
	if beginOK && endOK {
		return fmt.Sprintf("【第 %d–%d 条（%d 块）】\n", begin, end, blocks)
	}
	return fmt.Sprintf("【（%d 块）】\n", blocks)
}

// deref：可空 string 指针取值（nil ⇒ 空串，查表必然查不着）。
func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
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

// sortedKeysOf：map 键排序（`state.sortedKeys` 不导出，这里各排各的）。
func sortedKeysOf[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// withStatePair：材料前后附段首/段末状态（为空的那份不附；两份都空 ⇒ 原样返回）。
func withStatePair(material, before, after string) string {
	out := material
	if strings.TrimSpace(before) != "" {
		out = stateBeforeHeader + before + "\n\n" + out
	}
	if strings.TrimSpace(after) != "" {
		out = out + "\n\n" + stateAfterHeader + after
	}
	return out
}

// estimateTokens：
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
