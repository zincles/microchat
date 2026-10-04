package tg

// 命令表：**唯一一份** —— bot 菜单、`/help` 文本、派发都读它（各写一份必然漂移）。
//
// bot 的命令面照 CLI（TUI）那份搬，但**只搬 bot 能干的**：配置管理（`/provider-add`、
// `/provider-del`、`/telegram-*`）留在终端 CLI；`/reroll*` 是状态机交互（下一步搬）；
// `/quit` 对 bot 没意义。**不占位假装有** —— 那几条在 `/help` 尾注里如实点名。
//
// 每条命令 run 跑之前，`onMessage` 已经先过了一遍 `gateMessage`（未绑定者只收绑定提示）。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"microchat/internal/apiclient"
)

// botCommand：一条命令 —— name 不含斜杠（Telegram 菜单名只许 [a-z0-9_]）；顺序 = 展示顺序。
type botCommand struct {
	name string
	help string
	run  func(ctx context.Context, r *Runner, b *bot.Bot, update *models.Update, args []string)
}

// botCommands：注册表本体 —— 在 init 里填（`help` 那条要**回头读这张表**，
// 写成包级 var 的初始化表达式就是初始化环，Go 直接拒绝编译）。
var botCommands []botCommand

// MenuCommands：BotFather 菜单里摆的那些 —— **从 botCommands 派生**（不许两处手写）。
var MenuCommands []models.BotCommand

func init() {
	botCommands = []botCommand{
		{name: "start", help: "开始（报绑定状态）", run: func(ctx context.Context, r *Runner, b *bot.Bot, update *models.Update, _ []string) {
			r.say(ctx, b, update, "microchat 已连接（单账户已绑定）。直接发话就是一轮，/status 看状态，/help 看命令。")
		}},
		{name: "help", help: "列全部命令", run: func(ctx context.Context, r *Runner, b *bot.Bot, update *models.Update, _ []string) {
			r.say(ctx, b, update, helpText())
		}},
		{name: "status", help: "看状态（会话 / 上下文 / 轮次 / TG / 后端）", run: func(ctx context.Context, r *Runner, b *bot.Bot, update *models.Update, _ []string) {
			r.say(ctx, b, update, r.cmdStatus(ctx))
		}},
		{name: "new", help: "新建会话并切过去", run: func(ctx context.Context, r *Runner, b *bot.Bot, update *models.Update, _ []string) {
			r.say(ctx, b, update, r.cmdNew(ctx))
		}},
		{name: "resume", help: "列出最近会话（/resume N 切换）", run: func(ctx context.Context, r *Runner, b *bot.Bot, update *models.Update, args []string) {
			r.say(ctx, b, update, r.cmdResume(ctx, strings.Join(args, " ")))
		}},
		{name: "rename", help: "改当前会话名（/rename 新名字，可含空格）", run: func(ctx context.Context, r *Runner, b *bot.Bot, update *models.Update, args []string) {
			r.say(ctx, b, update, r.cmdRename(ctx, strings.Join(args, " ")))
		}},
		{name: "copy", help: "复制当前会话并切过去", run: func(ctx context.Context, r *Runner, b *bot.Bot, update *models.Update, _ []string) {
			r.say(ctx, b, update, r.cmdCopy(ctx))
		}},
		{name: "delete", help: "删当前会话（按钮确认；删完切最近一条或新建）", run: func(ctx context.Context, r *Runner, b *bot.Bot, update *models.Update, _ []string) {
			r.cmdDelete(ctx, b, update)
		}},
		{name: "cut", help: "删一条消息及之后（/cut N，先预览再确认）", run: func(ctx context.Context, r *Runner, b *bot.Bot, update *models.Update, args []string) {
			r.cmdCut(ctx, b, update, strings.Join(args, " "))
		}},
		{name: "stop", help: "停这一轮（幂等）", run: func(ctx context.Context, r *Runner, b *bot.Bot, update *models.Update, _ []string) {
			r.say(ctx, b, update, r.cmdStop(ctx))
		}},
		{name: "compact", help: "触发压缩（/compact [N]，不给用默认值）", run: func(ctx context.Context, r *Runner, b *bot.Bot, update *models.Update, args []string) {
			r.say(ctx, b, update, r.cmdCompact(ctx, strings.Join(args, " ")))
		}},
		{name: "state", help: "看当前会话的世界状态", run: func(ctx context.Context, r *Runner, b *bot.Bot, update *models.Update, _ []string) {
			r.say(ctx, b, update, r.cmdState(ctx))
		}},
		{name: "outgoing", help: "看真请求（method/url/headers/体）", run: func(ctx context.Context, r *Runner, b *bot.Bot, update *models.Update, _ []string) {
			r.say(ctx, b, update, r.cmdOutgoing(ctx))
		}},
		{name: "usage", help: "看上下文占用与渠道套餐余量", run: func(ctx context.Context, r *Runner, b *bot.Bot, update *models.Update, _ []string) {
			r.say(ctx, b, update, r.cmdUsage(ctx))
		}},
		{name: "think", help: "看当前会话的思考全文", run: func(ctx context.Context, r *Runner, b *bot.Bot, update *models.Update, _ []string) {
			r.say(ctx, b, update, r.cmdThink(ctx))
		}},
		{name: "system", help: "看生效的系统提示词", run: func(ctx context.Context, r *Runner, b *bot.Bot, update *models.Update, _ []string) {
			r.say(ctx, b, update, r.cmdSystem(ctx))
		}},
		{name: "providers", help: "列全部渠道（每条一行）", run: func(ctx context.Context, r *Runner, b *bot.Bot, update *models.Update, _ []string) {
			r.say(ctx, b, update, r.cmdProviders(ctx))
		}},
		{name: "model", help: "挑渠道 / 模型（按钮翻页选）", run: func(ctx context.Context, r *Runner, b *bot.Bot, update *models.Update, _ []string) {
			r.cmdModel(ctx, b, update)
		}},
	}
	MenuCommands = menuFromCommands(botCommands)
}

