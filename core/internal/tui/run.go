// 启动 TUI（被主程序调用；它自己不是 main —— 整个程序只有一个 main）。
package tui

import (
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/term"
)

// Run：进入全屏 TUI，直到用户退出。`client` 指向哪个后端由调用方决定
// （本机模式指向自己起的服务，远端模式指向别处）。
func Run(client *Client) error {
	m := initialModel(client)
	m.style, m.ascii = detectTerminal() // 生产路径上**唯一**一处读终端能力（见「降级两档」）
	_, err := tea.NewProgram(m).Run()
	return err
}

// detectTerminal：终端能力的**唯一**判据（降级两档）—— 颜色与字符集一次读清。
//
// 这里之所以能读环境变量而不破坏 `View()` 的纯粹性：判断只发生一次、结果存进 model
// ⇒ 同一份 model 仍然渲染出同一个字符串（测试用零值 styler + `ascii: false`，见 model.style）。
//
// `-debug` 不用管：它打印 JSON、根本不过 TUI ⇒ 天然不带色（管道/断言读它）。
func detectTerminal() (styler, bool) {
	stdoutIsTTY := term.IsTerminal(int(os.Stdout.Fd()))
	return styler{enabled: colorEnabled(os.Getenv, stdoutIsTTY)}, asciiEnabled(os.Getenv, stdoutIsTTY)
}

// colorEnabled：降级三档 —— `NO_COLOR`（业界约定，只看有没有设）/ `TERM=dumb` / 非 TTY
// ⇒ 一律纯文本。
func colorEnabled(env func(string) string, stdoutIsTTY bool) bool {
	if env("NO_COLOR") != "" {
		return false
	}
	if strings.EqualFold(env("TERM"), "dumb") {
		return false
	}
	return stdoutIsTTY
}

// asciiEnabled：**字符集**降级 —— `TERM=dumb` / 非 TTY ⇒ 界面骨架退回 ASCII（满线 `-`、
// 位次标记 `< 2/3 >` 这些"这台终端不认识"的字符）。
//
// `NO_COLOR` **不在**这里：它说的是"别给我上色"，不是"我不认 Unicode" ⇒ 只关颜色（见 colorEnabled）。
func asciiEnabled(env func(string) string, stdoutIsTTY bool) bool {
	if strings.EqualFold(env("TERM"), "dumb") {
		return true
	}
	return !stdoutIsTTY
}
