package model

import (
	"encoding/json"
	"slices"
)

// Exchange 是产生一条已归档回复的上游调用。一个 run 内按消息 ID 排列各次调用。
// 旧消息缺省此记录；不根据会话今天的渠道推断历史。Task 仍由进程内登记表管理。
type Exchange struct {
	NodeID       string          `json:"node_id,omitempty"` // Flow 节点产物可能经过后续变换。
	Provider     string          `json:"provider"`
	Model        string          `json:"model"`
	Protocol     string          `json:"protocol"`
	EndpointHash string          `json:"endpoint_hash,omitempty"`
	Usage        json.RawMessage `json:"usage,omitempty"`
	ResponseID   string          `json:"response_id,omitempty"`
	FinishReason string          `json:"finish_reason,omitempty"`
	Items        []OutputItem    `json:"items,omitempty"`
}

// OutputItem 按上游输出顺序保存。CallID 配对 message_tools 的参数和执行结果，
// ID 是上游 item 的身份。Payload 留给带签名的推理项、托管工具和未知扩展。
type OutputItem struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	CallID  string          `json:"call_id,omitempty"`
	Text    string          `json:"text,omitempty"`
	Phase   string          `json:"phase,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// ForStorage 落实渠道的思考留档开关。本轮工具续传仍能使用内存中的完整输出。
func (e *Exchange) ForStorage(keepReasoning bool) *Exchange {
	if e == nil || keepReasoning || !slices.ContainsFunc(e.Items, func(item OutputItem) bool { return item.Type == "reasoning" }) {
		return e
	}
	archive := *e
	archive.Items = nil
	for _, item := range e.Items {
		if item.Type != "reasoning" {
			archive.Items = append(archive.Items, item)
		}
	}
	return &archive
}

// DirectExchange 返回直接生成这条消息的调用。Flow 的图输出可能拼接、重排多个节点。
func (m Message) DirectExchange() *Exchange {
	if len(m.Exchanges) == 1 && m.Exchanges[0] != nil && m.Exchanges[0].NodeID == "" {
		return m.Exchanges[0]
	}
	return nil
}
