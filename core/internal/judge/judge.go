// Package judge：JEV 决策的那一发 —— 状态 + 类型化问题 → 概率表。
//
// 两层：`Evaluate` 是裸调用（拼请求、发出去、解析；不挂号、不读配置、不碰库），
// `Service.JudgeFor` 是"可编程动态组合"的那块积木 —— **开关 + 选渠道 + 挂号 + 失败降级**：
//
//	开关：agent 上 `judge` 能力关掉 ⇒ 明确拒绝（不是静默降级）
//	选渠道：能力上填了 provider/model 就用它，没填就骑会话的（与 compact/title 同一条回退）
//	挂号：`task.KindJudgement`（调外部模型就要归属可查，会话 id 必填）
//	失败：原样返回错误（调用方决定降级 —— 产出是材料，不进历史/变量）
//
// 后续"动作→追加动作 prompt"这类动态组合术，调的就是 `JudgeFor`
// （state/question 由调用方拼，这里只管"问出去、拿回来"）。
package judge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"microchat/internal/abilities"
	"microchat/internal/config"
	"microchat/internal/model"
	"microchat/internal/providers"
	"microchat/internal/store"
	"microchat/internal/task"
)

// judgeTimeout：这一发的总超时（ctx 可更早取消）。
const judgeTimeout = 10 * time.Second

// Answers：JEV 回的概率表（问题 id → 选中项/概率，由调用方解释）。
type Answers map[string]any

// Evaluate：拼请求、发出去、解析 `{answers, usage?, model?}`（只取 `answers`）。
//
// 只认 2xx；`answers` 必须是 object，否则报错。**失败原样返回，不重试**
// （重试由调用方决定 —— 与 `title` / `compact` 同一条脾气）。
func Evaluate(ctx context.Context, provider providers.Provider, model string, state any, questions map[string]any) (Answers, error) {
	request, err := providers.BuildSystemOne(provider, model, state, questions)
	if err != nil {
		return nil, err
	}
	request = request.WithContext(ctx)

	client := &http.Client{Timeout: judgeTimeout}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("judge: 上游回 %d：%s", response.StatusCode, string(body))
	}

	var parsed struct {
		Answers json.RawMessage `json:"answers"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("judge: 响应不是 JSON：%w", err)
	}
	if len(parsed.Answers) == 0 {
		return nil, fmt.Errorf("judge: 响应里没有 answers")
	}
	var answers Answers
	if err := json.Unmarshal(parsed.Answers, &answers); err != nil {
		return nil, fmt.Errorf("judge: answers 不是 object：%w", err)
	}
	return answers, nil
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

// Service：`judge` 能力的唯一实现处（`Evaluate` 之上的开关 + 选渠道 + 挂号）。
type Service struct {
	Store *store.Store
	Paths config.Paths
	Tasks *task.Registry
}

func New(st *store.Store, paths config.Paths, tasks *task.Registry) *Service {
	return &Service{Store: st, Paths: paths, Tasks: tasks}
}

// JudgeFor：问一次 JEV —— state 与 questions 由调用方拼（动态组合术就住在那儿），
// 这里只管"开关 + 选渠道 + 挂号 + 发出去"。产出是材料：**不落库**（调用方处置）。
//
// 能力关掉 ⇒ 明确拒绝；渠道不在 / 模型为空 ⇒ invalid；上游失败 ⇒ 原样报错
// （**不重试、不吞错**；失败降级由调用方决定 —— 与 `title.Generate` 同一条脾气）。
func (s *Service) JudgeFor(ctx context.Context, session model.Session, state any, questions map[string]any) (Answers, error) {
	_, agents, providersConfig, err := config.Load(s.Paths)
	if err != nil {
		// 读不出来就说读不出来：能力开关默认"全开"，静默按默认跑会让"配了要关"的人以为关上了
		return nil, &Error{Code: "internal", Message: "读配置失败：" + err.Error()}
	}
	agent, _ := agents.Resolve(session.AgentID)
	setting := abilities.Resolve(agent, abilities.Judge)
	if !setting.Enabled {
		return nil, invalid("会话 %s 的 agent（%s）关掉了 judge 能力（abilities.judge.enabled = false）："+
			"判断不做就是不做，不会降级执行", shortID(session.ID), agentLabel(agent))
	}

	// 生效渠道 / 模型：能力上填了就用它，没填就骑会话的
	// （辅助调用本来就要同一个会话 —— 与 compact/title 同一条回退）。
	providerID, modelID := session.Provider, session.Model
	if setting.Provider != "" {
		providerID = setting.Provider
	}
	if setting.Model != "" {
		modelID = setting.Model
	}
	provider, ok := providersConfig.Get(providerID)
	if !ok {
		return nil, invalid("这条会话没有可用的渠道（渠道 %q 不在 providers.json 里）："+
			"判断要真调一次上游 —— 先配好渠道", providerID)
	}
	if modelID == "" {
		// JEV 的模型名（如 jev-latest）与聊天模型不在一张表里 ⇒ 不能复用"模型名空=fallback"；
		// 空就是没配，说清楚。
		return nil, invalid("judge 能力没有模型名（能力覆盖的 model 与会话的 model 都是空）："+
			"填一个 JEV 模型名（如 jev-latest）")
	}

	// 挂号：**会调模型的作业必带会话 id** ⇒ `Begin` 自己会断言
	guard := s.Tasks.Begin(task.KindJudgement, &session.ID, "判断 · 会话 "+shortID(session.ID))
	defer guard.Interrupted() // 忘了收 / panic ⇒ 记成"中断"，绝不留僵尸条目

	wire := providers.FromConfig(provider)
	answers, err := Evaluate(ctx, wire, modelID, state, questions)
	if err != nil {
		guard.Fail(err.Error())
		return nil, upstream(err.Error())
	}
	guard.Succeed()
	return answers, nil
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
