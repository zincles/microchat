package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

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
	// selectedID：当前会话 —— **认 id，不认位置**。`GET /sessions` 按 `updated_at` 排 ⇒
	// 发一句话就会把那条会话顶到最前 ⇒ 存下标的话，下一次刷新就指到**别人**身上
	// （用户实跑抓到的"发一句话后消息区一片空白"就是这个：消息回来时对不上号，被丢掉）。
	// 空串 = **空会话**：刚打开 TUI 就是这个（相当于 Pi 那个待输入的输入框）。
	selectedID string

	// 命令面板（输入以 `/` 开头时出现）
	paletteIndex int

	// 只读查看器（`/outgoing`）：整屏列东西，任意键关掉
	viewer      []string
	viewerTitle string
	// viewerHint：表头右边那句提示。确认框要写清"按什么才算答应"，不能是"任意键关掉"。
	viewerHint string
	// confirm：**不可逆动作**的"先看清楚、再点头"（`/delete` 与 `/cut` 都走它）。
	// 非空时按键的意义变了：只是"答应 / 不答应"，不再是"关掉查看器"。
	confirm *confirm

	// 挑选项（`/resume` 与 `/model`）
	picker      picker
	pickerIndex int
	models      []ModelListItem

	// 选中的渠道 / 模型：有会话就写进会话；没有就留给**下一条新会话**
	chosenProvider string
	chosenModel    string

	// 界面
	width, height int
	ready         bool
	input         string // 底下那行输入（命令与消息都从这儿走）
	// inputCursor：输入行里光标的位置 —— **rune 下标**（不是字节下标，也不是"第几格"）。
	//
	// 为什么是 rune 下标：增删要能精确落在"第几个字"上（中文一个字 = 一个 rune）；
	// **显示列**则由 `runewidth` 现算（一个字可能占两格，见 inputCursorColumn）——
	// 拿 rune 下标当列用，遇 CJK 光标就指到字中间（已立为纪律，见 AGENTS.md「踩过的坑」）。
	inputCursor int
	// style：着色开关（**只有前景色**）。零值 = 关 ⇒ 测试拿到的 View() 是纯文本
	// （逐字节断言靠这个；生产路径的开关在 detectStyler，见「降级三档」）。
	style styler
	// lastFailure：**最近一次失败**的原话。状态行里只有它此刻仍与 lastAction 逐字相同
	// （= 那句话还摆在那儿）才标红 —— 别的分支一改底栏，红自己就退了，
	// 不需要每个非失败分支都记得清标志（漏一处 = 红留在成功消息上，更难查）。
	lastFailure string

	// context / contextFor：上下文占用，以及它**属于哪条会话**（换会话时那份还旧着 ⇒ 不许拿去画）。
	context    *ContextUsage
	contextFor string

	// 正在生成的这一轮（界面按状态**合成**的气泡；库里还没有它）
	turn *liveTurn
}

// liveTurn：这一轮生成在界面上的样子。
//
// 库里的那条回复要**整段拿到才落库** ⇒ 生成中这条气泡是**界面合成的**：
// 转圈 + 耗时 + 「/stop 停止」。`messageID` 就是它将来落库的那个 id
// （受理回执里给的）⇒ 气泡消失、真消息出现是无缝的。
type liveTurn struct {
	sessionID string
	messageID string
	phase     string
	elapsedMS int64
	text      string // 已经画出来的正文（增量累加）
	thinking  string // 思考流（只服务动画）
	from      int    // 正文游标
	thinkFrom int    // 思考游标
}

// Busy：还在跑（`error` 不算 —— 它是上一轮的结局）。
func (t *liveTurn) Busy() bool {
	return t != nil && (t.phase == "pending" || t.phase == "streaming")
}

// turnPollInterval：生成中每 300ms 看一眼（口径见 AGENTS.md）。
const turnPollInterval = 300 * time.Millisecond

type picker int

const (
	pickerNone picker = iota
	pickerResume
	pickerModel
)

// confirm：等着用户点头的那件事（模型里只存**意图**，命令由 `confirmExecute` 派生）。
type confirm struct {
	kind      string // confirmDeleteSession / confirmCut
	sessionID string
	messageID string       // cut 的目标
	plan      DeletionPlan // cut：后端算好的预览（执行时照它，并把末尾那条带回去核对）
}

