package main

// ── 直操模式（`./microchat -debug <op> [参数…]`）──────────────────────────────
//
// **同进程**调 `internal/*`：不起服务、不发 HTTP、不开 TUI，打印结果就退出。
//
// 为什么要有它（用户 2026-09-30 定）：TUI 与 HTTP 都隔着几层，验收「生成那一轮」时看不清
// 到底是哪一层错了；这个模式把 `internal/*` 直接摊在终端上 —— **每行一条 JSON**，好接管道（jq）。
//
// 三条规矩：
//  1. **不新增依赖**：一个 flag 分支，只用标准库；
//  2. 参数一律用**完整 id**（UUID）；输出每行一条 JSON，错误体固定 `{"error":{"code","message"}}`
//     （与 HTTP 契约同一个形状 —— 客户端按 `code` 分支的那条规矩照旧）；
//  3. 与 `-config` / `-data` / `-addr` 共存；给了 `-debug` 就**不起服务**。
//
// 它**不属于**任何一层架构：只从 `main.go` 进来（不进 HTTP、不进 TUI），
// op 一览与用法在 `debugUsage` 里。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"microchat/internal/abilities"
	"microchat/internal/chat"
	"microchat/internal/compact"
	"microchat/internal/config"
	"microchat/internal/model"
	"microchat/internal/providers"
	"microchat/internal/registry"
	"microchat/internal/state"
	"microchat/internal/store"
	"microchat/internal/task"
	"microchat/internal/title"
	"microchat/internal/turn"
)

// debugUsage：用法与 op 一览。给错参数时打到 **stderr** —— stdout 上只跑 JSON。
const debugUsage = `microchat 直操模式（同进程调 internal/*；不起服务、不发 HTTP、不开 TUI）

用法：./microchat [-config 目录] [-data 目录] -debug <op> [参数…]

op：
  sessions                                          列会话（id / 标题 / provider / model）
  new [--title X] [--provider P] [--model M]        建会话
  send <session_id> <内容>                          同步跑完一整轮（走 chat.Service）
  outgoing <session_id>                             逐条打印要发出去的东西（含出处）
  status <session_id>                               这一轮的 turn 状态
  stop <session_id>                                 按停（幂等）
  delete-preview <session_id> <message_id>          删除预览（只算不动）
  delete <session_id> <message_id> <last_deleted_message_id>
                                                    执行删除（末尾核对不符 ⇒ 409）
  copy <session_id>                                 复制会话
  state <session_id>                                世界状态（effective / tables）
  compact <session_id> [--blocks N | --begin <id> --end <id>]
                                                    压缩：同步跑完一次并打印区间 / 块数 / 摘要
  title <session_id>                                手动起一次标题（不管现有没有；产物落 sessions.title）
  abilities <session_id>                            这个会话的 agent 在三个能力上的解析结果
  refresh-models [provider_id]                      **启动时那条路**：每个渠道各拉一次 /models 并落库
                                                    （同步跑完；跳过 dummy / 要去外网又没配 key 的；
                                                     每行一个渠道：拉了几个模型 / 跳过原因 / 失败原因）
  tasks                                             任务面板
  usage [provider_id]                               OpenCode GO 的套餐余量（只读 GET；不给 id 就用第一个 opencode-go 渠道）

输出：每行一条 JSON；错误体固定 {"error":{"code","message"}}（与 HTTP 契约同一个形状）。
退出码：0 成功；1 失败（看那行错误体 / stored=false）；2 用法错。
`

// runDebug：直操模式的入口（`main.go` 里**唯一**开它的地方）。返回值就是进程退出码。
//
// 每个 op 都是"打开库 → 调 internal/* → 打印 → 退出"：没有 HTTP、没有 TUI、没有监听。
// 三个登记表（turn / task）与本进程同生共死 —— 这与后端"状态是进程内的事实"是同一条口径
// （所以 `-debug` 每次调用都是新进程，看到的就是"这一刻这个进程的事实"）。
func runDebug(paths config.Paths, cfg config.Config, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, debugUsage)
		return 2
	}
	// 直操模式可能跑在"数据目录还不存在"的机器上（第一次用）：先建出来，
	// 别让 store.Open 回一句 SQLite 的 "unable to open database file"。
	if err := os.MkdirAll(paths.DataDir, 0o755); err != nil {
		return fail("internal", "建数据目录失败: "+err.Error())
	}
	st, err := store.Open(filepath.Join(paths.DataDir, "microchat.db"))
	if err != nil {
		return fail("internal", "打开数据库失败: "+err.Error())
	}
	defer st.Close()

	turns, tasks := turn.NewRegistry(), task.NewRegistry()
	env := &debugEnv{
		store: st, cfg: cfg, paths: paths, turns: turns, tasks: tasks,
		chat:    chat.New(st, paths, turns, tasks),
		compact: compact.New(st, paths, turns, tasks),
		titles:  title.New(st, paths, tasks),
	}
	// 起标题挂在一轮生成上（拿到回复之后自动一次）—— 与 main.go 同一条口径
	env.chat.Titles = env.titles

	switch op := args[0]; op {
	case "sessions":
		return env.sessions()
	case "new":
		return env.newSession(args[1:])
	case "send":
		return env.send(args[1:])
	case "outgoing":
		return env.outgoing(args[1:])
	case "status":
		return env.status(args[1:])
	case "stop":
		return env.stop(args[1:])
	case "delete-preview":
		return env.deletionPreview(args[1:])
	case "delete":
		return env.deleteMessage(args[1:])
	case "copy":
		return env.copySession(args[1:])
	case "state":
		return env.stateOf(args[1:])
	case "compact":
		return env.compactSession(args[1:])
	case "title":
		return env.titleOp(args[1:])
	case "abilities":
		return env.abilitiesOf(args[1:])
	case "refresh-models":
		return env.refreshModelsOp(args[1:])
	case "tasks":
		return env.board()
	case "usage":
		return env.usageOf(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "直操模式：不认识这个 op：%s\n\n", op)
		fmt.Fprint(os.Stderr, debugUsage)
		return 2
	}
}

