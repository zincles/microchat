package server

import (
	"net/http"
	"strconv"
	"strings"

	"microchat/internal/auth"
	"microchat/internal/model"
)

// 用户管理（S2，2026-10-10）：**admin**（= root 权限；今天只有 uid 0）的人事口 ——
// 看列表 / 加人 / 改名 / 改口令 / 删人。
//
// 三条规矩（DEFINE「用户与鉴权」）：
//   - **root（uid 0）不许删**；名字不许纯数字（那是 uid 的地盘）。
//   - **改口令**：admin 改谁都能改（`ver+1` ⇒ 那人旧票全废 = 踢人）；**自己改自己谁都行**。
//   - **删人对会话必须显式**（R6）：`?sessions=transfer`（会话转给 root）或 `purge`（连会话一起没）。

type createUserReq struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	users, err := s.store.ListUsers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "查用户失败")
		return
	}
	out := make([]map[string]any, 0, len(users))
	for i := range users {
		out = append(out, userViewFull(&users[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var req createUserReq
	if !decodeJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "invalid", "缺 name")
		return
	}
	if len([]rune(name)) > 32 {
		writeError(w, http.StatusBadRequest, "invalid", "名字太长（32 字以内）")
		return
	}
	if len(req.Password) < auth.MinPassword {
		writeError(w, http.StatusBadRequest, "invalid", "口令至少 "+strconv.Itoa(auth.MinPassword)+" 位")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "口令哈希失败")
		return
	}
	user, err := s.store.CreateUser(name, hash, false) // 新加的都不是 admin（root 权限暂不外发）
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, userViewFull(&user))
}

// updateUser：改名。admin 改谁都能改；**自己改自己也行**（名字是登录凭据之一，但不是特权）。
func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	target, ok := pathUserID(w, r)
	if !ok {
		return
	}
	id := identityOf(r.Context())
	if !id.Admin && !id.Sys && id.UserID != target {
		writeError(w, http.StatusForbidden, "forbidden", "只有管理员能改别人的名字")
		return
	}
	var req struct {
		Name *string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == nil {
		missingField(w, "name")
		return
	}
	name := strings.TrimSpace(*req.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "invalid", "名字不能是空的")
		return
	}
	if err := s.store.UpdateUserName(target, name); err != nil {
		writeStoreError(w, err)
		return
	}
	user, err := s.store.GetUserByID(target)
	if err != nil || user == nil {
		writeError(w, http.StatusNotFound, "not_found", "用户不存在")
		return
	}
	writeJSON(w, http.StatusOK, userViewFull(user))
}

// setUserPassword：改口令 —— admin 改谁都能改（= 踢人：`ver+1` 旧票全废）；**自己改自己谁都行**。
func (s *Server) setUserPassword(w http.ResponseWriter, r *http.Request) {
	target, ok := pathUserID(w, r)
	if !ok {
		return
	}
	id := identityOf(r.Context())
	if !id.Admin && !id.Sys && id.UserID != target {
		writeError(w, http.StatusForbidden, "forbidden", "只有管理员能改别人的口令")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Password) < auth.MinPassword {
		writeError(w, http.StatusBadRequest, "invalid", "口令至少 "+strconv.Itoa(auth.MinPassword)+" 位")
		return
	}
	targetUser, err := s.store.GetUserByID(target)
	if err != nil || targetUser == nil {
		writeError(w, http.StatusNotFound, "not_found", "用户不存在")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "口令哈希失败")
		return
	}
	if err := s.store.UpdateUserPassword(target, hash); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteUser：删人（admin）。root 不许删；**对会话必须显式**（`?sessions=transfer|purge`）。
func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	target, ok := pathUserID(w, r)
	if !ok {
		return
	}
	moved, err := s.store.DeleteUser(target, strings.TrimSpace(r.URL.Query().Get("sessions")))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions_affected": moved})
}

// pathUserID：路径里的 uid（非整数 / 负数 ⇒ 400）。
func pathUserID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := strings.TrimSpace(r.PathValue("user_id"))
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		writeError(w, http.StatusBadRequest, "invalid", "uid 要是非负整数")
		return 0, false
	}
	return n, true
}

// userViewFull：给管理员页看的一条（**绝不含口令哈希**；口令有没有是布尔）。
func userViewFull(u *model.User) map[string]any {
	return map[string]any{
		"id": u.ID, "name": u.Name, "is_admin": u.IsAdmin,
		"has_password": u.PassHash != "", "created_at": u.CreatedAt,
		"tg_user_id": u.TgUserID,
	}
}
