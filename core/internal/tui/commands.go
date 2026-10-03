package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/mattn/go-runewidth"
)

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
		{name: "new", help: "新建会话（当前会话还没有消息时无效 —— 你已经在一条新会话里了）", run: commandNew},
		{name: "delete", help: "删除当前会话（删完立刻再建一条并进去；不可逆，先确认）", run: commandDelete},
		{name: "cut", args: "[message_id]", help: "删一条消息及它之后的全部（先预览，再确认）", run: commandCut},
		{name: "copy", help: "把当前会话复制成新的一条（分岔）", run: commandCopy},
		{name: "rename", args: "<新名字>", help: "给当前会话改名（名字里可以有空格；改过名后自动起标题不再覆盖）", run: commandRename},
		{name: "resume", args: "[uuid]", help: "挑一条已有会话；带 uuid 直接进", run: commandResume},
		{name: "model", help: "挑渠道 / 模型（有会话就改它，没有则留给下一条）", run: commandModel},
		{name: "outgoing", help: "看当前已定历史的载荷（哪几条是压缩出来的；不含还没发的那句）", run: commandOutgoing},
		{name: "state", help: "看当前会话的世界状态", run: commandState},
		{name: "usage", help: "看当前渠道的套餐余量（OpenCode GO 套餐；别的渠道会如实说不支持）", run: commandUsage},
		{name: "providers", help: "看全部渠道（每条一行：渠道、协议、有无密钥、几个模型；附预设名单）", run: commandProviders},
		{name: "provider-add", help: "加一条渠道（四问：编号、预设 vendor、协议、密钥；dummy 跳过密钥）", run: commandProviderAdd},
		{name: "provider-del", args: "<id>", help: "删一条渠道（不可逆，先确认）", run: commandProviderDel},
		{name: "agents", args: "[defaults | use <agentID> | set-prompt <文本>]", help: "看人格列表（回车进详情；t 拨能力开关；缺省三件附在下面）", run: commandAgents},
		{name: "agent-create", args: "<名字>", help: "建一份人格（名唯一必填；建完自动进详情）", run: commandAgentCreate},
		{name: "agent-del", args: "<id>", help: "删一份人格（不可逆，先确认）", run: commandAgentDel},
		{name: "telegram-bind", args: "<tg_id>", help: "绑定 Telegram 单账户（绑新的自动顶掉旧的；bot 只认绑定的这个 id）", run: commandTelegramBind},
		{name: "telegram-bot-token-set", args: "<token>", help: "设置 TG bot token（写 config.json；改完要重启生效）", run: commandTelegramTokenSet},
		{name: "telegram-toggle", help: "开/关 TG bot（显式动作；开了才连外网，关了就停轮询）", run: commandTelegramToggle},
		{name: "telegram-status", help: "看 TG bot 状态（跑没跑、绑了谁、在线多久）", run: commandTelegramStatus},
		{name: "think", help: "展开当前会话的思考全文（默认折叠在消息上方，想读全文才用）", run: commandThink},
		{name: "system", help: "显示 / 隐藏消息区顶部的系统提示词（调试用，开关记在 ~/.config/microchat/tui.json）", run: commandSystem},
		{name: "compact", args: "[N]", help: "按块压 N 个块成摘要（块=assistant→user交界，一块≥2条；底层不够就抬头并摘要；不给N用默认值）", run: commandCompact},
		{name: "reroll", args: "[switch <n> | delete <n> | off | list]",
			help: "重摇尾条那条回复（不带参数 = 进模式并摇一次；候选只在内存里）", run: commandReroll},
		{name: "reroll-summary", args: "<idx> | switch <n> | delete <n> | off | list",
			help: "重摇一条摘要（<idx> = 1-based 消息序号，上溯到它的根摘要；候选只在内存里）", run: commandRerollSummary},
		{name: "stop", help: "打断正在生成的那一轮（幂等）", run: commandStop},
		{name: "refresh", help: "重新拉会话列表", run: commandRefresh},
		{name: "help", help: "列命令（含还没搬完的）", run: commandHelp},
		{name: "quit", help: "退出", run: commandQuit},
	}
}

// pendingCommands：路由还没搬完的（如实列着，不假装支持）。
var pendingCommands = []string{"/archive"}

func commandNew(m model, _ []string) (tea.Model, tea.Cmd) {
	// 规矩：**当前会话还没有消息时 /new 无效**（你已经在一条新会话里了 —— 再建就是造垃圾行）。
	// 注意"没有会话"（兵灾态）**不算空**：那时 /new 正是出路，真建一条。
	if m.isEmptySession() {
		m.lastAction = "当前会话还没有消息 —— 你已经在一条新会话里了，/new 无效（/resume 挑一条旧的）"
		return m, nil
	}
	m.lastAction = "新建会话…"
	return m, createSessionCmd(m.client, m.chosenProvider, m.chosenModel)
}

// commandDelete：删**整条会话**（删完**立刻再建一条并进去** —— 没有"没有会话"这个状态）。
//
// 不可逆 ⇒ 先摊开"会没掉什么"（几条消息、几份它挂着的摘要 —— 数字现算），
// 等用户点头（回车 / y）才真发；点头之前一个字节都不动。
func commandDelete(m model, _ []string) (tea.Model, tea.Cmd) {
	session := m.currentSession()
	if session == nil {
		return m.fail("没有可删的会话：" + noSession), nil
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
		return m.fail("没有可删的消息：" + noSession), nil
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
		return m.fail("没有可复制的会话：" + noSession), nil
	}
	m.lastAction = "复制 " + describeSession(*session) + "…"
	return m, copySessionCmd(m.client, session.ID)
}

// commandRename：给当前会话改名（`/rename <新名字>`）。
//
// 走**已有的** `PATCH /sessions/{session_id}`（`{"title": …}`）—— **不新增路由**。
// 改过名就是"用户改过名" ⇒ 后端的自动起标题**永不再覆盖**（那条不变量在后端，客户端不多事）。
// 名字里可以有空格（用引号括起来也行）；**空名字 ⇒ 拒绝**（那会变成"还没起名"，把它退回去了）。
func commandRename(m model, args []string) (tea.Model, tea.Cmd) {
	session := m.currentSession()
	if session == nil {
		return m.fail("没有可改名的会话：" + noSession), nil
	}
	title := renameArg(args)
	if title == "" {
		m.lastAction = "用法：/rename <新名字>（名字里可以有空格；引号可省）—— 空名字不行"
		return m, nil
	}
	m.lastAction = "改名中…"
	return m, renameSessionCmd(m.client, session.ID, title)
}

// renameArg：把 `/rename` 后面那串参数拼回**一个名字**。
//
// 命令是按空白切开的 ⇒ 名字里有空格时要么整串拼回来（`/rename 我的 新名字`），
// 要么用引号括起来（`/rename "我的 新名字"`，两头那对引号剥掉）。空串 = 没给名字。
func renameArg(args []string) string {
	title := strings.TrimSpace(strings.Join(args, " "))
	if len(title) >= 2 {
		first, last := title[0], title[len(title)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			title = strings.TrimSpace(title[1 : len(title)-1])
		}
	}
	return title
}

