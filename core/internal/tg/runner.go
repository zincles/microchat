package tg

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// Runner：bot 长轮询的那一整个活 —— 启动 / 状态 / 停。
//
// 生命周期归 main 管：Enable + 有 token 才 `Go(ctx)` 跑起来；ctx 取消就停。
// 翻页状态（page）在进程内（与 turn 登记表同一条"进程内事实"脾气）：重启就没。
type Runner struct {
	mu      sync.Mutex
	bot     *bot.Bot
	cancel  context.CancelFunc
	started *time.Time
	allowed int64
	pages   map[string]*pageState // key: chatID:msgID（翻页回调按它找状态）
}

type pageState struct {
	title string
	pages []string
	index int
}

// MenuCommands：BotFather 菜单里摆的那几个（/telegram-bind 在 TUI 不在 bot —— bot 侧只读）。
var MenuCommands = []models.BotCommand{
	{Command: "start", Description: "开始（报绑定状态）"},
	{Command: "status", Description: "看 bot 状态"},
	{Command: "help", Description: "能干什么"},
}

// commandScopes：清旧菜单要挨个招呼的那几个作用域。
// 旧 bot（如 Hermes）可能把命令摆在这些作用域上，残留会盖住新菜单 —— default 之外还得清三处"批量"作用域。
func commandScopes() []models.BotCommandScope {
	return []models.BotCommandScope{
		&models.BotCommandScopeDefault{},
		&models.BotCommandScopeAllPrivateChats{},
		&models.BotCommandScopeAllGroupChats{},
		&models.BotCommandScopeAllChatAdministrators{},
	}
}

// unknownCommandReply：text 是 `/xxx` 形状（斜杠开头、非空）⇒ 给"不认识"的回复（含原文与可用清单）；
// 否则 ok=false（走 echo 那条路）。
//
// 只看形状、不认名单：/start 这类已知命令也返回 true —— 它们由注册顺序在前面的精确匹配处理器接走，
// 根本走不到 onEcho；到这儿还在的斜杠串就是没登记过的。
func unknownCommandReply(text string) (string, bool) {
	if text == "" || text[0] != '/' {
		return "", false
	}
	names := make([]string, 0, len(MenuCommands))
	for _, c := range MenuCommands {
		names = append(names, "/"+c.Command)
	}
	return "不认识这个命令：" + text + "（可用：" + strings.Join(names, " ") + "）", true
}

// startupGreeting：上线问候 —— 绑了人才发（没绑就没处发）。返回 false 表示不发。
func startupGreeting(allowedID int64) (string, bool) {
	if allowedID <= 0 {
		return "", false
	}
	return "microchat 上线了 ✓ 直接发话；/status 看状态", true
}

// NewRunner：只建壳，不连网（连网是 Go 的事 —— 建壳失败只可能是 token 空）。
func NewRunner(token string, allowed int64) (*Runner, error) {
	if token == "" {
		return nil, fmt.Errorf("tg: 没有 token（config.json 的 telegram.bot_token 或 TELEGRAM_BOT_TOKEN）")
	}
	b, err := bot.New(token)
	if err != nil {
		return nil, err
	}
	r := &Runner{bot: b, allowed: allowed, pages: map[string]*pageState{}}
	b.RegisterHandler(bot.HandlerTypeMessageText, "/start", bot.MatchTypeExact, r.onStart)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/status", bot.MatchTypeExact, r.onStatus)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/help", bot.MatchTypeExact, r.onHelp)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "page:", bot.MatchTypePrefix, r.onPage)
	b.RegisterHandler(bot.HandlerTypeMessageText, "", bot.MatchTypePrefix, r.onEcho)
	return r, nil
}

