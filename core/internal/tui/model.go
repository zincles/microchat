package tui

import (
	"fmt"
	"sort"
	"strconv"
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
	version     string
	lastAction  string
	sessions    []Session
	messages    []Message
	messagesFor string // m.messages 属于哪条会话 —— 别拿"还没拉完"当"这条没有消息"
	selected    int    // **-1 = 空会话**：刚打开 TUI 就是这个（相当于 Pi 那个待输入的输入框）

	// 命令面板（输入以 `/` 开头时出现）
	paletteIndex int

	// 只读查看器（`/outgoing`）：整屏列东西，任意键关掉
	viewer      []string
	viewerTitle string

	// 挑选项（`/resume` 与 `/model`）
	picker      picker
	pickerIndex int
	models      []ModelListItem

	// 选中的渠道 / 模型：有会话就写进会话；没有就留给**下一条新会话**
	chosenProvider string
	chosenModel    string

	// 新建之后要选中的那条（列表刷新回来才知道它在第几个）
	pendingSelect string

	// 界面
	width, height int
	ready         bool
	input         string // 底下那行输入（命令与将来的消息都从这儿走）
}

type picker int

const (
	pickerNone picker = iota
	pickerResume
	pickerModel
)

// ── 消息（tea.Msg）──

type healthMsg struct {
	health Health
	err    error
}

type sessionsMsg struct {
	sessions []Session
	err      error
}

type messagesMsg struct {
	sessionID string
	messages  []Message
	err       error
}

type createdMsg struct {
	session Session
	err     error
}

type deletedMsg struct {
	id  string
	err error
}

type updatedMsg struct {
	session Session
	err     error
}

type modelsMsg struct {
	models []ModelListItem
	err    error
}

type outgoingMsg struct {
	items []Outgoing
	err   error
}

type stateMsg struct {
	state State
	err   error
}

func initialModel(client *Client) model {
	// **一进来就是空会话**（`selected: -1`）：不建库里的行、也不自动跳进旧会话 ——
	// 界面停在一个待输入的输入框上（用户 2026-09-29 定的口径）。
	return model{client: client, selected: -1, width: 80, height: 24}
}

func (m model) Init() tea.Cmd { return loadHealth(m.client) }

func loadHealth(client *Client) tea.Cmd {
	return func() tea.Msg {
		health, err := client.Health()
		return healthMsg{health: health, err: err}
	}
}

func loadSessions(client *Client) tea.Cmd {
	return func() tea.Msg {
		sessions, err := client.Sessions()
		return sessionsMsg{sessions: sessions, err: err}
	}
}

func loadMessages(client *Client, sessionID string) tea.Cmd {
	return func() tea.Msg {
		messages, err := client.Messages(sessionID)
		return messagesMsg{sessionID: sessionID, messages: messages, err: err}
	}
}

func createSessionCmd(client *Client, provider, model string) tea.Cmd {
	return func() tea.Msg {
		session, err := client.CreateSession(provider, model)
		return createdMsg{session: session, err: err}
	}
}

func deleteSessionCmd(client *Client, id string) tea.Cmd {
	return func() tea.Msg {
		return deletedMsg{id: id, err: client.DeleteSession(id)}
	}
}

func updateSessionCmd(client *Client, id, provider, model string) tea.Cmd {
	return func() tea.Msg {
		session, err := client.UpdateSession(id, provider, model)
		return updatedMsg{session: session, err: err}
	}
}

func loadModelsCmd(client *Client) tea.Cmd {
	return func() tea.Msg {
		models, err := client.Models()
		return modelsMsg{models: models, err: err}
	}
}

func loadOutgoingCmd(client *Client, sessionID string) tea.Cmd {
	return func() tea.Msg {
		items, err := client.Outgoing(sessionID)
		return outgoingMsg{items: items, err: err}
	}
}