// debugEnv：直操模式手里的那一套（一个进程一副）。
type debugEnv struct {
	store   *store.Store
	chat    *chat.Service
	compact *compact.Service
	titles  *title.Service
	turns   *turn.Registry
	tasks   *task.Registry
	cfg     config.Config
	paths   config.Paths
}

// ── 输出与错误（**只有这两条出口**）────────────────────────────────────────

// emit：一行一条 JSON。**不转义 HTML**（与 server 的 writeJSON 同一个口径：正文里全是 LaTeX）；
// 正文里的换行由 JSON 自己转义 ⇒ "一行一条"这件事不会破。Encode 补的那个 `\n` 正好收尾。
func emit(value any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		fmt.Fprintln(os.Stderr, "microchat: 写输出失败: "+err.Error())
	}
}

// fail：错误体固定 `{"error":{"code","message"}}`（**打到 stdout** —— 验收要的就是
// "把 409 的错误体打出来"，接管道的一侧得看得见）。`code` ∈ 与 HTTP 同一套词。
func fail(code, message string) int {
	emit(map[string]any{"error": map[string]any{"code": code, "message": message}})
	return 1
}

// debugError：一趟 op 的失败 —— `code` 就是错误体里那个 code（不再另造一套词）。
type debugError struct{ code, message string }

func (e debugError) Error() string { return e.message }

// errf：造一个带 code 的失败（别处一律用 `report(err)` 出口）。
func errf(code, format string, args ...any) error {
	return debugError{code: code, message: fmt.Sprintf(format, args...)}
}

// report：op 失败时**唯一**的出口 —— 带 code 的按那个 code 打，其余算 `internal`。
func report(err error) int {
	if err == nil {
		return 0
	}
	var coded debugError
	if errors.As(err, &coded) {
		return fail(coded.code, coded.message)
	}
	return fail("internal", err.Error())
}

// messageError：消息级操作的错误映射 —— 与 `server.writeMessageError` 一个口径
// （"会话不存在"会误导：多半是那条消息没了）。
func messageError(err error) error {
	var invalid store.InvalidError
	switch {
	case errors.Is(err, store.ErrNotFound):
		return errf("not_found", "这条会话里没有这条消息")
	case errors.As(err, &invalid):
		return errf("invalid", "%s", invalid.Error())
	}
	return err
}

// argAt：第 index 个参数（没有就空串 ⇒ 由各自的用法检查报错）。
func argAt(args []string, index int) string {
	if index < 0 || index >= len(args) {
		return ""
	}
	return args[index]
}

// ── op：会话与消息 ────────────────────────────────────────────────────────

// sessionLine：`sessions` 的一行 —— 只列人类认得出会话的三样（id 是句柄，剩下是"这是什么"）。
type sessionLine struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// sessions：列会话 —— 与 `GET /sessions` 同一份排序口径（store 说什么就是什么）。
func (e *debugEnv) sessions() int {
	sessions, err := e.store.ListSessions()
	if err != nil {
		return report(err)
	}
	for _, session := range sessions {
		emit(sessionLine{ID: session.ID, Title: session.Title, Provider: session.Provider, Model: session.Model})
	}
	return 0
}

// newSession：建会话 —— 与 `POST /sessions` 同一条路：省略的字段取 `config.json` 的 defaults，
// agent 取 `agents.json` 的 `default_agent`（**不然新会话永远带着内置 default** —— server 那边踩过）。
//
// 标题一律留给首条用户消息（`--title` 显式给了才先写一个）。
func (e *debugEnv) newSession(args []string) int {
	values, err := parseNamedFlags(args, "title", "provider", "model")
	if err != nil {
		return report(err)
	}
	provider := e.cfg.Defaults.Provider
	if value, ok := values["provider"]; ok {
		provider = value
	}
	modelID := e.cfg.Defaults.Model
	if value, ok := values["model"]; ok {
		modelID = value
	}
	session, err := e.store.CreateSession(provider, modelID, "")
	if err != nil {
		return report(err)
	}
	agentID := e.defaultAgentID()
	if agentID != "" && agentID != session.AgentID {
		if err := e.store.SetSessionAgent(session.ID, agentID); err != nil {
			return report(err)
		}
		session.AgentID = agentID
	}
	if title, ok := values["title"]; ok && title != "" {
		if err := e.store.UpdateTitle(session.ID, title); err != nil {
			return report(err)
		}
		session.Title = title
	}
	emit(session)
	return 0
}

