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
	agent config.Agent, chosen providers.Backend, providersConfig config.ProvidersConfig) {
	sessionID := session.ID
	guard := s.Tasks.Begin(task.KindTurn, &sessionID, "生成 · 会话 "+shortID(sessionID))
	defer guard.Interrupted()
	guard.Cancellable(func() { s.Turns.Stop(sessionID) })

	ctx, cancel := context.WithCancel(context.Background())
	s.Turns.Attach(sessionID, token, cancel)
	defer cancel()

	started := time.Now()
	history, resolvedInput, err := s.flowConversation(session, runID, input)
	if err != nil {
		s.Turns.Finish(sessionID, token, err.Error())
		guard.Fail(err.Error())
		return
	}
	var graph flowGraph
	if err := json.Unmarshal(agent.Flow, &graph); err != nil {
		err = fmt.Errorf("流程 JSON 解析失败：%w", err)
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
			guard.Cancel()
			return
		}
		s.Turns.Finish(sessionID, token, err.Error())
		guard.Fail(err.Error())
		return
	}
	if !s.Turns.IsMine(sessionID, token) {
		// 被按停 / 被顶掉：结果直接丢掉（用户按的是“取消 = 没做过”）。
		guard.Cancel()
		return
	}

	durationMS := time.Since(started).Milliseconds()
	message := model.Message{
		ID: replyID, SessionID: sessionID, Role: model.RoleAssistant,
		Kind: model.MessageKindChat, Status: model.MessageStatusDone, RunID: runID,
		Content: outcome.Final.Text, DurationMS: &durationMS, Usage: outcome.Usage,
	}
	if chosen.StoresReasoning() {
		message.Reasoning = lastFlowReasoning(outcome.Result.Steps)
	}
	if _, err := s.Store.InsertMessage(message); err != nil {
		s.Turns.Finish(sessionID, token, err.Error())
		guard.Fail(err.Error())
		return
	}
	if s.Titles != nil {
		s.Titles.Auto(session)
	}
	s.Turns.Finish(sessionID, token, "")
	guard.Succeed()
	if s.Compactor != nil {
		go s.MaybeAutoCompact(session)
	}
}

func lastFlowReasoning(steps []FlowTestStep) string {
	for index := len(steps) - 1; index >= 0; index-- {
		if steps[index].Type == "llm" && strings.TrimSpace(steps[index].Reasoning) != "" {
			return steps[index].Reasoning
		}
	}
	return ""
}
