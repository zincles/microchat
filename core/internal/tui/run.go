// 启动 TUI（被主程序调用；它自己不是 main —— 整个程序只有一个 main）。
package tui

import tea "charm.land/bubbletea/v2"

// Run：进入全屏 TUI，直到用户退出。`client` 指向哪个后端由调用方决定
// （本机模式指向自己起的服务，远端模式指向别处）。
func Run(client *Client) error {
	_, err := tea.NewProgram(initialModel(client)).Run()
	return err
}