// commandSystem：切换消息区顶部那条"系统提示词"（调试用）—— 顺手把开关**记到本地**。
//
// 界面偏好住 `~/.config/microchat/tui.json`（**不进**后端 `config/` 与 `data/`，也**不动** Godot 那份 `frontend.json`）。
func commandSystem(m model, _ []string) (tea.Model, tea.Cmd) {
	m.showSystemPrompt = !m.showSystemPrompt
	if m.showSystemPrompt {
		m.lastAction = "系统提示词：显示在消息区最上面（已记住）"
	} else {
		m.lastAction = "系统提示词：不再显示（已记住）"
	}
	cmds := []tea.Cmd{saveUIPrefCmd(m.uiPath, m.showSystemPrompt)}
	// 打开时顺手把这份拉回来（关着的时候可能从没拉过 / 是上一条会话的）
	if session := m.currentSession(); m.showSystemPrompt && session != nil && m.promptFor != session.ID {
		cmds = append(cmds, loadSystemPrompt(m.client, session.ID))
	}
	return m, tea.Batch(cmds...)
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
	case confirmDeleteProvider:
		m.lastAction = "删除渠道 " + pending.provider + "…"
		return m, deleteProviderCmd(m.client, pending.provider)
	case confirmDeleteAgent:
		m.lastAction = "删除人格 " + shortID(pending.agentID) + "…"
		return m, deleteAgentCmd(m.client, pending.agentID)
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
		return m.fail("没有载荷可看：" + noSession), nil
	}
	m.lastAction = "拉载荷…"
	return m, loadOutgoingCmd(m.client, session.ID)
}

func commandState(m model, _ []string) (tea.Model, tea.Cmd) {
	session := m.currentSession()
	if session == nil {
		return m.fail("没有状态可看：" + noSession), nil
	}
	if len(m.messages) == 0 {
		m.lastAction = "这条会话还没有消息，状态是空的"
		return m, nil
	}
	m.lastAction = "取状态…"
	return m, loadState(m.client, session.ID)
}

// commandThink：展开当前会话的**思考全文**（默认折叠在每条消息上方那一行）。
//
// 走**现有查看器**（铺满消息区、任意键关掉）；没有思考就如实说一句。
func commandThink(m model, _ []string) (tea.Model, tea.Cmd) {
	session := m.currentSession()
	if session == nil {
		return m.fail("没有可看的思考：" + noSession), nil
	}
	if m.messagesFor != session.ID {
		m.lastAction = "先把消息拉全再看思考（正在取…）"
		return m, loadMessages(m.client, session.ID)
	}
	lines := m.thinkingLines()
	if len(lines) == 0 {
		m.lastAction = "这条会话里没有思考内容（渠道没存思考，或这几条回复本来就没有思考）"
		return m, nil
	}
	m.viewer, m.viewerTitle = lines, "思考全文"
	m.viewerHint = "任意键关掉"
	m.lastAction = "思考全文（任意键关掉）"
	return m, nil
}

// thinkingLines：当前会话里**每一条带思考的消息**的全文（暗色表头 + 正文）。
func (m model) thinkingLines() []string {
	lines := []string{}
	for _, message := range m.messages {
		if strings.TrimSpace(message.Reasoning) == "" {
			continue
		}
		lines = append(lines, m.style.dim(truncate(
			fmt.Sprintf(" %s · %s", shortID(message.ID), thinkingLabel(message)), max(1, m.width))))
		for _, line := range strings.Split(message.Reasoning, "\n") {
			lines = append(lines, truncate("   "+line, max(1, m.width)))
		}
		lines = append(lines, "")
	}
	return lines
}

// commandUsage：看当前会话那条渠道的**套餐余量**（OpenCode GO）。
//
// 只读；渠道不是 opencode-go（或没配 key）时后端 400，界面照实说一句 —— 不假装有数。
func commandUsage(m model, _ []string) (tea.Model, tea.Cmd) {
	session := m.currentSession()
	if session == nil {
		return m.fail("没有可用渠道可查：" + noSession), nil
	}
	if session.Provider == "" {
		return m.fail("这条会话没有渠道（先用 /model 选一个）"), nil
	}
	m.lastAction = "查余量…"
	return m, loadUsage(m.client, session.Provider)
}

// usageLines：套餐余量摊成查看器的行 —— 每个窗口一行：
// `Rolling (5h)  6% · 重置 2026-09-30T13:47Z`（标签暗色；百分比超 80% 黄、到 100% 红）。
func (m model) usageLines(usage PlanUsage) []string {
	lines := []string{}
	if usage.Plan != "" {
		lines = append(lines, m.style.dim(truncate(
			" 套餐："+usage.Plan+" · 渠道 "+usage.ProviderID, max(1, m.width))))
	}
	for _, window := range usage.Windows {
		kind := stylePlain
		switch {
		case window.Percent >= 100:
			kind = styleRed
		case window.Percent > 80:
			kind = styleYellow
		}
		reset := window.ResetsAt.UTC().Format("2006-01-02T15:04Z")
		lines = append(lines, m.style.joined([]segment{
			{" " + window.Label + "  ", styleDim},
			{fmt.Sprintf("%.0f%%", window.Percent), kind},
			{" · 重置 " + reset, styleDim},
		}, max(1, m.width), ""))
	}
	return lines
}

// commandProviders：看全部渠道（只读查看器 —— 复用 viewer，任意键关掉）。
//
// 查两份（渠道 + 预设）：每条渠道一行 `id · vendor/protocol · 有无 key · N 个模型`；
// 下面一行 `presets: a, b, c` 列预设 vendor 名。失败照实说。
func commandProviders(m model, _ []string) (tea.Model, tea.Cmd) {
	m.lastAction = "拉渠道…"
	return m, loadProvidersCmd(m.client)
}

// providerLines：渠道查看器的行（渠道行 + 预设名单行 —— 后者只有一行，别摊开）。
func (m model) providerLines(providers []ProviderInfo, presets []PresetItem) []string {
	lines := []string{}
	for _, provider := range providers {
		key := "无 key"
		if provider.HasKey {
			key = "有 key"
		}
		lines = append(lines, truncate(
			fmt.Sprintf(" %s · %s/%s · %s · %d 个模型",
				provider.ID, provider.Vendor, provider.Protocol, key, len(provider.Models)),
			max(1, m.width)))
	}
	names := make([]string, 0, len(presets))
	for _, preset := range presets {
		names = append(names, preset.Vendor)
	}
	lines = append(lines, truncate(" presets: "+strings.Join(names, ", "), max(1, m.width)))
	return lines
}

// commandProviderAdd：加一条渠道（四问向导，不开 picker —— 输入行就是问卷）。
//
// `id?` → `vendor?`（打 presets 名单）→ `protocol?`（缺省 chat；只接受该 vendor 名单里的）
// → `key?`（可空；dummy 直接跳过）→ 一次 Create。重进覆盖旧草稿。
func commandProviderAdd(m model, _ []string) (tea.Model, tea.Cmd) {
	m.addStep, m.addDraft, m.addPresets = 1, providerDraft{}, nil
	m.agentDetail, m.viewer, m.viewerTitle, m.viewerHint, m.viewerMode = nil, nil, "", "", ""
	m.agentRows, m.detailAbils, m.agentSel = nil, nil, 0
	m.defStep, m.defDraft = 0, Defaults{}
	m.lastAction = "新渠道 id？（输入编号，回车继续；Esc 取消）"
	return m, loadPresetsCmd(m.client)
}

// loadPresetsCmd：向导第二问要打 presets 名单 —— 先拿回来存着（拿不到就向导里如实说）。
func loadPresetsCmd(client *Client) tea.Cmd {
	return func() tea.Msg {
		presets, err := client.Presets()
		return presetsMsg{presets: presets, err: err}
	}
}

