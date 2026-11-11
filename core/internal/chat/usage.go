// 预算与占用：`GET /sessions/{session_id}/context` 的那几个数字（**全是估算**）。
//
// 口径（照旧版，一字不改）：
//
//	ctx_len        = 用户覆盖 > 上游发现 > 设置兜底（`registry.ContextLen` 一处解析）
//	max_output     = 上游发现，缺省 4096
//	budget_tokens  = ctx_len − 输出预留（**没有别的上限** —— 别吃到爆由触发阈值负责）
//	trigger_tokens = 用户设的，缺省 = 预算 × 0.8（自动压缩在 80% 触发）
//	used_tokens    = 这次出站全部文本的估算（`state.EstimateTokens`）
//	remaining      = 预算 − 占用，**超了就照实报负数**（夹到 0 会把"超了多少"藏起来）
//
// `last_prompt_tokens` 是唯一来自上游的地面真相（上一轮 `usage` 报的）—— 摆出来是为了让
// "估算漂没漂"一眼可见。
package chat

import (
	"encoding/json"
	"strings"

	"microchat/internal/config"
	"microchat/internal/model"
	"microchat/internal/providers"
	"microchat/internal/registry"
	"microchat/internal/state"
)

// defaultMaxOutput：上游没报 `max_output` 时的输出预留。
const defaultMaxOutput = 4096

// ContextUsage：`GET /sessions/{session_id}/context` 的响应 —— **只有数字**
// （不必为了一个数去拉整份 `/outgoing`：长战役那是上百 KB）。
type ContextUsage struct {
	UsedTokens      int     `json:"used_tokens"`
	BudgetTokens    int     `json:"budget_tokens"`
	TriggerTokens   int     `json:"trigger_tokens"`
	RemainingTokens int     `json:"remaining_tokens"`
	CtxLen          int     `json:"ctx_len"`
	MaxOutput       int     `json:"max_output"`
	Ratio           float64 `json:"ratio"`
	// Estimated：恒 true —— 这是估算，不是上游计数。
	Estimated        bool `json:"estimated"`
	LastPromptTokens int  `json:"last_prompt_tokens"`
	OverBudget       bool `json:"over_budget"`
	// FlowNodes：FlowAgent 的节点级上下文。普通 Prompt Agent 不出现（omitempty）。
	// 顶层 used/budget 取其中**占用比例最高**的节点 —— 环只回答“哪个节点最紧”。
	FlowNodes []FlowContextNode `json:"flow_nodes,omitempty"`
}

type FlowContextNode struct {
	ID           string  `json:"id"`
	Label        string  `json:"label"`
	Provider     string  `json:"provider"`
	Model        string  `json:"model"`
	UsedTokens   int     `json:"used_tokens"`
	BudgetTokens int     `json:"budget_tokens"`
	CtxLen       int     `json:"ctx_len"`
	MaxOutput    int     `json:"max_output"`
	PromptTokens int     `json:"prompt_tokens"`
	Ratio        float64 `json:"ratio"`
	OverBudget   bool    `json:"over_budget"`
}

// ContextUsage：算这次出站的占用。
func (s *Service) ContextUsage(session model.Session) (ContextUsage, error) {
	chatConfig, _, _ := s.files()
	if _, ok := s.flowAgentOf(session); ok {
		trace, hasTrace := s.FlowTrace(session.ID)
		if !hasTrace {
			return ContextUsage{Estimated: true}, nil
		}
		return s.flowContextUsage(session, trace, chatConfig), nil
	}
	messages, err := s.Store.ListMessages(session.ID)
	if err != nil {
		return ContextUsage{}, err
	}
	summaries, err := s.Store.ListSummaries(session.ID)
	if err != nil {
		return ContextUsage{}, err
	}
	prompt, _ := s.EffectiveSystemPrompt(session)
	entries, presetActive := s.promptEntries(session, messages)
	if presetActive {
		prompt = strings.TrimSpace(session.SystemPrompt)
	}
	tables := state.FromSources(session.ID, messages).Tables
	// 与真发出去的那一份**同一个拼装**（用 /outgoing 核对时不该出现第二份答案）
	outgoing := s.renderMacros(session, state.BuildOutgoingWithPromptEntries(
		prompt, messages, summaries, tables, false, entries))

	row, err := s.Store.GetModel(session.Provider, session.Model)
	if err != nil {
		return ContextUsage{}, err
	}
	var (
		override, discovered, reportedMaxOutput *int64
		tokenizer                               json.RawMessage
	)
	if row != nil {
		override, discovered = row.ContextOverride, row.ContextLength
		reportedMaxOutput, tokenizer = row.MaxOutput, row.Tokenizer
	}
	ctxLen := registry.ContextLen(override, discovered, chatConfig.ModelContextTokens)
	maxOutput := defaultMaxOutput
	if reportedMaxOutput != nil && *reportedMaxOutput >= 0 {
		maxOutput = int(*reportedMaxOutput)
	}
	budget := ctxLen - maxOutput
	if budget < 0 {
		budget = 0
	}
	trigger := budget * 8 / 10
	if chatConfig.CompactTriggerTokens != nil {
		trigger = *chatConfig.CompactTriggerTokens
	}
	ratio := state.TokenizerRatio(tokenizer)
	used := 0
	for _, item := range outgoing {
		used += state.EstimateTokens(item.Content, ratio)
	}
	return ContextUsage{
		UsedTokens:       used,
		BudgetTokens:     budget,
		TriggerTokens:    trigger,
		RemainingTokens:  budget - used,
		CtxLen:           ctxLen,
		MaxOutput:        maxOutput,
		Ratio:            ratio,
		Estimated:        true,
		LastPromptTokens: lastPromptTokens(messages),
		OverBudget:       used > budget,
	}, nil
}

