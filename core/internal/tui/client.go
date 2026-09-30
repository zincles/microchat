// Package tui：microchat 的终端客户端 —— 与后端**同一个二进制**，但走 HTTP 说话。
//
// 它是**HTTP 客户端**（与 Godot 前端同一套契约）—— 不碰后端内部包、不直连数据库，
// 所以既能连本机后端，也能连远端（平板 Termux / SSH 过去都行）。
package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client：后端 REST 的一层薄封装（只做取数，不做业务）。
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func NewClient(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// ── 后端返回的形状（与 AGENTS.md 的契约一致；只取这个界面要用的字段）──

// TurnStatus：这一轮生成的状态（`GET .../status`）。
//
// `idle` 之外都别让用户再发（同一会话在跑时再发，后端会 409）。
type TurnStatus struct {
	Phase string `json:"phase"`
	// MessageID：这条**正在生成的回复**的 id（受理时定好，那会儿还没进库）。
	MessageID     *string `json:"message_id,omitempty"`
	ElapsedMS     int64   `json:"elapsed_ms"`
	Chars         int     `json:"chars"`
	ThinkingChars int     `json:"thinking_chars"`
	Error         string  `json:"error,omitempty"`
}

func (t TurnStatus) Busy() bool { return t.Phase == "pending" || t.Phase == "streaming" }

// TurnAccepted：发送的受理回执（**202**）—— 不含回复正文。
type TurnAccepted struct {
	User    *Message   `json:"user,omitempty"`
	Backend string     `json:"backend"`
	Turn    TurnStatus `json:"turn"`
}

// StreamSlice：游标读的结果（正文与思考**各一条**游标，`done` 表示这轮结束）。
type StreamSlice struct {
	Text      string `json:"text"`
	Next      int    `json:"next"`
	Thinking  string `json:"thinking"`
	ThinkNext int    `json:"think_next"`
	Done      bool   `json:"done"`
}

type Session struct {
	ID       string     `json:"id"`
	Title    string     `json:"title"`
	Provider string     `json:"provider"`
	Model    string     `json:"model"`
	AgentID  string     `json:"agent_id"`
	Turn     TurnStatus `json:"turn"`
}

// UpdatedAt：列表按它排序（后端已排好，这里只是留着看）。
type SessionView = Session

type Message struct {
	ID         string  `json:"id"`
	Role       string  `json:"role"`
	Content    string  `json:"content"`
	Reasoning  string  `json:"reasoning,omitempty"`
	// ReasoningMS：思考用时（受理 → 第一段正文）—— 折叠行「思考（2.1s）」用它。
	ReasoningMS *int64  `json:"reasoning_ms,omitempty"`
	DurationMS  *int64  `json:"duration_ms,omitempty"`
	SummaryID   *string `json:"summary_id,omitempty"`
}

type Health struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// ModelListItem：`GET /models` 的一项（跨 provider 拍平 —— 正是"按 provider 排列"要的形状）。
type ModelListItem struct {
	Provider     string  `json:"provider"`
	ProviderName *string `json:"provider_name"`
	UpstreamID   string  `json:"upstream_id"`
	Name         string  `json:"name"`
	DisplayName  *string `json:"display_name"`
}

// Label：显示名三级回退（用户覆盖 → 上游名 → upstream_id）——**只在这一处**做。
func (item ModelListItem) Label() string {
	if item.DisplayName != nil && *item.DisplayName != "" {
		return *item.DisplayName
	}
	if item.Name != "" {
		return item.Name
	}
	return item.UpstreamID
}

// ProviderLabel：渠道的显示名（缺省回退 id）。
func (item ModelListItem) ProviderLabel() string {
	if item.ProviderName != nil && *item.ProviderName != "" {
		return *item.ProviderName
	}
	return item.Provider
}

// PlanUsage：`GET /providers/{provider_id}/usage` 回的形状（套餐余量）。
// **只对 kind=opencode-go 的渠道有义** —— 别的渠道后端回 400（这里照实把消息透出去）。
type PlanUsage struct {
	ProviderID string       `json:"provider_id"`
	Plan       string       `json:"plan"`
	Windows    []PlanWindow `json:"windows"`
}

// PlanWindow：一个配额窗口（5 小时 / 每周 / 每月）。`ResetsAt` 是 RFC3339。
type PlanWindow struct {
	ID       string    `json:"id"`
	Label    string    `json:"label"`
	Percent  float64   `json:"percent"`
	Status   string    `json:"status"`
	ResetsAt time.Time `json:"resets_at"`
}

// get：只读。
func (c *Client) get(path string, into any) error { return c.do(http.MethodGet, path, nil, into) }

// do：**唯一**一处请求 + 错误体解析（GET/POST/PATCH/DELETE 共用）。
// `into == nil` 表示只关心成败（DELETE 回 204，没有响应体）。
func (c *Client) do(method, path string, body any, into any) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = strings.NewReader(string(payload))
	}
	request, err := http.NewRequest(method, c.BaseURL+"/api/v1"+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		request.Header.Set("Authorization", "Bearer "+c.Token)
	}
	response, err := c.HTTP.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	// 名字别叫 body：参数里的 `body any` 是**请求体**，读到的是**响应体**（撞过一次）
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated &&
		response.StatusCode != http.StatusAccepted && response.StatusCode != http.StatusNoContent {
		// 错误体固定 {"error":{"code","message"}} —— 客户端按 code 分支，别匹配文案
		var failure struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &failure) == nil && failure.Error.Code != "" {
			return fmt.Errorf("%s（%d）：%s", failure.Error.Code, response.StatusCode, failure.Error.Message)
		}
		return fmt.Errorf("HTTP %d：%s", response.StatusCode, strings.TrimSpace(string(raw)))
	}
	if into == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, into)
}

