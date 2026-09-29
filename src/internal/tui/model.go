package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/mattn/go-runewidth"
)

// model：**界面的全部状态**（Bubble Tea 的规矩：状态只有这一处，`View()` 是它的纯函数）。
//
// 之所以选 Elm 式框架就是为了这条：`View(model) → 字符串` ⇒
// "这一屏长什么样"可以直接**喂 fixture、逐字节比**（见 model_test.go）。
type model struct {
	client *Client

	// 连接与取数
	version       string
	lastAction    string
	conversations []Conversation
	messages      []Message
	selected      int

	// 界面
	width, height int
	ready         bool
	input         string // 底下那行输入（命令与将来的消息都从这儿走）
}

// ── 消息（tea.Msg）──

type healthMsg struct {
	health Health
	err    error
}

type conversationsMsg struct {
	conversations []Conversation
	err           error
}

type messagesMsg struct {
	conversationID string
	messages       []Message
	err            error
}

type createdMsg struct {
	conversation Conversation
	err          error
}

type stateMsg struct {
	state State
	err   error
}

func initialModel(client *Client) model {
	return model{client: client, width: 80, height: 24}
}

func (m model) Init() tea.Cmd { return loadHealth(m.client) }

func loadHealth(client *Client) tea.Cmd {
	return func() tea.Msg {
		health, err := client.Health()
		return healthMsg{health: health, err: err}
	}
}

func loadConversations(client *Client) tea.Cmd {
	return func() tea.Msg {
		conversations, err := client.Conversations()
		return conversationsMsg{conversations: conversations, err: err}
	}
}

func loadMessages(client *Client, conversationID string) tea.Cmd {
	return func() tea.Msg {
		messages, err := client.Messages(conversationID)
		return messagesMsg{conversationID: conversationID, messages: messages, err: err}
	}
}

func createConversation(client *Client) tea.Cmd {
	return func() tea.Msg {
		conversation, err := client.CreateConversation()
		return createdMsg{conversation: conversation, err: err}
	}
}

func loadState(client *Client, conversationID string) tea.Cmd {
	return func() tea.Msg {
		state, err := client.State(conversationID)
		return stateMsg{state: state, err: err}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch message := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height, m.ready = message.Width, message.Height, true
		return m, nil

	case healthMsg:
		if message.err != nil {
			m.version = "" // 连不上 ⇒ 如实"未连接"，别留着上一次的版本号（界面口径：两半互不顶替）
			m.lastAction = "连不上后端：" + message.err.Error()
			return m, nil
		}
		m.version = message.health.Version
		m.lastAction = "已连接 " + m.client.BaseURL
		return m, loadConversations(m.client)

	case conversationsMsg:
		if message.err != nil {
			m.lastAction = "拉会话失败：" + message.err.Error()
			return m, nil
		}
		// 取数成功说明连上了 ⇒ 版本号还空着就补一次（初次连接失败后刷新成功时会发生）
		var also []tea.Cmd
		if m.version == "" {
			also = append(also, loadHealth(m.client))
		}
		m.conversations = message.conversations
		m.lastAction = fmt.Sprintf("会话 %d 条", len(m.conversations))
		if len(m.conversations) == 0 {
			return m, nil
		}
		if m.selected >= len(m.conversations) {
			m.selected = 0
		}
		also = append(also, loadMessages(m.client, m.conversations[m.selected].ID))
		return m, tea.Batch(also...)

	case messagesMsg:
		if message.err != nil {
			m.lastAction = "拉消息失败：" + message.err.Error()
			return m, nil
		}
		if len(m.conversations) > 0 && message.conversationID == m.conversations[m.selected].ID {
			m.messages = message.messages
			m.lastAction = fmt.Sprintf("消息 %d 条", len(m.messages))
		}
		return m, nil

	case createdMsg:
		if message.err != nil {
			m.lastAction = "新建失败：" + message.err.Error()
			return m, nil
		}
		m.lastAction = "已新建会话 " + message.conversation.ID[:8]
		return m, loadConversations(m.client)

	case stateMsg:
		if message.err != nil {
			m.lastAction = "取状态失败：" + message.err.Error()
			return m, nil
		}
		// 一行摘要（多行显示留给将来的调试面板）
		parts := make([]string, 0, len(message.state.Effective))
		for key, value := range message.state.Effective {
			parts = append(parts, key+"="+value)
		}
		sort.Strings(parts)
		if len(parts) == 0 {
			m.lastAction = "当前状态：（空）"
			return m, nil
		}
		m.lastAction = "状态：" + strings.Join(parts, " · ")
		return m, nil

	case tea.KeyPressMsg:
		// 先认输入相关的键（打字优先），再认导航键。
		// 注意：**没有裸 `q` 退出了** —— 那会和打字打架（Pi 也是 /quit 与 ctrl+c）。
		switch message.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "enter":
			return m.submit()
		case "backspace":
			if m.input != "" {
				runes := []rune(m.input)
				m.input = string(runes[:len(runes)-1])
			}
			return m, nil
		case "up", "down":
			if m.input == "" { // 输入框空着时，方向键才当导航用（有字的时候留给将来的历史）
				if message.String() == "up" && m.selected > 0 {
					m.selected--
					return m, loadMessages(m.client, m.conversations[m.selected].ID)
				}
				if message.String() == "down" && m.selected < len(m.conversations)-1 {
					m.selected++
					return m, loadMessages(m.client, m.conversations[m.selected].ID)
				}
			}
			return m, nil
		}
		if message.Text != "" { // 可打印字符（含中文、粘贴）
			m.input += message.Text
		}
	}
	return m, nil
}

