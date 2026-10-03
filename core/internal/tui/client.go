// Package tui：microchat 的终端客户端 —— 与后端**同一个二进制**，但走 HTTP 说话。
//
// 它是**HTTP 客户端**（与 Godot 前端同一套契约）—— 不碰后端内部包、不直连数据库，
// 所以既能连本机后端，也能连远端（平板 Termux / SSH 过去都行）。
package tui

import "microchat/internal/apiclient"

// 本文件只是后端 HTTP 封装的一层再导出：真正实现整份住在 `microchat/internal/apiclient`
// （TUI 与 TG bot 共用）。这里用类型别名 + 薄包装保留 tui 包内原有的引用面，调用方无感。

// Client：后端 REST 的一层薄封装（只做取数 / 写数，不做业务）。
type Client = apiclient.Client

// NewClient：建一个指向 baseURL、带 token 的客户端。
func NewClient(baseURL, token string) *Client { return apiclient.NewClient(baseURL, token) }

// ── 后端返回的形状（转 apiclient 的别名）──

type TurnStatus = apiclient.TurnStatus
type TurnAccepted = apiclient.TurnAccepted
type StreamSlice = apiclient.StreamSlice
type Session = apiclient.Session
type Message = apiclient.Message
type Health = apiclient.Health
type ModelListItem = apiclient.ModelListItem
type PlanUsage = apiclient.PlanUsage
type PlanWindow = apiclient.PlanWindow
type TaskRecord = apiclient.TaskRecord
type TaskBoard = apiclient.TaskBoard
type TelegramBinding = apiclient.TelegramBinding
type DeletionPlan = apiclient.DeletionPlan
type Outgoing = apiclient.Outgoing
type ProviderInfo = apiclient.ProviderInfo
type PresetItem = apiclient.PresetItem
type CompactStatus = apiclient.CompactStatus
type ContextUsage = apiclient.ContextUsage
type State = apiclient.State
type SystemPrompt = apiclient.SystemPrompt
type RerollItem = apiclient.RerollItem
type RerollState = apiclient.RerollState
type RerollAccepted = apiclient.RerollAccepted

// 重摇的两个家族（与后端的 `target_kind` 同一套词）。同一个会话同一时刻只有一个活着。
const (
	RerollKindMessage = apiclient.RerollKindMessage
	RerollKindSummary = apiclient.RerollKindSummary
)