// presetsMsg：向导用的预设名单（失败也回来 —— 向导里如实说，不卡死）。
type presetsMsg struct {
	presets []PresetItem
	err     error
}

// presetByVendor：在名单里按 vendor 找（大小写不敏感 —— 用户手打的，别为大小写拦人）。
func presetByVendor(presets []PresetItem, vendor string) *PresetItem {
	for index := range presets {
		if strings.EqualFold(presets[index].Vendor, vendor) {
			return &presets[index]
		}
	}
	return nil
}

// answerWizard：向导的一问答完了（输入行非 `/` 开头的那句就是答案）。
func (m model) answerWizard(text string) (tea.Model, tea.Cmd) {
	answer := strings.TrimSpace(text)
	switch m.addStep {
	case 1: // id?
		if answer == "" {
			m.lastAction = "编号不能为空（再输一次；Esc 取消）"
			return m, nil
		}
		m.addDraft.id = answer
		m.addStep = 2
		m.lastAction = "vendor？（" + presetNamesOf(m.addPresets) + "；Esc 取消）"
		return m, nil
	case 2: // vendor?（打 presets 名单）
		if answer == "" {
			m.lastAction = "vendor 不能为空（" + presetNamesOf(m.addPresets) + "；Esc 取消）"
			return m, nil
		}
		// 名单没拿到时不拦（别为一次网络抖动卡死向导 —— 后端建时还会过矩阵）。
		if preset := presetByVendor(m.addPresets, answer); preset != nil {
			m.addDraft.vendor = preset.Vendor
			m.addStep = 3
			m.lastAction = "protocol？（缺省 chat；可选 " + strings.Join(preset.Protocols, ", ") + "；Esc 取消）"
			return m, nil
		} else if len(m.addPresets) > 0 {
			m.lastAction = "没见过 vendor " + answer + "（可选：" + presetNamesOf(m.addPresets) + "；Esc 取消）"
			return m, nil
		}
		m.addDraft.vendor = answer
		m.addStep = 3
		m.lastAction = "protocol？（缺省 chat；Esc 取消）"
		return m, nil
	case 3: // protocol?（缺省 chat；只接受该 vendor 名单里的）
		preset := presetByVendor(m.addPresets, m.addDraft.vendor)
		protocol := ""
		if answer != "" {
			protocol = normalizeProtocol(answer)
			if preset != nil && !protocolAllowed(preset.Protocols, protocol) {
				m.lastAction = "vendor " + m.addDraft.vendor + " 不支持 protocol " + answer +
					"（可选 " + strings.Join(preset.Protocols, ", ") + "；Esc 取消）"
				return m, nil
			}
		}
		// 空 = 缺省 chat：draft 里留空，Create 时不发 protocol 这个键（后端兜底）。
		m.addDraft.protocol = protocol
		m.addStep = 4
		if strings.EqualFold(m.addDraft.vendor, "dummy") {
			return m.finishWizard()
		}
		m.lastAction = "key？（可空，直接回车跳过；Esc 取消）"
		return m, nil
	case 4: // key?（可空；dummy 直接跳过）
		m.addDraft.apiKey = answer
		return m.finishWizard()
	}
	return m, nil
}

// finishWizard：第四问答完（dummy 在第三问答完）—— 发一次 Create。
func (m model) finishWizard() (tea.Model, tea.Cmd) {
	draft := m.addDraft
	m.lastAction = "建渠道 " + draft.id + "…"
	return m, createProviderCmd(m.client, draft)
}

// presetNamesOf：向导第二问括号里的名单（拿不到名单时照实说没拿到 —— 别空着让人猜）。
func presetNamesOf(presets []PresetItem) string {
	if len(presets) == 0 {
		return "预设名单没拿到，先照 vendor 名填"
	}
	names := make([]string, 0, len(presets))
	for _, preset := range presets {
		names = append(names, preset.Vendor)
	}
	return strings.Join(names, ", ")
}

// normalizeProtocol：用户手打的协议名收敛一下（`chat` 是缺省的别名 —— 后端认全名）。
func normalizeProtocol(protocol string) string {
	lowered := strings.ToLower(strings.TrimSpace(protocol))
	if lowered == "chat" {
		return "openai-chat-completion"
	}
	return lowered
}

// protocolAllowed：这个 protocol 在该 vendor 名单里吗（空名单 = 名单没拿到 —— 别拦）。
func protocolAllowed(protocols []string, protocol string) bool {
	if len(protocols) == 0 {
		return true
	}
	for _, candidate := range protocols {
		if strings.EqualFold(candidate, protocol) || (normalizeProtocol(candidate) == protocol) {
			return true
		}
	}
	return false
}

// commandProviderDel：删一条渠道（不可逆 ⇒ 走现有 confirm，先摊开再点头）。
func commandProviderDel(m model, args []string) (tea.Model, tea.Cmd) {
	if len(args) == 0 {
		return m.fail("用法：/provider-del <id>（先用 /providers 看编号）"), nil
	}
	m.confirm = &confirm{kind: confirmDeleteProvider, provider: args[0]}
	m.viewer, m.viewerTitle = []string{
		m.style.red(" 不可逆") + "：删掉渠道 " + args[0],
		"",
		" 回车 / y 执行 · 其他键取消",
	}, "确认删除"
	m.viewerHint = "回车 / y 执行 · 其他键取消"
	m.lastAction = "看清了再点头（回车 / y）"
	return m, nil
}

// ── /agents 面板：人格列表 → 回车详情三段；缺省三问；t 拨开关 ──
// 详情与 provider 向导同屏互斥：进一边清另一边（输入行同一根问卷线）。
const (
	agentViewerList   = "agents"
	agentViewerDetail = "agent-detail"
)

// agentCol：分栏焦点（左列表 / 右详情）。左/右键在它俩之间挪；回车只在左栏进详情（= 焦点挪右）。
const (
	agentColList = iota
	agentColDetail
)

// agentAbilityOrder：详情②段的固定行序（title/compact/judge —— 后端登记表同一顺序）。
var agentAbilityOrder = []string{"title", "compact", "judge"}

// agentAbilityLabel：能力中文名（与后端登记表同一份词）。
var agentAbilityLabel = map[string]string{"title": "起标题", "compact": "压缩", "judge": "判断"}

// agentsMsg：GET /agents 的结果（成功才开 viewer；失败如实说）。
type agentsMsg struct {
	config AgentsConfig
	err    error
}

// defaultsMsg：GET /config/defaults 的结果（只拼缺省行，不开 viewer）。
type defaultsMsg struct {
	defaults Defaults
	err      error
}

// createdAgentMsg：POST /agents 的结果（成功自动进详情）。
type createdAgentMsg struct {
	agent Agent
	err   error
}

// updatedAgentMsg：PATCH /agents/{id} 的结果（详情就地刷新；拨开关与 set-prompt 都走它）。
type updatedAgentMsg struct {
	agent Agent
	err   error
}

// deletedAgentMsg：DELETE /agents/{id} 的结果。
type deletedAgentMsg struct {
	id  string
	err error
}

// setDefaultsMsg：PUT /config/defaults 的结果（缺省三问与 use 快捷都走它）。
type setDefaultsMsg struct {
	defaults Defaults
	err      error
}