// menuFromCommands：注册表 → Telegram 菜单（描述一行化、截到 TG 的上限内）。
func menuFromCommands(commands []botCommand) []models.BotCommand {
	menu := make([]models.BotCommand, 0, len(commands))
	for _, command := range commands {
		description := strings.Join(strings.Fields(command.help), " ") // 不许换行
		menu = append(menu, models.BotCommand{Command: command.name, Description: clip(description, 250)})
	}
	return menu
}

// helpText：`/help` 的正文 —— 逐条名字 + 一句话，尾注如实说哪些**没搬**（不占位假装有）。
func helpText() string {
	lines := make([]string, 0, len(botCommands)+2)
	lines = append(lines, "命令：")
	for _, command := range botCommands {
		lines = append(lines, "/"+command.name+" — "+command.help)
	}
	lines = append(lines, "")
	lines = append(lines, "不做（留在终端 CLI）：/provider-add /provider-del /telegram-* /reroll* /quit"+
		" —— bot 只管聊天与只读面板。")
	lines = append(lines, "直接发一句话就是一轮对话。")
	return strings.Join(lines, "\n")
}

// parseCommand：拆 `/resume 2` / `/rename 我的 新名字` / `/status@mybot` —— 返回名字（不含斜杠）与参数。
func parseCommand(text string) (string, []string) {
	fields := strings.Fields(text)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return "", nil
	}
	name := fields[0][1:]
	if at := strings.IndexByte(name, '@'); at >= 0 { // `/status@mybot` 那种带后缀的
		name = name[:at]
	}
	return name, fields[1:]
}

// findCommand：按名字查注册表（没登记过 ⇒ nil，交给"不认识"那条路）。
func findCommand(name string) *botCommand {
	for index := range botCommands {
		if botCommands[index].name == name {
			return &botCommands[index]
		}
	}
	return nil
}

// ── 取数 / 说话的小工具 ────────────────────────────────────────────────

// APITimeout：bot 一次取数的上限 —— 后端在本机回环上，真慢就是出事了。
// `apiclient.Client` 的方法不带 ctx（它是共享的薄封装），所以超时靠它那份 `http.Client.Timeout`
// 钉 —— main 建 TG 专用客户端时用这个常量赋值（见 main.go 的接线段）。
const APITimeout = 5 * time.Second

// say：把一段文本发回去（超长自动分段）。
func (r *Runner) say(ctx context.Context, b *bot.Bot, update *models.Update, text string) {
	if update.Message == nil {
		return
	}
	_ = sendLong(ctx, b, update.Message.Chat.ID, text)
}

