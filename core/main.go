// microchat 后端（Go 版）—— 主体代码。老的 Rust 版在 deprecated/ 里当参照，别再加功能。
//
// 迁移纪律（写在这里，免得忘）：
//  1. **表结构照搬** `deprecated/src/store.rs` 的 MIGRATIONS（一字不改，见 internal/store/migrations.go）；
//  2. **接口照搬** AGENTS.md 那张表 —— `python3 deprecated/tools/api-audit.py` 全绿 = 接口层次转对了；
//  3. **行为照搬** 171 条测试 —— `go test ./...` 全绿 = 行为层次转对了；
//  4. **只许对账**：`python3 deprecated/tools/go-parity.py` 把两版的响应逐字节比 —— 说法不同就是没转对；
//  5. Rust 版先原地留着当参照，Go 版跑绿了再谈删。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"

	"golang.org/x/term"

	"microchat/internal/abilities"
	"microchat/internal/chat"
	"microchat/internal/compact"
	"microchat/internal/config"
	"microchat/internal/model"
	"microchat/internal/registry"
	"microchat/internal/reroll"
	"microchat/internal/server"
	"microchat/internal/store"
	"microchat/internal/task"
	"microchat/internal/title"
	"microchat/internal/tui"
	"microchat/internal/turn"
)