func loadAgentsCmd(client *Client) tea.Cmd {
	return func() tea.Msg {
		config, err := client.Agents()
		return agentsMsg{config: config, err: err}
	}
}
func loadDefaultsCmd(client *Client) tea.Cmd {
	return func() tea.Msg {
		defaults, err := client.Defaults()
		return defaultsMsg{defaults: defaults, err: err}
	}
}
func createAgentCmd(client *Client, name string) tea.Cmd {
	return func() tea.Msg {
		agent, err := client.CreateAgent(name, "")
		return createdAgentMsg{agent: agent, err: err}
	}
}
func updateAgentCmd(client *Client, id string, body map[string]any) tea.Cmd {
	return func() tea.Msg {
		agent, err := client.UpdateAgent(id, body)
		return updatedAgentMsg{agent: agent, err: err}
	}
}
func updateAgentPromptCmd(client *Client, id, prompt string) tea.Cmd {
	return updateAgentCmd(client, id, map[string]any{"system_prompt": prompt})
}
func toggleAgentAbilityCmd(client *Client, agent Agent, key string) tea.Cmd {
	return updateAgentCmd(client, agent.ID, map[string]any{"abilities": toggledAbilities(agent, key)})
}
func deleteAgentCmd(client *Client, id string) tea.Cmd {
	return func() tea.Msg {
		if err := client.DeleteAgent(id); err != nil {
			return deletedAgentMsg{id: id, err: err}
		}
		return deletedAgentMsg{id: id}
	}
}
func strptr(s string) *string { return new(s) }
func setDefaultsCmd(client *Client, provider, model, agent *string) tea.Cmd {
	return func() tea.Msg {
		defaults, err := client.SetDefaults(provider, model, agent)
		return setDefaultsMsg{defaults: defaults, err: err}
	}
}

// toggledAbilities：详情②段 `t` 拨动的整段 abilities（读出整段改一位整段 PATCH —— 后端语义是整段替换）。
func toggledAbilities(agent Agent, key string) map[string]any {
	out := map[string]any{}
	for _, k := range agentAbilityOrder {
		cur := false
		if ab, ok := agent.Abilities[k]; ok && ab.Enabled != nil {
			cur = *ab.Enabled
		}
		if k == key {
			cur = !cur
		}
		out[k] = map[string]any{"enabled": cur}
	}
	return out
}
func abilityOn(agent Agent, key string) bool {
	if ab, ok := agent.Abilities[key]; ok && ab.Enabled != nil {
		return *ab.Enabled
	}
	return false
}
func agentByID(config AgentsConfig, id string) *Agent {
	for i := range config.Agents {
		if config.Agents[i].ID == id {
			return &config.Agents[i]
		}
	}
	return nil
}
func shortPrompt(prompt string) string {
	first := strings.TrimSpace(strings.SplitN(prompt, "\n", 2)[0])
	runes := []rune(first)
	if len(runes) > 100 {
		return string(runes[:100])
	}
	return first
}

// agentListLines：/agents 列表 viewer 行（每行：★默认标记 · name · id 短串 · 开/关摘要；下面附缺省行）。
func (m model) agentListLines() []string {
	lines := []string{}
	for _, agent := range m.agents.Agents {
		mark := " "
		if agent.ID == m.agents.DefaultAgent {
			mark = "★"
		}
		on, off := 0, 0
		for _, k := range agentAbilityOrder {
			if abilityOn(agent, k) {
				on++
			} else {
				off++
			}
		}
		lines = append(lines, truncate(fmt.Sprintf(" %s%s · %s · 开%d/关%d", mark, agent.Name, shortID(agent.ID), on, off), max(1, m.width)))
	}
	def := m.defaults
	if m.defDraft.Provider != "" || m.defDraft.Model != "" || m.defDraft.Agent != "" {
		def = m.defDraft
	}
	if m.defaultsLoaded {
		lines = append(lines, truncate(fmt.Sprintf(" 缺省：%s/%s/%s", def.Provider, def.Model, def.Agent), max(1, m.width)))
	}
	return lines
}

// agentDetailLines：详情三段（①人格 name/system_prompt 前 100 字；②能力开关；③能力覆盖有值才列）。
func (m model) agentDetailLines(agent Agent) []string {
	lines := []string{truncate(" "+agent.Name, max(1, m.width))}
	if prompt := strings.TrimSpace(agent.SystemPrompt); prompt != "" {
		lines = append(lines, truncate(" "+shortPrompt(agent.SystemPrompt), max(1, m.width)))
	} else {
		lines = append(lines, " （还没有人格提示词）")
	}
	abKeys := []string{}
	for _, k := range agentAbilityOrder {
		state := "关"
		if abilityOn(agent, k) {
			state = "开"
		}
		lines = append(lines, truncate(fmt.Sprintf(" %s%s：%s", agentAbilityLabel[k], k, state), max(1, m.width)))
		abKeys = append(abKeys, k)
	}
	_ = abKeys
	for _, k := range agentAbilityOrder {
		ab := agent.Abilities[k]
		if ab.Provider != nil && *ab.Provider != "" {
			lines = append(lines, truncate(fmt.Sprintf(" %s渠道：%s", k, *ab.Provider), max(1, m.width)))
		}
		if ab.Model != nil && *ab.Model != "" {
			lines = append(lines, truncate(fmt.Sprintf(" %s模型：%s", k, *ab.Model), max(1, m.width)))
		}
	}
	return lines
}

// agentSplitLines：分栏拼行（左 agent 列表 | 右选中详情 + 底按钮行）。
// 两栏按 runewidth 对半切（左 12 格保底，人名再长也截）；焦点那栏行首 `>`，另一栏两格空。
// 详情字段行（可编辑那几行）行尾挂 `✎` —— e 改的就是它们。
func (m model) agentSplitLines() []string {
	width := max(1, m.width)
	leftW := max(12, width/2-1)
	rightW := max(1, width-leftW-3)
	left := m.agentSplitLeft(leftW)
	right := m.agentSplitRight(rightW)
	height := max(len(left), len(right))
	lines := make([]string, 0, height+2)
	for i := range height {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		// 先排版（pad 到固定格宽）、后上色：`│` 的列只认格数，不认内容长短。
		lines = append(lines, padCell(l, leftW)+" │ "+padCell(r, rightW))
	}
	lines = append(lines, truncate(" [n]新增 [d]删除 ", width))
	return lines
}

// padCell：截断 + 右侧补空格到 width 格（CJK 按两格算，见 runewidth）。
// 只吃未着色原文 —— 着色码零宽，pad 完再包色（"先排版、后上色"那条纪律）。
func padCell(text string, width int) string {
	text = truncate(text, width)
	if pad := width - runewidth.StringWidth(text); pad > 0 {
		text += strings.Repeat(" ", pad)
	}
	return text
}

// agentSplitLeft：左栏行（`>` = 焦点在左且选中；焦点在右时左栏无标记 —— 焦点在哪一眼见）。
func (m model) agentSplitLeft(leftW int) []string {
	lines := []string{}
	for i, agent := range m.agents.Agents {
		mark := " "
		if agent.ID == m.agents.DefaultAgent {
			mark = "★"
		}
		cursor := "  "
		if m.agentCol == agentColList && i == m.agentSel {
			cursor = "> "
		}
		lines = append(lines, truncate(fmt.Sprintf("%s%s%s · %s", cursor, mark, agent.Name, shortID(agent.ID)), leftW))
	}
	return lines
}