// sayWithButtons：文本 + 一行按钮（确认 / 选择那种）。
func (r *Runner) sayWithButtons(ctx context.Context, b *bot.Bot, update *models.Update, text string,
	buttons []models.InlineKeyboardButton) {
	if update.Message == nil {
		return
	}
	if len(buttons) == 0 {
		_ = sendLong(ctx, b, update.Message.Chat.ID, text)
		return
	}
	if _, err := sendMarkdownWithMarkup(ctx, b, update.Message.Chat.ID, clip(text, tgMaxLen),
		&models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{buttons}}); err != nil {
		_ = sendLong(ctx, b, update.Message.Chat.ID, text)
	}
}

// backend：后端调用前的守卫（壳建起来时就有 client —— 走到这儿是装配 bug，照实说）。
func (r *Runner) backend() error {
	if r.api == nil {
		return fmt.Errorf("后端客户端没接上（装配 bug）")
	}
	return nil
}

// ── 当前会话（进程内事实：重启就回到"最近更新那条"）────────────────────

// sessionList：按最近更新排序的会话清单（后端已排好序 —— 第一项就是"最近那条"）。
func (r *Runner) sessionList() ([]apiclient.Session, error) {
	if err := r.backend(); err != nil {
		return nil, err
	}
	sessions, err := r.api.Sessions()
	if err != nil {
		return nil, fmt.Errorf("会话列表没问到：%w", err)
	}
	return sessions, nil
}

// currentSession：bot 内存里的当前会话，惰性取 —— 初始 = 最近更新那条；一条都没有就建一条。
func (r *Runner) currentSession(ctx context.Context) (apiclient.Session, error) {
	sessions, err := r.sessionList()
	if err != nil {
		return apiclient.Session{}, err
	}
	if id := r.sessionID(); id != "" {
		for _, session := range sessions {
			if session.ID == id {
				return session, nil
			}
		}
	}
	if len(sessions) > 0 {
		r.setSessionID(sessions[0].ID)
		return sessions[0], nil
	}
	created, err := r.api.CreateSession("", "")
	if err != nil {
		return apiclient.Session{}, fmt.Errorf("建会话失败：%w", err)
	}
	r.setSessionID(created.ID)
	return created, nil
}

// ── 各命令的实现（返回要说的话；发出去由注册表那层干）──────────────────

func (r *Runner) cmdStatus(ctx context.Context) string {
	view := statusView{}
	status := r.Status()
	view.Running = status.Running
	view.AllowedID = status.AllowedID
	view.UptimeMS = status.UptimeMS
	if err := r.backend(); err != nil {
		view.Problems = append(view.Problems, err.Error())
		return strings.Join(statusLines(view), "\n")
	}
	if health, err := r.api.Health(); err == nil {
		view.Version = health.Version
	} else {
		view.Problems = append(view.Problems, "后端没连上："+err.Error())
	}
	session, err := r.currentSession(ctx)
	if err != nil {
		view.Problems = append(view.Problems, err.Error())
		return strings.Join(statusLines(view), "\n")
	}
	view.Session = &session
	view.Turn = session.Turn
	if turn, err := r.api.TurnStatus(session.ID); err == nil {
		view.Turn = turn
	}
	if usage, err := r.api.Context(session.ID); err == nil {
		view.Context = &usage
	} else {
		view.Problems = append(view.Problems, "上下文没问到："+err.Error())
	}
	return strings.Join(statusLines(view), "\n")
}

func (r *Runner) cmdNew(ctx context.Context) string {
	if err := r.backend(); err != nil {
		return err.Error()
	}
	session, err := r.api.CreateSession("", "")
	if err != nil {
		return "新建会话失败：" + err.Error() + "（后端在跑吗？）"
	}
	r.setSessionID(session.ID)
	return "已新建并切过去：" + sessionName(session) + " (" + shortID(session.ID) + ")"
}