func main() {
	// 与旧版同名：目录可用环境变量覆盖（MICROCHAT_CONFIG_DIR / MICROCHAT_DATA_DIR）
	paths := config.LoadPaths()
	flag.StringVar(&paths.ConfigDir, "config", paths.ConfigDir, "配置目录（含 agents.json 等）")
	flag.StringVar(&paths.DataDir, "data", paths.DataDir, "数据目录（含 microchat.db）")
	addr := flag.String("addr", "", "监听地址（留空 = 用 config.json 的 server.host:port）")
	// 直操模式：**同进程**调 internal/*（不起服务、不发 HTTP、不开 TUI），打印结果就退出。
	// 用 bool 而不是"`-debug` 吃掉下一个参数"：Go 的 flag 在第一个非 flag 参数处停下，
	// 于是 `-debug new --title X` 里的 `--title X` 会原样落进 flag.Args()，由 debug.go 自己解析。
	debugMode := flag.Bool("debug", false, "直操模式：-debug <op> [参数…]（同进程调 internal/*，不起服务）")
	flag.Parse()

	cfg, err := config.LoadConfig(paths)
	if err != nil {
		fatal("读配置失败: %v", err)
	}
	// `agents.json` 里的能力开关：未知的能力 id **在启动时就报清楚** ——
	// 写了不生效（比如把 `abilities` 拼错、或写了个不存在的 id）是最难查的一类问题。
	// 顺带把 `providers.json` 读进来：**一启动就要对每个渠道问一次模型列表**（见下面那段）。
	_, agents, providerConfig, err := config.Load(paths)
	if err != nil {
		fatal("读 agents.json 失败: %v", err)
	}
	if err := abilities.Validate(agents); err != nil {
		fatal("agents.json 的能力开关不合法: %v", err)
	}
	// 给了 -debug 就**只干这一件事**：在监听端口之前就退出（也别进 TUI）。
	if *debugMode {
		os.Exit(runDebug(paths, cfg, flag.Args()))
	}
	if *addr == "" {
		*addr = fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	}

	// **一个二进制，一副面孔**（用户 2026-09-29 定）：起来就**既对外服务、又在终端里能直接聊**。
	// TUI 与别的客户端走**同一条 HTTP 契约**（回环到自己的端口）⇒ 接口天天被主力界面跑着，不会烂。
	// 唯一自动处理的情形：**没有 TTY**（丢进 tmux 重定向到日志文件那种）⇒ 不进 TUI，只服务。
	interactive := interactive()

	st, err := store.Open(paths.DataDir + "/microchat.db")
	if err != nil {
		fatal("打开数据库失败: %v", err)
	}
	defer st.Close()

	version, _ := st.UserVersion()

	// 三份状态各管一段：`task` = 身份与生死，`turn` = 流细节，`chat` = 一轮怎么跑。
	// 都是**进程内**的（不进库）：重启后"没有生成在跑"是诚实的事实。
	turns := turn.NewRegistry()
	tasks := task.NewRegistry()
	chatService := chat.New(st, paths, turns, tasks)
	compactService := compact.New(st, paths, turns, tasks)
	// 重摇：**把"最后那条 assistant 回复"重摇几版、挑一版定下来**（候选只在内存里）。
	// 装配借的就是 `chat`（`RerollMessages`）—— 重摇与一轮对话共用同一份拼装，没有第二份。
	rerollService := reroll.New(st, paths, turns, tasks, chatService)
	// 摘要重摇借 compact 的"只生成、不落库"半程 —— 摘要不许绕过 compact 自己拼请求。
	// 适配器把 `compact.Generated` 翻成 `reroll.GeneratedSummary`（字段同名逐个搬）。
	rerollService.Summaries = summaryRoller{compact: compactService}
	// 反向也点一下：**新消息一到 ⇒ 候选全清**（候选只属于"当前那条尾巴"）。
	chatService.Candidates = rerollService
	// 起标题挂在一轮生成上（拿到回复之后自动一次）—— 与压缩同一条口径：能力的事归能力
	chatService.Titles = title.New(st, paths, tasks)

	// **先真听上端口，再切日志到文件**：端口被占这类启动失败必须留在**终端**上看得见 ——
	// 不然 TUI 模式下日志去了 data/microchat.log，终端里只剩一句 "exit status 1"（真发生过）。
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		fatal("监听 %s 失败: %v（已经有一个在跑？用 -addr 换个端口，或先把它停了）", *addr, err)
	}
	if interactive {
		// 起来了才把日志挪进文件：别糊在 TUI 上
		if logFile, fileErr := os.OpenFile(paths.DataDir+"/microchat.log",
			os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); fileErr == nil {
			log.SetOutput(logFile)
			loggingToFile = true // 从这一刻起，致命错要**额外**写一份到 stderr（否则终端什么也看不到）
			defer logFile.Close()
		}
	}
	log.Printf("microchat 起在 http://%s（数据 %s，配置 %s，user_version=%d，TUI=%v）",
		*addr, paths.DataDir, paths.ConfigDir, version, interactive)
	go func() {
		if err := http.Serve(listener, server.New(st, cfg, paths, chatService, compactService, rerollService).Handler()); err != nil {
			log.Printf("服务退出: %v", err)
		}
	}()

	// **一启动就去问每个渠道有哪些模型**（用户 2026-09-30 定）：会话里绑的是模型的**字符串 id**
	// （`provider` + `upstream_id`，不是 UUID）⇒ 列表随时可以重建，每次打开软件重建一遍最省事。
	// 三条口径：整批放**后台**（绝不阻塞启动与界面）、逐渠道各自挂号 `refresh_models` 的 Task、
	// **失败只 log**（不弹错、不退出）。关掉它：`config.json` 的 `server.refresh_models_on_start`（默认 true）。
	if cfg.RefreshModelsOnStart() {
		go refreshModelsOnStart(st, tasks, cfg, providerConfig.Providers)
	}

	if !interactive {
		select {} // 没有终端：就是一台服务，跑到被杀
	}
	// 有终端：进 TUI（连自己起的这个服务）
	if err := tui.Run(tui.NewClient(loopbackURL(*addr, cfg.Server.Port), tokenOf(cfg))); err != nil {
		fatal("TUI 出错: %v", err)
	}
	log.Printf("TUI 退出，服务继续在 %s 上跑（Ctrl-C 停）", *addr)
	select {}
}

// refreshModelsOnStart：**启动时的自动刷新**（跑在后台 goroutine 里；见 main 里那段注释）。
//
// 这里只"记账"：每个渠道的结果落一行日志 —— **失败只 log**（不弹错、不退出、不挡界面）。
// 真正的编排（跳过谁 / 顺序 / 失败不响）在 `refreshModels` 与 `registry.RefreshAll`。
func refreshModelsOnStart(st *store.Store, tasks *task.Registry, cfg config.Config, list []config.Provider) {
	outcomes := refreshModels(st, tasks, cfg, list)
	if len(outcomes) == 0 {
		return // 一个渠道都没配：什么都不说
	}
	failed := 0
	for _, outcome := range outcomes {
		switch {
		case outcome.Error != "":
			failed++
			log.Printf("启动刷新模型：渠道 %s 失败（%s）", outcome.Provider, outcome.Error)
		case outcome.Skipped != "":
			log.Printf("启动刷新模型：跳过渠道 %s（%s）", outcome.Provider, outcome.Skipped)
		default:
			log.Printf("启动刷新模型：渠道 %s 拉到 %d 个模型（没见到的删了 %d 条）",
				outcome.Provider, outcome.Models, outcome.Removed)
		}
	}
	log.Printf("启动刷新模型：%d 个渠道，失败 %d 个", len(outcomes), failed)
}