// agentSplitRight：右栏 = 选中详情的可编辑字段行（名/提示词/当前模型 + 开关行）。
// `>` = 焦点在右且选中这行；行尾 `✎` = e 改的就是这行。
func (m model) agentSplitRight(rightW int) []string {
	if m.agentDetail == nil {
		return []string{" （左栏选一份人格）"}
	}
	agent := *m.agentDetail
	rows := []agentFieldRow{
		{key: "name", label: "名", text: agent.Name},
		{key: "prompt", label: "提示词", text: shortPrompt(agent.SystemPrompt)},
		{key: "model", label: "当前模型", text: agentModelText(agent)},
	}
	for _, k := range agentAbilityOrder {
		state := "关"
		if abilityOn(agent, k) {
			state = "开"
		}
		rows = append(rows, agentFieldRow{key: "abil:" + k, label: agentAbilityLabel[k], text: state})
	}
	lines := []string{}
	for i, row := range rows {
		cursor := "  "
		if m.agentCol == agentColDetail && i == m.agentSel {
			cursor = "> "
		}
		lines = append(lines, truncate(fmt.Sprintf("%s%s：%s ✎", cursor, row.label, row.text), rightW))
	}
	return lines
}

// agentFieldRow：右栏一行可编辑字段（key = e 改时 Dess PATCH 哪一格）。
type agentFieldRow struct {
	key   string
	label string
	text  string
}

// agentDetailRows：右栏字段行（与 agentSplitRight 同一顺序 —— 键处理按下标找它）。
func (m model) agentDetailRows() []agentFieldRow {
	if m.agentDetail == nil {
		return nil
	}
	agent := *m.agentDetail
	rows := []agentFieldRow{
		{key: "name", label: "名", text: agent.Name},
		{key: "prompt", label: "提示词", text: shortPrompt(agent.SystemPrompt)},
		{key: "model", label: "当前模型", text: agentModelText(agent)},
	}
	for _, k := range agentAbilityOrder {
		state := "关"
		if abilityOn(agent, k) {
			state = "开"
		}
		rows = append(rows, agentFieldRow{key: "abil:" + k, label: agentAbilityLabel[k], text: state})
	}
	return rows
}

// agentModelText：右栏"当前模型"那行 —— compact 覆盖的模型 ?: 会话缺省（只读看，不写）。
func agentModelText(agent Agent) string {
	if ab, ok := agent.Abilities["compact"]; ok && ab.Model != nil && *ab.Model != "" {
		return *ab.Model
	}
	return "（跟缺省走）"
}

// openAgentList：分栏铺开（左列表 + 右详情常驻；焦点在左）。
// agentRows 存列表行号→agent id；右详情跟左光标走（syncAgentDetail），列表空 ⇒ 详情空。
func (m model) openAgentList() model {
	m.agentDetail = nil
	m.agentRows = make([]string, 0, len(m.agents.Agents))
	for _, agent := range m.agents.Agents {
		m.agentRows = append(m.agentRows, agent.ID)
	}
	if m.agentSel > len(m.agentRows)-1 {
		m.agentSel = max(len(m.agentRows)-1, 0)
	}
	m.agentCol, m.agentMode = agentColList, true
	m.syncAgentDetail()
	m.viewer, m.viewerTitle = m.agentSplitLines(), "人格"
	m.viewerHint = "←→ 焦点 · ↑↓ 选行 · 回车进详情 · n 新增 · d 删除 · Esc 关掉"
	m.viewerMode = agentViewerList
	return m
}

// syncAgentDetail：右详情跟左光标走（分栏常驻 ⇒ 详情永远是选中那条，不重拉）。
func (m *model) syncAgentDetail() {
	m.agentDetail = nil
	m.detailAbils = append([]string{}, agentAbilityOrder...)
	if m.agentSel < 0 || m.agentSel >= len(m.agentRows) {
		return
	}
	if agent := agentByID(m.agents, m.agentRows[m.agentSel]); agent != nil {
		cp := *agent
		m.agentDetail = &cp
	}
}

// openAgentDetail：回车进详情 = 焦点挪右（分栏不动，只是焦点过去；详情内容 sync 时已就位）。
func (m model) openAgentDetail(agent Agent) model {
	cp := agent
	m.agentDetail = &cp
	m.detailAbils = append([]string{}, agentAbilityOrder...)
	m.agentCol = agentColDetail
	m.viewer, m.viewerTitle = m.agentSplitLines(), "人格"
	m.viewerHint = "←→ 焦点 · ↑↓ 选字段 · t/回车拨开关 · e 改 · d 删 · Esc 关掉"
	m.viewerMode = agentViewerDetail
	m.agentSel = 0
	return m
}

// commandAgents：`/agents` 列表 viewer（含缺省行）+ 子命令（defaults/use/set-prompt）。
func commandAgents(m model, args []string) (tea.Model, tea.Cmd) {
	if len(args) > 0 {
		switch args[0] {
		case "defaults":
			m.addStep, m.addDraft, m.addPresets = 0, providerDraft{}, nil
			m.agentDetail, m.viewer, m.viewerTitle, m.viewerHint, m.viewerMode = nil, nil, "", "", ""
			m.agentRows, m.detailAbils, m.agentSel = nil, nil, 0
			m.defStep, m.defDraft = 1, m.defaults
			m.lastAction = "缺省 provider？（空=不动；dummy-式填法：opencode-go/deepseek-v4-flash/default；Esc 取消）"
			return m, nil
		case "use":
			if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
				return m.fail("用法：/agents use <agentID>（先用 /agents 看编号）"), nil
			}
			m.lastAction = "设缺省人格…"
			return m, setDefaultsCmd(m.client, nil, nil, strptr(args[1]))
		case "set-prompt":
			if m.agentDetail == nil {
				return m.fail("先用 /agents 回车进一份人格详情，再 /agents set-prompt <文本>"), nil
			}
			text := strings.TrimSpace(strings.Join(args[1:], " "))
			if text == "" {
				return m.fail("用法：/agents set-prompt <文本>（空文本不行）"), nil
			}
			m.lastAction = "写人格提示词…"
			return m, updateAgentPromptCmd(m.client, m.agentDetail.ID, text)
		}
	}
	m.addStep, m.addDraft, m.addPresets = 0, providerDraft{}, nil
	m.agentDetail, m.agentRows, m.detailAbils, m.agentSel = nil, nil, nil, 0
	m.defStep, m.defDraft = 0, Defaults{}
	m.viewerMode = ""
	m.lastAction = "拉人格…"
	return m, tea.Batch(loadAgentsCmd(m.client), loadDefaultsCmd(m.client))
}

// commandAgentCreate：`/agent-create <名字>`（名唯一必填；建完自动进详情）。
func commandAgentCreate(m model, args []string) (tea.Model, tea.Cmd) {
	name := strings.TrimSpace(strings.Join(args, " "))
	if name == "" {
		return m.fail("用法：/agent-create <名字>（名字必填；建完自动进详情）"), nil
	}
	m.addStep, m.addDraft, m.addPresets = 0, providerDraft{}, nil
	m.agentRows, m.detailAbils, m.agentSel = nil, nil, 0
	m.defStep, m.defDraft = 0, Defaults{}
	m.lastAction = "建人格 " + name + "…"
	return m, createAgentCmd(m.client, name)
}

// commandAgentDel：`/agent-del <id>`（走现有 confirm，加 confirmDeleteAgent 种，删完如实报）。
func commandAgentDel(m model, args []string) (tea.Model, tea.Cmd) {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return m.fail("用法：/agent-del <id>（先用 /agents 看编号）"), nil
	}
	return m.confirmDeleteAgentByID(strings.TrimSpace(args[0]))
}