func (r *Runner) cmdResume(ctx context.Context, arg string) string {
	sessions, err := r.sessionList()
	if err != nil {
		return err.Error()
	}
	if arg = strings.TrimSpace(arg); arg == "" {
		if len(sessions) == 0 {
			return "现在一条会话都没有（/new 建一条）"
		}
		shown := sessions
		note := ""
		if len(shown) > resumeLimit {
			shown, note = shown[:resumeLimit], fmt.Sprintf("\n（只列了最近 %d 条，共 %d 条）", resumeLimit, len(sessions))
		}
		return strings.Join(resumeLines(shown, r.sessionID()), "\n") + note
	}
	index, err := parsePick(arg, len(sessions))
	if err != nil {
		return err.Error()
	}
	picked := sessions[index]
	r.setSessionID(picked.ID)
	return "已切到：" + sessionName(picked) + " (" + shortID(picked.ID) + ")"
}

func (r *Runner) cmdRename(ctx context.Context, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "得给新名字，比如 `/rename 修 bug`（名字里可以有空格）"
	}
	session, err := r.currentSession(ctx)
	if err != nil {
		return err.Error()
	}
	updated, err := r.api.RenameSession(session.ID, name)
	if err != nil {
		return "改名失败：" + err.Error()
	}
	return "已改名为：" + sessionName(updated)
}

func (r *Runner) cmdCopy(ctx context.Context) string {
	session, err := r.currentSession(ctx)
	if err != nil {
		return err.Error()
	}
	copied, err := r.api.CopySession(session.ID)
	if err != nil {
		return "复制失败：" + err.Error()
	}
	r.setSessionID(copied.ID)
	return "已复制并切过去：" + sessionName(copied) + " (" + shortID(copied.ID) + ")"
}

// cmdDelete：删当前会话 —— **按钮确认**（callback_data 只带短 key，操作细节在内存 pending 表里）。
func (r *Runner) cmdDelete(ctx context.Context, b *bot.Bot, update *models.Update) {
	// 先看一眼有没有会话：没有就别走 currentSession（那个会顺手建一条，然后立刻被删 —— 白折腾）。
	sessions, err := r.sessionList()
	if err != nil {
		r.say(ctx, b, update, err.Error())
		return
	}
	if len(sessions) == 0 {
		r.say(ctx, b, update, "现在一条会话都没有，没得删（/new 建一条）")
		return
	}
	session, err := r.currentSession(ctx)
	if err != nil {
		r.say(ctx, b, update, err.Error())
		return
	}
	key := r.addPending(&pendingOp{kind: "delete", sessionID: session.ID})
	r.sayWithButtons(ctx, b, update,
		"要删掉当前会话：「"+sessionName(session)+"」("+shortID(session.ID)+")。\n"+
			"消息与摘要一起没，不可逆；删完切到最近一条（没有了就新建一条）。",
		[]models.InlineKeyboardButton{
			{Text: "确认删除", CallbackData: confirmData(key)},
			{Text: "取消", CallbackData: cancelData(key)},
		})
}

// cmdCut：删第 arg 条消息及之后 —— 先预览、再按钮确认；确认时按 last_deleted_message_id 核对。
func (r *Runner) cmdCut(ctx context.Context, b *bot.Bot, update *models.Update, arg string) {
	session, err := r.currentSession(ctx)
	if err != nil {
		r.say(ctx, b, update, err.Error())
		return
	}
	target, label, err := r.resolveMessage(ctx, session.ID, arg)
	if err != nil {
		r.say(ctx, b, update, err.Error())
		return
	}
	plan, err := r.api.DeletionPreview(session.ID, target.ID)
	if err != nil {
		r.say(ctx, b, update, "预览失败："+err.Error()+"（这条消息可能已经不在末尾了 —— 重新看一眼消息再 /cut）")
		return
	}
	key := r.addPending(&pendingOp{
		kind: "cut", sessionID: session.ID, messageID: target.ID, lastDeleted: plan.LastDeletedMessageID,
	})
	r.sayWithButtons(ctx, b, update, deletionPreviewText(plan, label),
		[]models.InlineKeyboardButton{
			{Text: "确认删除", CallbackData: confirmData(key)},
			{Text: "取消", CallbackData: cancelData(key)},
		})
}

func (r *Runner) cmdStop(ctx context.Context) string {
	session, err := r.currentSession(ctx)
	if err != nil {
		return err.Error()
	}
	stopped, err := r.api.StopTurn(session.ID)
	if err != nil {
		return "停止失败：" + err.Error()
	}
	if !stopped {
		return "这一轮本来就没在跑（幂等，不算错）"
	}
	return "已叫停这一轮。"
}