// post / patch / del：三个写动作，全部走 do（错误体的处理只有一处）。
func (c *Client) post(path string, body any, into any) error {
	return c.do(http.MethodPost, path, body, into)
}

func (c *Client) patch(path string, body any, into any) error {
	return c.do(http.MethodPatch, path, body, into)
}

func (c *Client) del(path string) error { return c.do(http.MethodDelete, path, nil, nil) }

// CreateSession：新建一条会话。provider/model 为空 ⇒ 交给后端的 defaults。
func (c *Client) CreateSession(provider, model string) (Session, error) {
	body := map[string]any{}
	if provider != "" {
		body["provider"] = provider
	}
	if model != "" {
		body["model"] = model
	}
	var session Session
	err := c.post("/sessions", body, &session)
	return session, err
}

// UpdateSession：改会话的渠道 / 模型（`/model` 在已选中会话上用它）。
func (c *Client) UpdateSession(id, provider, model string) (Session, error) {
	var session Session
	err := c.patch("/sessions/"+id, map[string]any{"provider": provider, "model": model}, &session)
	return session, err
}

// RenameSession：改会话标题（`/rename`）—— 复用 `PATCH /sessions/{session_id}` 的 `title` 那一格，
// **不新增路由**。改完即"用户改过名" ⇒ 后端的自动起标题**永不再覆盖**（这条不变量在后端，客户端不多事）。
func (c *Client) RenameSession(id, title string) (Session, error) {
	var session Session
	err := c.patch("/sessions/"+id, map[string]any{"title": title}, &session)
	return session, err
}

// DeleteSession：删掉整条会话（`/delete`）。
func (c *Client) DeleteSession(id string) error { return c.del("/sessions/" + id) }

// CopySession：把一条会话**复制**成新的一条（线性会话里的"分岔"，不叫 fork）。
// 消息与摘要一并复制（摘要换新 id、消息上的指针重映射）；世界状态不复制（它本来就现演）。
func (c *Client) CopySession(id string) (Session, error) {
	var session Session
	err := c.post("/sessions/"+id+"/copy", nil, &session)
	return session, err
}