// submit：回车了 —— 是命令就派发，不是就如实说"发送还没实现"。
func (m model) submit() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.input)
	m.input = ""
	if text == "" {
		return m, nil
	}
	if !strings.HasPrefix(text, "/") {
		// 发送还没搬完（③ chat 那一波）—— 如实说，不假装能发。
		m.lastAction = "发送还没实现（后端还没有 POST /messages）"
		return m, nil
	}
	return m.runCommand(text)
}

// runCommand：`/命令`（照 Pi 的用法）。**只放已经有路由的命令** ——
// 没搬完的在 /help 里如实列出来，不假装支持。
func (m model) runCommand(text string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(strings.TrimPrefix(text, "/"))
	if len(fields) == 0 {
		return m, nil
	}
	switch fields[0] {
	case "help", "h", "?":
		m.lastAction = "命令：" + strings.Join(availableCommands, " · ") + "（还没搬完的：" + strings.Join(pendingCommands, " · ") + "）"
		return m, nil
	case "quit", "q", "exit":
		return m, tea.Quit
	case "refresh", "r":
		m.lastAction = "刷新中…"
		return m, loadConversations(m.client)
	case "new", "n":
		m.lastAction = "新建会话…"
		return m, createConversation(m.client)
	case "state":
		conversation := m.currentConversation()
		if conversation == nil {
			m.lastAction = "先选一条会话（左栏）"
			return m, nil
		}
		m.lastAction = "取状态…"
		return m, loadState(m.client, conversation.ID)
	}
	m.lastAction = "不认识这个命令：" + text + "（试试 /help）"
	return m, nil
}

// availableCommands：现在真的能用的；pendingCommands：路由还没搬完的（如实列着）。
var (
	availableCommands = []string{"/help", "/new", "/refresh", "/state", "/quit"}
	pendingCommands   = []string{"/compact", "/fork", "/archive", "/stop", "/tasks", "/model"}
)

// ── 渲染 ──

const sidebarWidth = 28

// View：**纯函数**（同样的 model ⇒ 同样的字符串）。
func (m model) View() tea.View {
	return tea.NewView(m.render())
}

func (m model) render() string {
	bodyHeight := m.height - 3 // 输入行 + 分隔线 + 底栏
	if bodyHeight < 3 {
		bodyHeight = 3
	}
	sidebar := m.renderSidebar(bodyHeight)
	main := m.renderMessages(bodyHeight)
	lines := make([]string, 0, bodyHeight)
	for index := 0; index < bodyHeight; index++ {
		lines = append(lines, pad(sidebar[index], sidebarWidth)+"|"+main[index])
	}
	lines = append(lines, truncate("> "+m.input, max(1, m.width))) // 输入行（命令与将来的消息都从这儿走）
	lines = append(lines, strings.Repeat("-", max(1, m.width)))    // ASCII：`─` 是模糊宽度字符，算两格 ⇒ 会超宽
	lines = append(lines, m.renderFooter())
	return strings.Join(lines, "\n")
}

func (m model) renderSidebar(height int) []string {
	lines := make([]string, 0, height)
	lines = append(lines, "＋ 新建对话")
	lines = append(lines, strings.Repeat("-", sidebarWidth-2)) // 分隔线用 ASCII：`·` 是**模糊宽度**字符，终端里对不齐
	for index, conversation := range m.conversations {
		title := conversation.Title
		if strings.TrimSpace(title) == "" {
			title = "（还没起名）"
		}
		cursor := "  "
		if index == m.selected {
			cursor = "> "
		}
		busy := ""
		if conversation.Turn.Phase == "pending" || conversation.Turn.Phase == "streaming" {
			busy = " · 生成中…"
		}
		// **先给标记留位置**：标题再长也不能把「生成中」挤掉（那才是要看的信息）
		budget := sidebarWidth - 1 - runewidth.StringWidth(cursor) - runewidth.StringWidth(busy)
		lines = append(lines, cursor+truncate(title, budget)+busy)
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:height]
}