func (r *Runner) cmdCompact(ctx context.Context, arg string) string {
	blocks, err := parseBlocks(arg)
	if err != nil {
		return err.Error()
	}
	session, sessionErr := r.currentSession(ctx)
	if sessionErr != nil {
		return sessionErr.Error()
	}
	accepted, err := r.api.Compact(session.ID, blocks)
	if err != nil {
		return "压缩没受理：" + err.Error()
	}
	detail := "用默认块数"
	if blocks > 0 {
		detail = fmt.Sprintf("压 %d 个块", blocks)
	}
	if accepted.Blocks > 0 && blocks == 0 {
		detail = fmt.Sprintf("用默认值 %d 个块", accepted.Blocks)
	}
	return "已受理压缩（" + detail + "，状态 " + accepted.State + "）—— 跑完的结局在 /status 里。"
}

func (r *Runner) cmdState(ctx context.Context) string {
	session, err := r.currentSession(ctx)
	if err != nil {
		return err.Error()
	}
	state, err := r.api.State(session.ID)
	if err != nil {
		return "世界状态没问到：" + err.Error()
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return "世界状态没法序列化：" + err.Error()
	}
	return "世界状态（会话 " + shortID(session.ID) + "）：\n" + string(raw)
}

func (r *Runner) cmdOutgoing(ctx context.Context) string {
	session, err := r.currentSession(ctx)
	if err != nil {
		return err.Error()
	}
	wire, err := r.api.PreviewWire(session.ID, "")
	if err != nil {
		return "载荷没问到：" + err.Error()
	}
	raw, err := json.MarshalIndent(wire, "", "  ")
	if err != nil {
		return "载荷没法序列化：" + err.Error()
	}
	return "真请求（会话 " + shortID(session.ID) + "）：\n" + string(raw)
}

func (r *Runner) cmdUsage(ctx context.Context) string {
	session, err := r.currentSession(ctx)
	if err != nil {
		return err.Error()
	}
	lines := []string{}
	if usage, err := r.api.Context(session.ID); err == nil {
		lines = append(lines, "上下文："+contextLabel(usage))
	} else {
		lines = append(lines, "上下文：没问到（"+err.Error()+"）")
	}
	if session.Provider == "" {
		lines = append(lines, "渠道：还没选（/model 挑一个）")
		return strings.Join(lines, "\n")
	}
	plan, err := r.api.ProviderUsage(session.Provider)
	if err != nil {
		// 非 opencode-go 的渠道后端回 400 —— **原样说**（别猜、别吞）。
		lines = append(lines, "渠道 "+session.Provider+" 的套餐余量："+err.Error())
		return strings.Join(lines, "\n")
	}
	lines = append(lines, "渠道："+plan.ProviderID+"（套餐 "+plan.Plan+"）")
	if len(plan.Windows) == 0 {
		lines = append(lines, "  没有配额窗口")
	}
	for _, window := range plan.Windows {
		lines = append(lines, fmt.Sprintf("  %s：%.1f%%（%s，重置 %s）",
			window.Label, window.Percent, window.Status, window.ResetsAt.Local().Format("2006-01-02 15:04")))
	}
	return strings.Join(lines, "\n")
}

func (r *Runner) cmdThink(ctx context.Context) string {
	session, err := r.currentSession(ctx)
	if err != nil {
		return err.Error()
	}
	messages, err := r.api.Messages(session.ID)
	if err != nil {
		return "消息没问到：" + err.Error()
	}
	lines := []string{}
	for index, message := range messages {
		if strings.TrimSpace(message.Reasoning) == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("第 %d 条（%s）：\n%s", index+1, message.Role, message.Reasoning))
	}
	if len(lines) == 0 {
		return "这条会话还没有思考内容。"
	}
	return strings.Join(lines, "\n\n")
}

func (r *Runner) cmdSystem(ctx context.Context) string {
	session, err := r.currentSession(ctx)
	if err != nil {
		return err.Error()
	}
	prompt, err := r.api.SystemPrompt(session.ID)
	if err != nil {
		return "系统提示词没问到：" + err.Error()
	}
	if strings.TrimSpace(prompt.Text) == "" {
		return "生效的系统提示词是空的（来源：" + prompt.Type + "）"
	}
	return "生效的系统提示词（来源：" + prompt.Type + "）：\n" + prompt.Text
}