func loadState(client *Client, sessionID string) tea.Cmd {
	return func() tea.Msg {
		state, err := client.State(sessionID)
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
		return m, loadSessions(m.client)

	case sessionsMsg:
		if message.err != nil {
			m.lastAction = "拉会话失败：" + message.err.Error()
			return m, nil
		}
		// 取数成功说明连上了 ⇒ 版本号还空着就补一次（初次连接失败后刷新成功时会发生）
		var also []tea.Cmd
		if m.version == "" {
			also = append(also, loadHealth(m.client))
		}
		m.sessions = message.sessions
		if m.pendingSelect != "" {
			if index := m.findSession(m.pendingSelect); index >= 0 {
				m.selected = index
				m.pendingSelect = ""
				also = append(also, loadMessages(m.client, m.sessions[index].ID))
			}
		}
		// 列表变短了要兜住；`-1`（空会话）是**合法状态**，不许被"修正"成第 0 条
		if m.selected >= len(m.sessions) {
			m.selected = -1
		}
		m.lastAction = fmt.Sprintf("会话 %d 条", len(m.sessions))
		return m, tea.Batch(also...)

	case messagesMsg:
		if message.err != nil {
			m.lastAction = "拉消息失败：" + message.err.Error()
			return m, nil
		}
		session := m.currentSession()
		if session != nil && message.sessionID == session.ID {
			m.messages = message.messages
			m.messagesFor = message.sessionID
			m.lastAction = fmt.Sprintf("消息 %d 条", len(m.messages))
		}
		return m, nil

	case createdMsg:
		if message.err != nil {
			m.lastAction = "新建失败：" + message.err.Error()
			return m, nil
		}
		m.lastAction = "已新建会话 " + shortID(message.session.ID)
		m.pendingSelect = message.session.ID // 列表刷新回来才选得上它
		return m, loadSessions(m.client)

	case deletedMsg:
		if message.err != nil {
			m.lastAction = "删除失败：" + message.err.Error()
			return m, nil
		}
		// 删完**回到空会话**（不自动跳去别的旧会话 —— 用户要的是"进入新会话"）
		m.selected, m.messages, m.messagesFor, m.pendingSelect = -1, nil, "", ""
		m.lastAction = "已删除 " + shortID(message.id) + "（现在是空会话）"
		return m, loadSessions(m.client)

	case updatedMsg:
		if message.err != nil {
			m.lastAction = "改渠道 / 模型失败：" + message.err.Error()
			return m, nil
		}
		for index := range m.sessions {
			if m.sessions[index].ID == message.session.ID {
				m.sessions[index] = message.session
			}
		}
		m.lastAction = "已改用 " + message.session.Provider + " / " + message.session.Model
		return m, nil

	case modelsMsg:
		if message.err != nil {
			m.picker = pickerNone
			m.lastAction = "拉模型失败：" + message.err.Error()
			return m, nil
		}
		m.models = message.models
		m.pickerIndex = m.indexOfCurrentModel()
		return m, nil

	case stateMsg:
		if message.err != nil {
			m.lastAction = "取状态失败：" + message.err.Error()
			return m, nil
		}
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

	case outgoingMsg:
		if message.err != nil {
			m.lastAction = "拉载荷失败：" + message.err.Error()
			return m, nil
		}
		lines := make([]string, 0, len(message.items)*3+2)
		for index, item := range message.items {
			who := "消息"
			switch item.Source {
			case "system":
				who = "系统提示词"
			case "summary":
				blocks := int64(0)
				if item.Blocks != nil {
					blocks = *item.Blocks
				}
				id := ""
				if item.SummaryID != nil {
					id = shortID(*item.SummaryID)
				}
				who = "摘要 " + id + "（覆盖 " + strconv.FormatInt(blocks, 10) + " 块）"
			default:
				if item.MessageID != nil {
					who = "消息 " + shortID(*item.MessageID)
				}
			}
			role := item.Role
			head := fmt.Sprintf("%2d %-9s %-8s", index+1, role, "")
			lines = append(lines, truncate(head+who, m.mainWidth()))
			for _, line := range strings.Split(item.Content, "\n") {
				lines = append(lines, truncate("     "+line, m.mainWidth()))
			}
			lines = append(lines, "")
		}
		m.viewer, m.viewerTitle = lines, fmt.Sprintf("下次真发出去的载荷（%d 条）", len(message.items))
		m.lastAction = fmt.Sprintf("载荷 %d 条（Esc 关掉）", len(message.items))
		return m, nil

	case tea.KeyPressMsg:
		// 先认输入相关的键（打字优先），再认导航键。
		// 注意：**没有裸 `q` 退出了** —— 那会和打字打架（Pi 也是 /quit 与 ctrl+c）。
		if message.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.viewer != nil { // 查看器开着：任意键关掉（只读，没什么可操作的）
			m.viewer = nil
			return m, nil
		}
		if m.picker != pickerNone {
			return m.pickerKey(message)
		}
		switch message.String() {
		case "esc":
			// 关面板 / 清输入（命令面板是**推导**出来的：清了输入它自然就没了）
			m.input, m.paletteIndex = "", 0
			return m, nil
		case "enter":
			return m.submit()
		case "tab":
			// 把高亮那条补全（面板的意义就在这儿）
			if matches := m.paletteMatches(); len(matches) > 0 {
				m.input = "/" + matches[m.clampPalette()].name + " "
			}
			return m, nil
		case "backspace":
			if m.input != "" {
				runes := []rune(m.input)
				m.input = string(runes[:len(runes)-1])
				m.paletteIndex = 0
			}
			return m, nil
		case "up", "down":
			if matches := m.paletteMatches(); len(matches) > 0 {
				// 面板开着时，方向键在面板里走（不给侧栏）
				step := 1
				if message.String() == "up" {
					step = -1
				}
				m.paletteIndex = (m.clampPalette() + step + len(matches)) % len(matches)
				return m, nil
			}
			if m.input == "" { // 输入框空着时，方向键才当导航用（有字的时候留给将来的历史）
				if message.String() == "up" && m.selected > 0 {
					m.selected--
					return m, loadMessages(m.client, m.sessions[m.selected].ID)
				}
				if message.String() == "down" && m.selected < len(m.sessions)-1 {
					m.selected++
					return m, loadMessages(m.client, m.sessions[m.selected].ID)
				}
			}
			return m, nil
		}
		if message.Text != "" { // 可打印字符（含中文、粘贴）
			m.input += message.Text
			m.paletteIndex = 0
		}
	}
	return m, nil
}

