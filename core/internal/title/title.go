// Package title：**起标题的唯一入口** —— 把一条会话的开头收成一行标题。
//
// 形状（照 `compact` 那一套，只是产物更小）：
//
//	输入 = 会话的**前几条消息**（`headMaterial`：够看出"在聊什么"就行 —— 取法写在那个函数上）
//	模板 = `abilities` 的生效模板（agent 覆盖 `abilities.title.prompt` ?: 代码里的默认）
//	调用 = `providers`（**非流式**一次小调用，骑**本会话 id** ⇒ 网关路由与前缀缓存都一致）
//	输出 = 一行标题；**落库由调用方** —— `sessions.title`，一次 UPDATE（`store.SetTitleIfEmpty`）
//
// 触发规则（`DEFINE.md`「Title 什么时候起」，**定死的**）：
//
//   - **自动**（`Auto`）：只在**标题还空着**时，首次发消息并拿到回复之后起一次；
//     **用户改过名 ⇒ 标题非空 ⇒ 永不再自动覆盖**（这条判定落在 SQL 的 WHERE 里，见 `Auto`）；
//   - **手动**（`Generate`）：随时可以再起一次（`-debug title` 走它）；
//   - **可关**：开关就是 agent 上的 `title` 能力 —— 关掉 ⇒ 不起（`Generate` **明确拒绝**）。
//
// **便宜与无害**：短输出、一次性子调用（`CacheRetention: none`）；失败**只记日志**，
// 那一轮照常（`Auto` 的调用方在 chat 的后台半程里，返回值它不处理）。
//
// 关掉 / 起不了**都不是降级执行能力** ✗：会话照常，只是名字退回到**首句截断**这个兜底
// （`fallbackTitle`）—— 那是兜底，不是"偷偷把能力跑一遍"。
package title

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"microchat/internal/abilities"
	"microchat/internal/config"
	"microchat/internal/model"
	"microchat/internal/providers"
	"microchat/internal/statelang"
	"microchat/internal/store"
	"microchat/internal/task"
)

// 材料那一段的抬头（让模型一眼知道下面是什么）。
const materialHeader = "【要起名字的这段对话】\n"

// 材料的取法：**克制** —— 从会话开头取最多 `materialMessages` 条（两条一轮 ⇒ 两轮以内），
// 每条剔掉 `<state>` 之后最多留 `materialChars` 个字。
//
// 为什么克制：标题是"这一段在聊什么"的一行概括，喂进去的上下文越多越贵、也越容易被
// 后面的枝节带跑；会话开头那几句就足够定调。**不改这个取法的语义**（改 = 换标题的口径）。
const (
	materialMessages = 4
	materialChars    = 300
)

// dummyReply：本地假上游（`kind: dummy`）吐的那句标题 —— 确定性、不联网，一眼认得出是假的。
const dummyReply = "（测试用假标题）"

// autoTimeout：**自动起标题这一趟子调用的上限** —— 30 秒。
//
// 为什么必须给：渠道的 `timeouts.total_seconds` 缺省 300s，上游"接了不回"时这趟会一直挂着；
// 而 `Auto` 排在 `chat.run` 的 `Finish`（翻 idle）**之前** ⇒ 那一轮就一直不翻 idle（界面像卡死）。
// 到点即撤：`Generate` 原样报错 ⇒ `Auto` 记日志 + 退**首句截断**兜底，会话不受影响。
//
// 为什么是 30 而不是 10：response 系模型的首字节要 3–5 秒（实测 muse-spark-1.3-contributor），
// 10 秒会把"走对门但回得慢"的调用 whole-sale 掐掉，而掐掉的代价（标题退回首句截断）
// 比多等 20 秒更大。chat 系照旧几秒就回，上限放宽不影响它们。
//
// **只加给辅助调用**（title —— compact 另有它自己的 60s，见 `compact.compactTimeout`）；
// 前台那一轮的取消由 `turn` 登记表管，不在这里。
const autoTimeout = 30 * time.Second

// Result：一次起标题的结局（`-debug title` 直接打它）。
type Result struct {
	SessionID     string `json:"session_id"`
	Title         string `json:"title"`
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	PromptVersion int64  `json:"prompt_version"`
	// Text：模型原话（`Title` 是它过完"一行化"之后的样子 —— 两者不同时，看这里）。
	Text string `json:"text"`
}

// Error：带 code 的失败 —— `code` 与 HTTP 错误体同一套词（client 按 code 分支，别匹配文案）。
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

func upstream(message string) error {
	return &Error{Code: "upstream", Message: message}
}