func (r *Runner) cmdProviders(ctx context.Context) string {
	if err := r.backend(); err != nil {
		return err.Error()
	}
	providers, err := r.api.Providers()
	if err != nil {
		return "渠道列表没问到：" + err.Error()
	}
	if len(providers) == 0 {
		return "一个渠道都没有（在终端 CLI 里 /provider-add 加）。"
	}
	lines := []string{"渠道："}
	for _, provider := range providers {
		name := provider.ID
		if provider.Name != nil && strings.TrimSpace(*provider.Name) != "" {
			name = *provider.Name
		}
		key := "没密钥"
		if provider.HasKey {
			key = "有密钥"
		}
		lines = append(lines, fmt.Sprintf("%s — %s（%s，%s，%d 个模型）",
			provider.ID, name, provider.Vendor, key, len(provider.Models)))
	}
	return strings.Join(lines, "\n")
}

// resolveMessage：把 `/cut` 的参数解析成一条消息。
//
//	纯数字 ⇒ 1-based 消息序号（清单里的第几条）；
//	别的   ⇒ 消息 id 前缀（唯一命中才行 —— 撞了就要求多给几位）。
func (r *Runner) resolveMessage(ctx context.Context, sessionID, arg string) (apiclient.Message, string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return apiclient.Message{}, "", fmt.Errorf("得给消息：`/cut 3`（第 3 条）或 `/cut <消息 id 前缀>`")
	}
	messages, err := r.api.Messages(sessionID)
	if err != nil {
		return apiclient.Message{}, "", fmt.Errorf("消息没问到：%w", err)
	}
	if len(messages) == 0 {
		return apiclient.Message{}, "", fmt.Errorf("这条会话还没有消息，没得删")
	}
	if number, err := strconv.Atoi(arg); err == nil {
		if number < 1 || number > len(messages) {
			return apiclient.Message{}, "", fmt.Errorf("序号得在 1–%d 之间（这条会话 %d 条消息）", len(messages), len(messages))
		}
		message := messages[number-1]
		return message, fmt.Sprintf("第 %d 条（%s %s）", number, message.Role, shortID(message.ID)), nil
	}
	matches := []apiclient.Message{}
	for _, message := range messages {
		if strings.HasPrefix(message.ID, arg) {
			matches = append(matches, message)
		}
	}
	switch len(matches) {
	case 0:
		return apiclient.Message{}, "", fmt.Errorf("没有消息 id 以 `%s` 开头 —— 序号也行：`/cut 3`", arg)
	case 1:
		return matches[0], matches[0].Role + " " + shortID(matches[0].ID), nil
	default:
		return apiclient.Message{}, "", fmt.Errorf("有 %d 条消息 id 以 `%s` 开头，多给几位（或用序号 `/cut 3`）", len(matches), arg)
	}
}

// ── /model：面板 + 翻页 + 选中 ─────────────────────────────────────────

func (r *Runner) cmdModel(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.Message == nil {
		return
	}
	if err := r.backend(); err != nil {
		r.say(ctx, b, update, err.Error())
		return
	}
	items, err := r.api.Models()
	if err != nil {
		r.say(ctx, b, update, "模型列表没问到："+err.Error())
		return
	}
	rows, pages := modelButtons(items, 0)
	if pages == 0 {
		r.say(ctx, b, update, modelPageText(items, 0, 0))
		return
	}
	var markup *models.InlineKeyboardMarkup
	if len(rows) > 0 {
		markup = &models.InlineKeyboardMarkup{InlineKeyboard: rows}
	}
	if _, err := sendMarkdownWithMarkup(ctx, b, update.Message.Chat.ID,
		modelPageText(items, 0, pages), markup); err != nil {
		r.say(ctx, b, update, "模型面板没发出去："+err.Error())
	}
}