// refreshModels：**启动与 `-debug refresh-models` 共用的那一段** —— 逐渠道（**串行**）刷一遍。
//
// 注入给 `registry.RefreshAll` 的就是"挂号 + 刷 + 收尾"这一下：每个渠道**各自一个
// `refresh_models` 的 Task**（后台活都要挂号），失败记在 Task 上、也记进 `Outcome.Error`。
// 编排（跳过谁、顺序、失败不影响别的）只有一份，在 registry 那边（那一份好单测）。
func refreshModels(st *store.Store, tasks *task.Registry, cfg config.Config, list []config.Provider) []registry.Outcome {
	applied := make([]config.Provider, 0, len(list))
	for _, provider := range list {
		applied = append(applied, cfg.WithIdentity(provider)) // 服务器级默认特征（与 HTTP 那条路同一处规则）
	}
	return registry.RefreshAll(applied, func(provider config.Provider) registry.Outcome {
		guard := tasks.Begin(task.KindRefreshModels, nil, "刷新模型 · "+provider.ID)
		defer guard.Interrupted() // Go 没有 Drop：兜底那一行必须在（忘了收就记成"中断"）
		outcome, err := registry.RefreshOne(provider, st)
		if err != nil {
			guard.Fail(err.Error())
			outcome.Error = err.Error()
			return outcome
		}
		guard.Succeed()
		return outcome
	})
}

// fatal：启动 / 运行中的致命错 —— **一律 stderr + 日志双写**。
//
// 为什么不能只用 log：TUI 模式下日志已经挪进 data/microchat.log ⇒ 终端里只剩一句
// "exit status 1"，看不到为什么（真发生过）。
func fatal(format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	if loggingToFile { // 日志已经去文件了 ⇒ 终端这边必须**另外**说一句
		fmt.Fprintln(os.Stderr, "microchat: "+message)
	}
	log.Print(message)
	os.Exit(1)
}

// loggingToFile：日志是否已被挪进 data/microchat.log（见 fatal 的注释）。
var loggingToFile bool

// interactive：stdin 与 stdout **都是真终端**才进 TUI。
//
// 别用 `ModeCharDevice` 判 ✗ —— `/dev/null` 也是字符设备，脚本 / 服务把 stdio 丢 DEVNULL
// 时会被误判成"有终端" ⇒ 跑去开 TUI、端口上永远没人听（真发生过：api-audit 的沙盒起不来）。
func interactive() bool {
	// stdin 不是终端就没法打字，进去也没用
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// loopbackURL：TUI 连的那个地址。`0.0.0.0`/`::` 这种"监听所有网卡"的要换成回环才能连。
func loopbackURL(addr string, port uint16) string {
	host, listenPort, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://127.0.0.1:" + fmt.Sprint(port)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, listenPort)
}

func tokenOf(cfg config.Config) string {
	if cfg.Server.AuthToken == nil {
		return ""
	}
	return *cfg.Server.AuthToken
}

// summaryRoller：摘要重摇的生成器适配器（`reroll.SummaryGenerator` 的唯一实现）——
// 把 `compact` 的"只生成、不落库"半程接给 reroll。形状刻意不同名：reroll 不认识 compact 的包。
type summaryRoller struct{ compact *compact.Service }

func (r summaryRoller) RegenerateSummary(ctx context.Context, session model.Session, summaryID string) (reroll.GeneratedSummary, error) {
	generated, err := r.compact.RegenerateSummary(ctx, session, summaryID)
	if err != nil {
		return reroll.GeneratedSummary{}, err
	}
	return reroll.GeneratedSummary{
		Text: generated.Text, Tokens: generated.Tokens, Provider: generated.Provider,
		Model: generated.Model, PromptVersion: generated.PromptVersion, Usage: generated.Usage,
	}, nil
}
