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

type TurnStatus struct {
	Phase     string `json:"phase"`
	ElapsedMS int64  `json:"elapsed_ms"`
	Chars     int    `json:"chars"`
	Error     string `json:"error,omitempty"`
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
	ID              string  `json:"id"`
	Role            string  `json:"role"`
	Content         string  `json:"content"`
	Reasoning       string  `json:"reasoning,omitempty"`
	DurationMS      *int64  `json:"duration_ms,omitempty"`
	ParentMessageID *string `json:"parent_message_id"`
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
		response.StatusCode != http.StatusNoContent {
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

// DeleteSession：删掉整条会话（`/delete`）。
func (c *Client) DeleteSession(id string) error { return c.del("/sessions/" + id) }

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
