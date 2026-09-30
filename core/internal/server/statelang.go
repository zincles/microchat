package server

import (
	"net/http"

	"microchat/internal/statelang"
)

// StatelangReq：`POST /api/v1/statelang` 的请求体 —— 就是一段**裸文本**（消息正文、提示词、随便什么）。
type StatelangReq struct {
	Text *string `json:"text"`
}

// StatelangView：响应。**解析**与**计算**分开给：
//
//   - `statements` = 解析结果：读出来的操作，按出现顺序，删除还没生效；
//   - `tables`     = 计算结果：折叠之后的值（删除生效、后写覆盖前写、空表消失），
//     不写表名的块进 `global` 表，而 **`global` 恒在**（空也回 `{}`）；
//   - `diagnostics` = 坏行与提醒（行号是**整段文本**里的行号）。
//
// 字段顺序是**契约**：Go 与 Rust 两版逐字节一致（对账脚本按它比）。
type StatelangView struct {
	Tables      map[string]map[string]string `json:"tables"`
	Statements  []statelang.Statement        `json:"statements"`
	Diagnostics []statelang.Diagnostic       `json:"diagnostics"`
}

// statelangParse：把一段文本里的所有 `<state>` 块解析出来，并算成 `{表: {键: 值}}`。
//
// 给外部工具用的（curl / 脚本 / 将来的前端）：不用起对话、不落库、纯函数式。
func (s *Server) statelangParse(w http.ResponseWriter, r *http.Request) {
	var req StatelangReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Text == nil {
		missingField(w, "text")
		return
	}
	document := statelang.Scan(*req.Text)
	view := StatelangView{
		Tables:      statelang.Tables(*req.Text),
		Statements:  document.Statements,
		Diagnostics: document.Diagnostics,
	}
	if view.Statements == nil {
		view.Statements = []statelang.Statement{}
	}
	if view.Diagnostics == nil {
		view.Diagnostics = []statelang.Diagnostic{}
	}
	writeJSON(w, http.StatusOK, view)
}