// Go：后台跑长轮询（阻塞由调用方决定 —— main 里 go r.Go(ctx)）。
func (r *Runner) Go(ctx context.Context) error {
	me, err := r.bot.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("tg: 连不上 Telegram（token 错 / 没网）：%w", err)
	}
	// 先清旧菜单：旧 bot 可能把命令摆在别的批量作用域上，残留会盖住新菜单。
	// 清不掉不阻断 —— 顶多菜单不对，摆新的这步才是正经事。
	for _, scope := range commandScopes() {
		if _, err := r.bot.DeleteMyCommands(ctx, &bot.DeleteMyCommandsParams{Scope: scope}); err != nil {
			log.Printf("TG 旧菜单没清掉（%T）：%v", scope, err)
		}
	}
	if _, err := r.bot.SetMyCommands(ctx, &bot.SetMyCommandsParams{Commands: MenuCommands}); err != nil {
		return fmt.Errorf("tg: 菜单没摆上：%w", err)
	}
	// 上线问候：绑了人才发。对方从没跟 bot 说过话 ⇒ Telegram 报 chat not found，这不是错，只记日志。
	if greeting, ok := startupGreeting(r.allowedID()); ok {
		if err := sendLong(ctx, r.bot, r.allowedID(), greeting); err != nil {
			log.Printf("TG 上线问候没发出去：%v", err)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	r.mu.Lock()
	r.cancel = cancel
	now := time.Now()
	r.started = &now
	r.mu.Unlock()
	// 起来了就把"是谁、绑的谁"落一行日志 —— 排障时这行最有用（TUI 底栏只有个 ✓）。
	log.Printf("TG bot 连上：@%s（%s）· 菜单已摆 %d 条 · 绑定 %d",
		me.Username, me.FirstName, len(MenuCommands), r.allowedID())
	r.bot.Start(ctx)
	log.Printf("TG bot 长轮询退出（@%s）", me.Username)
	return nil
}

// Stop：停轮询（幂等）。
func (r *Runner) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	r.started = nil
}

// Running：轮询跑没跑（给 server 的 telegramStatus 看 —— Server 只读这一句）。
func (r *Runner) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cancel != nil
}

// Status：给 `/telegram-status` 与 `GET /config/telegram` 看的那份。
func (r *Runner) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	running := r.cancel != nil
	var since *int64
	if r.started != nil {
		ms := time.Since(*r.started).Milliseconds()
		since = &ms
	}
	return Status{Running: running, AllowedID: r.allowed, UptimeMS: since}
}

// Status：bot 的活状态（只读）。
type Status struct {
	Running   bool   `json:"running"`
	AllowedID int64  `json:"allowed_id"`
	UptimeMS  *int64 `json:"uptime_ms,omitempty"`
}

// SetAllowed：绑定换人（TUI 绑完调这个，不用重启 bot）。
func (r *Runner) SetAllowed(id int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.allowed = id
}

func (r *Runner) allowedID() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.allowed
}

func (r *Runner) onStart(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.Message == nil {
		return
	}
	if !gateMessage(ctx, b, update.Message, r.allowedID()) {
		return
	}
	_ = sendLong(ctx, b, update.Message.Chat.ID, "microchat 已连接（单账户已绑定）。直接发话就是一轮，/status 看状态。")
}

func (r *Runner) onStatus(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.Message == nil {
		return
	}
	if !gateMessage(ctx, b, update.Message, r.allowedID()) {
		return
	}
	st := r.Status()
	uptime := "—"
	if st.UptimeMS != nil {
		uptime = (time.Duration(*st.UptimeMS) * time.Millisecond).Round(time.Second).String()
	}
	_ = sendLong(ctx, b, update.Message.Chat.ID, "bot 运行中 · 已绑定 "+strconv.FormatInt(st.AllowedID, 10)+" · 在线 "+uptime)
}

func (r *Runner) onHelp(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.Message == nil {
		return
	}
	if !gateMessage(ctx, b, update.Message, r.allowedID()) {
		return
	}
	_ = sendLong(ctx, b, update.Message.Chat.ID, "直接发话就是一轮对话（先回声验证）；/status 看状态；翻页按钮在长输出上。")
}

