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
	"microchat/internal/server"
	"microchat/internal/store"
	"microchat/internal/task"
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
	if _, agents, _, err := config.Load(paths); err != nil {
		fatal("读 agents.json 失败: %v", err)
	} else if err := abilities.Validate(agents); err != nil {
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
		if err := http.Serve(listener, server.New(st, cfg, paths, chatService, compactService).Handler()); err != nil {
			log.Printf("服务退出: %v", err)
		}
	}()

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