// pickerKey：挑选项开着时的按键（此时不接受打字 —— 挑东西就是挑东西）。
func (m model) pickerKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "esc":
		m.picker = pickerNone
		m.lastAction = "已取消"
		return m, nil
	case "up":
		if m.pickerIndex > 0 {
			m.pickerIndex--
		}
		return m, nil
	case "down":
		if m.pickerIndex < m.pickerLen()-1 {
			m.pickerIndex++
		}
		return m, nil
	case "enter":
		return m.pickerChoose()
	}
	return m, nil
}

func (m model) pickerLen() int {
	if m.picker == pickerResume {
		return len(m.sessions)
	}
	if m.picker == pickerModel {
		return len(m.models)
	}
	return 0
}

// pickerChoose：选中了 —— `/resume` 就进那条会话，`/model` 就定下渠道 / 模型。
func (m model) pickerChoose() (tea.Model, tea.Cmd) {
	switch m.picker {
	case pickerResume:
		if m.pickerIndex < 0 || m.pickerIndex >= len(m.sessions) {
			return m, nil
		}
		session := m.sessions[m.pickerIndex]
		m.picker = pickerNone
		m.selected = m.pickerIndex
		m.messages, m.messagesFor = nil, ""
		m.lastAction = "进入 " + describeSession(session)
		return m, loadMessages(m.client, session.ID)

	case pickerModel:
		if m.pickerIndex < 0 || m.pickerIndex >= len(m.models) {
			return m, nil
		}
		item := m.models[m.pickerIndex]
		m.picker = pickerNone
		m.chosenProvider, m.chosenModel = item.Provider, item.UpstreamID
		session := m.currentSession()
		if session == nil {
			m.lastAction = "已选 " + item.ProviderLabel() + " / " + item.Label() + "（留给下一条新会话）"
			return m, nil
		}
		m.lastAction = "改渠道 / 模型…"
		return m, updateSessionCmd(m.client, session.ID, item.Provider, item.UpstreamID)
	}
	return m, nil
}