const (
	// confirmDeleteSession：删整条会话（回到空会话）。
	confirmDeleteSession = "delete-session"
	// confirmCut：删一条消息及其之后的全部。
	confirmCut = "cut"
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

// contextMsg：这一次出站会用掉多少上下文（**只有数字**，见 `GET .../context`）。
// 拉它的时机只有两个：**进会话时**、**一轮结束后**（不是每 300ms 一次 —— 占用只在整段落库后才变）。
type contextMsg struct {
	sessionID string
	usage     ContextUsage
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

type cutPreviewMsg struct {
	sessionID string
	messageID string
	plan      DeletionPlan
	err       error
}

type cutDoneMsg struct {
	plan DeletionPlan
	err  error
}

type copiedMsg struct {
	session Session
	err     error
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

// sentMsg：一句话发出去了（202 回来了）。`created` 只在"空会话里冒出新会话"时非空。
type sentMsg struct {
	sessionID string
	created   *Session
	accepted  TurnAccepted
	err       error
}

// turnTickMsg：轮询的节拍（把"每 300ms 看一眼"做成 tick，而不是在 Cmd 里 sleep）。
type turnTickMsg struct{ sessionID string }

// turnPollMsg：一次轮询的结果（状态 + 这一段的增量）。两次请求在同一个 Cmd 里先后发 ——
// 状态是"这轮什么样"、增量是"这轮说了什么"，客户端要的就是这一对。
type turnPollMsg struct {
	sessionID string
	status    TurnStatus
	slice     StreamSlice
	err       error
}

type stoppedMsg struct {
	sessionID string
	stopped   bool
	err       error
}

// compactMsg：一次压缩的**受理**回执（202）—— "压缩中"这件事由后端说了算。
type compactMsg struct {
	status CompactStatus
	err    error
}

func initialModel(client *Client) model {
	// **一进来就是空会话**（`selectedID: ""`）：不建库里的行、也不自动跳进旧会话 ——
	// 界面停在一个待输入的输入框上（用户 2026-09-29 定的口径）。
	return model{client: client, width: 80, height: 24}
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

// loadContext：量一次上下文占用（**只有数字** —— 不必为一个数去拉整份 `/outgoing`）。
func loadContext(client *Client, sessionID string) tea.Cmd {
	return func() tea.Msg {
		usage, err := client.Context(sessionID)
		return contextMsg{sessionID: sessionID, usage: usage, err: err}
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

func cutPreviewCmd(client *Client, sessionID, messageID string) tea.Cmd {
	return func() tea.Msg {
		plan, err := client.DeletionPreview(sessionID, messageID)
		return cutPreviewMsg{sessionID: sessionID, messageID: messageID, plan: plan, err: err}
	}
}

func cutExecuteCmd(client *Client, sessionID, messageID, lastDeletedMessageID string) tea.Cmd {
	return func() tea.Msg {
		plan, err := client.DeleteMessagesFrom(sessionID, messageID, lastDeletedMessageID)
		return cutDoneMsg{plan: plan, err: err}
	}
}

func copySessionCmd(client *Client, id string) tea.Cmd {
	return func() tea.Msg {
		session, err := client.CopySession(id)
		return copiedMsg{session: session, err: err}
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

// sendCmd：把一句话真发出去。
//
// **空会话**（`selected == -1`，界面一进来的那个"待输入的输入框"）时顺手新建一条会话 ——
// 那条输入框的意义正是"第一句话"（`/new` 在空会话上是无效的，见 commandNew）。
func sendCmd(client *Client, session *Session, provider, model, content string) tea.Cmd {
	return func() tea.Msg {
		created := (*Session)(nil)
		target := session
		if target == nil {
			fresh, err := client.CreateSession(provider, model)
			if err != nil {
				return sentMsg{err: err}
			}
			created, target = &fresh, &fresh
		}
		accepted, err := client.SendMessage(target.ID, content)
		if err != nil {
			return sentMsg{sessionID: target.ID, created: created, err: err}
		}
		return sentMsg{sessionID: target.ID, created: created, accepted: accepted}
	}
}

// turnTickCmd：下一次轮询的节拍。
func turnTickCmd(sessionID string) tea.Cmd {
	return tea.Tick(turnPollInterval, func(time.Time) tea.Msg { return turnTickMsg{sessionID: sessionID} })
}

// pollTurnCmd：看一眼状态 + 拉这一段的增量（两条游标各拿各的）。
func pollTurnCmd(client *Client, sessionID string, from, thinkFrom int) tea.Cmd {
	return func() tea.Msg {
		status, err := client.TurnStatus(sessionID)
		if err != nil {
			return turnPollMsg{sessionID: sessionID, err: err}
		}
		slice, err := client.TurnText(sessionID, from, thinkFrom)
		if err != nil {
			return turnPollMsg{sessionID: sessionID, status: status, err: err}
		}
		return turnPollMsg{sessionID: sessionID, status: status, slice: slice}
	}
}

func stopTurnCmd(client *Client, sessionID string) tea.Cmd {
	return func() tea.Msg {
		stopped, err := client.StopTurn(sessionID)
		return stoppedMsg{sessionID: sessionID, stopped: stopped, err: err}
	}
}

// compactCmd：受理一次压缩（202）。`blocks` = 0 ⇒ 用后端的默认值。
func compactCmd(client *Client, sessionID string, blocks int) tea.Cmd {
	return func() tea.Msg {
		status, err := client.Compact(sessionID, blocks)
		return compactMsg{status: status, err: err}
	}
}

// fail：把一次失败摆到底栏，并**记下这句原话** —— 状态行靠"lastAction 与它逐字相同"判定
// 该不该标红（见 model.lastFailure 的注释：别的分支一改底栏，红就自己退了）。
func (m model) fail(text string) model {
	m.lastAction, m.lastFailure = text, text
	return m
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch message := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height, m.ready = message.Width, message.Height, true
		return m, nil

	case healthMsg:
		if message.err != nil {
			m.version = "" // 连不上 ⇒ 如实"未连接"，别留着上一次的版本号（界面口径：两半互不顶替）
			return m.fail("连不上后端：" + message.err.Error()), nil
		}
		m.version = message.health.Version
		m.lastAction = "已连接 " + m.client.BaseURL
		return m, loadSessions(m.client)

	case sessionsMsg:
		if message.err != nil {
			m = m.fail("拉会话失败：" + message.err.Error())
			return m, nil
		}
		// 取数成功说明连上了 ⇒ 版本号还空着就补一次（初次连接失败后刷新成功时会发生）
		var also []tea.Cmd
		if m.version == "" {
			also = append(also, loadHealth(m.client))
		}
		m.sessions = message.sessions
		// **认 id 不认位置**：列表按 `updated_at` 排，随时会重排（发一句话就重排一次）。
		// id 还在 ⇒ 什么都不用做（位置变了与"选中的是哪条"无关）；
		// id 没了（别处把这条会话删了）⇒ 老实回到空会话，别赖在别人身上。
		if m.selectedID != "" && m.findSession(m.selectedID) < 0 {
			m.selectedID, m.messages, m.messagesFor = "", nil, ""
			return m.fail("那条会话没了（别处删的）—— 现在是空会话"), tea.Batch(also...)
		}
		m.lastAction = fmt.Sprintf("会话 %d 条", len(m.sessions))
		return m, tea.Batch(also...)

	case messagesMsg:
		if message.err != nil {
			m = m.fail("拉消息失败：" + message.err.Error())
			return m, nil
		}
		session := m.currentSession()
		if session != nil && message.sessionID == session.ID {
			m.messages = message.messages
			m.messagesFor = message.sessionID
			m.lastAction = fmt.Sprintf("消息 %d 条", len(m.messages))
			// **进会话时**顺手量一次上下文占用（这份属于哪条会话记在 contextFor ⇒ 换会话会重新拉）
			if m.contextFor != session.ID {
				return m, loadContext(m.client, session.ID)
			}
		}
		return m, nil

	case contextMsg:
		// 拉不到就先不显示这一档 —— **别去打扰底栏**（那不是用户刚做的动作，红了反而是噪音）
		if message.err != nil {
			m.context, m.contextFor = nil, ""
			return m, nil
		}
		m.context, m.contextFor = &message.usage, message.sessionID
		return m, nil

	case createdMsg:
		if message.err != nil {
			m = m.fail("新建失败：" + message.err.Error())
			return m, nil
		}
		m.lastAction = "已新建会话 " + shortID(message.session.ID)
		m.selectedID, m.messages, m.messagesFor = message.session.ID, nil, ""
		return m, tea.Batch(loadSessions(m.client), loadMessages(m.client, message.session.ID))

	case deletedMsg:
		if message.err != nil {
			m = m.fail("删除失败：" + message.err.Error())
			return m, nil
		}
		// 删完**回到空会话**（不自动跳去别的旧会话 —— 用户要的是"进入新会话"）
		m.selectedID, m.messages, m.messagesFor = "", nil, ""
		m.lastAction = "已删除 " + shortID(message.id) + "（现在是空会话）"
		return m, loadSessions(m.client)

	case cutPreviewMsg:
		if message.err != nil {
			m = m.fail("取删除预览失败：" + message.err.Error())
			return m, nil
		}
		m.confirm = &confirm{
			kind: confirmCut, sessionID: message.sessionID,
			messageID: message.messageID, plan: message.plan,
		}
		m.viewer, m.viewerTitle = m.cutPreviewLines(message.plan), "确认删除"
		m.viewerHint = "回车 / y 执行 · 其他键取消"
		m.lastAction = "看清了再点头（回车 / y）"
		return m, nil

	case cutDoneMsg:
		m.confirm, m.viewer, m.viewerHint = nil, nil, ""
		if message.err != nil {
			m = m.fail("删除失败：" + message.err.Error())
			return m, m.reloadMessagesCmd()
		}
		m.lastAction = fmt.Sprintf("已删 %d 条消息 · %d 份摘要",
			len(message.plan.DeletedMessageIDs), len(message.plan.DeletedSummaryIDs))
		return m, tea.Batch(m.reloadMessagesCmd(), loadSessions(m.client))

	case copiedMsg:
		if message.err != nil {
			m = m.fail("复制失败：" + message.err.Error())
			return m, nil
		}
		m.lastAction = "已复制成 " + shortID(message.session.ID) + "（新会话）"
		m.selectedID, m.messages, m.messagesFor = message.session.ID, nil, ""
		return m, tea.Batch(loadSessions(m.client), loadMessages(m.client, message.session.ID))

	case updatedMsg:
		if message.err != nil {
			m = m.fail("改渠道 / 模型失败：" + message.err.Error())
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
			m = m.fail("拉模型失败：" + message.err.Error())
			return m, nil
		}
		m.models = message.models
		m.pickerIndex = m.indexOfCurrentModel()
		return m, nil

	case stateMsg:
		if message.err != nil {
			m = m.fail("取状态失败：" + message.err.Error())
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
			m = m.fail("拉载荷失败：" + message.err.Error())
			return m, nil
		}
		lines := make([]string, 0, len(message.items)*3+2)
		for index, item := range message.items {
			// 出处一眼看出来（口径见 AGENTS.md）：**摘要是压缩过的 ⇒ 黄**、系统提示词 ⇒ 暗、
			// 原样消息 ⇒ 默认色。正文一律默认色（内容才是主角）。
			who, kind := "消息", stylePlain
			switch item.Source {
			case "system":
				who, kind = "系统提示词", styleDim
			case "summary":
				blocks := int64(0)
				if item.Blocks != nil {
					blocks = *item.Blocks
				}
				id := ""
				if item.SummaryID != nil {
					id = shortID(*item.SummaryID)
				}
				who, kind = "摘要 "+id+"（覆盖 "+strconv.FormatInt(blocks, 10)+" 块）", styleYellow
			default:
				if item.MessageID != nil {
					who = "消息 " + shortID(*item.MessageID)
				}
			}
			head := fmt.Sprintf("%2d %-9s %-8s", index+1, item.Role, "")
			line, _ := m.style.concat([]segment{{head, stylePlain}, {who, kind}}, max(1, m.width), "")
			lines = append(lines, line)
			for _, line := range strings.Split(item.Content, "\n") {
				lines = append(lines, truncate("     "+line, max(1, m.width)))
			}
			lines = append(lines, "")
		}
		m.viewer, m.viewerTitle = lines, fmt.Sprintf("下次真发出去的载荷（%d 条）", len(message.items))
		m.viewerHint = "任意键关掉"
		m.lastAction = fmt.Sprintf("载荷 %d 条（Esc 关掉）", len(message.items))
		return m, nil

	case sentMsg:
		if message.err != nil {
			m = m.fail("发送失败：" + message.err.Error())
			return m, nil
		}
		if message.created != nil { // 空会话里冒出来的新会话：进列表并**立刻认它当当前会话**
			m.sessions = append(m.sessions, *message.created)
			m.selectedID, m.messages, m.messagesFor = message.created.ID, nil, ""
		}
		m.turn = &liveTurn{
			sessionID: message.sessionID,
			messageID: derefID(message.accepted.Turn.MessageID),
			phase:     message.accepted.Turn.Phase,
		}
		m.lastAction = "已发出（" + message.accepted.Backend + "）· 生成中…"
		// 用户那句已经在库里了 ⇒ 消息列表要重拉；同时开始每 300ms 看一次进度
		return m, tea.Batch(
			loadSessions(m.client),
			loadMessages(m.client, message.sessionID),
			turnTickCmd(message.sessionID),
		)

	case turnTickMsg:
		if !m.turn.Busy() || m.turn.sessionID != message.sessionID {
			return m, nil
		}
		return m, pollTurnCmd(m.client, m.turn.sessionID, m.turn.from, m.turn.thinkFrom)

	case turnPollMsg:
		// 属于旧的一轮 / 已经没有在生成的轮次 ⇒ 丢掉（界面只认当前这一轮）
		if m.turn == nil || m.turn.sessionID != message.sessionID {
			return m, nil
		}
		if message.err != nil {
			m = m.settleTurn(message.sessionID)
			m = m.fail("取生成进度失败：" + message.err.Error())
			return m, nil
		}
		m.turn.phase, m.turn.elapsedMS = message.status.Phase, message.status.ElapsedMS
		m.turn.text += message.slice.Text
		m.turn.thinking += message.slice.Thinking
		m.turn.from, m.turn.thinkFrom = message.slice.Next, message.slice.ThinkNext
		switch message.status.Phase {
		case "error":
			// 失败：库里**半条都没有**（气泡直接消失），原因摆在底栏
			m = m.settleTurn(message.sessionID)
			m = m.fail("生成失败：" + message.status.Error)
			return m, nil
		case "idle":
			// 收到 idle ⇒ 整段已经落库 ⇒ **一次性重拉**（气泡被真消息取代），并量一次**新的占用**
			// （一轮只量一次：占用只在整段落库后才变，别跟着 300ms 的轮询一起拉）
			m = m.settleTurn(message.sessionID)
			m.lastAction = "生成完成"
			return m, tea.Batch(
				loadMessages(m.client, message.sessionID),
				loadContext(m.client, message.sessionID),
			)
		}
		return m, turnTickCmd(message.sessionID)

	case compactMsg:
		// 压缩是**后台**跑的（受理即回 202）：这里只说"受理了"，跑完的结局在后端的压缩状态里
		// （`/status` 的 compact 那一档 —— 界面这一份不另存，两处状态必然打架）。
		if message.err != nil {
			m = m.fail("压缩没有受理：" + message.err.Error())
			return m, nil
		}
		m.lastAction = fmt.Sprintf("压缩已受理：%d 个块（跑完发一轮或看 /outgoing 就知道效果）", message.status.Blocks)
		return m, nil

	case stoppedMsg:
		if message.err != nil {
			m = m.fail("停止失败：" + message.err.Error())
			return m, nil
		}
		if !message.stopped {
			m.lastAction = "现在没有在生成（/stop 是幂等的，什么都没发生）"
			return m, nil
		}
		m = m.settleTurn(message.sessionID)
		m.lastAction = "已停止这一轮（这条回复没有落库）"
		return m, nil

	case tea.KeyPressMsg:
		// 先认输入相关的键（打字优先），再认导航键。
		// 注意：**没有裸 `q` 退出了** —— 那会和打字打架（Pi 也是 /quit 与 ctrl+c）。
		if message.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.confirm != nil { // 等着点头：只有"答应"与"不答应"两种按键
			switch message.String() {
			case "enter", "y", "Y":
				return m.confirmExecute()
			default:
				m.confirm, m.viewer, m.viewerTitle, m.viewerHint = nil, nil, "", ""
				m.lastAction = "已取消（什么都没删）"
				return m, nil
			}
		}
		if m.viewer != nil { // 查看器开着：任意键关掉（只读，没什么可操作的）
			m.viewer, m.viewerTitle, m.viewerHint = nil, "", ""
			return m, nil
		}
		if m.picker != pickerNone {
			return m.pickerKey(message)
		}
		switch message.String() {
		case "esc":
			// 关面板 / 清输入（命令面板是**推导**出来的：清了输入它自然就没了）
			m.input, m.inputCursor, m.paletteIndex = "", 0, 0
			return m, nil
		case "enter":
			return m.submit()
		case "tab":
			// 把高亮那条补全（面板的意义就在这儿）；光标跟到末尾（接着打参数）
			if matches := m.paletteMatches(); len(matches) > 0 {
				m.input = "/" + matches[m.clampPalette()].name + " "
				m.inputCursor = len([]rune(m.input))
			}
			return m, nil
		case "left":
			if m.inputCursor > 0 {
				m.inputCursor--
			}
			return m, nil
		case "right":
			if m.inputCursor < len([]rune(m.input)) {
				m.inputCursor++
			}
			return m, nil
		case "home", "ctrl+a":
			m.inputCursor = 0
			return m, nil
		case "end", "ctrl+e":
			m.inputCursor = len([]rune(m.input))
			return m, nil
		case "backspace":
			// 删**光标前**那个 rune（行首无事发生）
			runes := []rune(m.input)
			if at := clampCursor(m.inputCursor, len(runes)); at > 0 {
				m.input = string(runes[:at-1]) + string(runes[at:])
				m.inputCursor = at - 1
				m.paletteIndex = 0
			}
			return m, nil
		case "delete":
			// 删**光标处**那个 rune（行尾无事发生）—— 与退格成对：一个删左一个删右
			runes := []rune(m.input)
			if at := clampCursor(m.inputCursor, len(runes)); at < len(runes) {
				m.input = string(runes[:at]) + string(runes[at+1:])
				m.inputCursor = at
				m.paletteIndex = 0
			}
			return m, nil
		case "up", "down":
			// 方向键只服务**命令面板**（输入以 `/` 开头时）；会话选择只走 `/resume`
			// —— 输入框上方没有会话列表可走（用户 2026-09-30 定的口径）。
			// （↑/↓ 管面板、←/→ 管光标，两者不冲突。）
			if matches := m.paletteMatches(); len(matches) > 0 {
				step := 1
				if message.String() == "up" {
					step = -1
				}
				m.paletteIndex = (m.clampPalette() + step + len(matches)) % len(matches)
			}
			return m, nil
		}
		if message.Text != "" { // 可打印字符（含中文）—— 插在**光标处**，光标跟着往右挪
			// （括号粘贴走另一条路 `tea.PasteMsg`，本界面还没接 ✗ —— 与光标无关，另议。）
			runes := []rune(m.input)
			at := clampCursor(m.inputCursor, len(runes))
			m.input = string(runes[:at]) + message.Text + string(runes[at:])
			m.inputCursor = at + len([]rune(message.Text))
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
		m.selectedID = session.ID
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

// submit：回车了 —— 是命令就派发，不是就**真发**。
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
	m.input, m.inputCursor, m.paletteIndex = "", 0, 0
	if text == "" {
		return m, nil
	}
	if !strings.HasPrefix(text, "/") {
		return m.send(text)
	}
	return m.runCommand(text)
}

// send：把这句话发出去（**输入行非命令 ⇒ 真发**）。
//
// 空会话（还没建过会话）也能发：`sendCmd` 会顺手建一条。
// 已经在生成中就**不必发**（后端会 409）—— 这里先说清楚，省一次白跑。
func (m model) send(text string) (tea.Model, tea.Cmd) {
	if m.turn.Busy() {
		m.lastAction = "这个会话还在生成中（/stop 可以打断它），等它跑完再发"
		return m, nil
	}
	m.lastAction = "发送中…"
	return m, sendCmd(m.client, m.currentSession(), m.chosenProvider, m.chosenModel, text)
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
	return m.fail("不认识这个命令：" + text + "（打 `/` 看能用哪些）"), nil
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
		{name: "delete", help: "删除当前会话，回到空会话（不可逆，先确认）", run: commandDelete},
		{name: "cut", args: "[message_id]", help: "删一条消息及它之后的全部（先预览，再确认）", run: commandCut},
		{name: "copy", help: "把当前会话复制成新的一条（分岔）", run: commandCopy},
		{name: "resume", args: "[uuid]", help: "挑一条已有会话；带 uuid 直接进", run: commandResume},
		{name: "model", help: "挑渠道 / 模型（有会话就改它，没有则留给下一条）", run: commandModel},
		{name: "outgoing", help: "看下次真发出去的载荷（哪几条是压缩出来的）", run: commandOutgoing},
		{name: "state", help: "看当前会话的世界状态", run: commandState},
		{name: "compact", args: "[N]", help: "把最老的 N 个已闭合块压成摘要（不给 N 用默认值）", run: commandCompact},
		{name: "stop", help: "打断正在生成的那一轮（幂等）", run: commandStop},
		{name: "refresh", help: "重新拉会话列表", run: commandRefresh},
		{name: "help", help: "列命令（含还没搬完的）", run: commandHelp},
		{name: "quit", help: "退出", run: commandQuit},
	}
}

// pendingCommands：路由还没搬完的（如实列着，不假装支持）。
var pendingCommands = []string{"/archive"}

func commandNew(m model, _ []string) (tea.Model, tea.Cmd) {
	// 规矩：**当前会话为空时 /new 无效**（空会话已经是新的了，再建就是造垃圾行）
	if m.isEmptySession() {
		m.lastAction = "当前会话是空的 —— /new 无效（/resume 挑一条旧的）"
		return m, nil
	}
	m.lastAction = "新建会话…"
	return m, createSessionCmd(m.client, m.chosenProvider, m.chosenModel)
}

// commandDelete：删**整条会话**（回到空会话）。
//
// 不可逆 ⇒ 先摊开"会没掉什么"（几条消息、几份它挂着的摘要 —— 数字现算），
// 等用户点头（回车 / y）才真发；点头之前一个字节都不动。
func commandDelete(m model, _ []string) (tea.Model, tea.Cmd) {
	session := m.currentSession()
	if session == nil {
		m.lastAction = "当前就是空会话，没有可删的"
		return m, nil
	}
	if m.messagesFor != session.ID {
		// 还没拉全就报不出"会删几条" ⇒ 先把消息取回来（同一句话里说清楚）
		m.lastAction = "先把消息拉全再删（正在取…）"
		return m, loadMessages(m.client, session.ID)
	}
	m.confirm = &confirm{kind: confirmDeleteSession, sessionID: session.ID}
	m.viewer, m.viewerTitle = m.deleteSessionLines(*session), "确认删除"
	m.viewerHint = "回车 / y 执行 · 其他键取消"
	m.lastAction = "看清了再点头（回车 / y）"
	return m, nil
}

// commandCut：删**一条消息及其之后的全部**（线性会话里的"从这里重新开始"）。
//
// 不给参数 = 最后一条（"把最后这句撤了"）；给了就按完整 id 或唯一前缀找。
// 会删掉什么由**后端**算（`deletion-preview`），摊开给用户看过才真删 —— 预览与执行同一份计算。
func commandCut(m model, args []string) (tea.Model, tea.Cmd) {
	session := m.currentSession()
	if session == nil {
		m.lastAction = "当前是空会话，没有可删的消息"
		return m, nil
	}
	if m.messagesFor != session.ID {
		m.lastAction = "先把消息拉全再删（正在取…）"
		return m, loadMessages(m.client, session.ID)
	}
	if len(m.messages) == 0 {
		m.lastAction = "这条会话还没有消息"
		return m, nil
	}
	target := m.messages[len(m.messages)-1].ID
	if len(args) > 0 {
		if target = m.findMessage(args[0]); target == "" {
			return m.fail("这条会话里找不到消息 " + args[0] + "（什么都没有发生）"), nil
		}
	}
	m.lastAction = "取删除预览…"
	return m, cutPreviewCmd(m.client, session.ID, target)
}

// commandCopy：把当前会话**复制**成新的一条（线性会话里"分岔"就是这个，不叫 fork）。
func commandCopy(m model, _ []string) (tea.Model, tea.Cmd) {
	session := m.currentSession()
	if session == nil {
		m.lastAction = "当前是空会话，没有可复制的"
		return m, nil
	}
	m.lastAction = "复制 " + describeSession(*session) + "…"
	return m, copySessionCmd(m.client, session.ID)
}

// confirmExecute：用户点头了 —— 按存下来的**意图**派生该发的命令。
func (m model) confirmExecute() (tea.Model, tea.Cmd) {
	pending := m.confirm
	m.confirm, m.viewer, m.viewerTitle, m.viewerHint = nil, nil, "", ""
	if pending == nil {
		return m, nil
	}
	switch pending.kind {
	case confirmCut:
		m.lastAction = "删除 " + shortID(pending.messageID) + " 及其之后…"
		return m, cutExecuteCmd(m.client, pending.sessionID, pending.messageID, pending.plan.LastDeletedMessageID)
	case confirmDeleteSession:
		m.lastAction = "删除会话 " + shortID(pending.sessionID) + "…"
		return m, deleteSessionCmd(m.client, pending.sessionID)
	}
	return m, nil
}

// deleteSessionLines：摊开"删这条会话会没掉什么"（数字现算 —— 还没拉全的消息不算数，
// 所以 commandDelete 先确保拉全）。行要短：查看器一行就是一屏宽，长了会被截掉。
func (m model) deleteSessionLines(session Session) []string {
	summaries := map[string]bool{}
	for _, message := range m.messages {
		if message.SummaryID != nil {
			summaries[*message.SummaryID] = true
		}
	}
	return []string{
		m.style.red(" 不可逆") + "：删掉整条会话",
		" 会话 " + describeSession(session),
		fmt.Sprintf(" 会没掉：%d 条消息 · %d 份摘要", len(m.messages), len(summaries)),
		" （摘要是消息上挂着的；区间覆盖的随会话级联）",
		"",
		" 回车 / y 执行 · 其他键取消",
	}
}

// cutPreviewLines：摊开后端算好的那份删除预览（五组各自一行）。
func (m model) cutPreviewLines(plan DeletionPlan) []string {
	return []string{
		m.style.red(" 不可逆") + "：删这条及其之后的全部",
		fmt.Sprintf(" 消息 %d 条：%s", len(plan.DeletedMessageIDs), idsSummary(plan.DeletedMessageIDs)),
		fmt.Sprintf(" 摘要 %d 份：%s", len(plan.DeletedSummaryIDs), idsSummary(plan.DeletedSummaryIDs)),
		fmt.Sprintf(" 解链的消息 %d 条：%s", len(plan.UnlinkedMessageIDs), idsSummary(plan.UnlinkedMessageIDs)),
		fmt.Sprintf(" 解链的摘要 %d 份：%s", len(plan.UnlinkedSummaryIDs), idsSummary(plan.UnlinkedSummaryIDs)),
		" 末尾核对：" + shortID(plan.LastDeletedMessageID),
		"",
		" 回车 / y 执行 · 其他键取消",
	}
}

// idsSummary：把一组 id 挤成一行（最多列 3 个，其余只报数）—— 查看器一行就是一屏宽。
func idsSummary(ids []string) string {
	if len(ids) == 0 {
		return "（无）"
	}
	shown := make([]string, 0, 3)
	for index, id := range ids {
		if index == 3 {
			break
		}
		shown = append(shown, shortID(id))
	}
	line := strings.Join(shown, " ")
	if len(ids) > 3 {
		line += fmt.Sprintf(" …（共 %d 条）", len(ids))
	}
	return line
}

func commandResume(m model, args []string) (tea.Model, tea.Cmd) {
	if len(args) > 0 {
		id := args[0]
		index := m.findSession(id)
		if index < 0 {
			// 要的东西不存在 ⇒ **报错，什么都不发生**（口径：不许偷偷跳到别的会话）
			return m.fail("找不到会话 " + id + "（打 /resume 看列表）—— 什么都没有发生"), nil
		}
		session := m.sessions[index]
		m.selectedID = session.ID
		m.messages, m.messagesFor = nil, ""
		m.lastAction = "进入 " + describeSession(session)
		return m, loadMessages(m.client, session.ID)
	}
	if len(m.sessions) == 0 {
		m.lastAction = "还没有任何会话（/new 建一条）"
		return m, nil
	}
	m.picker = pickerResume
	m.pickerIndex = max(m.findSession(m.selectedID), 0)
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

// commandCompact：把最老的 N 个已闭合块压成摘要（**手工按钮的语义**）。
//
// 走路由 ⇒ **202 受理**（压缩真调一次上游 + 落库，不在这一趟里等）；底栏报受理结果。
// 不给 N ⇒ 用后端的默认值（`config.json` 的 `compact_blocks`）。
// 正在生成时先别压：这一轮的上文已经定稿了，压了也影响不到它（白花一次上游调用）。
func commandCompact(m model, args []string) (tea.Model, tea.Cmd) {
	session := m.currentSession()
	if session == nil {
		m.lastAction = "当前是空会话，没有可压的"
		return m, nil
	}
	blocks := 0
	if len(args) > 0 {
		count, err := strconv.Atoi(args[0])
		if err != nil || count <= 0 {
			return m.fail("压缩块数要正整数：" + args[0] + "（不给就按后端的默认值压）"), nil
		}
		blocks = count
	}
	if m.turn.Busy() && m.turn.sessionID == session.ID {
		m.lastAction = "这个会话还在生成中：等这一轮跑完再压（这一轮的上文已经定稿了）"
		return m, nil
	}
	m.lastAction = "受理压缩…"
	return m, compactCmd(m.client, session.ID, blocks)
}

// commandStop：打断正在生成的那一轮（**幂等** —— 没在跑也 200，界面照实说"什么都没发生"）。
//
// 问的是**后端**（权威在那儿）：界面这一份 `m.turn` 只是"我知道的那一轮"，
// 别的客户端起的生成、或界面重启前起的那一轮，它并不知情。
func commandStop(m model, _ []string) (tea.Model, tea.Cmd) {
	target := ""
	if m.turn != nil {
		target = m.turn.sessionID
	} else if session := m.currentSession(); session != nil {
		target = session.ID
	}
	if target == "" {
		m.lastAction = "没有会话在生成，没什么可停的"
		return m, nil
	}
	m.lastAction = "停止…"
	return m, stopTurnCmd(m.client, target)
}

// settleTurn：这一轮在**本地**收干 —— 气泡消失，界面这一份状态也跟着落回 idle。
//
// 为什么**不重拉会话列表**：那会让底栏刚刚写上的「已停止 / 已完成 / 生成失败」立刻被
// "会话 N 条"顶掉（两个 Cmd 谁先回来还不一定）。这里改的是**已经确定的事实**（这一轮结束了），
// 下一次刷新（`/refresh` 或别的动作）自然与后端对账。
func (m model) settleTurn(sessionID string) model {
	m.turn = nil
	for index := range m.sessions {
		if m.sessions[index].ID == sessionID && m.sessions[index].Turn.Phase != "idle" {
			m.sessions[index].Turn = TurnStatus{Phase: "idle"}
		}
	}
	return m
}

// derefID：可空 id 的可读形式（没有就空串）。
func derefID(id *string) string {
	if id == nil {
		return ""
	}
	return *id
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
// 空 id = **空会话**，不是"前缀匹配所有" ⇒ 直接回 -1。
func (m model) findSession(id string) int {
	if id == "" {
		return -1
	}
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

// findMessage：在当前会话的消息里按完整 id 或**唯一前缀**找（前缀有歧义 ⇒ 当找不到，别猜）。
func (m model) findMessage(id string) string {
	found, hits := "", 0
	for _, message := range m.messages {
		if message.ID == id {
			return message.ID
		}
		if strings.HasPrefix(message.ID, id) {
			found, hits = message.ID, hits+1
		}
	}
	if hits == 1 {
		return found
	}
	return ""
}

// reloadMessagesCmd：把当前会话的消息重拉一遍（没有选中会话就什么都不做 —— 空 id 会去查
// 一条不存在的会话，界面上就成了"拉消息失败"）。
func (m model) reloadMessagesCmd() tea.Cmd {
	if session := m.currentSession(); session != nil {
		return loadMessages(m.client, session.ID)
	}
	return nil
}

// indexOfCurrentModel：当前会话用的渠道 / 模型在列表里第几个（挑选项的落点）。
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
//
// 一屏就三块（用户 2026-09-30 定的口径，见 AGENTS.md「界面该长什么样」）：
//
//	消息区：**只显示当前会话的对话**（最近的贴着输入框，从下往上排）
//	> 输入行（命令与消息都从这儿走）
//	------ （ASCII 分隔线：`─` 是模糊宽度字符，算两格 ⇒ 会超宽）
//	会话状态行（1-2 行：会话名 | 短 id | 渠道/模型 | 在不在跑 | 已连接 vX | 最近动作）
//
// **没有左栏**：会话选择只走 `/resume`（挑选项与查看器照旧**铺满消息区**）。

// View：**纯函数**（同样的 model ⇒ 同样的字符串）。
//
// 光标**不是画进正文的字符**：它是**真终端光标**（`View.Cursor`，Bubble Tea v2 的字段）⇒
// `Content` 一个字节都不变（现有那些逐字节断言一个字都不用改）。
// 位置由 render 一并算出来 —— 与正文同一份布局，不会各算各的而漂掉。
func (m model) View() tea.View {
	content, cursor := m.render()
	view := tea.NewView(content)
	view.Cursor = cursor
	return view
}

// render：**纯函数**（同样的 model ⇒ 同样的字符串）；顺带给出光标该摆在第几行第几列。
//
// 返回的 cursor 为 nil = **藏起来**：挑选项（`/resume` `/model`）与查看器（`/outgoing`、
// 确认框）铺满消息区时，输入行不是当前活动面 —— 别让光标留在屏幕上乱闪。
// **生成中照旧显示** ✓（输入行仍然可用，只是提交会被后端 409 顶回来）。
func (m model) render() (string, *tea.Cursor) {
	width, height := max(1, m.width), m.height
	if height < 1 {
		height = 24 // 还没收到 WindowSizeMsg 时的兜底（免得算出 0 行画面）
	}
	status := m.renderStatus(width) // 1-2 行
	palette := m.renderPalette(width)
	// 面板弹在输入行上方：屏幕再矮也要留一行消息区 ⇒ 装不下就只显示高亮那条附近的一段
	if room := height - 2 - len(status) - 1; len(palette) > room {
		palette = paletteWindow(palette, m.clampPalette(), max(room, 0))
	}
	bodyHeight := height - 2 - len(status) - len(palette) // 输入行 + 分隔线 + 状态行
	if bodyHeight < 1 {
		bodyHeight = 1
	}
	lines := m.renderMessages(bodyHeight, width)
	lines = append(lines, palette...)
	inputLine := len(lines)                                  // 输入行在 `lines` 里的下标（消息区 + 面板之后）
	lines = append(lines, truncate("> "+m.input, width))     // 输入行（命令与消息都从这儿走）
	lines = append(lines, m.style.dim(rule(width, m.width))) // 满线（暗色）：窄屏退回 ASCII `-`
	lines = append(lines, status...)
	if len(lines) > height { // 屏幕实在太矮（< 4 行）：宁可切掉上面的消息，也别把输入行挤没
		shift := len(lines) - height
		lines, inputLine = lines[shift:], inputLine-shift
	}
	var cursor *tea.Cursor
	if m.picker == pickerNone && m.viewer == nil && inputLine >= 0 && inputLine < len(lines) {
		// 列 = 前缀 2 格 + 光标**前**那段的**显示宽度**（CJK 一个字两格）；
		// 输入太长被截断时夹到最后一格（终端也就只能摆到那儿）。
		column := m.inputCursorColumn()
		if column > width-1 {
			column = width - 1
		}
		cursor = tea.NewCursor(max(column, 0), inputLine)
	}
	return strings.Join(lines, "\n"), cursor
}

// ── 输入行的光标：两个身份别混 ──
//
// **rune 下标**（model 里存的、增删要用的）≠ **显示列**（摆到终端上的）。
// 一个 rune 可能占**两格**（CJK）⇒ 列只能由 runewidth 现算（见 inputCursorColumn）。

// clampCursor：把光标夹回 [0, rune 数] —— 改动输入之后都过它一次，别让光标停到输入外面。
func clampCursor(cursor, runes int) int {
	if cursor < 0 {
		return 0
	}
	if cursor > runes {
		return runes
	}
	return cursor
}

// inputCursorColumn：光标在输入行里**第几格**（含 `> ` 前缀那两格）—— 按**显示宽度**算。
//
// 这是"光标指到字中间"的唯一防线：`你好|` 的列是 2+4=6 ✗ 不是 2+2=4（那是 rune 数）；
// `a你|` 是 2+1+2=5。
func (m model) inputCursorColumn() int {
	runes := []rune(m.input)
	return 2 + runewidth.StringWidth(string(runes[:clampCursor(m.inputCursor, len(runes))]))
}

// paletteWindow：面板装不下时只显示**高亮那条**附近的一段（不加行，也不让高亮跑出屏幕）。
func paletteWindow(lines []string, current, room int) []string {
	if room >= len(lines) {
		return lines
	}
	if room <= 0 {
		return nil
	}
	start := current - room/2
	if start < 0 {
		start = 0
	}
	if start+room > len(lines) {
		start = len(lines) - room
	}
	return lines[start : start+room]
}

// renderPalette：命令面板（输入以 `/` 开头时**推导**出来的 —— 不是又一个状态）。
func (m model) renderPalette(width int) []string {
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
		lines = append(lines, m.style.selected(truncate(cursor+label+"  "+candidate.help, width), index == current))
	}
	return lines
}

// renderMessages：**只画当前会话的对话**（没有左栏、也没有会话列表）。
//
// 为什么从底部往上整块排：聊天记录里**最新的在下面**（贴着输入行）；而且一块消息
// （署名 + 正文）要么整块出现、要么不出现 —— 从顶部截断会把署名切掉，
// 看起来像"不知道谁说的"（这个 bug 是测试抓出来的）。装不下的在顶部如实报数。
func (m model) renderMessages(height, width int) []string {
	if m.viewer != nil {
		hint := m.viewerHint
		if hint == "" {
			hint = "任意键关掉"
		}
		lines := []string{
			m.style.joined([]segment{{" " + m.viewerTitle, styleBold}, {hint, styleDim}}, width, "   "),
			m.style.dim(truncate(" "+rule(max(1, width-2), m.width), width)),
		}
		for _, line := range m.viewer {
			if len(lines) >= height {
				break
			}
			lines = append(lines, line)
		}
		return fillLines(lines, height)
	}
	switch m.picker {
	case pickerResume:
		return m.renderResumePicker(height, width)
	case pickerModel:
		return m.renderModelPicker(height, width)
	}
	session := m.currentSession()
	if session == nil {
		// 空会话：老实说清现在是什么、能做什么（**不是**会话列表）
		return fillLines([]string{
			truncate(" 空会话 —— /resume 挑一条旧会话，或直接在下面输入开始聊", width),
		}, height)
	}
	busy := m.turn.Busy() && m.turn.sessionID == session.ID
	if m.messagesFor == session.ID && len(m.messages) == 0 && !busy {
		return fillLines([]string{truncate(" 这条会话还没有消息 —— 在下面输入就开始了", width)}, height)
	}
	blocks := make([][]string, 0, len(m.messages)+1)
	for _, message := range m.messages {
		blocks = append(blocks, m.messageBlock(message, width))
	}
	if busy { // 生成中那条回复：库里还没有它 ⇒ 界面**合成**一个气泡（落库后同 id 的真消息自然取代它）
		blocks = append(blocks, m.liveBlock(width))
	}
	return fitBlocks(blocks, height, width)
}

// messageBlock：一条消息在界面上的样子（署名 + 正文 + 一个空行）—— **整块**是它的最小单位。
//
// 署名上色（口径见 AGENTS.md）：你自己 = 青，助手 = 洋红（**耗时数字用暗色** —— 它是次要信息）。
// 正文保持默认色（内容才是主角）。
func (m model) messageBlock(message Message, width int) []string {
	label := "你"
	kind := styleCyan
	if message.Role == "assistant" {
		label = "助手" // TODO：等 /agents 搬完，改用该会话 agent 的名字（界面口径见 AGENTS.md）
		kind = styleMagenta
	}
	detail := ""
	if message.DurationMS != nil {
		detail = fmt.Sprintf("%.1fs", float64(*message.DurationMS)/1000)
	}
	signature := []segment{{" " + label, kind}}
	if detail != "" { // `助手 2.1s：` —— 耗时**暗色**，署名与冒号还是本色
		signature = append(signature,
			segment{" ", stylePlain}, segment{detail, styleDim}, segment{"：", kind})
	} else {
		signature = append(signature, segment{"：", kind})
	}
	block := []string{m.style.joined(signature, width, "")}
	for _, line := range strings.Split(message.Content, "\n") {
		block = append(block, truncate("   "+line, width))
	}
	return append(block, "")
}

// fitBlocks：从**最新**那块往上装（整块装、装不下就整块不显示）；
// 顶上被挤掉了几条就如实写一行"（上面还有 N 条）"。
func fitBlocks(blocks [][]string, height, width int) []string {
	taken, used := fitFromBottom(blocks, height)
	if taken > 0 { // 顶上那行提示自己也要占一格 ⇒ 少一格再量一次
		taken, used = fitFromBottom(blocks, height-1)
	}
	lines := make([]string, 0, height)
	if taken > 0 {
		lines = append(lines, truncate(fmt.Sprintf(" （上面还有 %d 条）", taken), width))
	}
	for len(lines)+used < height { // 消息贴着输入行 ⇒ 空行全留在上面
		lines = append(lines, "")
	}
	for _, block := range blocks[taken:] {
		lines = append(lines, block...)
	}
	return fillLines(lines, height)
}

// fitFromBottom：从最新那块往上量，返回"最上面从第几块开始显示"与"一共用了几行"。
func fitFromBottom(blocks [][]string, height int) (taken, used int) {
	taken = len(blocks)
	for taken > 0 {
		size := len(blocks[taken-1])
		if used+size > height {
			break
		}
		used += size
		taken--
	}
	return taken, used
}

// fillLines：补空行到 height 行（多了就砍尾巴 —— 每块自己已经按宽度截过了）。
func fillLines(lines []string, height int) []string {
	for len(lines) < height {
		lines = append(lines, "")
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return lines
}

// liveBlock：**生成中那一刻**的气泡（库里还没有那条回复）。
//
// 口径照 AGENTS.md：转圈 + "生成中… 12.3s" + 「/stop」；思考只报**字数**
// （token 数流式帧里没有 —— 卡片脚注那儿的 token 是另一回事，别混）。
func (m model) liveBlock(width int) []string {
	// 口径：`生成中…` 那段**黄**（含下面的「/stop 停止」）；署名还是助手本色；思考是次要信息 ⇒ 暗色。
	block := []string{m.style.joined([]segment{
		{" 助手（", styleMagenta},
		{fmt.Sprintf("生成中… %.1fs", float64(m.turn.elapsedMS)/1000), styleYellow},
		{"）：", styleMagenta},
	}, width, "")}
	if m.turn.thinking != "" {
		block = append(block, m.style.dim(truncate(
			fmt.Sprintf("   [思考中… %d 字]", len([]rune(m.turn.thinking))), width)))
	}
	for _, line := range strings.Split(m.turn.text, "\n") {
		block = append(block, truncate("   "+line, width))
	}
	block = append(block, m.style.yellow(truncate("   （/stop 停止）", width)))
	return append(block, "")
}

func (m model) renderResumePicker(height, width int) []string {
	lines := []string{
		m.style.joined([]segment{
			{" 挑一条已有会话", styleBold},
			{"上/下 选 · 回车进 · Esc 取消", styleDim},
		}, width, "   "),
		m.style.dim(truncate(" "+rule(max(1, width-2), m.width), width)),
	}
	for index, session := range m.sessions {
		if len(lines) >= height {
			break
		}
		cursor := "  "
		if index == m.pickerIndex {
			cursor = "> "
		}
		title := strings.TrimSpace(session.Title)
		if title == "" {
			title = "（还没起名）"
		}
		lines = append(lines, m.pickerLine([]segment{
			{cursor + shortID(session.ID) + " ", styleDim}, // 短 id：次要信息 ⇒ 暗色
			{title, stylePlain},
			{"    " + session.Provider + " / " + session.Model, styleDim},
		}, width, index == m.pickerIndex))
	}
	return fillLines(lines, height)
}

// renderModelPicker：**按 provider 排列**（用户 2026-09-29 的口径：先不分能力，按渠道分组）。
func (m model) renderModelPicker(height, width int) []string {
	lines := []string{
		m.style.joined([]segment{
			{" 挑渠道 / 模型", styleBold},
			{"上/下 选 · 回车定 · Esc 取消", styleDim},
		}, width, "   "),
		m.style.dim(truncate(" "+rule(max(1, width-2), m.width), width)),
	}
	if len(m.models) == 0 {
		lines = append(lines, truncate(" （还没有发现任何模型 —— 设置里给渠道点一次「获取模型」）", width))
	}
	currentProvider := ""
	for index, item := range m.models {
		if len(lines) >= height {
			break
		}
		if item.Provider != currentProvider {
			currentProvider = item.Provider
			lines = append(lines, m.style.bold(truncate(" -- "+item.ProviderLabel()+" --", width)))
			if len(lines) >= height {
				break
			}
		}
		cursor := "  "
		if index == m.pickerIndex {
			cursor = "> "
		}
		current := ""
		if item.Provider == m.chosenProvider && item.UpstreamID == m.chosenModel {
			current = "（当前）"
		}
		lines = append(lines, m.pickerLine([]segment{
			{cursor + item.Label(), stylePlain},
			{current, styleDim},
		}, width, index == m.pickerIndex))
	}
	return fillLines(lines, height)
}

// pickerLine：挑选项里的一行 —— 选中的那条**粗体 + 青**（其余保持默认）。
//
// 选中时**不再叠暗色** ✗：套两层转义会在行中间插一个复位，把后半行打回默认色（看出来像掉色）。
func (m model) pickerLine(segments []segment, width int, selected bool) string {
	if selected {
		return m.style.boldCyan(truncate(plainOf(segments, ""), width))
	}
	return m.style.joined(segments, width, "")
}

// currentSession：当前会话 —— **按 id 找**（不按下标：列表随时会重排，见 selectedID）。
func (m model) currentSession() *Session {
	if m.selectedID == "" {
		return nil
	}
	index := m.findSession(m.selectedID)
	if index < 0 {
		return nil
	}
	return &m.sessions[index]
}

// renderStatus：底下的**会话状态行**（口径见 AGENTS.md「界面该长什么样」）。
//
// 两半**互不顶替**（旧口径就这么定的）：会话那半（名字 | 短 id | 渠道/模型 | 上下文占用 | 在不在跑）
// 与"已连接 vX | 最近一次动作"。一行放得下就一行，放不下就折成两行 —— **最多两行**。
func (m model) renderStatus(width int) []string {
	session := m.currentSession()
	head := []segment{{"空会话", styleDim}}
	if session != nil {
		title := strings.TrimSpace(session.Title)
		if title == "" {
			title = "（还没起名）"
		}
		where := session.Provider + "/" + session.Model
		if session.Provider == "" || session.Model == "" {
			where = "（还没选模型）"
		}
		head = []segment{
			{title, styleBold},              // 会话名：粗体（拉层级，不上色）
			{shortID(session.ID), styleDim}, // 短 id / 渠道·模型都是次要信息 ⇒ 暗色
			{where, styleDim},
		}
		if usage := m.contextUsage(session); usage != nil {
			kind := styleDim
			if usage.OverBudget {
				kind = styleRed // **超预算 ⇒ 这一段用红**
			}
			head = append(head, segment{contextLabel(*usage), kind})
		}
	}
	// 在不在跑**比名字重要**（用户口径）⇒ 先给它留位置，再把前面那几档按余量截断
	state := m.turnLabel(session)
	stateKind := styleDim
	if strings.HasPrefix(state, "生成中") {
		stateKind = styleYellow // 生成中 ⇒ 黄（含耗时）
	}
	room := width - runewidth.StringWidth(state) - len(" | ")
	sessionInfo, infoWidth := m.style.concat(head, max(room, 0), " | ")
	if room < 1 { // 屏幕太窄：只留"在不在跑"
		state = truncate(state, width)
		sessionInfo, infoWidth = m.style.paint(stateKind, state), runewidth.StringWidth(state)
	} else {
		sessionInfo += " | " + m.style.paint(stateKind, state)
		infoWidth += len(" | ") + runewidth.StringWidth(state)
	}
	tail := []segment{{"未连接", styleDim}}
	if m.version != "" {
		tail = []segment{{"已连接 v" + m.version, styleDim}}
	}
	if m.lastAction != "" {
		kind := stylePlain
		if m.lastAction == m.lastFailure { // 还摆着的那句失败 ⇒ 红（见 model.lastFailure）
			kind = styleRed
		}
		tail = append(tail, segment{m.lastAction, kind})
	}
	tailPlain := plainOf(tail, " | ")
	if infoWidth+len(" | ")+runewidth.StringWidth(tailPlain) <= width {
		return []string{sessionInfo + " | " + m.style.joined(tail, width, " | ")}
	}
	return []string{sessionInfo, m.style.joined(tail, width, " | ")}
}

// contextUsage：当前会话的上下文占用 —— 拉回来的那份**必须属于这条会话**才用
// （换会话时那份还旧着 ⇒ 拿它画就是撒谎）。
func (m model) contextUsage(session *Session) *ContextUsage {
	if m.context == nil || session == nil || m.contextFor != session.ID {
		return nil
	}
	return m.context
}

// contextLabel：状态行里那一档（照 Pi 的形状）：`12.3k/131k (9.4%)`。
//
// 百分比 = `used / budget`（**自己算** ✗ 别拿契约里的 `ratio` —— 那是分词器标定比，不是占用比例，见 client.go）。
// 超预算就照实报 >100%（夹到 100% 会把"超了多少"藏起来）。
func contextLabel(usage ContextUsage) string {
	percent := 0.0
	if usage.BudgetTokens > 0 {
		percent = float64(usage.UsedTokens) / float64(usage.BudgetTokens) * 100
	}
	return fmt.Sprintf("%s/%s (%.1f%%)",
		formatTokens(usage.UsedTokens), formatTokens(usage.BudgetTokens), percent)
}

// formatTokens：token 数缩写（≥1000 用 k，整千不带小数点）：`12.3k` / `131k`。
func formatTokens(tokens int) string {
	if tokens < 1000 {
		return strconv.Itoa(tokens)
	}
	return strings.Replace(fmt.Sprintf("%.1fk", float64(tokens)/1000), ".0k", "k", 1)
}

// turnLabel：这一轮在不在跑（"生成中 + 耗时" / "空闲"）。
//
// 界面自己那份（`m.turn`）优先：它比会话列表里那份新（列表是上一次刷新时的样子）。
func (m model) turnLabel(session *Session) string {
	if session == nil {
		return "空闲"
	}
	if m.turn.Busy() && m.turn.sessionID == session.ID {
		return fmt.Sprintf("生成中 %.1fs", float64(m.turn.elapsedMS)/1000)
	}
	if session.Turn.Busy() {
		return fmt.Sprintf("生成中 %.1fs", float64(session.Turn.ElapsedMS)/1000)
	}
	return "空闲"
}

// ── 小工具（宽度一律按**显示宽度**算：中文占两格，所以必须用 runewidth）──

// truncate：按**显示宽度**截断到 width 格以内（中文占两格 ⇒ 不能按字符数截）；短了不补空格。
//
// 两个坑：
//  1. 不用 `runewidth.Truncate`：它把省略号算进宽度却**少填一格**（"abcdefghij",5 ⇒ "abc…" ✗）；
//  2. 省略号用 **ASCII `...`** ✗ 不用 `…`：`…` 是**模糊宽度**字符（East Asian Ambiguous），
//     runewidth 当一格、CJK 终端可能当两格 ⇒ 宽度账与实际差一格。满线 `─` 是同一个坑
//     （所以它只在**宽屏**上用，窄屏退回 ASCII —— 见 rule），结构性字符（提示符 / 竖线）则一律 ASCII。
func truncate(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if runewidth.StringWidth(text) <= width {
		return text
	}
	if width <= len(ellipsis) { // 连省略号都放不下 ⇒ 只给三点里放得下的那几格（ASCII ⇒ 一格一个）
		return ellipsis[:width]
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

// ── 画线：**重复**，不是"填充" ──

// ruleMinWidth：满线用 `─` 的最小屏幕宽度。`─` 是**模糊宽度**字符（East Asian Ambiguous）：
// 宽度账上按一格算（见 init），但 CJK 终端可能按两格排 ⇒ 窄屏上会溢出换行。
// 所以窄屏退回 ASCII `-`（横线是界面骨架，骨架歪了整屏都歪）。
const ruleMinWidth = 60

// rule：画一条 `cells` 格的满线。**绝不用模糊宽度字符去"填充"对齐** —— 只用重复（`strings.Repeat`）。
func rule(cells, screen int) string {
	cells = max(1, cells)
	if screen < ruleMinWidth {
		return strings.Repeat("-", cells)
	}
	return strings.Repeat("─", cells)
}

// ── 着色：**只有前景色**，绝不给背景上色（用户明确要求）──

// styler：极小的着色器。三条硬规矩（见 AGENTS.md「踩过的坑」）：
//
//  1. **只前景**：16 色基础 ANSI（30–37 / 90–97）+ 粗体 / 暗色；不用 256 色 / truecolor，不用背景色；
//  2. **先排版、后上色**：转义序列零宽，而 truncate / 对齐是按格算的 ⇒
//     截断一律作用在**未着色**的原文上（落点是 concat / plainOf），颜色最后才包上去；
//  3. **可关**：零值 = 关 ⇒ 测试拿到的 View() 是纯文本（逐字节断言靠这个）。
//
// 生产路径的开关在 detectStyler（`NO_COLOR` / `TERM=dumb` / 非 TTY 三档降级）。
type styler struct{ enabled bool }

// styleKind：要包哪一层前景色（零值 = 不上色）。
type styleKind int

const (
	stylePlain styleKind = iota
	styleBold
	styleDim
	styleCyan
	styleMagenta
	styleYellow
	styleRed
	styleBoldCyan
)

func (s styler) paint(kind styleKind, text string) string {
	if !s.enabled || text == "" {
		return text
	}
	code := ""
	switch kind {
	case styleBold:
		code = "1"
	case styleDim:
		code = "2"
	case styleCyan:
		code = "36"
	case styleMagenta:
		code = "35"
	case styleYellow:
		code = "33"
	case styleRed:
		code = "31"
	case styleBoldCyan:
		code = "1;36"
	default:
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func (s styler) bold(text string) string     { return s.paint(styleBold, text) }
func (s styler) dim(text string) string      { return s.paint(styleDim, text) }
func (s styler) yellow(text string) string   { return s.paint(styleYellow, text) }
func (s styler) red(text string) string      { return s.paint(styleRed, text) }
func (s styler) boldCyan(text string) string { return s.paint(styleBoldCyan, text) }

// selected：挑选项里**选中**的那一行 —— 粗体 + 青（没选中就原样）。
func (s styler) selected(text string, on bool) string {
	if !on {
		return text
	}
	return s.boldCyan(text)
}

// segment：一段"先排版、后上色"的文本：text 是**未着色**的原文（宽度账只算它），kind 决定包什么色。
//
// ⚠ 别在同一段里嵌套两种颜色：行中间那个复位会把后半段打回默认色（宁可分两段）。
type segment struct {
	text string
	kind styleKind
}

// concat：把若干段按 sep 拼进 limit 格，返回拼好的字符串与它**未着色**的宽度。
//
// 装不下的段**整段丢掉**（含它前面的分隔符）；最后那一段太长就**截断**（带省略号）后收工 ——
// 截断发生在 paint 之前，所以宽度账与终端实际排出来的格数一致（**先排版、后上色**）。
func (s styler) concat(segments []segment, limit int, sep string) (string, int) {
	var builder strings.Builder
	used := 0
	for _, seg := range segments {
		if seg.text == "" {
			continue
		}
		gap := 0
		if used > 0 {
			gap = runewidth.StringWidth(sep)
		}
		room := limit - used - gap
		if room <= 0 {
			break
		}
		text := seg.text
		clipped := runewidth.StringWidth(text) > room
		if clipped {
			if room <= len(ellipsis) { // 连省略号都放不下 ⇒ 这一段不显示（宁可空着，也不超宽）
				break
			}
			text = truncate(text, room)
		}
		if used > 0 {
			builder.WriteString(sep)
		}
		builder.WriteString(s.paint(seg.kind, text))
		used += gap + runewidth.StringWidth(text)
		if clipped {
			break
		}
	}
	return builder.String(), used
}

// joined：只要拼好的字符串（不关心它占了几格）。
func (s styler) joined(segments []segment, limit int, sep string) string {
	text, _ := s.concat(segments, limit, sep)
	return text
}

// plainOf：把几段按 sep 拼成**未着色**的原文 —— 宽度账与"一行放不放得下"都只算它。
func plainOf(segments []segment, sep string) string {
	parts := make([]string, 0, len(segments))
	for _, seg := range segments {
		if seg.text != "" {
			parts = append(parts, seg.text)
		}
	}
	return strings.Join(parts, sep)
}

// 宽度账与终端自洽：模糊宽度（East Asian Ambiguous，如 `│` `▶` `·` `…`）**按一格**算 ——
// CJK 宽字（`你` 这类 Wide）不受影响，仍是两格 ✓。
// **结构性字符**（提示符、竖线、省略号）一律 ASCII ✗ 例外只有一个：满线 `─` 在**宽屏**上用
// （窄屏退回 ASCII，见 rule —— 模糊宽度字符在 CJK 终端可能按两格排）。
func init() { runewidth.DefaultCondition.EastAsianWidth = false }

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
