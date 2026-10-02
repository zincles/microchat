package tg

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"microchat/internal/apiclient"
)

// Runner：bot 长轮询的那一整个活 —— 启动 / 状态 / 停。
//
// 生命周期归 main 管：Enable + 有 token 才 `Go(ctx)` 跑起来；ctx 取消就停。
// 几条**进程内事实**（重启就没）：当前会话（currentSessionID）、
// 等确认的待办（pending）、最近一条回复那组按钮（activeReply）—— 与 turn 登记表同一条脾气，不进库。
type Runner struct {
	mu      sync.Mutex
	bot     *bot.Bot
	cancel  context.CancelFunc
	started *time.Time
	allowed int64
	api     *apiclient.Client
	session string                // 当前会话 id（"" = 还没定，惰性取最近一条）
	pending map[string]*pendingOp // key: 短 key（确认按钮按它找"要动哪件事"）
	// replyMu / activeReply：最近一条回复那组 [◀ n/total ▶] 按钮（见 reroll.go）。
	// 单独一把锁：按钮回调里的网络调用不许把 r.mu 占住。
	replyMu     sync.Mutex
	activeReply map[int64]*replyButtons // chatID → 最新一条回复的按钮组
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
// 否则 ok=false（交给文本入口真发一轮）。
//
// 只看形状、不认名单：`/status` 这类已知命令也返回 true —— 它们由 onMessage 的注册表派发接走，
// 根本走不到这儿；到这儿还在的斜杠串就是没登记过的。
func unknownCommandReply(text string) (string, bool) {
	if text == "" || text[0] != '/' {
		return "", false
	}
	names := make([]string, 0, len(botCommands))
	for _, command := range botCommands {
		names = append(names, "/"+command.name)
	}
	return "不认识这个命令：" + text + "（可用：" + strings.Join(names, " ") + "）", true
}

// startupGreeting：上线问候 —— 绑了人才发（没绑就没处发）。返回 false 表示不发。
func startupGreeting(allowedID int64) (string, bool) {
	if allowedID <= 0 {
		return "", false
	}
	return "microchat 上线了 ✓ 直接发话；/status 看状态，/help 看命令", true
}

// NewRunner：只建壳，不连网（连网是 Go 的事）。api = 连本机后端的客户端（命令都靠它取数）。
func NewRunner(token string, allowed int64, api *apiclient.Client) (*Runner, error) {
	if token == "" {
		return nil, fmt.Errorf("tg: 没有 token（config.json 的 telegram.bot_token 或 TELEGRAM_BOT_TOKEN）")
	}
	if api == nil {
		return nil, fmt.Errorf("tg: 没有后端客户端（main 里要用 apiclient.NewClient 建一个传进来）")
	}
	b, err := bot.New(token)
	if err != nil {
		return nil, err
	}
	r := &Runner{
		bot: b, allowed: allowed, api: api,
		pending: map[string]*pendingOp{},
	}
	r.register(b)
	return r, nil
}

// register：把两个入口挂上 —— **一条文本入口 + 命令表派发**，加上三个回调前缀。
//
// 不再逐条注册精确匹配：带参数的命令（`/rename a b`）在精确匹配下根本到不了，
// 而且注册表与派发分成两处必然漂移。
func (r *Runner) register(b *bot.Bot) {
	b.RegisterHandler(bot.HandlerTypeMessageText, "", bot.MatchTypePrefix, r.onMessage)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, cbConfirm, bot.MatchTypePrefix, r.onConfirm)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, cbCancel, bot.MatchTypePrefix, r.onConfirm)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, cbModel, bot.MatchTypePrefix, r.onModel)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, cbReroll, bot.MatchTypePrefix, r.onRerollButton)
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

// sessionID / setSessionID：当前会话（进程内事实）。
func (r *Runner) sessionID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.session
}

func (r *Runner) setSessionID(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.session = id
}

// onMessage：**唯一的文本入口**。
//
// 顺序：门卫 → 命令表派发（命中就干，`/xxx` 形状但不认识 ⇒ 明说不认识）→ 都不是就**真发一轮**。
// 门卫在最前：没绑定的人先收到绑定提示，命令清单与任何数据都不泄露。
func (r *Runner) onMessage(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.Message == nil || update.Message.Text == "" {
		return
	}
	if !gateMessage(ctx, b, update.Message, r.allowedID()) {
		return
	}
	text := update.Message.Text
	if strings.HasPrefix(text, "/") {
		if name, args := parseCommand(text); name != "" {
			if command := findCommand(name); command != nil {
				command.run(ctx, r, b, update, args)
				return
			}
		}
		// 斜杠形状但没登记过：明说"不认识"（别让它当普通文本发进会话）。
		if reply, ok := unknownCommandReply(text); ok {
			_ = sendLong(ctx, b, update.Message.Chat.ID, reply)
			return
		}
	}
	// 非命令文本 = 真发一轮（发进当前会话 → 轮询 → 节流 edit 把回复长出来）。
	r.handleText(ctx, b, update)
}