// Service：起标题的唯一实现处。
type Service struct {
	Store *store.Store
	Paths config.Paths
	Tasks *task.Registry
}

func New(st *store.Store, paths config.Paths, tasks *task.Registry) *Service {
	return &Service{Store: st, Paths: paths, Tasks: tasks}
}

// ── 自动那一条路 ───────────────────────────────────────────────────────────

// Auto：自动起一次 —— 调用方在"拿到回复之后"调它（`chat` 的后台半程）。
//
// 三条"不起"都是**正常**的，这一轮照常：
//
//  1. 标题已经有字（含**用户改过名**）⇒ 一个字都不动（**永不再自动覆盖** ✗）；
//  2. 能力关掉 / 这条会话没有可用的模型 ⇒ 退回首句截断兜底（会话总得有个名字）；
//  3. 上游失败 ⇒ 同上（只记日志）。
//
// 落库走 `store.SetTitleIfEmpty`：条件在 SQL 里 ⇒ 起名期间用户刚改的名不会被覆盖。
// 返回值刻意是空 —— 起标题的失败**不该**有调用方要处理的东西。
func (s *Service) Auto(session model.Session) {
	current, err := s.Store.GetSession(session.ID)
	if err != nil {
		log.Printf("起标题：会话 %s 读不出来（这一轮照常）：%v", shortID(session.ID), err)
		return
	}
	if current == nil || strings.TrimSpace(current.Title) != "" {
		return // 已经有名字（人起的或上次自动起的）⇒ 永不再自动覆盖
	}
	// 辅助调用只给这一趟短超时（理由见 `autoTimeout`）：到点即撤 ⇒ 这一轮照常翻 idle。
	ctx, cancel := context.WithTimeout(context.Background(), autoTimeout)
	defer cancel()
	result, generateErr := s.Generate(ctx, *current)
	if generateErr != nil {
		log.Printf("起标题：会话 %s 没起成，退回首句截断兜底（这一轮照常）：%v", shortID(current.ID), generateErr)
		s.fallback(*current)
		return
	}
	s.write(current.ID, result.Title)
}

// fallback：兜底 —— **首句截断**（原来住在 `chat` 里的那段就是它）。
//
// 只在"能力不可用 / 关掉 / 失败"时走这条路：会话因此总有个名字（空标题在左栏里认不出）。
// 取**第一条用户消息**（不是"这一次发的那句"：起名可能发生在会话中段）。
func (s *Service) fallback(session model.Session) {
	chars := config.DefaultChat().TitleChars
	if chatConfig, _, _, err := config.Load(s.Paths); err == nil {
		chars = chatConfig.TitleChars
	}
	messages, err := s.Store.ListMessages(session.ID)
	if err != nil {
		return
	}
	for _, message := range messages {
		if message.Role != model.RoleUser {
			continue
		}
		if text := model.TitleFrom(message.Content, chars); text != "" {
			s.write(session.ID, text)
			return
		}
	}
}

// write：把名字写下去（**只有还空着才写** —— 条件在 SQL 里）。
func (s *Service) write(sessionID, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	if _, err := s.Store.SetTitleIfEmpty(sessionID, text); err != nil {
		log.Printf("起标题：会话 %s 的名字没写下去：%v", shortID(sessionID), err)
	}
}

// ── 手动那一条路（也是自动那条路的实底）────────────────────────────────────