// onModel：`model:` 回调 —— 翻页就地 edit；选中就 PATCH 当前会话再回执。
func (r *Runner) onModel(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !r.gateCallback(ctx, b, update) {
		return
	}
	page, index, err := parseModelData(update.CallbackQuery.Data)
	if err != nil {
		r.answerCallback(ctx, b, update, clip(err.Error(), 180), true)
		return
	}
	items, err := r.api.Models()
	if err != nil {
		r.answerCallback(ctx, b, update, "模型列表没问到："+clip(err.Error(), 120), true)
		return
	}
	if index < 0 { // 翻页：就地重画
		rows, pages := modelButtons(items, page)
		text := modelPageText(items, page, pages)
		var markup *models.InlineKeyboardMarkup
		if len(rows) > 0 {
			markup = &models.InlineKeyboardMarkup{InlineKeyboard: rows}
		}
		_ = r.editCallback(ctx, b, update, text, markup)
		r.answerCallback(ctx, b, update, fmt.Sprintf("第 %d/%d 页", page+1, max(pages, 1)), false)
		return
	}
	if index >= len(items) {
		r.answerCallback(ctx, b, update, "这个编号已经不在列表里了 —— 重新 /model 一次", true)
		return
	}
	item := items[index]
	session, err := r.currentSession(ctx)
	if err != nil {
		r.answerCallback(ctx, b, update, clip(err.Error(), 180), true)
		return
	}
	updated, err := r.api.UpdateSession(session.ID, item.Provider, item.UpstreamID)
	if err != nil {
		r.answerCallback(ctx, b, update, "切换失败："+clip(err.Error(), 140), true)
		return
	}
	_ = r.editCallback(ctx, b, update,
		"已切到："+item.ProviderLabel()+"/"+item.Label()+"\n当前会话："+sessionName(updated)+" ("+shortID(updated.ID)+")", nil)
	r.answerCallback(ctx, b, update, "已切到 "+item.Label(), false)
}

// ── 待办：确认按钮背后的那件事（细节留内存，callback_data 只带短 key）──

// pendingOp：等着用户点头的一件事（删会话 / 删消息）。命令一发就记下"要动哪条"，
// 回调里只认短 key —— callback_data 因此永远只有十几字节。
type pendingOp struct {
	kind        string // delete | cut
	sessionID   string
	messageID   string // cut 用
	lastDeleted string // cut 用：预览里的末尾那条（执行时后端要核对）
	at          time.Time
}

// pendingTTL：待办最多摆这么久（再点也不认了 —— 内存里的东西，重启就没）。
const pendingTTL = 10 * time.Minute

// pendingSeq：拿不到随机数时的退路（只要进程内唯一就够 —— 这不是安全边界）。
var pendingSeq atomic.Uint64