// confirmDeleteAgentByID：删人格确认框（d 键与 /agent-del 共用 —— id 来自光标或参数）。
func (m model) confirmDeleteAgentByID(id string) (model, tea.Cmd) {
	m.confirm = &confirm{kind: confirmDeleteAgent, agentID: id}
	m.viewer, m.viewerTitle = []string{
		m.style.red(" 不可逆") + "：删掉人格 " + id,
		"",
		" 回车 / y 执行 · 其他键取消",
	}, "确认删除"
	m.viewerHint = "回车 / y 执行 · 其他键取消"
	m.viewerMode = ""
	m.lastAction = "看清了再点头（回车 / y）"
	return m, nil
}

// answerAgentEdit：编辑态回车 —— 名/提示词/模型三格 + 新增名字，各走各的 PATCH。
// 空回答 = 取消（别把字段清空 —— 清空另有 Coronary 语义，这里不给）；
// 编辑态收工都清 editField（成了不用留，败了重进 e —— 留着只会让人误会还能续上）。
func (m model) answerAgentEdit(text string) (tea.Model, tea.Cmd) {
	field := m.editField
	m.editField = ""
	answer := strings.TrimSpace(text)
	if field == "new-name" {
		if answer == "" {
			m.lastAction = "名字不能为空（已取消，什么都没建）"
			return m, nil
		}
		m.lastAction = "建人格 " + answer + "…"
		return m, createAgentCmd(m.client, answer)
	}
	if m.agentDetail == nil {
		m.lastAction = "人格详情丢了（已取消，什么都没改）"
		return m, nil
	}
	if answer == "" {
		m.lastAction = "空回答 = 取消（什么都没改）"
		return m, nil
	}
	id := m.agentDetail.ID
	switch field {
	case "name":
		m.lastAction = "改名…"
		return m, updateAgentCmd(m.client, id, map[string]any{"name": answer})
	case "prompt":
		m.lastAction = "写人格提示词…"
		return m, updateAgentPromptCmd(m.client, id, text)
	case "model":
		// "当前模型"改的是 compact 覆盖的 model（整段 abilities 改一位 —— 后端语义整段替换）。
		m.lastAction = "改 compact 模型…"
		return m, updateAgentCmd(m.client, id, map[string]any{"abilities": abilityModelOverride(*m.agentDetail, answer)})
	default:
		m.lastAction = "不认识的编辑态（已取消，什么都没改）"
		return m, nil
	}
}

// abilityModelOverride：compact 覆盖的 model 改一位（其余能力原样带回 —— 整段替换语义）。
func abilityModelOverride(agent Agent, model string) map[string]any {
	out := map[string]any{}
	for _, k := range agentAbilityOrder {
		entry := map[string]any{}
		if ab, ok := agent.Abilities[k]; ok {
			if ab.Enabled != nil {
				entry["enabled"] = *ab.Enabled
			}
			if ab.Provider != nil && *ab.Provider != "" {
				entry["provider"] = *ab.Provider
			}
			if ab.Model != nil && *ab.Model != "" {
				entry["model"] = *ab.Model
			}
			if ab.Prompt != nil && *ab.Prompt != "" {
				entry["prompt"] = *ab.Prompt
			}
		}
		out[k] = entry
	}
	entry := out["compact"].(map[string]any)
	entry["model"] = model
	out["compact"] = entry
	return out
}

func (m model) answerDefDraft(text string) (tea.Model, tea.Cmd) {
	answer := strings.TrimSpace(text)
	switch m.defStep {
	case 1:
		if answer != "" {
			m.defDraft.Provider = answer
		}
		m.defStep = 2
		m.lastAction = "缺省 model？（空=不动；dummy-式填法：dummy；Esc 取消）"
		return m, nil
	case 2:
		if answer != "" {
			m.defDraft.Model = answer
		}
		m.defStep = 3
		m.lastAction = "缺省 agentID？（空=不动；dummy-式填法：default；Esc 取消）"
		return m, nil
	case 3:
		if answer != "" {
			m.defDraft.Agent = answer
		}
		draft := m.defDraft
		m.defStep, m.defDraft = 0, Defaults{}
		m.lastAction = "设缺省…"
		return m, setDefaultsCmd(m.client, strptr(draft.Provider), strptr(draft.Model), strptr(draft.Agent))
	}
	return m, nil
}

// agentViewerKey：分栏开着时的键（左/右挪焦点；回车只在左栏进详情；右栏 t/回车拨开关、e 改、d 删；n 新增）。
// Esc 关整屏；其余键返回 handled=false 走"任意键关掉"。
func (m model) agentViewerKey(key string) (bool, model, tea.Cmd) {
	if m.viewerMode != agentViewerList && m.viewerMode != agentViewerDetail {
		return false, m, nil
	}
	refresh := func() model {
		m.viewer, m.viewerTitle = m.agentSplitLines(), "人格"
		return m
	}
	switch key {
	case "esc":
		m.viewer, m.viewerTitle, m.viewerHint = nil, "", ""
		m.viewerMode, m.agentRows, m.detailAbils, m.agentSel = "", nil, nil, 0
		m.agentDetail, m.agentCol, m.agentMode, m.editField = nil, agentColList, false, ""
		return true, m, nil
	case "left":
		// 焦点挪左：右栏回来时 agentSel 指回列表行（右栏字段下标在左栏越界 ⇒ 夹住）。
		m.agentCol = agentColList
		m.viewerMode = agentViewerList
		if m.agentSel > len(m.agentRows)-1 {
			m.agentSel = max(len(m.agentRows)-1, 0)
		}
		m.syncAgentDetail()
		m.viewerHint = "←→ 焦点 · ↑↓ 选行 · 回车进详情 · n 新增 · d 删除 · Esc 关掉"
		return true, refresh(), nil
	case "right":
		// 焦点挪右：agentSel 指到字段行（左栏行号在右栏越界 ⇒ 从 0 起）。
		m.agentCol = agentColDetail
		m.viewerMode = agentViewerDetail
		if m.agentDetail == nil {
			return true, m, nil
		}
		if m.agentSel >= len(m.agentDetailRows()) {
			m.agentSel = 0
		}
		m.viewerHint = "←→ 焦点 · ↑↓ 选字段 · t/回车拨开关 · e 改 · d 删 · Esc 关掉"
		return true, refresh(), nil
	case "up":
		if m.agentCol == agentColDetail {
			if m.agentSel > 0 {
				m.agentSel--
			}
		} else if m.agentSel > 0 {
			m.agentSel--
			m.syncAgentDetail()
		}
		return true, refresh(), nil
	case "down":
		if m.agentCol == agentColDetail {
			if m.agentSel < len(m.agentDetailRows())-1 {
				m.agentSel++
			}
		} else if m.agentSel < len(m.agentRows)-1 {
			m.agentSel++
			m.syncAgentDetail()
		}
		return true, refresh(), nil
	case "enter":
		if m.agentCol == agentColList {
			// 左栏回车 = 进详情（焦点挪右，内容 sync 时已就位，不重拉）。
			if m.agentSel < 0 || m.agentSel >= len(m.agentRows) {
				return true, m, nil
			}
			if agent := agentByID(m.agents, m.agentRows[m.agentSel]); agent != nil {
				m = m.openAgentDetail(*agent)
				return true, refresh(), nil
			}
			return true, m, nil
		}
		// 右栏回车 = 开关行拨一下（字段行什么都不做 —— 改走 e）。
		next, cmd := m.toggleAtCursor()
		return true, next, cmd
	case "t", "T":
		next, cmd := m.toggleAtCursor()
		return true, next, cmd
	case "e", "E":
		// 进编辑态：输入行改选中字段（名/提示词/模型三格；开关行 e = 同 t 拨一下）。
		if m.agentCol != agentColDetail || m.agentDetail == nil {
			return true, m, nil
		}
		rows := m.agentDetailRows()
		if m.agentSel < 0 || m.agentSel >= len(rows) {
			return true, m, nil
		}
		field := rows[m.agentSel].key
		if strings.HasPrefix(field, "abil:") {
			next, cmd := m.toggleAtCursor()
			return true, next, cmd
		}
		m.editField = field
		m.input, m.inputCursor, m.paletteIndex = "", 0, 0
		m.lastAction = "改" + rows[m.agentSel].label + "（回车 PATCH，Esc 取消）"
		return true, m, nil
	case "d", "D":
		// 删：左栏删选中那条（进确认框），右栏删当前这条（同一确认框）。
		id := ""
		if m.agentCol == agentColDetail && m.agentDetail != nil {
			id = m.agentDetail.ID
		} else if m.agentSel >= 0 && m.agentSel < len(m.agentRows) {
			id = m.agentRows[m.agentSel]
		}
		if id == "" {
			return true, m, nil
		}
		next, cmd := m.confirmDeleteAgentByID(id)
		return true, next, cmd
	case "n", "N":
		// 新增：输入行问名字（与 /agent-create 同一条路，回车 Create）。
		m.editField = "new-name"
		m.input, m.inputCursor, m.paletteIndex = "", 0, 0
		m.lastAction = "新人格名字？（回车创建，Esc 取消）"
		return true, m, nil
	}
	return false, m, nil
}

