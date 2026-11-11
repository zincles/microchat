// FlowAgent 的聊天运行时外壳 —— 真正执行图的是 flowtest.go 的 executeFlowGraph
// （编辑器测试与聊天共用同一份 switch），这里只负责把它接进 chat 的受理/任务/落库。
package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"microchat/internal/config"
	"microchat/internal/model"
	"microchat/internal/providers"
	"microchat/internal/statelang"
	"microchat/internal/task"
)

// flowAgentOf：会话绑的是不是 FlowAgent。不是 / 悬空 ⇒ false（继续走普通装配）。
func (s *Service) flowAgentOf(session model.Session) (config.Agent, bool) {
	_, agents, _ := s.files()
	agent, ok := agents.Get(session.AgentID)
	return agent, ok && agent.Kind == "flow"
}

// flowConversation：给图的 history / input 节点准备材料。
//   - history = 当前用户消息之前的消息（第一句话 ⇒ 空）；
//   - input = 本轮用户消息（Accept 已经落库；Resend 没有新消息时取最后一条 user）。
//
// 历史先按存库正文剔掉 <state>：Flow 节点是手写 messages，不能把状态块当正文发出去。
// 摘要/状态注入先不在 MVP 里做（见 AGENTS.md 的接线阶段记录）。
func (s *Service) flowConversation(session model.Session, runID, input string) ([]providers.ChatMessage, string, error) {
	messages, err := s.Store.ListMessages(session.ID)
	if err != nil {
		return nil, "", err
	}
	cut := len(messages)
	if strings.TrimSpace(input) != "" {
		for index := len(messages) - 1; index >= 0; index-- {
			if messages[index].ID == runID && messages[index].Role == model.RoleUser {
				cut = index
				break
			}
		}
		// Resend 的 runID 是回复 id，不是某条 user；此时按最后一条 user 截。
		if cut == len(messages) {
			for index := len(messages) - 1; index >= 0; index-- {
				if messages[index].Role == model.RoleUser {
					cut = index
					break
				}
			}
		}
	} else {
		for index := len(messages) - 1; index >= 0; index-- {
			if messages[index].Role == model.RoleUser {
				input = statelang.Scan(messages[index].Content).Cleaned
				cut = index
				break
			}
		}
	}
	history := make([]providers.ChatMessage, 0, cut)
	for _, message := range messages[:cut] {
		content := statelang.Scan(message.Content).Cleaned
		if strings.TrimSpace(content) == "" {
			continue
		}
		history = append(history, providers.ChatMessage{Role: string(message.Role), Content: content})
	}
	return history, input, nil
}

// runFlow：后台那半程。先完整执行图，再一次性 INSERT —— 取消/失败都不会留半条回复。
func (s *Service) runFlow(session model.Session, replyID string, token uint64, runID, input string,
	agent config.Agent, providersConfig config.ProvidersConfig) {
	sessionID := session.ID
	guard := s.Tasks.Begin(task.KindTurn, &sessionID, "生成 · 会话 "+shortID(sessionID))
	defer guard.Interrupted()
	guard.Cancellable(func() { s.Turns.Stop(sessionID) })

	ctx, cancel := context.WithCancel(context.Background())
	s.Turns.Attach(sessionID, token, cancel)
	defer cancel()

	started := time.Now()
	s.setFlowTrace(FlowRunTrace{SessionID: sessionID, Status: "running", StartedAt: started.UnixMilli()})
	failTrace := func(err error) {
		s.setFlowTrace(FlowRunTrace{
			SessionID: sessionID, Status: "error", Error: err.Error(),
			StartedAt: started.UnixMilli(), DurationMS: time.Since(started).Milliseconds(),
		})
	}
	history, resolvedInput, err := s.flowConversation(session, runID, input)
	if err != nil {
		failTrace(err)
		s.Turns.Finish(sessionID, token, err.Error())
		guard.Fail(err.Error())
		return
	}
	var graph flowGraph
	if err := json.Unmarshal(agent.Flow, &graph); err != nil {
		err = fmt.Errorf("流程 JSON 解析失败：%w", err)
		failTrace(err)
		s.Turns.Finish(sessionID, token, err.Error())
		guard.Fail(err.Error())
		return
	}
	outcome, err := s.executeFlowGraph(ctx, graph, flowRunOptions{
		SessionID:       sessionID,
		Input:           resolvedInput,
		History:         history,
		DefaultProvider: session.Provider,
		DefaultModel:    session.Model,
		ProvidersConfig: providersConfig,
	})
	if err != nil {
		if !s.Turns.IsMine(sessionID, token) {
			s.setFlowTrace(FlowRunTrace{
				SessionID: sessionID, Status: "canceled", Steps: outcome.Result.Steps,
				StartedAt: started.UnixMilli(), DurationMS: time.Since(started).Milliseconds(),
			})
			guard.Cancel()
			return
		}
		s.setFlowTrace(FlowRunTrace{
			SessionID: sessionID, Status: "error", Error: err.Error(), Steps: outcome.Result.Steps,
			StartedAt: started.UnixMilli(), DurationMS: time.Since(started).Milliseconds(),
		})
		s.Turns.Finish(sessionID, token, err.Error())
		guard.Fail(err.Error())
		return
	}
	if !s.Turns.IsMine(sessionID, token) {
		// 被按停 / 被顶掉：结果直接丢掉（用户按的是“取消 = 没做过”）。
		s.setFlowTrace(FlowRunTrace{
			SessionID: sessionID, Status: "canceled", Steps: outcome.Result.Steps,
			StartedAt: started.UnixMilli(), DurationMS: time.Since(started).Milliseconds(),
		})
		guard.Cancel()
		return
	}

	durationMS := time.Since(started).Milliseconds()
	message := model.Message{
		ID: replyID, SessionID: sessionID, Role: model.RoleAssistant,
		Kind: model.MessageKindChat, Status: model.MessageStatusDone, RunID: runID,
		Content: outcome.Final.Text, DurationMS: &durationMS, Usage: outcome.Usage,
	}
	// 思考只来自 output 的 reasoning 端口；不再从任意 LLM 节点自动回填
	// （那会把“正文就是思考”的图又显示成两遍思考）。
	message.Reasoning = strings.TrimSpace(outcome.Final.Reasoning)
	if _, err := s.Store.InsertMessage(message); err != nil {
		failTrace(err)
		s.Turns.Finish(sessionID, token, err.Error())
		guard.Fail(err.Error())
		return
	}
	s.setFlowTrace(FlowRunTrace{
		SessionID: sessionID, Status: "done", Final: outcome.Final.Text, Steps: outcome.Result.Steps,
		StartedAt: started.UnixMilli(), DurationMS: time.Since(started).Milliseconds(),
	})
	if s.Titles != nil {
		s.Titles.Auto(session)
	}
	s.Turns.Finish(sessionID, token, "")
	guard.Succeed()
	if s.Compactor != nil {
		go s.MaybeAutoCompact(session)
	}
}