// submit：回车了 —— 是命令就派发，不是就如实说"发送还没实现"。
func (m model) submit() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.input)
	// 面板补全要在**清空输入之前**算候选（先清空再算 ⇒ 候选恒为空，补全永远扑空 —— 测试抓到的）
	if strings.HasPrefix(text, "/") && !strings.Contains(text, " ") {
		if matches := m.paletteMatches(); len(matches) > 0 {
			if name := strings.TrimPrefix(text, "/"); name != matches[m.clampPalette()].name {
				text = "/" + matches[m.clampPalette()].name
			}
		}
	}
	m.input, m.paletteIndex = "", 0
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

// runCommand：按**命令表**派发（面板、/help、这里读的是同一份表）。
func (m model) runCommand(text string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(strings.TrimPrefix(text, "/"))
	if len(fields) == 0 {
		return m, nil
	}
	for _, candidate := range commands {
		if candidate.name == fields[0] {
			return candidate.run(m, fields[1:])
		}
	}
	m.lastAction = "不认识这个命令：" + text + "（打 `/` 看能用哪些）"
	return m, nil
}

// ── 命令表：**唯一一份** —— 面板、/help、派发都读它（各写一份必然漂移）──

type command struct {
	name string // 不含斜杠
	args string // 参数提示（没有就空）
	help string
	run  func(m model, args []string) (tea.Model, tea.Cmd)
}

// commands：在 init 里填 —— 表里有两项（help）要**回头读这张表**，
// 写成包级 var 就是初始化环（Go 直接拒绝编译）。
var commands []command

func init() {
	commands = []command{
		{name: "new", help: "新建会话（当前会话为空时无效）", run: commandNew},
		{name: "delete", help: "删除当前会话，回到空会话", run: commandDelete},
		{name: "resume", args: "[uuid]", help: "挑一条已有会话；带 uuid 直接进", run: commandResume},
		{name: "model", help: "挑渠道 / 模型（有会话就改它，没有则留给下一条）", run: commandModel},
		{name: "outgoing", help: "看下次真发出去的载荷（哪几条是压缩出来的）", run: commandOutgoing},
		{name: "state", help: "看当前会话的世界状态", run: commandState},
		{name: "refresh", help: "重新拉会话列表", run: commandRefresh},
		{name: "help", help: "列命令（含还没搬完的）", run: commandHelp},
		{name: "quit", help: "退出", run: commandQuit},
	}
}

// pendingCommands：路由还没搬完的（如实列着，不假装支持）。
var pendingCommands = []string{"/compact", "/fork", "/archive", "/stop", "/tasks"}

func commandNew(m model, _ []string) (tea.Model, tea.Cmd) {
	// 规矩：**当前会话为空时 /new 无效**（空会话已经是新的了，再建就是造垃圾行）
	if m.isEmptySession() {
		m.lastAction = "当前会话是空的 —— /new 无效（/resume 挑一条旧的）"
		return m, nil
	}
	m.lastAction = "新建会话…"
	return m, createSessionCmd(m.client, m.chosenProvider, m.chosenModel)
}