// toggleAtCursor：右栏 t/回车 —— 开关行拨一下（字段行不动手，改走 e）。
func (m model) toggleAtCursor() (model, tea.Cmd) {
	if m.agentDetail == nil || len(m.detailAbils) == 0 {
		return m, nil
	}
	rows := m.agentDetailRows()
	if m.agentSel < 0 || m.agentSel >= len(rows) {
		return m, nil
	}
	field := rows[m.agentSel].key
	if !strings.HasPrefix(field, "abil:") {
		return m, nil
	}
	key := strings.TrimPrefix(field, "abil:")
	m.lastAction = "拨开关 " + key + "…"
	return m, toggleAgentAbilityCmd(m.client, *m.agentDetail, key)
}

// bindTelegramMsg：一次绑定的结果（走后端 HTTP：改的是服务器配置，远端 TUI 照用）。
type bindTelegramMsg struct {
	id  int64
	err error
}

// bindTelegramCmd：调后端的绑定（写 `allowed_id`，顶掉旧的）。
func bindTelegramCmd(client *Client, id int64) tea.Cmd {
	return func() tea.Msg {
		binding, err := client.BindTelegram(id)
		return bindTelegramMsg{id: binding.AllowedID, err: err}
	}
}

// setTelegramMsg：改 TG 配置的结果（三格回显：token 只回有没有）。
type setTelegramMsg struct {
	binding TelegramBinding
	err     error
}

// setTelegramCmd：调后端的三格改配置。
func setTelegramCmd(client *Client, body map[string]any) tea.Cmd {
	return func() tea.Msg {
		binding, err := client.SetTelegram(body)
		return setTelegramMsg{binding: binding, err: err}
	}
}

// toggleTelegramCmd：先读开关再翻转（读不到就不动 —— 别把"没读到"当"关着"给开了）。
func toggleTelegramCmd(client *Client) tea.Cmd {
	return func() tea.Msg {
		current, err := client.TelegramStatus()
		if err != nil {
			return setTelegramMsg{err: err}
		}
		binding, err := client.SetTelegram(map[string]any{"enabled": !current.Enabled})
		return setTelegramMsg{binding: binding, err: err}
	}
}

