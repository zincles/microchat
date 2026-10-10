package server

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"microchat/internal/auth"
	"microchat/internal/model"
)

// 鉴权的三个开放口 + 两个日常口（DEFINE「用户与鉴权」）。
// **uid 0 = ROOT 恒在**（建库即种下、口令空着等 setup）；setup 只开一次，之后加人走 /users（S2）。

// authStatus：**开放口**（门板/探针要用：后端活着吗、root 初始化了没）。版本号照旧给。
func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	needs, err := s.store.RootNeedsSetup()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "查用户失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": "0.1.0", "needs_setup": needs})
}

type setupReq struct {
	Password string `json:"password"`
}

// authSetup：**给 root（uid 0）设口令** —— 唯一一次；已经设过 ⇒ 403（加人走 /users，S2）。
// 名字固定在种子里是 `root`（要改名字走将来的用户管理）；这里只设口令。
func (s *Server) authSetup(w http.ResponseWriter, r *http.Request) {
	var req setupReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Password) < auth.MinPassword {
		writeError(w, http.StatusBadRequest, "invalid", fmt.Sprintf("口令至少 %d 位", auth.MinPassword))
		return
	}
	needs, err := s.store.RootNeedsSetup()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "查用户失败")
		return
	}
	if !needs {
		writeError(w, http.StatusForbidden, "forbidden", "root 已经初始化过了（加人请在用户页里做）")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "口令哈希失败")
		return
	}
	if err := s.store.SetRootPassword(hash); err != nil {
		writeStoreError(w, err)
		return
	}
	root, err := s.store.GetUserByID(0)
	if err != nil || root == nil {
		writeError(w, http.StatusInternalServerError, "internal", "root 不见了（不该发生）")
		return
	}
	s.issueToken(w, root)
}

// authLogin：**用户名或 uid** + 口令 ⇒ JWT（30 天定长）。错就是 401（不区分"没这人"与"口令错"，
// 不给人猜）。`user` 字段**全数字 ⇒ 当 uid 查**（Linux 味；用户名不许是纯数字，创建时就拦着）。
func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	key := strings.TrimSpace(req.User)
	var user *model.User
	var err error
	if uid, perr := strconv.ParseInt(key, 10, 64); perr == nil {
		user, err = s.store.GetUserByID(uid)
	} else {
		user, err = s.store.GetUserByName(key)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "查用户失败")
		return
	}
	if user == nil || !auth.CheckPassword(user.PassHash, req.Password) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "用户名或口令不对")
		return
	}
	s.issueToken(w, user)
}

// authMe：我是谁（前端启动自检 / 用户视图用）。系统身份 = **uid 0 = root**（规矩：它恒在）+ sys 标记。
func (s *Server) authMe(w http.ResponseWriter, r *http.Request) {
	id := identityOf(r.Context())
	if id.Sys {
		root, err := s.store.GetUserByID(0)
		if err != nil || root == nil {
			writeError(w, http.StatusInternalServerError, "internal", "root 不见了（不该发生）")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": 0, "name": root.Name, "is_admin": true, "sys": true})
		return
	}
	user, err := s.store.GetUserByID(id.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "查用户失败")
		return
	}
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "用户不存在")
		return
	}
	writeJSON(w, http.StatusOK, userView(user))
}

// authLogout：JWT 是无状态的 —— 服务端没东西可清（吊销走 `ver` / 换 `jwt_key`）。
// 这个口只是给客户端一个**明确动作**（前端就此丢掉本地令牌）。
func (s *Server) authLogout(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

// issueToken：签 JWT 回 {token, user}（注册与登录同一条路）。
func (s *Server) issueToken(w http.ResponseWriter, user *model.User) {
	token, err := auth.Sign(s.jwtKey, user.ID, user.IsAdmin, user.Ver)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "签令牌失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "user": userView(user)})
}

func userView(u *model.User) map[string]any {
	return map[string]any{"id": u.ID, "name": u.Name, "is_admin": u.IsAdmin}
}