func commandDelete(m model, _ []string) (tea.Model, tea.Cmd) {
	session := m.currentSession()
	if session == nil {
		m.lastAction = "当前就是空会话，没有可删的"
		return m, nil
	}
	m.lastAction = "删除 " + describeSession(*session) + "…"
	return m, deleteSessionCmd(m.client, session.ID)
}

func commandResume(m model, args []string) (tea.Model, tea.Cmd) {
	if len(args) > 0 {
		id := args[0]
		index := m.findSession(id)
		if index < 0 {
			// 要的东西不存在 ⇒ **报错，什么都不发生**（口径：不许偷偷跳到别的会话）
			m.lastAction = "找不到会话 " + id + "（打 /resume 看列表）—— 什么都没有发生"
			return m, nil
		}
		session := m.sessions[index]
		m.selected = index
		m.messages, m.messagesFor = nil, ""
		m.lastAction = "进入 " + describeSession(session)
		return m, loadMessages(m.client, session.ID)
	}
	if len(m.sessions) == 0 {
		m.lastAction = "还没有任何会话（/new 建一条）"
		return m, nil
	}
	m.picker = pickerResume
	m.pickerIndex = max(m.selected, 0)
	return m, nil
}

func commandModel(m model, _ []string) (tea.Model, tea.Cmd) {
	m.picker = pickerModel
	m.pickerIndex = m.indexOfCurrentModel()
	m.lastAction = "拉模型列表…"
	return m, loadModelsCmd(m.client)
}

func commandOutgoing(m model, _ []string) (tea.Model, tea.Cmd) {
	session := m.currentSession()
	if session == nil {
		m.lastAction = "当前是空会话，没有载荷可看"
		return m, nil
	}
	m.lastAction = "拉载荷…"
	return m, loadOutgoingCmd(m.client, session.ID)
}

func commandState(m model, _ []string) (tea.Model, tea.Cmd) {
	session := m.currentSession()
	if session == nil {
		m.lastAction = "当前是空会话，没有状态可看"
		return m, nil
	}
	if len(m.messages) == 0 {
		m.lastAction = "这条会话还没有消息，状态是空的"
		return m, nil
	}
	m.lastAction = "取状态…"
	return m, loadState(m.client, session.ID)
}

func commandRefresh(m model, _ []string) (tea.Model, tea.Cmd) {
	m.lastAction = "刷新中…"
	return m, loadSessions(m.client)
}

func commandHelp(m model, _ []string) (tea.Model, tea.Cmd) {
	names := make([]string, 0, len(commands))
	for _, candidate := range commands {
		names = append(names, "/"+candidate.name)
	}
	m.lastAction = "命令：" + strings.Join(names, " · ") + "（还没搬完的：" + strings.Join(pendingCommands, " · ") + "）"
	return m, nil
}

func commandQuit(m model, _ []string) (tea.Model, tea.Cmd) { return m, tea.Quit }

// ── 小查询（都住在 model 上，免得散落各处算同一件事）──

// isEmptySession：当前是不是"空会话" —— 要么没有选中任何会话，要么选中的那条**确实**没有消息
// （还没拉完不算：拿"还没拉完"当"没有消息"会误判）。
func (m model) isEmptySession() bool {
	if m.currentSession() == nil {
		return true
	}
	return m.messagesFor == m.currentSession().ID && len(m.messages) == 0
}

// findSession：按完整 id 或**唯一前缀**找（前缀有歧义 ⇒ 当找不到，别猜）。
func (m model) findSession(id string) int {
	found, hits := -1, 0
	for index, session := range m.sessions {
		if session.ID == id {
			return index
		}
		if strings.HasPrefix(session.ID, id) {
			found, hits = index, hits+1
		}
	}
	if hits == 1 {
		return found
	}
	return -1
}

func (m model) indexOfCurrentModel() int {
	provider, model := m.chosenProvider, m.chosenModel
	if session := m.currentSession(); session != nil {
		provider, model = session.Provider, session.Model
	}
	for index, item := range m.models {
		if item.Provider == provider && item.UpstreamID == model {
			return index
		}
	}
	return 0
}