// statusTelegramCmd：读 TG 状态。
func statusTelegramCmd(client *Client) tea.Cmd {
	return func() tea.Msg {
		binding, err := client.TelegramStatus()
		return setTelegramMsg{binding: binding, err: err}
	}
}
func commandTelegramBind(m model, args []string) (tea.Model, tea.Cmd) {
	if len(args) == 0 {
		return m.fail("用法：/telegram-bind <tg_id>（bot 告诉你的那串数字）"), nil
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || id <= 0 {
		return m.fail("tg_id 是那串纯数字 id（不是 @用户名）：" + args[0]), nil
	}
	m.lastAction = "绑定 Telegram 单账户…"
	return m, bindTelegramCmd(m.client, id)
}

// commandTelegramTokenSet：`/telegram-bot-token-set <token>` —— 写文件里的 token（改完重启生效）。
func commandTelegramTokenSet(m model, args []string) (tea.Model, tea.Cmd) {
	if len(args) == 0 {
		return m.fail("用法：/telegram-bot-token-set <token>（BotFather 给的那串）"), nil
	}
	m.lastAction = "设置 TG bot token…"
	return m, setTelegramCmd(m.client, map[string]any{"bot_token": args[0]})
}

// commandTelegramToggle：`/telegram-toggle` —— 开/关 bot（显式动作；改完重启生效）。
func commandTelegramToggle(m model, _ []string) (tea.Model, tea.Cmd) {
	m.lastAction = "切 TG bot 开关…"
	return m, toggleTelegramCmd(m.client)
}

// commandTelegramStatus：`/telegram-status` —— 看 bot 状态（跑没跑、绑了谁、在线多久）。
func commandTelegramStatus(m model, _ []string) (tea.Model, tea.Cmd) {
	m.lastAction = "看 TG bot 状态…"
	return m, statusTelegramCmd(m.client)
}

func commandRefresh(m model, _ []string) (tea.Model, tea.Cmd) {
	m.lastAction = "刷新中…"
	return m, loadSessions(m.client)
}

// commandCompact：把最老的 N 个已闭合**块**压成摘要（**按块，不按条** —— 块在 assistant→user 交界处切，
// 一块 ≥2 条；`/compact 1` 不是压 1 条，是压 1 块。底层凑不够就抬头并摘要，见 compact 策略）。
//
// 走路由 ⇒ **202 受理**（压缩真调一次上游 + 落库，不在这一趟里等）；底栏报受理结果。
// 不给 N ⇒ 用后端的默认值（`config.json` 的 `compact_blocks`）。
// 正在生成时先别压：这一轮的上文已经定稿了，压了也影响不到它（白花一次上游调用）。
func commandCompact(m model, args []string) (tea.Model, tea.Cmd) {
	session := m.currentSession()
	if session == nil {
		return m.fail("没有可压的会话：" + noSession), nil
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

// commandReroll：**重摇尾条那条 assistant 回复**。
//
//	`/reroll`              进模式并**立刻摇一次**（已经在模式里 ⇒ 再摇一版 —— 分母是这么长出来的）
//	`/reroll switch <n>`   选中第 n 版（后端就地重建那条消息：UUID 不变，正文与附带信息一起换）
//	`/reroll delete <n>`   删掉第 n 版（后方位次统一 -1；只剩一版时后端退出模式）
//	`/reroll off`          **显式退出**（清列表；库里那条消息保留当前这版）
//	`/reroll list`         看一眼每一版的预览（铺满消息区的查看器）
//
// 候选**只在内存里**（后端也是）：一发出新消息、删那条消息、切会话、进程重启 ⇒ 自然消失。
// 摇出来的那一版**不进历史**，选中才 apply 回库里那条 Message。
func commandReroll(m model, args []string) (tea.Model, tea.Cmd) {
	return rerollCommand(m, args, RerollKindMessage)
}

// commandRerollSummary：**重摇一条摘要** —— 与 `/reroll` 一一镜像，目标换成
// `idx`（1-based 消息序号）上溯到的那条根摘要（同树任意一条都指向同一个目标）。
//
//	`/reroll-summary <idx>`        进模式并立刻摇一版（已经在模式里 ⇒ 再摇一版）
//	`/reroll-summary switch <n>`   选中第 n 版（后端就地换那条摘要的正文；id / 区间都不变）
//	`/reroll-summary delete <n>`   删掉第 n 版
//	`/reroll-summary off`          显式退出
//	`/reroll-summary list`         看一眼每一版的预览（铺满消息区的查看器）
func commandRerollSummary(m model, args []string) (tea.Model, tea.Cmd) {
	return rerollCommand(m, args, RerollKindSummary)
}

// rerollCommand：两个家族共用那一套用法（`/reroll` 与 `/reroll-summary` 镜像）。
//
// `kind` 决定打哪个家族的接口；两家只有"进模式"的入参不同：消息家族摇尾条、不带参数；
// 摘要家族要带 `idx`（挖哪一段）。其余（switch / delete / off / list）逐字一套。
func rerollCommand(m model, args []string, kind string) (tea.Model, tea.Cmd) {
	session := m.currentSession()
	example, subject, title := "/reroll", "重摇", "重摇 · 第几版"
	if kind == RerollKindSummary {
		example, subject, title = "/reroll-summary", "摘要重摇", "摘要重摇 · 第几版"
	}
	if session == nil {
		return m.fail("没有可" + subject + "的会话：" + noSession), nil
	}
	// `list`：把每一版的预览摊开（只读查看器，任意键关掉）
	if len(args) > 0 && args[0] == "list" {
		state := m.rerollState()
		if state == nil || state.TargetKind != kind {
			if kind == RerollKindSummary {
				m.lastAction = "没在摘要重摇模式里（`/reroll-summary <idx>` 先摇一版）"
			} else {
				m.lastAction = "没在重摇模式里（`/reroll` 先摇一版）"
			}
			return m, nil
		}
		m.viewer, m.viewerTitle = m.rerollLines(*state), title
		m.viewerHint = "任意键关掉"
		return m, nil
	}
	if len(args) > 0 && args[0] == "off" {
		m.lastAction = "退出" + subject + "模式…"
		return m, rerollClearCmd(m.client, session.ID, kind)
	}
	if len(args) > 0 && (args[0] == "switch" || args[0] == "delete") {
		if len(args) < 2 {
			return m.fail(fmt.Sprintf("用法：%s %s <n>（n 是位次，从 1 起；`%s list` 看有哪几版）",
				example, args[0], example)), nil
		}
		idx, err := strconv.Atoi(args[1])
		if err != nil || idx <= 0 {
			return m.fail("位次要正整数：" + args[1]), nil
		}
		if args[0] == "switch" {
			m.lastAction = fmt.Sprintf("切到第 %d 版…", idx)
			return m, rerollSwitchCmd(m.client, session.ID, kind, idx)
		}
		m.lastAction = fmt.Sprintf("删掉第 %d 版…", idx)
		return m, rerollDeleteCmd(m.client, session.ID, kind, idx)
	}
	if len(args) > 0 && kind == RerollKindMessage {
		return m.fail("不认识的用法：" + strings.Join(args, " ") +
			"（可用：/reroll · /reroll switch <n> · /reroll delete <n> · /reroll off · /reroll list）"), nil
	}
	// 进模式并摇一次（已经在模式里 ⇒ 再摇一版）。
	idx := 0
	if kind == RerollKindSummary {
		if len(args) == 0 {
			return m.fail("用法：/reroll-summary <idx>（idx 是 1-based 消息序号，上溯到它的根摘要；" +
				"`/reroll-summary list` 看有哪几版）"), nil
		}
		parsed, err := strconv.Atoi(args[0])
		if err != nil || parsed <= 0 {
			return m.fail("消息序号要正整数（1-based）：" + args[0]), nil
		}
		idx = parsed
	}
	if m.turn.Busy() && m.turn.sessionID == session.ID {
		m.lastAction = "这个会话还在生成中：等这一轮跑完再摇（重摇与生成共用同一把闸）"
		return m, nil
	}
	m.lastAction = subject + "中…"
	return m, rerollEnterCmd(m.client, session.ID, kind, idx)
}

// rerollLines：`/reroll list` 那个查看器的每一行（**当前那版高亮**，在摇的那版标出来）。
// 摘要模式先摊一行目标区间（消息模式不画位次标记，信息都放在这儿/底栏）。
func (m model) rerollLines(state RerollState) []string {
	lines := make([]string, 0, len(state.Items)*2+2)
	if state.TargetKind == RerollKindSummary {
		lines = append(lines,
			m.style.dim(truncate("目标："+rerollTargetLabel(state)+"那条摘要", max(1, m.width))), "")
	}
	for _, item := range state.Items {
		marker := "  "
		if item.Current {
			marker = "> "
		}
		label := fmt.Sprintf("第 %d 版", item.Idx)
		detail := ""
		switch {
		case item.Pending:
			detail = "（还在摇…）"
		case item.Current:
			detail = "（当前）"
		}
		line, _ := m.style.concat([]segment{
			{marker, stylePlain}, {label, styleDim}, {detail, styleDim},
		}, max(1, m.width), " ")
		preview := item.Preview
		if preview == "" {
			preview = "（还没有正文）"
		}
		lines = append(lines, line, truncate("     "+preview, max(1, m.width)))
	}
	return lines
}

// commandStop：打断正在生成的那一轮（**幂等** —— 没在跑也 200，界面照实说"什么都没发生"）。
//
// 问的是**后端**（权威在那儿）：界面这一份 `m.turn` 只是"我知道的那一轮"，
// 别的客户端起的生成、或界面重启前起的那一轮，它并不知情。
// 重摇也在这一把闸上 ⇒ 按停也能把正在摇的那一趟停下来（`/stop` 就是那个意思）。
func commandStop(m model, _ []string) (tea.Model, tea.Cmd) {
	target := ""
	if m.turn != nil {
		target = m.turn.sessionID
	} else if session := m.currentSession(); session != nil {
		target = session.ID
	}
	if target == "" {
		return m.fail("没有可停的：" + noSession), nil
	}
	m.lastAction = "停止…"
	return m, stopTurnCmd(m.client, target)
}

// settleTurn：这一轮在**本地**收干 —— 气泡消失，界面这一份状态也跟着落回 idle。
//
// 它自己**不动会话列表**：那会让底栏刚刚写上的「已停止 / 已完成 / 生成失败」立刻被
// "会话 N 条"顶掉（两个 Cmd 谁先回来还不一定）。这里改的是**已经确定的事实**（这一轮结束了），
// 下一次刷新（`/refresh` 或别的动作）自然与后端对账。
// 唯一"非刷不可"的是跑完收到 idle 那一刻（自动起的标题刚落库）—— 那一处另走**静默**那一趟
// （`loadSessionsQuiet`：更新列表但不碰底栏）。
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