// Generate：**同步起一次**（`-debug title` 与将来的手动按钮走它）—— 不管标题现有没有。
//
// 能力关掉 ⇒ 明确拒绝（不是静默成功、也不是降级）；上游失败 ⇒ 原样报错（**不重试、不吞错**；
// 重试由调用方决定 —— 与 `compact` 同一条脾气）。**落库不在这儿**（调用方说了算）。
func (s *Service) Generate(ctx context.Context, session model.Session) (Result, error) {
	chatConfig, agents, providersConfig, err := config.Load(s.Paths)
	if err != nil {
		// 读不出来就说读不出来：能力开关默认"全开"，静默按默认跑会让"配了要关"的人以为关上了
		return Result{}, &Error{Code: "internal", Message: "读配置失败：" + err.Error()}
	}
	agent, _ := agents.Resolve(session.AgentID)
	setting := abilities.Resolve(agent, abilities.Title)
	if !setting.Enabled {
		return Result{}, invalid("会话 %s 的 agent（%s）关掉了 title 能力（abilities.title.enabled = false）："+
			"起名字不做就是不做，不会降级执行", shortID(session.ID), agentLabel(agent))
	}

	messages, err := s.Store.ListMessages(session.ID)
	if err != nil {
		return Result{}, err
	}
	material := headMaterial(messages)
	if material == "" {
		return Result{}, invalid("这条会话还没有可以起名字的正文（前几条消息剔掉 <state> 之后是空的）")
	}

	// 生效渠道 / 模型：能力上填了就用它，没填就骑会话的
	providerID, modelID := session.Provider, session.Model
	if setting.Provider != "" {
		providerID = setting.Provider
	}
	if setting.Model != "" {
		modelID = setting.Model
	}
	backend := providers.SelectBackend(providerID, modelID, providersConfig)
	if backend.Name == providers.BackendFallback {
		return Result{}, invalid("这条会话没有可用的模型（渠道 %q 不在 providers.json 里，或模型名为空）："+
			"起标题要真调一次上游 —— 先配好渠道，或把这条会话切到 dummy", providerID)
	}
	local := ""
	if backend.Name == providers.BackendDummy {
		local = dummyReply
	}

	// 挂号：**会调模型的作业必带会话 id** ⇒ `Begin` 自己会断言
	guard := s.Tasks.Begin(task.KindTitle, &session.ID, "起标题 · 会话 "+shortID(session.ID))
	defer guard.Interrupted() // 忘了收 / panic ⇒ 记成"中断"，绝不留僵尸条目

	template := abilities.Template(setting, abilities.Title)
	wire := providers.FromConfig(backend.Provider)
	// 与 chat.go Accept 同一条查表：有行且 api=="openai-responses" ⇒ 走 /responses，否则 chat 缺省。
	wire.Protocol = providers.ResolveProtocol(s.Store.ModelRoute, backend.Provider.ID, modelID)
	completion, err := providers.NewClient(wire).Complete(ctx, wire, providers.Request{
		Model: modelID,
		Messages: []providers.ChatMessage{
			{Role: "system", Content: template},
			{Role: "user", Content: materialHeader + material},
		},
		// 骑**同一个会话 id**（网关按它路由、缓存按前缀算；缺了会被上游拒）
		SessionID: session.ID,
		// 一次性的子调用：不写提示词缓存（与压缩同一条理由）
		CacheRetention: providers.CacheNone,
	}, local)
	if err != nil {
		guard.Fail(err.Error())
		return Result{}, upstream(err.Error())
	}
	text := oneLine(completion.Text, chatConfig.TitleChars)
	if text == "" {
		guard.Fail("上游回来的标题是空的")
		return Result{}, upstream("上游回来的标题是空的（剔掉空白之后什么都没有）")
	}
	guard.Succeed()
	return Result{
		SessionID: session.ID, Title: text, Provider: backend.Provider.ID, Model: modelID,
		PromptVersion: abilities.PromptVersion(template), Text: completion.Text,
	}, nil
}

// ── 材料与收尾 ────────────────────────────────────────────────────────────

// headMaterial：标题的材料 —— 会话开头最多 `materialMessages` 条，每条剔掉 `<state>` 块
// 之后按 `materialChars` 个**字符**截断（中文按字节截会切出残字）。
//
// 思考（reasoning）不进材料：它只服务显示与回传上游，不是"说过的话"。
func headMaterial(messages []model.Message) string {
	var builder strings.Builder
	taken := 0
	for _, message := range messages {
		if taken >= materialMessages {
			break
		}
		text := strings.TrimSpace(statelang.Scan(message.Content).Cleaned)
		if text == "" {
			continue // 整条都是状态块 ⇒ 不给模型留一行空话
		}
		label := "用户"
		if message.Role == model.RoleAssistant {
			label = "助手"
		}
		builder.WriteString(label)
		builder.WriteString("：")
		builder.WriteString(truncate(text, materialChars))
		builder.WriteString("\n\n")
		taken++
	}
	return strings.TrimSpace(builder.String())
}

// truncate：按**字符**截断（不是字节）。
func truncate(text string, chars int) string {
	if chars <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= chars {
		return text
	}
	return string(runes[:chars])
}

// oneLine：模型原话 → **一行标题** —— 换行压平、剥掉两头的引号壳（模型十次有九次自己加），
// 再按 `title_chars` 截断。空 ⇒ 空串（调用方据此判失败）。
func oneLine(text string, chars int) string {
	flat := strings.TrimSpace(strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(text))
	flat = strings.TrimSpace(strings.Trim(flat, `"'“”‘’「」『』`))
	return strings.TrimSpace(model.TitleFrom(flat, chars))
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
