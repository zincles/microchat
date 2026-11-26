package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"microchat/internal/config"
	"microchat/internal/model"
	"microchat/internal/providers"
	"microchat/internal/statelang"
	"microchat/internal/turn"
)

// 同一份工具清单用于预演、声明、执行授权和留档。模型渠道与搜索执行渠道可分别选择。
type turnTools struct {
	declared []providers.Tool
	executor providers.Provider
	replayReasoning bool
}

func (s *Service) toolsFor(session model.Session, chosen providers.Backend, protocol providers.Protocol, channels config.ProvidersConfig) (turnTools, error) {
	var tools turnTools
	if chosen.Name == providers.BackendDummy || chosen.Name == providers.BackendFallback {
		return tools, nil
	}
	cfg, err := config.LoadConfig(s.Paths)
	if err != nil {
		return tools, err
	}
	channel := chosen.Provider
	profile := cfg.ModelProfileWithRulesFor(channel.ID, session.Model, string(protocol), config.ModelProfile{}, channel.ModelDefineRules)
	if channel.LocalToolProvider != "" {
		executor, ok := channels.Get(channel.LocalToolProvider)
		if !ok || executor.Vendor != string(providers.VendorDeepseek) {
			return tools, fmt.Errorf("本地搜索执行渠道 %q 必须是已配置的 DeepSeek 渠道", channel.LocalToolProvider)
		}
		tools.executor = providers.FromConfig(executor)
	} else if channel.Vendor == string(providers.VendorDeepseek) {
		tools.executor = providers.FromConfig(channel)
	}
	local := map[string]bool{}
	if tools.executor.ID != "" {
		for _, t := range providers.DeepSeekTools() {
			name := t.Function.Name
			if slices.Contains(session.NativeToolsDisabled, name) {
				continue
			}
			if channel.Vendor == string(providers.VendorDeepseek) || slices.Contains(profile.LocalTools, name) {
				tools.declared = append(tools.declared, t)
				local[name] = true
			}
		}
	}
	if channel.Vendor != string(providers.VendorDeepseek) {
		for _, name := range profile.ServerTools {
			if !local[name] && !slices.Contains(session.NativeToolsDisabled, name) {
				tools.declared = append(tools.declared, providers.ServerTool(name))
			}
		}
	}
	tools.replayReasoning = profile.ReplayReasoningForToolCount(len(tools.declared))
	return tools, nil
}

// roundProgress 的生命周期是一发模型请求，取消只归档当前一发已收到的内容。
type roundProgress struct {
	turns           *turn.Registry
	session         string
	token           uint64
	text, reasoning strings.Builder
	firstTextAt     time.Time
	tools           []model.MessageTool
	hosted          map[string]int
}

func (p *roundProgress) delta(d providers.Delta) {
	if d.Tool != nil {
		p.hostedTool(*d.Tool)
	}
	if d.Reasoning != "" {
		p.reasoning.WriteString(d.Reasoning)
		p.turns.AppendReasoning(p.session, p.token, d.Reasoning)
	}
	if d.Text != "" {
		if p.firstTextAt.IsZero() {
			p.firstTextAt = time.Now()
		}
		p.text.WriteString(d.Text)
		p.turns.AppendContent(p.session, p.token, d.Text)
	}
}

func (p *roundProgress) offset() int {
	return utf8.RuneCountInString(statelang.Scan(p.text.String()).Cleaned)
}

func (p *roundProgress) hostedTool(e providers.ToolEvent) {
	if e.ItemID == "" {
		p.turns.SetTool(p.session, p.token, e.Kind, e.State)
		return
	}
	p.rememberHostedTool(e, true)
	p.turns.SetHostedTools(p.session, p.token, e.Kind, e.State, p.tools)
}

func (p *roundProgress) rememberHostedTool(e providers.ToolEvent, streamed bool) {
	i, exists := p.hosted[e.ItemID]
	if !exists {
		i = len(p.tools)
		if p.hosted == nil {
			p.hosted = make(map[string]int)
		}
		p.hosted[e.ItemID] = i
		tool := model.MessageTool{Source: "server", ItemID: e.ItemID, ToolName: e.Kind}
		if streamed {
			tool.BodyOffset = new(p.text.Len())
			tool.TextOffset = new(p.offset())
		}
		p.tools = append(p.tools, tool)
	}
	t := &p.tools[i]
	if e.State != "" {
		t.Status = e.State
		if t.Status == "completed" {
			t.Status = "done"
		}
	}
	if len(e.Queries) != 0 {
		t.Queries = e.Queries
	}
	if len(e.Sources) != 0 {
		t.Sources = e.Sources
		p.turns.AddSources(p.session, p.token, e.Sources)
	}
}

// 完整输出项能恢复并行调用在正文中的位置，即使网关只发了终帧。
func (p *roundProgress) finish(r providers.Result) {
	for _, event := range r.ServerTools {
		if event.ItemID != "" {
			if event.State == "" {
				event.State = "completed"
			}
			p.rememberHostedTool(event, false)
		}
	}
	// 终态可能补齐迟到工具的位置，但只在该坐标与已经播出的正文一致时更新实时预览。
	// 归档消息由列表接口按保存的输出项定位，不依赖事件到达顺序。
	if len(p.tools) != 0 && p.text.String() == r.Text {
		r.Exchange.LocateToolBodies(r.Text, p.tools)
		p.turns.SetHostedTools(p.session, p.token, "", "", p.tools)
	}
	if r.Exchange == nil || r.Exchange.Protocol != string(providers.ProtocolOpenAIResponse) || len(r.ToolCalls) == 0 {
		return
	}
	offsets := make(map[string]int, len(r.ToolCalls))
	var prefix strings.Builder
	for _, part := range r.Exchange.Items {
		switch part.Type {
		case "message":
			prefix.WriteString(part.Text)
		case "function_call":
			offsets[part.CallID] = utf8.RuneCountInString(statelang.Scan(prefix.String()).Cleaned)
		}
	}
	for i := range r.ToolCalls {
		if pos, ok := offsets[r.ToolCalls[i].ID]; ok {
			r.ToolCalls[i].TextOffset = &pos
		}
	}
}

// 一批调用先整批验证，再执行。普通工具错误作为 function 结果回填；取消立即结束这一轮。
func (t turnTools) execute(ctx context.Context, calls []providers.ToolCall, p *roundProgress) error {
	offset := new(p.offset())
	for _, call := range calls {
		if err := ctx.Err(); err != nil {
			return err
		}
		p.turns.SetTool(p.session, p.token, call.Name, "in_progress")
		content, sources, err := providers.RunDeepSeekTool(ctx, t.executor, call)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		status := "done"
		if err != nil {
			status = "failed"
			raw, _ := json.Marshal(map[string]string{"error": err.Error()})
			content = string(raw)
		}
		p.turns.SetTool(p.session, p.token, call.Name, status)
		p.turns.AddSources(p.session, p.token, sources)
		pos := call.TextOffset
		if pos == nil {
			pos = offset
		}
		p.tools = append(p.tools, model.MessageTool{Source: "client", ToolName: call.Name, ItemID: call.ID,
			Arguments: call.Arguments, Result: content, Sources: sources, Status: status,
			Queries: localToolQueries(call), TextOffset: pos})
	}
	return nil
}