// FlowRunTrace：最近一次 FlowAgent 图执行（进程内，覆盖式）。它是“整次图执行”的留档，
// 而不是普通 Agent 那种一个 LastPayload —— 一张图可能有多个上游请求。
type FlowRunTrace struct {
	SessionID  string         `json:"session_id"`
	Status     string         `json:"status"` // running / done / error / canceled
	Final      string         `json:"final,omitempty"`
	StartedAt  int64          `json:"started_at"`
	DurationMS int64          `json:"duration_ms"`
	Error      string         `json:"error,omitempty"`
	Steps      []FlowTestStep `json:"steps"`
}

func (s *Service) setFlowTrace(trace FlowRunTrace) {
	s.flowTraceMu.Lock()
	defer s.flowTraceMu.Unlock()
	if s.flowTraces == nil {
		s.flowTraces = map[string]FlowRunTrace{}
	}
	s.flowTraces[trace.SessionID] = trace
}

// FlowTrace：读最近一次图执行；没有跑过 ⇒ false（别编一份空轨迹）。
func (s *Service) FlowTrace(sessionID string) (FlowRunTrace, bool) {
	s.flowTraceMu.Lock()
	defer s.flowTraceMu.Unlock()
	trace, ok := s.flowTraces[sessionID]
	return trace, ok
}

// RerollFlow：FlowAgent 的消息重摇。取最后一条 assistant 之前的那条 user 当 input，
// 更早的消息当 history，执行图，把 output 的正文/思考交回 reroll 当候选。
// 这是**非流式**的：候选只在摇完后出现（FlowAgent 一轮本身也还不流式）。
func (s *Service) RerollFlow(ctx context.Context, session model.Session,
	emit func(text, reasoning string, usage json.RawMessage, durationMS, reasoningMS *int64)) (bool, error) {
	agent, ok := s.flowAgentOf(session)
	if !ok {
		return false, nil
	}
	messages, err := s.Store.ListMessages(session.ID)
	if err != nil {
		return true, err
	}
	if len(messages) == 0 || messages[len(messages)-1].Role != model.RoleAssistant {
		return true, fmt.Errorf("这条会话的最后一条不是 assistant 回复，没有什么可重摇的")
	}
	cut := 0
	input := ""
	for index := len(messages) - 2; index >= 0; index-- {
		if messages[index].Role == model.RoleUser {
			cut = index
			input = statelang.Scan(messages[index].Content).Cleaned
			break
		}
	}
	if strings.TrimSpace(input) == "" {
		return true, fmt.Errorf("这条 assistant 回复前面没有可用的 user 输入")
	}
	history := make([]providers.ChatMessage, 0, cut)
	for _, message := range messages[:cut] {
		content := statelang.Scan(message.Content).Cleaned
		if strings.TrimSpace(content) != "" {
			history = append(history, providers.ChatMessage{Role: string(message.Role), Content: content})
		}
	}
	var graph flowGraph
	if err := json.Unmarshal(agent.Flow, &graph); err != nil {
		return true, fmt.Errorf("流程 JSON 解析失败：%w", err)
	}
	started := time.Now()
	_, _, providersConfig := s.files()
	outcome, err := s.executeFlowGraph(ctx, graph, flowRunOptions{
		SessionID:       session.ID,
		Input:           input,
		History:         history,
		DefaultProvider: session.Provider,
		DefaultModel:    session.Model,
		ProvidersConfig: providersConfig,
	})
	if err != nil {
		return true, err
	}
	durationMS := time.Since(started).Milliseconds()
	emit(outcome.Final.Text, outcome.Final.Reasoning, outcome.Usage, &durationMS, nil)
	return true, nil
}