// paletteMatches：输入以 `/` 开头、且还没打空格时的候选（按名字前缀匹配）。
func (m model) paletteMatches() []command {
	if !strings.HasPrefix(m.input, "/") || strings.Contains(m.input, " ") {
		return nil
	}
	prefix := strings.TrimPrefix(m.input, "/")
	matches := make([]command, 0, len(commands))
	for _, candidate := range commands {
		if strings.HasPrefix(candidate.name, prefix) {
			matches = append(matches, candidate)
		}
	}
	return matches
}

func (m model) clampPalette() int {
	matches := m.paletteMatches()
	if len(matches) == 0 {
		return 0
	}
	if m.paletteIndex >= len(matches) {
		return len(matches) - 1
	}
	if m.paletteIndex < 0 {
		return 0
	}
	return m.paletteIndex
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func describeSession(session Session) string {
	title := strings.TrimSpace(session.Title)
	if title == "" {
		title = "（还没起名）"
	}
	return shortID(session.ID) + " " + title
}

// ── 渲染 ──

const sidebarWidth = 28

// View：**纯函数**（同样的 model ⇒ 同样的字符串）。
func (m model) View() tea.View {
	return tea.NewView(m.render())
}

func (m model) render() string {
	palette := m.renderPalette()
	// **空会话只留底部**（用户 2026-09-29 定的）：还没进任何会话时，左侧列表与右边空白
	// 全是噪音 —— 要看列表打 /resume，要挑渠道 /模型。进了会话（或挑选项开着）才铺开整个界面。
	if m.currentSession() == nil && m.picker == pickerNone && m.viewer == nil {
		lines := append([]string{}, palette...)
		lines = append(lines, truncate("> "+m.input, max(1, m.width)))
		lines = append(lines, strings.Repeat("-", max(1, m.width)))
		lines = append(lines, m.renderFooter())
		return strings.Join(lines, "\n")
	}
	bodyHeight := m.height - 3 - len(palette) // 输入行 + 分隔线 + 底栏（面板开着再占几行）
	if bodyHeight < 3 {
		bodyHeight = 3
	}
	sidebar := m.renderSidebar(bodyHeight)
	main := m.renderMessages(bodyHeight)
	lines := make([]string, 0, bodyHeight+len(palette)+3)
	for index := range bodyHeight {
		lines = append(lines, pad(sidebar[index], sidebarWidth)+"|"+main[index])
	}
	lines = append(lines, palette...)
	lines = append(lines, truncate("> "+m.input, max(1, m.width))) // 输入行（命令与将来的消息都从这儿走）
	lines = append(lines, strings.Repeat("-", max(1, m.width)))    // ASCII：`─` 是模糊宽度字符，算两格 ⇒ 会超宽
	lines = append(lines, m.renderFooter())
	return strings.Join(lines, "\n")
}

func (m model) renderSidebar(height int) []string {
	lines := make([]string, 0, height)
	lines = append(lines, "＋ 新建对话")
	lines = append(lines, strings.Repeat("-", sidebarWidth-2)) // 分隔线用 ASCII：`·` 是**模糊宽度**字符，终端里对不齐
	for index, session := range m.sessions {
		title := session.Title
		if strings.TrimSpace(title) == "" {
			title = "（还没起名）"
		}
		cursor := "  "
		if index == m.selected {
			cursor = "> "
		}
		busy := ""
		if session.Turn.Phase == "pending" || session.Turn.Phase == "streaming" {
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

// renderPalette：命令面板（输入以 `/` 开头时**推导**出来的 —— 不是又一个状态）。
func (m model) renderPalette() []string {
	matches := m.paletteMatches()
	if len(matches) == 0 {
		return nil
	}
	current := m.clampPalette()
	lines := make([]string, 0, len(matches))
	for index, candidate := range matches {
		cursor := "  "
		if index == current {
			cursor = "> "
		}
		label := "/" + candidate.name
		if candidate.args != "" {
			label += " " + candidate.args
		}
		lines = append(lines, truncate(cursor+pad(label+"  "+candidate.help, max(1, m.width-2)), max(1, m.width)))
	}
	return lines
}

// renderMessages：挑选项开着就显示挑选项；表头**钉在顶部**；消息**从底部往上**整块排。
//
// 为什么要从底部排：聊天记录里**最新的在下面**；而且一块消息（署名 + 正文）要么整块出现、
// 要么不出现 —— 从顶部截断会把署名切掉，看起来像"不知道谁说的"（这个 bug 是测试抓出来的）。
func (m model) renderMessages(height int) []string {
	if m.viewer != nil {
		lines := []string{truncate(" "+m.viewerTitle+"   任意键关掉", m.mainWidth()),
			truncate(" "+strings.Repeat("-", max(1, m.mainWidth()-2)), m.mainWidth())}
		for _, line := range m.viewer {
			if len(lines) >= height {
				break
			}
			lines = append(lines, line)
		}
		for len(lines) < height {
			lines = append(lines, "")
		}
		return lines[:height]
	}
	switch m.picker {
	case pickerResume:
		return m.renderResumePicker(height)
	case pickerModel:
		return m.renderModelPicker(height)
	}
	session := m.currentSession()
	if session == nil {
		// 空会话：老实说清楚现在是什么状态、能做什么
		lines := []string{
			truncate(" 空会话（还没有内容）", m.mainWidth()),
			truncate(" 打 / 看命令 · /resume 挑一条旧的 · /model 选渠道", m.mainWidth()),
		}
		for len(lines) < height {
			lines = append(lines, "")
		}
		return lines[:height]
	}
	header := " " + session.Provider + " / " + session.Model + " · agent " + session.AgentID
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

func (m model) renderResumePicker(height int) []string {
	lines := []string{
		truncate(" 挑一条已有会话   上/下 选 · 回车进 · Esc 取消", m.mainWidth()),
		truncate(" "+strings.Repeat("-", max(1, m.mainWidth()-2)), m.mainWidth()),
	}
	for index, session := range m.sessions {
		if len(lines) >= height {
			break
		}
		cursor := "  "
		if index == m.pickerIndex {
			cursor = "> "
		}
		label := describeSession(session) + "    " + session.Provider + " / " + session.Model
		lines = append(lines, truncate(cursor+label, m.mainWidth()))
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:height]
}

// renderModelPicker：**按 provider 排列**（用户 2026-09-29 的口径：先不分能力，按渠道分组）。
func (m model) renderModelPicker(height int) []string {
	lines := []string{
		truncate(" 挑渠道 / 模型   上/下 选 · 回车定 · Esc 取消", m.mainWidth()),
		truncate(" "+strings.Repeat("-", max(1, m.mainWidth()-2)), m.mainWidth()),
	}
	if len(m.models) == 0 {
		lines = append(lines, truncate(" （还没有发现任何模型 —— 设置里给渠道点一次「获取模型」）", m.mainWidth()))
	}
	currentProvider := ""
	for index, item := range m.models {
		if len(lines) >= height {
			break
		}
		if item.Provider != currentProvider {
			currentProvider = item.Provider
			lines = append(lines, truncate(" -- "+item.ProviderLabel()+" --", m.mainWidth()))
			if len(lines) >= height {
				break
			}
		}
		cursor := "  "
		if index == m.pickerIndex {
			cursor = "> "
		}
		label := item.Label()
		if item.Provider == m.chosenProvider && item.UpstreamID == m.chosenModel {
			label += "（当前）"
		}
		lines = append(lines, truncate(cursor+label, m.mainWidth()))
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:height]
}

func (m model) currentSession() *Session {
	if m.selected < 0 || m.selected >= len(m.sessions) {
		return nil
	}
	return &m.sessions[m.selected]
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