// DeletionPlan：`GET .../deletion-preview` 与 `DELETE .../messages/{message_id}` 共用的形状。
// 数量不单列 —— 数组长度就是（两份数字迟早打架）。
type DeletionPlan struct {
	DeletedMessageIDs    []string `json:"deleted_message_ids"`
	DeletedSummaryIDs    []string `json:"deleted_summary_ids"`
	UnlinkedMessageIDs   []string `json:"unlinked_message_ids"`
	UnlinkedSummaryIDs   []string `json:"unlinked_summary_ids"`
	LastDeletedMessageID string   `json:"last_deleted_message_id"`
}

// DeletionPreview：**只算不动** —— 告诉用户"删下去会没掉什么"，再让他点头。
func (c *Client) DeletionPreview(sessionID, messageID string) (DeletionPlan, error) {
	var plan DeletionPlan
	err := c.get("/sessions/"+sessionID+"/messages/"+messageID+"/deletion-preview", &plan)
	return plan, err
}

// DeleteMessagesFrom：删这条消息**及其之后的全部**。
//
// `lastDeletedMessageID` = 预览里的那条（后端核对它仍是会话末尾，对不上就 409 ⇒ 重新预览）。
func (c *Client) DeleteMessagesFrom(sessionID, messageID, lastDeletedMessageID string) (DeletionPlan, error) {
	var plan DeletionPlan
	err := c.do(http.MethodDelete, "/sessions/"+sessionID+"/messages/"+messageID,
		map[string]any{"last_deleted_message_id": lastDeletedMessageID}, &plan)
	return plan, err
}

// Outgoing：**(b) 当前已定历史的载荷**（标签已剔、状态已注入、压缩已生效）—— **不含还没发出去的那句**。
// 每一条自带出处（system / message+message_id / summary+summary_id+blocks）—— 检查压缩效果就靠它。
type Outgoing struct {
	Role      string  `json:"role"`
	Content   string  `json:"content"`
	Source    string  `json:"source"`
	MessageID *string `json:"message_id"`
	SummaryID *string `json:"summary_id"`
	Blocks    *int64  `json:"blocks"`
	// Idx：这一项是**第几条消息**（system ⇒ 0，它是合成的、不是消息）；摘要在 FromIdx/ToIdx 里
	// 报"它替代了第几条到第几条"。**只服务调试**（`/outgoing` 查看器），界面别的地儿不用它。
	Idx     *int `json:"idx,omitempty"`
	FromIdx *int `json:"from_idx,omitempty"`
	ToIdx   *int `json:"to_idx,omitempty"`
}

func (c *Client) Outgoing(sessionID string) ([]Outgoing, error) {
	var items []Outgoing
	err := c.get("/sessions/"+sessionID+"/outgoing", &items)
	return items, err
}

// Models：跨 provider 的全部模型（`/model` 的面板按 provider 分组显示）。
func (c *Client) Models() ([]ModelListItem, error) {
	var items []ModelListItem
	err := c.get("/models", &items)
	return items, err
}

func (c *Client) Health() (Health, error) {
	var health Health
	err := c.get("/health", &health)
	return health, err
}

// ProviderUsage：问一条渠道的套餐余量（只读）。渠道不是 opencode-go / 没配 key ⇒ 后端 400，
// 这里把那条消息原样透出去（界面照实说一句）。
func (c *Client) ProviderUsage(providerID string) (PlanUsage, error) {
	var usage PlanUsage
	err := c.get("/providers/"+providerID+"/usage", &usage)
	return usage, err
}

func (c *Client) Sessions() ([]Session, error) {
	var sessions []Session
	err := c.get("/sessions", &sessions)
	return sessions, err
}