// addPending：记一件事，返回它的短 key（8 字节 hex ⇒ 16 字节的 callback_data）。
func (r *Runner) addPending(op *pendingOp) string {
	op.at = time.Now()
	var raw [4]byte
	key := ""
	if _, err := rand.Read(raw[:]); err == nil {
		key = hex.EncodeToString(raw[:])
	} else {
		key = strconv.FormatUint(pendingSeq.Add(1), 36)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending == nil {
		r.pending = map[string]*pendingOp{}
	}
	// 顺手清过期的（表只在加东西时长大 —— 清在这儿就够）。
	for existing, entry := range r.pending {
		if time.Since(entry.at) > pendingTTL {
			delete(r.pending, existing)
		}
	}
	r.pending[key] = op
	return key
}

// popPending：取走一件事（一次性：点第二次就说"处理过了"）。
func (r *Runner) popPending(key string) (*pendingOp, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	op, ok := r.pending[key]
	if ok {
		delete(r.pending, key)
		if time.Since(op.at) > pendingTTL {
			return nil, false
		}
	}
	return op, ok
}

// onConfirm：确认 / 取消回调 —— 两个前缀共用一条路。
func (r *Runner) onConfirm(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !r.gateCallback(ctx, b, update) {
		return
	}
	data := update.CallbackQuery.Data
	cancelling := strings.HasPrefix(data, cbCancel)
	prefix := cbConfirm
	if cancelling {
		prefix = cbCancel
	}
	op, ok := r.popPending(strings.TrimPrefix(data, prefix))
	if !ok {
		r.answerCallback(ctx, b, update, "这件事已经处理过了（待办只在内存里，重启就没了）—— 重新发一次命令", true)
		return
	}
	if cancelling {
		_ = r.editCallback(ctx, b, update, "已取消。", nil)
		r.answerCallback(ctx, b, update, "已取消", false)
		return
	}
	outcome := r.executePending(ctx, op)
	_ = r.editCallback(ctx, b, update, outcome, nil)
	r.answerCallback(ctx, b, update, clip(outcome, 180), false)
}

// executePending：真动手（只有走到这儿才动库 —— 前面一直是预览）。
func (r *Runner) executePending(ctx context.Context, op *pendingOp) string {
	if op == nil {
		return "这件事已经处理过了。"
	}
	if err := r.backend(); err != nil {
		return err.Error()
	}
	switch op.kind {
	case "delete":
		if err := r.api.DeleteSession(op.sessionID); err != nil {
			return "删除失败：" + err.Error()
		}
		return r.settleAfterDelete()
	case "cut":
		plan, err := r.api.DeleteMessagesFrom(op.sessionID, op.messageID, op.lastDeleted)
		if err != nil {
			return "删除失败：" + err.Error() + "（会话大概又变了 —— 重新 /cut 一次）"
		}
		return fmt.Sprintf("已删除：消息 %d 条、摘要 %d 条。",
			len(plan.DeletedMessageIDs), len(plan.DeletedSummaryIDs))
	}
	return "不认识这件事（装配 bug）。"
}

// settleAfterDelete：删完切到最近一条；一条都没有就建一条新的（与 CLI 口径一致）。
func (r *Runner) settleAfterDelete() string {
	sessions, err := r.api.Sessions()
	if err != nil {
		r.setSessionID("")
		return "已删除；但会话列表没问到：" + err.Error() + "（下一条命令会重试）"
	}
	if len(sessions) == 0 {
		created, err := r.api.CreateSession("", "")
		if err != nil {
			r.setSessionID("")
			return "已删除；新建会话失败：" + err.Error()
		}
		r.setSessionID(created.ID)
		return "已删除；又新建了一条：" + sessionName(created) + " (" + shortID(created.ID) + ")"
	}
	r.setSessionID(sessions[0].ID)
	return "已删除；现在在：" + sessionName(sessions[0]) + " (" + shortID(sessions[0].ID) + ")"
}

// ── 回调的小工具（门卫 / 回执 / 就地 edit）─────────────────────────────

// gateCallback：回调的门卫 —— 未绑定的那个人只收到一句"没绑定这个 id"。
func (r *Runner) gateCallback(ctx context.Context, b *bot.Bot, update *models.Update) bool {
	if update.CallbackQuery == nil {
		return false
	}
	if r.allowedID() != 0 && update.CallbackQuery.From.ID != r.allowedID() {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			CallbackQueryID: update.CallbackQuery.ID, Text: "没绑定这个 id", ShowAlert: true,
		})
		return false
	}
	return true
}

// answerCallback：回执（TG 上那一下的 toast / 弹窗）。
func (r *Runner) answerCallback(ctx context.Context, b *bot.Bot, update *models.Update, text string, alert bool) {
	if update.CallbackQuery == nil {
		return
	}
	_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: update.CallbackQuery.ID, Text: clip(text, 180), ShowAlert: alert,
	})
}

// editCallback：就地改那条带按钮的消息（不刷屏）。
func (r *Runner) editCallback(ctx context.Context, b *bot.Bot, update *models.Update, text string,
	markup *models.InlineKeyboardMarkup) error {
	message := callbackMessage(update)
	if message == nil {
		return fmt.Errorf("这条回调没有消息可改")
	}
	_, err := editMarkdown(ctx, b, message.Chat.ID, message.ID, clip(text, tgMaxLen), markup)
	return err
}

// callbackMessage：回调挂在的那条消息（库的类型是 MaybeInaccessible，取可访问的那半边）。
func callbackMessage(update *models.Update) *models.Message {
	if update.CallbackQuery == nil {
		return nil
	}
	return update.CallbackQuery.Message.Message
}

// parseBlocks：`/compact [N]` 的 N（不给 = 0 = 后端默认值）。
func parseBlocks(arg string) (int, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return 0, nil
	}
	number, err := strconv.Atoi(arg)
	if err != nil {
		return 0, fmt.Errorf("`%s` 不是数字 —— N 是压几个块（块 = assistant→user 交界），比如 `/compact 3`；不给 N 用默认值", arg)
	}
	if number < 1 {
		return 0, fmt.Errorf("N 得 ≥ 1（你说的是 %d）；不给 N 用默认值", number)
	}
	return number, nil
}