// defaultAgentID：`agents.json` 的 `default_agent`（空串 / 缺失都算没配 ⇒ 内置默认）。
func (e *debugEnv) defaultAgentID() string {
	_, agents, _, _ := config.Load(e.paths)
	if id := strings.TrimSpace(agents.DefaultAgent); id != "" {
		return id
	}
	return model.DefaultAgentID()
}

// session：取会话；不存在 ⇒ 打一行 `not_found`（与 HTTP 的 404 一个口径）。
func (e *debugEnv) session(sessionID string) (*model.Session, bool) {
	if strings.TrimSpace(sessionID) == "" {
		fail("invalid", "要完整的 session_id（UUID）")
		return nil, false
	}
	session, err := e.store.GetSession(sessionID)
	if err != nil {
		fail("internal", err.Error())
		return nil, false
	}
	if session == nil {
		fail("not_found", "会话不存在")
		return nil, false
	}
	return session, true
}

// sendAccepted：受理那一行 —— 与 202 的 `TurnAccepted` 同一件事（回复还在生成，先给 id）。
type sendAccepted struct {
	Event     string          `json:"event"`
	SessionID string          `json:"session_id"`
	MessageID string          `json:"message_id"`
	Backend   string          `json:"backend"`
	Phase     model.TurnPhase `json:"phase"`
}

// sendDone：跑完那一行 —— **库里那条回复**原样（正文 / usage / 耗时），或"没有落库"的实话。
//
// `usage` 不做 omitempty：`null` 是**有意义**的（这条渠道没报用量），别让它"看不见"。
type sendDone struct {
	Event      string          `json:"event"`
	SessionID  string          `json:"session_id"`
	MessageID  string          `json:"message_id"`
	Stored     bool            `json:"stored"`
	Stopped    bool            `json:"stopped"`
	TimedOut   bool            `json:"timed_out"`
	Role       string          `json:"role,omitempty"`
	Content    string          `json:"content"`
	Usage      json.RawMessage `json:"usage"`
	DurationMS *int64          `json:"duration_ms,omitempty"`
	ElapsedMS  int64           `json:"elapsed_ms"`
	Status     turn.Status     `json:"status"`
}

// send：**同步跑完一整轮** —— 直操模式里最重要的一条（验收「生成那一轮」靠它）。
//
// 为什么同步：HTTP 那边是 202 + 轮询（客户端要动画）；这边要的是"跑完了再说话"，
// 于是把那些轮询收进一个循环里，最后把**库里那条回复**原样打出来（含 usage 与耗时）。
//
// 两条硬要求都在这里落地：`accepted` 那行的 `message_id` 就是**受理那一刻**算好的回复 id，
// 与落库那条回复是同一个（整段拿到才 INSERT ⇒ 没落库时 `stored=false`，库里干干净净）。
//
// 跨进程：一次 send 在数据目录里留一枚 pid 锁（见 turnLock）—— 两个 send 同时跑同一个会话，
// 后到的那个拿到 **409**（与 server 的 `ErrBusy` 同一个错误码、同一句话）；`-debug stop`
// 也靠它找到对面那个进程。
func (e *debugEnv) send(args []string) int {
	if len(args) < 2 {
		return report(errf("invalid", "用法：-debug send <session_id> <内容>"))
	}
	sessionID, content := args[0], strings.Join(args[1:], " ")
	if strings.TrimSpace(content) == "" {
		return report(errf("invalid", "内容不能是空白"))
	}
	// **先抢锁再看会话**：抢不到就已经知道是 409 了，连库都不用碰
	// （这也正是 server 那边的顺序：闸门挡住的那一刻，一个字节都还没写）。
	lock, busy, err := acquireTurnLock(e.paths, sessionID)
	if err != nil {
		return report(err)
	}
	if busy {
		return report(errf("conflict", "这个会话还在生成中，等它跑完或先按停止"))
	}
	defer lock.release()
	session, ok := e.session(sessionID)
	if !ok {
		return 1
	}

	// 被按停的那条路：另一个进程的 `-debug stop` 给我们发信号，或本机 Ctrl-C ⇒ 当成本地按停
	// （**正常操作，不是失败**）。信号只**记一下**，真正的 Stop 交给下面那个循环 ——
	// 免得信号比 Accept 还早到（那会儿还没登记，Stop 会白按一下）。
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	stopRequests := make(chan struct{}, 1)
	go func() {
		for range signals {
			select {
			case stopRequests <- struct{}{}:
			default: // 已经有一个待处理的了，别排队
			}
		}
	}()

	started := time.Now()
	accepted, err := e.chat.Accept(*session, content)
	if err != nil {
		if errors.Is(err, turn.ErrBusy) {
			return report(errf("conflict", "这个会话还在生成中，等它跑完或先按停止"))
		}
		return report(err)
	}
	replyID := ""
	if accepted.Turn.MessageID != nil {
		replyID = *accepted.Turn.MessageID
	}
	emit(sendAccepted{
		Event: "accepted", SessionID: sessionID, MessageID: replyID,
		Backend: accepted.Backend, Phase: accepted.Turn.Phase,
	})

	// 等这一轮落地：**上限给得宽**（上游自己的总超时就有 5 分钟）—— 真卡住了，
	// 另一个进程的 `-debug stop`（或本机 Ctrl-C）随时能按停。
	status, stopped, timedOut := e.waitTurn(sessionID, stopRequests, 10*time.Minute)

	// 落没落库**只看库**（受理时那个 id 就是它）：整段拿到才 INSERT ⇒ 被停 / 失败都是"查无此条"。
	messages, err := e.store.ListMessages(sessionID)
	if err != nil {
		return report(err)
	}
	var reply *model.Message
	for index := range messages {
		if messages[index].ID == replyID {
			reply = &messages[index]
			break
		}
	}
	done := sendDone{
		Event: "done", SessionID: sessionID, MessageID: replyID,
		Stored: reply != nil, Stopped: stopped, TimedOut: timedOut,
		ElapsedMS: time.Since(started).Milliseconds(), Status: status,
	}
	if reply != nil {
		done.Role = string(reply.Role)
		done.Content = reply.Content
		done.Usage = reply.Usage
		done.DurationMS = reply.DurationMS
	}
	emit(done)
	if reply == nil {
		return 1 // 没跑出回复：错误体没有，但这一行自己说了原因（stopped / timed_out / status.error）
	}
	return 0
}

