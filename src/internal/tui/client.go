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

type Conversation struct {
	ID       string     `json:"id"`
	Title    string     `json:"title"`
	Provider string     `json:"provider"`
	Model    string     `json:"model"`
	AgentID  string     `json:"agent_id"`
	Turn     TurnStatus `json:"turn"`
}

// UpdatedAt：列表按它排序（后端已排好，这里只是留着看）。
type ConversationView = Conversation

type Message struct {
	ID         string  `json:"id"`
	Role       string  `json:"role"`
	Content    string  `json:"content"`
	Reasoning  string  `json:"reasoning,omitempty"`
	DurationMS *int64  `json:"duration_ms,omitempty"`
	ParentID   *string `json:"parent_id"`
}

type Health struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

func (c *Client) get(path string, into any) error {
	request, err := http.NewRequest(http.MethodGet, c.BaseURL+"/api/v1"+path, nil)
	if err != nil {
		return err
	}
	if c.Token != "" {
		request.Header.Set("Authorization", "Bearer "+c.Token)
	}
	response, err := c.HTTP.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		// 错误体固定 {"error":{"code","message"}} —— 客户端按 code 分支，别匹配文案
		var failure struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &failure) == nil && failure.Error.Code != "" {
			return fmt.Errorf("%s（%d）：%s", failure.Error.Code, response.StatusCode, failure.Error.Message)
		}
		return fmt.Errorf("HTTP %d：%s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, into)
}

// post：带 JSON 体的写操作（错误体的处理与 get 一致）。
func (c *Client) post(path string, body any, into any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequest(http.MethodPost, c.BaseURL+"/api/v1"+path, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		request.Header.Set("Authorization", "Bearer "+c.Token)
	}
	response, err := c.HTTP.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
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
	return json.Unmarshal(raw, into)
}

// CreateConversation：新建一条会话（省略字段时后端取 config.json 的 defaults）。
func (c *Client) CreateConversation() (Conversation, error) {
	var conversation Conversation
	err := c.post("/conversations", map[string]any{}, &conversation)
	return conversation, err
}

func (c *Client) Health() (Health, error) {
	var health Health
	err := c.get("/health", &health)
	return health, err
}

func (c *Client) Conversations() ([]Conversation, error) {
	var conversations []Conversation
	err := c.get("/conversations", &conversations)
	return conversations, err
}

func (c *Client) Messages(conversationID string) ([]Message, error) {
	var messages []Message
	err := c.get("/conversations/"+conversationID+"/messages", &messages)
	return messages, err
}

// State：世界状态的快照（调试面板要看它）。
type State struct {
	Effective map[string]string            `json:"effective"`
	Tables    map[string]map[string]string `json:"tables"`
}

func (c *Client) State(conversationID string) (State, error) {
	var state State
	err := c.get("/conversations/"+conversationID+"/state", &state)
	return state, err
}