// renderMessages：表头**钉在顶部**；消息**从底部往上**整块排。
//
// 为什么要从底部排：聊天记录里**最新的在下面**；而且一块消息（署名 + 正文）要么整块出现、
// 要么不出现 —— 从顶部截断会把署名切掉，看起来像"不知道谁说的"（这个 bug 是测试抓出来的）。
func (m model) renderMessages(height int) []string {
	conversation := m.currentConversation()
	if conversation == nil {
		lines := []string{"（左栏选一条会话）"}
		for len(lines) < height {
			lines = append(lines, "")
		}
		return lines[:height]
	}
	header := " " + conversation.Provider + " / " + conversation.Model + " · agent " + conversation.AgentID
	head := []string{truncate(header, m.mainWidth())}
	if height <= len(head) {
		return head[:height]
	}

	// 从最后一块往前攒，攒到装不下为止
	blocks := make([][]string, 0, len(m.messages))
	for _, message := range m.messages {
		label := "你"
		if message.Role == "assistant" {
			label = "助手" // TODO：等 /agents 搬完，改用该会话 agent 的名字（界面口径见 AGENTS.md）
		}
		detail := ""
		if message.DurationMS != nil {
			detail = fmt.Sprintf(" %.1fs", float64(*message.DurationMS)/1000)
		}
		block := []string{truncate(" "+label+detail+"：", m.mainWidth())}
		for _, line := range strings.Split(message.Content, "\n") {
			block = append(block, truncate("   "+line, m.mainWidth()))
		}
		block = append(block, "")
		blocks = append(blocks, block)
	}
	budget := height - len(head)
	taken := len(blocks)
	used := 0
	for taken > 0 {
		size := len(blocks[taken-1])
		if used+size > budget {
			break
		}
		used += size
		taken--
	}
	dropped := taken // 上面还有几条没显示
	lines := append([]string{}, head...)
	lines = append(lines, "")
	used = height - len(head) - 1
	for _, block := range blocks[taken:] {
		lines = append(lines, block...)
		used -= len(block)
	}
	_ = used
	if dropped > 0 {
		// 挤掉了几条就如实说一句（别让界面骗人）
		lines[1] = truncate(fmt.Sprintf(" （上面还有 %d 条）", dropped), m.mainWidth())
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:height]
}

func (m model) currentConversation() *Conversation {
	if m.selected < 0 || m.selected >= len(m.conversations) {
		return nil
	}
	return &m.conversations[m.selected]
}

func (m model) mainWidth() int { return max(1, m.width-sidebarWidth-1) }

func (m model) renderFooter() string {
	left := "已连接 v" + m.version
	if m.version == "" {
		left = "未连接"
	}
	line := left + " · " + m.lastAction
	return truncate(line, m.width)
}

// ── 小工具（宽度一律按**显示宽度**算：中文占两格，所以必须用 runewidth）──

// truncate：按**显示宽度**截断，并**填满到 width**（中文占两格 ⇒ 不能按字符数截）。
//
// 两个坑：
//  1. 不用 `runewidth.Truncate`：它把省略号算进宽度而**少填一格**（"abcdefghij",5 ⇒ "abc…" ✗）；
//  2. 省略号用 **ASCII `...`**：`…` 是**模糊宽度**字符（East Asian Ambiguous），
//     runewidth 当两格 ⇒ 我们的宽度账与实际差一格。**结构性字符一律 ASCII** 是唯一稳妥的做法
//     （`─` `·` `…` 都栽过 —— 见 model_test.go 里的宽度断言）。
func truncate(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if runewidth.StringWidth(text) <= width {
		return text
	}
	budget := width - len(ellipsis)
	var builder strings.Builder
	used := 0
	for _, r := range text {
		cell := runewidth.RuneWidth(r)
		if used+cell > budget {
			break
		}
		builder.WriteRune(r)
		used += cell
	}
	return builder.String() + ellipsis
}

// ellipsis：ASCII 三点的省略号（见 truncate 的注释：模糊宽度字符会算错一格）。
const ellipsis = "..."

// 结构性字符一律 ASCII；但**用户内容**里什么字符都可能有 ⇒ 让宽度账与终端自洽：
// 模糊宽度（East Asian Ambiguous，如 `│` `▶` `·` `…` `─`）**按一格**算 ——
// CJK 宽字（`你` 这类 Wide）不受影响，仍是两格 ✓。
func init() { runewidth.DefaultCondition.EastAsianWidth = false }

func pad(text string, width int) string {
	text = truncate(text, width)
	if missing := width - runewidth.StringWidth(text); missing > 0 {
		return text + strings.Repeat(" ", missing)
	}
	return text
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