// waitTurn：等这一轮落地（登记表 busy ⇒ idle / error）—— 顺便把"要按停"的请求变成一次 stop。
//
// 顺序是刻意的：先看有没有按停请求，再看状态 —— 被停之后条目会被摘掉，状态立刻回 idle。
// `idle` 只可能是"回复已经 INSERT 完"或"被停 / 被顶掉"两种，两种都不必再等。
func (e *debugEnv) waitTurn(sessionID string, stopRequests <-chan struct{}, timeout time.Duration) (turn.Status, bool, bool) {
	deadline := time.Now().Add(timeout)
	stopped := false
	for {
		select {
		case <-stopRequests:
			if e.chat.Stop(sessionID) {
				stopped = true
			}
		default:
		}
		status := e.chat.Status(sessionID)
		if !status.Phase.IsBusy() {
			return status, stopped, false
		}
		if time.Now().After(deadline) {
			return status, stopped, true
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// outgoingLine：一条 `Outgoing` + 给人看的前几十字。
//
// `content` 内嵌进来的仍是**原样**的正文（这是验收装配的东西，不许打折扣）；
// `preview` 只是给终端一眼扫的，`content_chars` 是它的**真实**字符数（预览不当真）。
type outgoingLine struct {
	Index          int    `json:"index"`
	state.Outgoing        // role / content / source / message_id / summary_id / blocks 原样
	Preview        string `json:"preview"`
	ContentChars   int    `json:"content_chars"`
}

// outgoing：「下次真会发出去的东西」—— 与 `GET /sessions/{id}/outgoing` 同一条计算
// （标签已剔、状态已注入、按摘要收拢），逐条一行。
//
// **检查压缩效果靠 `source` / `summary_id` / `blocks`**，别去猜正文抬头。
func (e *debugEnv) outgoing(args []string) int {
	session, ok := e.session(argAt(args, 0))
	if !ok {
		return 1
	}
	messages, err := e.store.ListMessages(session.ID)
	if err != nil {
		return report(err)
	}
	summaries, err := e.store.ListSummaries(session.ID)
	if err != nil {
		return report(err)
	}
	prompt, source := e.chat.EffectiveSystemPrompt(*session)
	view := state.FromSources(session.ID, prompt, source, messages)
	for index, item := range state.BuildOutgoing(prompt, messages, summaries, view.Tables) {
		emit(outgoingLine{
			Index: index, Outgoing: item,
			Preview: preview(item.Content, 40), ContentChars: len([]rune(item.Content)),
		})
	}
	return 0
}

// preview：正文前 n 个**字符**（按字符切，别把中文切出残字）。
func preview(text string, n int) string {
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}
	return string(runes[:n]) + "…"
}

// externalTurn：别的**进程**正在这一轮上——本进程的登记表当然是 idle（状态是进程内的事实），
// 于是顺手把那条跨进程的线索也打出来，免得对着 idle 猜。
type externalTurn struct {
	Event     string `json:"event"`
	PID       int    `json:"pid"`
	SessionID string `json:"session_id"`
	StartedAt int64  `json:"started_at"`
}

// status：这一轮的 turn 状态（与 `GET /sessions/{id}/status` 同一个形状）。
func (e *debugEnv) status(args []string) int {
	sessionID := argAt(args, 0)
	if _, ok := e.session(sessionID); !ok {
		return 1
	}
	emit(e.chat.Status(sessionID))
	if info, live := readTurnLock(e.paths, sessionID); live {
		emit(externalTurn{Event: "external_turn", PID: info.PID, SessionID: info.SessionID, StartedAt: info.StartedAt})
	}
	return 0
}

// stop：按停 —— **幂等**（与 `POST /sessions/{id}/stop` 同一个语义：没在跑也回 200 的 `false`）。
//
// 不查会话是否存在：这个动作问的是"进程里有没有活在跑"，答案对不存在的会话同样是"没有"。
// 本进程里没在跑时，再往跨进程那条路上找（见 turnLock）—— 直操模式一个 op 一个进程，
// "另一个进程里 stop"要靠它。
func (e *debugEnv) stop(args []string) int {
	sessionID := argAt(args, 0)
	if strings.TrimSpace(sessionID) == "" {
		return report(errf("invalid", "用法：-debug stop <session_id>"))
	}
	stopped := e.chat.Stop(sessionID)
	if !stopped {
		remote, err := stopRemoteTurn(e.paths, sessionID)
		if err != nil {
			return report(err)
		}
		stopped = remote
	}
	emit(map[string]bool{"stopped": stopped})
	return 0
}

// copyResult：复制的结果 —— 新会话 + 它带过来几条消息（新 id 与条数是这次要看的两样）。
type copyResult struct {
	Session  model.Session `json:"session"`
	Messages int           `json:"messages"`
}

// copySession：复制会话（线性会话里的"分岔"）—— 与 `POST /sessions/{id}/copy` 同一条路。
func (e *debugEnv) copySession(args []string) int {
	sessionID := argAt(args, 0)
	if strings.TrimSpace(sessionID) == "" {
		return report(errf("invalid", "用法：-debug copy <session_id>"))
	}
	copied, err := e.store.CopySession(sessionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return report(errf("not_found", "会话不存在"))
		}
		return report(err)
	}
	messages, err := e.store.ListMessages(copied.ID)
	if err != nil {
		return report(err)
	}
	emit(copyResult{Session: copied, Messages: len(messages)})
	return 0
}

// stateOf：世界状态 —— 与 `GET /sessions/{id}/state` 同一条计算（**每次现算**，没有派生表）。
func (e *debugEnv) stateOf(args []string) int {
	session, ok := e.session(argAt(args, 0))
	if !ok {
		return 1
	}
	messages, err := e.store.ListMessages(session.ID)
	if err != nil {
		return report(err)
	}
	prompt, source := e.chat.EffectiveSystemPrompt(*session)
	emit(state.FromSources(session.ID, prompt, source, messages))
	return 0
}

// deletionPreview：删除预览 —— **只算不动**（与 `GET .../deletion-preview` 同一段计算）。
func (e *debugEnv) deletionPreview(args []string) int {
	if len(args) < 2 {
		return report(errf("invalid", "用法：-debug delete-preview <session_id> <message_id>"))
	}
	plan, err := e.store.DeletionPlan(args[0], args[1])
	if err != nil {
		return report(messageError(err))
	}
	emit(plan)
	return 0
}

// deleteMessage：执行删除 —— **先预览再删**（末条核对 id 就是那次预览给的）。
//
// 核对不符 ⇒ **409**（与 HTTP 那条路同一个错误码）：预览之后、执行之前有人往尾巴上追加 ⇒
// 照旧计划删会**多删**且静默。回的就是刚执行的那份计划（与预览同一个形状）。
func (e *debugEnv) deleteMessage(args []string) int {
	if len(args) < 3 {
		return report(errf("invalid", "用法：-debug delete <session_id> <message_id> <last_deleted_message_id>"))
	}
	sessionID, messageID, lastDeleted := args[0], args[1], args[2]
	plan, err := e.store.DeletionPlan(sessionID, messageID)
	if err != nil {
		return report(messageError(err))
	}
	if lastDeleted != plan.LastDeletedMessageID {
		return report(errf("conflict", "要删的这一段变了（会话末尾已经不是预览时的那条）：请重新取一次删除预览"))
	}
	if err := e.store.ApplyDeletion(plan); err != nil {
		return report(err)
	}
	emit(plan)
	return 0
}

// board：任务面板（正在跑的在最前；进程内的事实，不进库）。
func (e *debugEnv) board() int {
	emit(e.tasks.Board())
	return 0
}

// usageOf：某个渠道的**套餐余量** —— 只读 GET（不碰会话、不调模型、不发消息）。
//
// 不给参数 ⇒ 取 `providers.json` 里第一个 `opencode-go` 渠道；给了就按 id 找。
// 打出来的是**归一化 + 脱敏**的形状（plan / endpoint / 三个窗口），**没有 key**。
// 目前只有 OpenCode GO 有这接口（别的 kind 没有 ⇒ 报"没有这个渠道"，不瞎猜别家的路径）。
//
// 找不到渠道时，错误信息要**说清是哪一种找不到**，并把**真读的配置目录**报出来 ——
// "没有渠道"这句话本身会把人引错地方（见 usageChannel 的注释）。
func (e *debugEnv) usageOf(args []string) int {
	_, _, providerConfig, err := config.Load(e.paths)
	if err != nil {
		return report(errf("internal", "读配置失败：%s", err.Error()))
	}
	channel, err := usageChannel(providerConfig, argAt(args, 0))
	if err != nil {
		return report(errf("invalid", "%s（用法：-debug usage [provider_id]；配置目录：%s）", err.Error(), e.paths.ConfigDir))
	}
	usage, err := providers.OpencodeUsageAt(context.Background(), channel.BaseURL, channel.APIKey)
	if err != nil {
		return fail("upstream", err.Error())
	}
	emit(usage)
	return 0
}

// usageChannel：挑这次要问的渠道 —— 只在 `opencode-go` 里找（用量接口目前只它有）。
// 走 `FromConfig` + `ApplyPreset`（与真发那条路同一套转换，别手搓）。
//
// 三种"没有"要分得开（都真发生过）：
//
//	① 一个渠道都没读到 —— 多半是**配置目录没给对**（`-config` 默认是 `<data>/config`，
//	   **`-data` 不会连带改它**；而 `go -C core run .` 的 cwd 是 `core/`，相对路径会错位）；
//	② 有渠道但没这个 id；
//	③ 有这个 id，但它不是 `opencode-go`（那就没有用量接口可问）。
func usageChannel(configs config.ProvidersConfig, id string) (providers.Provider, error) {
	if len(configs.Providers) == 0 {
		return providers.Provider{}, errors.New("一个渠道都没读到：检查 -config 指向的目录里有没有 providers.json")
	}
	if id != "" {
		channel, found := configs.Get(id)
		if !found {
			return providers.Provider{}, fmt.Errorf("providers.json 里没有 id=%s 这个渠道", id)
		}
		applied := providers.ApplyPreset(providers.FromConfig(channel))
		if applied.Kind != providers.KindOpenCodeGo {
			return providers.Provider{}, fmt.Errorf("渠道 %s 的 kind 是 %q，不是 opencode-go（用量接口目前只 OpenCode GO 有）", id, applied.Kind)
		}
		return applied, nil
	}
	for _, channel := range configs.Providers {
		if applied := providers.ApplyPreset(providers.FromConfig(channel)); applied.Kind == providers.KindOpenCodeGo {
			return applied, nil
		}
	}
	return providers.Provider{}, errors.New("providers.json 里的渠道没有一个是 opencode-go（不给 id 时就用第一个这样的渠道）")
}

// compactLine：一次压缩的结果 —— "区间 / 块数 / 摘要前几十字 / 写了没 / prompt_version"。
type compactLine struct {
	Event          string          `json:"event"`
	SessionID      string          `json:"session_id"`
	BeginMessageID string          `json:"begin_message_id"`
	EndMessageID   string          `json:"end_message_id"`
	Blocks         int             `json:"blocks"`
	Messages       int             `json:"messages"`
	SummaryID      string          `json:"summary_id"`
	PromptVersion  int64           `json:"prompt_version"`
	Preview        string          `json:"preview"`
	TextChars      int             `json:"text_chars"`
	Provider       string          `json:"provider"`
	Model          string          `json:"model"`
	Usage          json.RawMessage `json:"usage"`
}

// compactSession：压缩 —— **同步跑完**（校验 → 调上游 → 落库 → 收尾全在这一趟里）。
//
// 两种给法二选一：`--blocks N` 或 `--begin <id> --end <id>`；都不给 ⇒ 用 `config.json` 的
// `chat.compact_blocks`。失败就照实回错误体（**带 code**）—— 会话一个字节都不动。
func (e *debugEnv) compactSession(args []string) int {
	if len(args) == 0 {
		return report(errf("invalid", "用法：-debug compact <session_id> [--blocks N | --begin <id> --end <id>]"))
	}
	session, ok := e.session(args[0])
	if !ok {
		return 1
	}
	values, err := parseNamedFlags(args[1:], "blocks", "begin", "end")
	if err != nil {
		return report(err)
	}
	request := compact.Request{Begin: values["begin"], End: values["end"]}
	if raw, given := values["blocks"]; given {
		blocks, err := strconv.Atoi(raw)
		if err != nil {
			return report(errf("invalid", "--blocks 要是整数：%s", raw))
		}
		request.Blocks = blocks
	}
	result, err := e.compact.Compact(context.Background(), *session, request)
	if err != nil {
		return report(compactError(err))
	}
	emit(compactLine{
		Event: "compact", SessionID: result.SessionID,
		BeginMessageID: result.BeginMessageID, EndMessageID: result.EndMessageID,
		Blocks: result.Blocks, Messages: result.Messages, SummaryID: result.SummaryID,
		PromptVersion: result.PromptVersion,
		Preview:       preview(result.Text, 40), TextChars: len([]rune(result.Text)),
		Provider: result.Provider, Model: result.Model, Usage: result.Usage,
	})
	return 0
}

// titleOp：**手动**起一次标题（`title.Service.Generate`）—— 不管标题现有没有，直接起一次。
//
// 手动就是手动：产物**由这里落库**（`sessions.title`，一次 UPDATE，**不带"还空着"的条件** ——
// 人点了按钮就是要换名）。自动那条路（`Auto`）才带条件（见 `DEFINE.md`「Title 什么时候起」）。
func (e *debugEnv) titleOp(args []string) int {
	session, ok := e.session(argAt(args, 0))
	if !ok {
		return 1
	}
	result, err := e.titles.Generate(context.Background(), *session)
	if err != nil {
		return report(titleError(err))
	}
	if err := e.store.UpdateTitle(result.SessionID, result.Title); err != nil {
		return report(err)
	}
	emit(result)
	return 0
}

// titleError：能力层的错误映射 —— 与 `compactError` 一个口径（client 按 code 分支）。
func titleError(err error) error {
	var coded *title.Error
	if errors.As(err, &coded) {
		return errf(coded.Code, "%s", coded.Message)
	}
	return err
}

// abilityLine：`-debug abilities` 的一行（一个能力一行：这份人格在它上面怎么配）。
type abilityLine struct {
	Event     string `json:"event"`
	SessionID string `json:"session_id"`
	AgentID   string `json:"agent_id"`
	AgentName string `json:"agent_name,omitempty"`
	Ability   string `json:"ability"`
	Enabled   bool   `json:"enabled"`
	// Provider / Model：**覆盖**（空 = 用会话自己的渠道 / 模型）。
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// abilitiesOf：这个会话的 agent 在三个能力上的解析结果（`abilities.Resolve` 那一处的原话）。
//
// 三个能力都**照实**打（含 `judge` —— 它的流程还没写，但它是能力，开关认得它）。
func (e *debugEnv) abilitiesOf(args []string) int {
	session, ok := e.session(argAt(args, 0))
	if !ok {
		return 1
	}
	_, agents, _, err := config.Load(e.paths)
	if err != nil {
		return report(errf("internal", "读配置失败：%s", err.Error()))
	}
	agent, _ := agents.Resolve(session.AgentID)
	for _, id := range abilities.IDs() {
		setting := abilities.Resolve(agent, id)
		emit(abilityLine{
			Event: "ability", SessionID: session.ID,
			AgentID: session.AgentID, AgentName: agent.Name,
			Ability: id, Enabled: setting.Enabled,
			Provider: setting.Provider, Model: setting.Model,
		})
	}
	return 0
}

// refreshLine：`-debug refresh-models` 的一行 —— 一个渠道这一趟的结果。
//
// 这是"启动即刷"的**主要验收工具**（用户 2026-09-30 定）：`models` 说清拉了几个、
// `skipped` 说清为什么没刷、`error` 说清为什么失败。`-debug` 的输出永远不带色、每行一条 JSON。
type refreshLine struct {
	Event string `json:"event"`
	registry.Outcome
}

// refreshModelsOp：**同步**把每个渠道刷一遍 —— 走的是启动那条路**同一段编排**（`refreshModels`：
// 串行、逐渠道挂号 Task、跳过规则一份）。给了 provider_id 就只刷那一个。
//
// 有失败 ⇒ **退出码 1**（但每个渠道的结果都照打：一个失败不影响别的，这点与启动那条路一致）。
func (e *debugEnv) refreshModelsOp(args []string) int {
	if len(args) > 1 {
		return report(errf("invalid", "用法：-debug refresh-models [provider_id]"))
	}
	_, _, providerConfig, err := config.Load(e.paths)
	if err != nil {
		return report(errf("internal", "读配置失败：%s", err.Error()))
	}
	list := providerConfig.Providers
	if id := argAt(args, 0); id != "" {
		provider, found := providerConfig.Get(id)
		if !found {
			return report(errf("not_found", "providers.json 里没有 id=%s 这个渠道", id))
		}
		list = []config.Provider{provider}
	} else if len(list) == 0 {
		// 一个渠道都没读到 ⇒ 空跑一趟最容易让人以为"功能没生效"，说清多半是**配置目录没给对**
		return report(errf("not_found", "一个渠道都没读到：检查配置目录 %s 里的 providers.json", e.paths.ConfigDir))
	}
	failed := 0
	for _, outcome := range refreshModels(e.store, e.tasks, e.cfg, list) {
		if outcome.Error != "" {
			failed++
		}
		emit(refreshLine{Event: "refresh", Outcome: outcome})
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// compactError：压缩那一路的错误 → 直操模式的错误体（`code` 就是压缩给的那个）。
func compactError(err error) error {
	var coded *compact.Error
	if errors.As(err, &coded) {
		return errf(coded.ErrorCode(), "%s", coded.Message)
	}
	return err
}

// ── 参数 ─────────────────────────────────────────────────────────────────

// parseNamedFlags：`--名字 值` 与 `--名字=值` 两种写法都收；不认识的名字 / 缺值 ⇒ **报错**
// （不静默忽略 —— 否则 `--titel X` 会变成"标题没写"，最难查的那类）。
func parseNamedFlags(args []string, allowed ...string) (map[string]string, error) {
	known := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		known[name] = true
	}
	values := map[string]string{}
	for index := 0; index < len(args); index++ {
		name, value, hasValue := strings.Cut(args[index], "=")
		if !strings.HasPrefix(name, "--") {
			return nil, errf("invalid", "不认识的参数：%s（只收 %s）", args[index], "--"+strings.Join(allowed, " / --"))
		}
		name = strings.TrimPrefix(name, "--")
		if !known[name] {
			return nil, errf("invalid", "不认识的参数：--%s", name)
		}
		if !hasValue {
			if index+1 >= len(args) {
				return nil, errf("invalid", "--%s 缺一个值", name)
			}
			index++
			value = args[index]
		}
		values[name] = value
	}
	return values, nil
}

// ── 跨进程的并发闸门 / 按停（**只服务直操模式**）────────────────────────────

// 为什么不直接读 turn 登记表：它是**进程内**的（刻意如此，见 `DEFINE.md`：重启后"没有生成在跑"
// 是诚实的事实）。而直操模式是"一个 op 一个进程" ⇒ 两个 `-debug send` 是两个互不知情的进程，
// "并发再发 ⇒ 其中一个 409"与"另一个进程里 stop"都没法只靠内存里那张表做到。
//
// 于是：一次 send 在数据目录里留一枚 per-session 的锁文件（pid 写在里面）。
//   - 已经有一枚**活的** ⇒ 409（与 server 的 `ErrBusy` 同一个错误码、同一句话）；
//   - `-debug stop` 找到活的 pid ⇒ 发 SIGTERM，那边的 send 当成本地按停；
//   - pid 已经死了（被 kill -9）⇒ 当成残留，清掉重来。
//
// 它与 server / TUI **无关**：那两边的权威仍然是进程内的登记表（它们从来不看这个文件）。
// 文件放 `<data>/debug-turns/`（`data/` 永不入库），send 退出时清掉。
//
// 已知的边界（写在这儿免得日后误判）：它只管直操模式自己的两个进程，不认 server 里正在跑的那一轮。

// turnLock：一次直操 send 持有的那枚锁。
type turnLock struct{ path string }

// lockInfo：锁文件里写的东西 —— 够 `stop` 找到人、够 `status` 说清"谁在跑"就行。
type lockInfo struct {
	PID       int    `json:"pid"`
	SessionID string `json:"session_id"`
	StartedAt int64  `json:"started_at"`
}

// turnLockPath：一枚锁一个文件（session_id 是 UUID ⇒ 直接当文件名，安全）。
func turnLockPath(paths config.Paths, sessionID string) string {
	return filepath.Join(paths.DataDir, "debug-turns", sessionID+".lock")
}

// acquireTurnLock：拿到这枚锁；`busy=true` ⇒ 已经有一个**活着的**进程在跑这个会话。
func acquireTurnLock(paths config.Paths, sessionID string) (lock *turnLock, busy bool, err error) {
	path := turnLockPath(paths, sessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, false, err
	}
	// 先把内容写进一个临时文件，再 `os.Link` 到锁的路径上 —— **link 不覆盖已存在的目标**
	// （目标在，就回 EEXIST）。于是"锁文件出现"与"里面已经有 pid"是**同一件事**。
	// 别用 `O_CREATE|O_EXCL` 之后再 Write：那中间有一个"文件在、内容还没有"的窗口，
	// 后到的进程会读到空文件、把它当残留清掉 ⇒ 两个进程同时跑（实测踩过）。
	body, _ := json.Marshal(lockInfo{PID: os.Getpid(), SessionID: sessionID, StartedAt: time.Now().UnixMilli()})
	tmp := path + "." + fmt.Sprint(os.Getpid()) + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return nil, false, err
	}
	defer func() { _ = os.Remove(tmp) }()
	for range 3 {
		err := os.Link(tmp, path)
		if err == nil {
			return &turnLock{path: path}, false, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, false, err
		}
		if _, live := readTurnLock(paths, sessionID); live {
			return nil, true, nil
		}
		_ = os.Remove(path) // 残留（上一个进程被 kill -9）：清掉重来
	}
	return nil, false, errf("internal", "拿不到直操模式的锁（连着撞了三次）：%s", path)
}

// release：退出时清掉自己的那枚（best-effort：清不掉也只是留下一个"pid 已死"的残留）。
func (l *turnLock) release() {
	if l == nil {
		return
	}
	_ = os.Remove(l.path)
}

// readTurnLock：读这枚锁；`live=false` ⇒ 没有，或者写它的进程已经死了（残留不当真）。
func readTurnLock(paths config.Paths, sessionID string) (lockInfo, bool) {
	body, err := os.ReadFile(turnLockPath(paths, sessionID))
	if err != nil {
		return lockInfo{}, false
	}
	var info lockInfo
	if json.Unmarshal(body, &info) != nil || info.PID <= 0 {
		return lockInfo{}, false
	}
	if !processAlive(info.PID) {
		return lockInfo{}, false
	}
	return info, true
}

// processAlive：信号 0 = "只问在不在，不打扰它"（Unix 的经典做法，标准库够用）。
func processAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

// stopRemoteTurn：把**另一个进程**正在跑的直操 send 按停。
//
// 步骤：读锁 → 向那个 pid 发 SIGTERM → **等它自己收尾**（它退出时会清掉锁）。
// 等锁消失很关键：验收是"停完立刻数库里有没有回复"，不等就可能数在一个还在收尾的进程上。
func stopRemoteTurn(paths config.Paths, sessionID string) (bool, error) {
	info, live := readTurnLock(paths, sessionID)
	if !live {
		return false, nil
	}
	process, err := os.FindProcess(info.PID)
	if err != nil {
		return false, nil
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		return false, nil // 刚好在这一刻退出了：那就是"没在跑"，回 false 是诚实的
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, still := readTurnLock(paths, sessionID); !still {
			return true, nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true, nil // 信号已送到：那边会按停（它也许还在收尾，但不会再落库了）
}
