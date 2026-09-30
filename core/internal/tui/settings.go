package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// 界面偏好（**只管界面**）：住 `~/.config/microchat/tui.json`。
//
// 三条边界（见 AGENTS.md「界面该长什么样」）：
//   - **不进**后端的 `config/`（那是服务端配置，程序整体重写）✗；
//   - **不进** `data/`（那是存档 + 配置的家，搬 `data/` 就该搬走一切）✗；
//   - **不动** Godot 那份 `frontend.json`（那是它的前端设置）✗ —— 各写各的文件。
type uiState struct {
	// ShowSystemPrompt：消息区顶部要不要摆"系统提示词"那条（调试用）。**默认开**。
	//
	// ⚠ 类型必须是 `*bool`（本项目踩过的坑）：`bool` 的零值会撒谎 —— 文件里**没写**这一格时
	// 会被读成 `false`（= 关掉），与"默认开"正好反着。`nil` = 没写 ⇒ 用默认。
	ShowSystemPrompt *bool `json:"show_system_prompt"`
}

// defaultUIStatePath：界面偏好的落点（`~/.config/microchat/tui.json`）。
//
// 拿不到家目录就回空串 ⇒ 这一次会话照常用，只是不落盘（偏好坏掉不该挡住 TUI）。
func defaultUIStatePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "microchat", "tui.json")
}

// loadShowSystemPrompt：读"显示系统提示词"这一格。
//
// **读不到 / 读坏 / 没写这一格 ⇒ 默认开**（与 `config/` 一样：文件可缺失，缺失就用代码里的默认）。
func loadShowSystemPrompt(path string) bool {
	if path == "" {
		return true
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	var state uiState
	if err := json.Unmarshal(body, &state); err != nil {
		return true
	}
	if state.ShowSystemPrompt == nil {
		return true
	}
	return *state.ShowSystemPrompt
}

// saveShowSystemPrompt：整体重写这份偏好（目录缺了就建）。空路径 ⇒ 不落盘（无事发生）。
func saveShowSystemPrompt(path string, show bool) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(uiState{ShowSystemPrompt: &show}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(body, '\n'), 0o644)
}