func (c *Client) Messages(sessionID string) ([]Message, error) {
	var messages []Message
	err := c.get("/sessions/"+sessionID+"/messages", &messages)
	return messages, err
}

// SendMessage：把一句话发出去 —— **受理与生成分开**：立刻回 202 回执（不含回复正文），
// 生成在后台跑；`accepted.Turn.MessageID` 就是那条正在生成的回复的 id。
//
// 同一会话在跑时再发 ⇒ 409（错误体里是 conflict）。
func (c *Client) SendMessage(sessionID, content string) (TurnAccepted, error) {
	var accepted TurnAccepted
	err := c.post("/sessions/"+sessionID+"/messages", map[string]any{"content": content}, &accepted)
	return accepted, err
}

// TurnStatus：这一轮现在处在哪一档（`idle` / `pending` / `streaming` / `error`）。
func (c *Client) TurnStatus(sessionID string) (TurnStatus, error) {
	var status TurnStatus
	err := c.get("/sessions/"+sessionID+"/status", &status)
	return status, err
}

// TurnText：游标读增量（正文与思考各一条游标）—— 只服务动画，读不消费。
func (c *Client) TurnText(sessionID string, from, thinkFrom int) (StreamSlice, error) {
	var slice StreamSlice
	path := fmt.Sprintf("/sessions/%s/turn/text?from=%d&think_from=%d", sessionID, from, thinkFrom)
	err := c.get(path, &slice)
	return slice, err
}

// StopTurn：按停这一轮。**幂等**：没在跑也 200（回 false）。
func (c *Client) StopTurn(sessionID string) (bool, error) {
	var result struct {
		Stopped bool `json:"stopped"`
	}
	err := c.post("/sessions/"+sessionID+"/stop", nil, &result)
	return result.Stopped, err
}

// CompactStatus：一次压缩的对外状态（`POST .../compact` 的 **202** 回执；
// 跑完的结局落在 `/status` 的 `compact` 那一档里，同一个形状）。
type CompactStatus struct {
	State     string  `json:"state"` // running / done / error
	Blocks    int     `json:"blocks"`
	Compacted int     `json:"compacted"`
	SummaryID *string `json:"summary_id,omitempty"`
	Error     string  `json:"error,omitempty"`
	AtMS      int64   `json:"at_ms"`
}

// Compact：受理一次压缩（202）。**不给 N 就用后端的默认值**（`config.json` 的 `compact_blocks`）。
//
// 它只回"受理了"——压缩在后台跑（调一次上游 + 落库）。跑完的结局在 `/status` 的压缩那一档。
func (c *Client) Compact(sessionID string, blocks int) (CompactStatus, error) {
	body := map[string]any{}
	if blocks > 0 {
		body["blocks"] = blocks
	}
	var status CompactStatus
	err := c.post("/sessions/"+sessionID+"/compact", body, &status)
	return status, err
}

// State：世界状态的快照（调试面板要看它）。
type State struct {
	Effective map[string]string            `json:"effective"`
	Tables    map[string]map[string]string `json:"tables"`
}

func (c *Client) State(sessionID string) (State, error) {
	var state State
	err := c.get("/sessions/"+sessionID+"/state", &state)
	return state, err
}

// ContextUsage：`GET /sessions/{session_id}/context` —— **只有数字**（状态行那一档用它）。
//
// ⚠ 只取界面要画的三个：占用 / 预算 / 是否超预算。其余字段本界面不画 ⇒ 不取（要用再加，别先摆着）。
// ⚠ **`ratio` 不是"用了百分之几"** ✗ —— 契约里那个 `ratio` 是**分词器标定比**
// （`state.TokenizerRatio`，用来把字数估成 token 数）；占用比例自己按 `used_tokens / budget_tokens` 算。
type ContextUsage struct {
	UsedTokens   int  `json:"used_tokens"`
	BudgetTokens int  `json:"budget_tokens"`
	OverBudget   bool `json:"over_budget"`
}