// onEcho：回声验证（chat 接进来之前，先证明"收得到、发得出、分段对"）。
func (r *Runner) onEcho(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.Message == nil || update.Message.Text == "" {
		return
	}
	if !gateMessage(ctx, b, update.Message, r.allowedID()) {
		return
	}
	// 斜杠形状但没登记过的命令：明说"不认识"（别让它当普通文本 echo 回去）。
	// 放门卫之后：没绑定的人先收到绑定提示，不泄露命令清单。
	if reply, ok := unknownCommandReply(update.Message.Text); ok {
		_ = sendLong(ctx, b, update.Message.Chat.ID, reply)
		return
	}
	text := "收到（" + strconv.Itoa(len([]rune(update.Message.Text))) + " 字）：" + update.Message.Text
	// 超长走翻页（按钮翻，不连发刷屏）：先发第一段 + 按钮，剩下的按回调 edit。
	parts := splitMessage(text)
	if len(parts) == 1 {
		_ = sendLong(ctx, b, update.Message.Chat.ID, text)
		return
	}
	r.sendPaged(ctx, b, update.Message.Chat.ID, "回声（"+strconv.Itoa(len(parts))+" 段）", parts)
}

// sendPaged：发第一段 + 翻页按钮（剩下的按回调 edit 同一条消息）。
func (r *Runner) sendPaged(ctx context.Context, b *bot.Bot, chatID int64, title string, parts []string) {
	msg, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID, Text: title + "（1/" + strconv.Itoa(len(parts)) + "）\n" + parts[0],
		ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: "下一段 →", CallbackData: "page:1"}},
		}},
	})
	if err != nil {
		return
	}
	key := strconv.FormatInt(chatID, 10) + ":" + strconv.Itoa(msg.ID)
	r.mu.Lock()
	r.pages[key] = &pageState{title: title, pages: parts, index: 0}
	r.mu.Unlock()
}

// onPage：翻页回调 —— edit 同一条消息（不刷屏）。
func (r *Runner) onPage(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.CallbackQuery == nil {
		return
	}
	if r.allowedID() != 0 && update.CallbackQuery.From.ID != r.allowedID() {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			CallbackQueryID: update.CallbackQuery.ID, Text: "没绑定这个 id", ShowAlert: true,
		})
		return
	}
	index, err := strconv.Atoi(update.CallbackQuery.Data[len("page:"):])
	if err != nil {
		return
	}
	key := strconv.FormatInt(update.CallbackQuery.Message.Message.Chat.ID, 10) + ":" +
		strconv.Itoa(update.CallbackQuery.Message.Message.ID)
	r.mu.Lock()
	state, ok := r.pages[key]
	r.mu.Unlock()
	if !ok || index < 0 || index >= len(state.pages) {
		return
	}
	state.index = index
	buttons := []models.InlineKeyboardButton{}
	if index > 0 {
		buttons = append(buttons, models.InlineKeyboardButton{Text: "← 上一段", CallbackData: "page:" + strconv.Itoa(index-1)})
	}
	if index < len(state.pages)-1 {
		buttons = append(buttons, models.InlineKeyboardButton{Text: "下一段 →", CallbackData: "page:" + strconv.Itoa(index+1)})
	}
	text := state.title + "（" + strconv.Itoa(index+1) + "/" + strconv.Itoa(len(state.pages)) + "）\n" + state.pages[index]
	var markup *models.InlineKeyboardMarkup
	if len(buttons) > 0 {
		markup = &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{buttons}}
	}
	_, _ = b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID: update.CallbackQuery.Message.Message.Chat.ID, MessageID: update.CallbackQuery.Message.Message.ID,
		Text: text, ReplyMarkup: markup,
	})
	_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: update.CallbackQuery.ID})
}