// flowContextUsage：FlowAgent 的上下文账。每个 LLM 节点按**自己的模型**算预算，
// 顶层取占用比例最高的那个（环不能把不同模型的分母混成一个）。
func (s *Service) flowContextUsage(session model.Session, trace FlowRunTrace, chatConfig config.ChatConfig) ContextUsage {
	out := ContextUsage{Estimated: true}
	for _, step := range trace.Steps {
		if step.Type != "llm" {
			continue
		}
		used := 0
		if len(step.Usage) > 0 {
			var usage providers.Usage
			if json.Unmarshal(step.Usage, &usage) == nil {
				used = int(usage.PromptTokens)
			}
		}
		provider, modelID := step.Provider, step.Model
		if strings.TrimSpace(provider) == "" {
			provider = session.Provider
		}
		if strings.TrimSpace(modelID) == "" {
			modelID = session.Model
		}
		ctxLen, maxOutput, budget := s.modelBudget(provider, modelID, chatConfig)
		ratio := 0.0
		if budget > 0 {
			ratio = float64(used) / float64(budget)
		}
		node := FlowContextNode{
			ID: step.ID, Label: step.Label, Provider: provider, Model: modelID,
			UsedTokens: used, BudgetTokens: budget, CtxLen: ctxLen, MaxOutput: maxOutput,
			PromptTokens: used, Ratio: ratio, OverBudget: used > budget,
		}
		out.FlowNodes = append(out.FlowNodes, node)
		if len(out.FlowNodes) == 1 || node.Ratio > out.Ratio {
			out.UsedTokens, out.BudgetTokens, out.CtxLen, out.MaxOutput = node.UsedTokens, node.BudgetTokens, node.CtxLen, node.MaxOutput
			out.Ratio, out.OverBudget, out.LastPromptTokens = node.Ratio, node.OverBudget, node.PromptTokens
			out.TriggerTokens = budget * 8 / 10
			if chatConfig.CompactTriggerTokens != nil {
				out.TriggerTokens = *chatConfig.CompactTriggerTokens
			}
			out.RemainingTokens = budget - used
			out.Estimated = used == 0
		}
	}
	return out
}

// modelBudget：某一对 (provider, model) 的上下文上限 / 输出预留 / 输入预算。
// 与普通 Agent 的 ContextUsage 用同一套 registry 口径，只是允许每个 Flow 节点各算各的。
func (s *Service) modelBudget(provider, modelID string, chatConfig config.ChatConfig) (int, int, int) {
	row, err := s.Store.GetModel(provider, modelID)
	if err != nil {
		row = nil
	}
	var override, discovered, reportedMax *int64
	if row != nil {
		override, discovered, reportedMax = row.ContextOverride, row.ContextLength, row.MaxOutput
	}
	ctxLen := registry.ContextLen(override, discovered, chatConfig.ModelContextTokens)
	maxOutput := defaultMaxOutput
	if reportedMax != nil && *reportedMax >= 0 {
		maxOutput = int(*reportedMax)
	}
	budget := ctxLen - maxOutput
	if budget < 0 {
		budget = 0
	}
	return ctxLen, maxOutput, budget
}

// lastPromptTokens：最近一条带用量的消息里那个 `prompt_tokens`。
//
// 认不出来（老消息没有 usage，或那份 usage 不认识）⇒ 0：没数据就报没有。
func lastPromptTokens(messages []model.Message) int {
	for index := len(messages) - 1; index >= 0; index-- {
		if len(messages[index].Usage) == 0 {
			continue
		}
		var usage struct {
			PromptTokens int `json:"prompt_tokens"`
		}
		if json.Unmarshal(messages[index].Usage, &usage) == nil {
			return usage.PromptTokens
		}
	}
	return 0
}