func (c *Client) Context(sessionID string) (ContextUsage, error) {
	var usage ContextUsage
	err := c.get("/sessions/"+sessionID+"/context", &usage)
	return usage, err
}

// SystemPrompt：**生效的系统提示词**（`GET /sessions/{session_id}/prompt`）—— 三级解析后的结果。
//
// `text` 就是真会发给模型的那份；`source` ∈ conversation / agent / builtin（会话覆盖 / agent 的 / 内置默认）。
// 调试用：界面把它当成一条 `role=system` 的消息摆在消息区最上方。
type SystemPrompt struct {
	Text   string `json:"text"`
	Source string `json:"source"`
}

func (c *Client) SystemPrompt(sessionID string) (SystemPrompt, error) {
	var prompt SystemPrompt
	err := c.get("/sessions/"+sessionID+"/prompt", &prompt)
	return prompt, err
}

// RerollItem：重摇里的一版（**位次不是身份** —— 它没有自己的 UUID）。
//
// `preview` 是后端截好的几十个字（不吐全文）；`pending` = 这一版还在摇；`current` = 它就是
// 库里那条 Message 现在的正文（状态行与消息块上的位次标记都照这两个标）。
type RerollItem struct {
	Idx     int    `json:"idx"`
	Preview string `json:"preview"`
	At      int64  `json:"at"`
	Pending bool   `json:"pending"`
	Current bool   `json:"current"`
}

// RerollState：`GET /sessions/{session_id}/reroll` 的形状（界面靠它画位次与「重摇中…」）。
type RerollState struct {
	Active          bool         `json:"active"`
	TargetMessageID string       `json:"target_message_id,omitempty"`
	Count           int          `json:"count"`
	CurrentIdx      int          `json:"current_idx"`
	Running         bool         `json:"running"`
	ElapsedMS       int64        `json:"elapsed_ms"`
	Error           string       `json:"error,omitempty"`
	Items           []RerollItem `json:"items"`
}

// Accepted：`POST .../reroll` 的 202 回执（受理那一刻：原文 + 正在摇的那一版）。
type RerollAccepted struct {
	TargetMessageID string      `json:"target_message_id"`
	TaskID          string      `json:"task_id,omitempty"`
	State           RerollState `json:"state"`
}

// Reroll：进重摇模式并**立刻摇一次**（已在模式里 ⇒ 再摇一版）。
func (c *Client) Reroll(sessionID string) (RerollAccepted, error) {
	var accepted RerollAccepted
	err := c.post("/sessions/"+sessionID+"/reroll", nil, &accepted)
	return accepted, err
}

// RerollState：这一条会话的重摇状态（没进模式 ⇒ `active: false`）。
func (c *Client) RerollStatus(sessionID string) (RerollState, error) {
	var state RerollState
	err := c.get("/sessions/"+sessionID+"/reroll", &state)
	return state, err
}

// RerollSwitch：选中第 N 版（后端**就地重建**那条 Message：UUID 不变，正文与附带信息一起换）。
func (c *Client) RerollSwitch(sessionID string, idx int) (RerollState, error) {
	var state RerollState
	err := c.post("/sessions/"+sessionID+"/reroll/switch", map[string]any{"idx": idx}, &state)
	return state, err
}

// RerollDelete：删掉第 N 版（后方位次统一 -1；只剩一版时后端退出模式并清列表）。
func (c *Client) RerollDelete(sessionID string, idx int) (RerollState, error) {
	var state RerollState
	err := c.do(http.MethodDelete, fmt.Sprintf("/sessions/%s/reroll/%d", sessionID, idx), nil, &state)
	return state, err
}

// RerollClear：**退出重摇模式**（清列表；库里那条 Message 保留当前这版）。
func (c *Client) RerollClear(sessionID string) error {
	return c.del("/sessions/" + sessionID + "/reroll")
}
