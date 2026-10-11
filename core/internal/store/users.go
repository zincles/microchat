package store

import (
	"database/sql"
	"fmt"
	"strconv"

	"microchat/internal/model"
)

// userColumns：用户自己的列（口令哈希在内 —— 只给内部用；model.User 的 JSON 已 `-` 掉）。
const userColumns = "id, name, pass_hash, is_admin, ver, tg_user_id, created_at"

func scanUser(row scanner) (model.User, error) {
	var u model.User
	var admin int64
	var tg sql.NullInt64
	err := row.Scan(&u.ID, &u.Name, &u.PassHash, &admin, &u.Ver, &tg, &u.CreatedAt)
	if err != nil {
		return model.User{}, err
	}
	u.IsAdmin = admin != 0
	if tg.Valid {
		u.TgUserID = &tg.Int64
	}
	return u, nil
}

// CreateUser：加人（**uid 0 = root 是建库就种下的，这里只会分到 ≥1**；admin 性由调用方显式给）。
// 重名 ⇒ InvalidError（"名字被占了"）。
func (s *Store) CreateUser(name, passHash string, isAdmin bool) (model.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return model.User{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var exists int
	if err := tx.QueryRow("SELECT COUNT(*) FROM users WHERE name = ?1", name).Scan(&exists); err != nil {
		return model.User{}, err
	}
	if exists > 0 {
		return model.User{}, InvalidError(fmt.Sprintf("名字被占了：%s", name))
	}
	if _, err := strconv.ParseInt(name, 10, 64); err == nil {
		// 数字是 **uid 的地盘**（登录时全数字按 uid 查）：名字不许是纯数字，免歧义。
		return model.User{}, InvalidError("名字不能是纯数字（那是 uid 的地盘）")
	}
	// uid 分配：**root 恒 0，其他用户从 1000 起**（Linux 味，用户 2026-10-10 定）；
	// 重置后（只余 root）再发人还是从 1000 起。
	var next int64
	if err := tx.QueryRow("SELECT MAX(1000, COALESCE(MAX(id)+1, 1000)) FROM users").Scan(&next); err != nil {
		return model.User{}, err
	}
	now := nowMS()
	user := model.User{ID: next, Name: name, PassHash: passHash, IsAdmin: isAdmin, CreatedAt: now}
	if _, err := tx.Exec(
		`INSERT INTO users (id, name, pass_hash, is_admin, ver, created_at) VALUES (?1, ?2, ?3, ?4, 0, ?5)`,
		user.ID, user.Name, user.PassHash, boolArg(user.IsAdmin), now); err != nil {
		return model.User{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.User{}, err
	}
	return user, nil
}

// CountUsers：有几个用户（注册闸/status 探测用）。
func (s *Store) CountUsers() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	err := s.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&n)
	return n, err
}

func (s *Store) GetUserByID(id int64) (*model.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, err := scanUser(s.db.QueryRow("SELECT "+userColumns+" FROM users WHERE id = ?1", id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (s *Store) GetUserByName(name string) (*model.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, err := scanUser(s.db.QueryRow("SELECT "+userColumns+" FROM users WHERE name = ?1", name))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// RootNeedsSetup：root（uid 0）还没设口令吗？（门板据此切"初始化"页；行缺失也按"要设置"处理。）
func (s *Store) RootNeedsSetup() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var hash string
	err := s.db.QueryRow("SELECT pass_hash FROM users WHERE id = 0").Scan(&hash)
	if err == sql.ErrNoRows {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return hash == "", nil
}

// SetRootPassword：**初始化 root 的口令**（唯一一次；已经设过 ⇒ InvalidError）。
// 空口令不许进来（调方已拦；这里再挡一道）。成功后 `ver+1`（老令牌作废，与改密同规矩）。
func (s *Store) SetRootPassword(passHash string) error {
	if passHash == "" {
		return InvalidError("口令不许为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.Exec(
		"UPDATE users SET pass_hash = ?1, ver = ver + 1 WHERE id = 0 AND pass_hash = ''", passHash)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return InvalidError("root 已经初始化过了")
	}
	return nil
}

// UpdateUserPassword：改口令 —— 顺带 **ver+1**（此人此前签发的所有 JWT 立刻失效，DEFINE「用户与鉴权」）。
func (s *Store) UpdateUserPassword(id int64, passHash string) error {
	return s.execTouch("UPDATE users SET pass_hash = ?1, ver = ver + 1 WHERE id = ?2", passHash, id)
}

// ResetRootPassword：**无视现状直接换 root 的口令**（`ver+1` ⇒ 旧票全废）。
//
// **只有 CLI 走它**（`-debug set-root-password`，用户定 2026-10-10）：网络口对 uid 0 的口令改写
// 一律 403 —— 本机 shell 才是"已经拿到一切"的信任边界，root 的凭据不许被网络口换掉/抢走。
// 与 `SetRootPassword`（setup 的"只开一次"）分开：语义不同就别共用一个门。
func (s *Store) ResetRootPassword(passHash string) error {
	if passHash == "" {
		return InvalidError("口令不许为空")
	}
	return s.execTouch("UPDATE users SET pass_hash = ?1, ver = ver + 1 WHERE id = 0", passHash)
}

// ResetUsers：**一键重置所有用户**（操作员逃生门，`-debug reset-users` 走它）：
// 删掉所有**非 root** 用户**连同他们的会话**（消息/摘要随外键级联），把 root 的口令清空
// （回到"无密码 = 服务未启用"的 setup 态、`ver+1` 让旧票全废）。root 行恒在、uid 0 不动。
// 返回 (删了几人, 删了几条会话)。
func (s *Store) ResetUsers() (int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.Exec("DELETE FROM sessions WHERE owner_id <> 0")
	if err != nil {
		return 0, 0, err
	}
	sessions, _ := res.RowsAffected()
	res, err = tx.Exec("DELETE FROM users WHERE id <> 0")
	if err != nil {
		return 0, 0, err
	}
	users, _ := res.RowsAffected()
	if _, err := tx.Exec("UPDATE users SET pass_hash = '', ver = ver + 1 WHERE id = 0"); err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return int(users), int(sessions), nil
}

// ListUsers：全体（管理员页用；口令哈希由 model.User 的 `json:"-"` 挡在 JSON 外）。
func (s *Store) ListUsers() ([]model.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT " + userColumns + " FROM users ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []model.User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// UpdateUserName：改名（root 也能改，id 不动）。纯数字名字拒掉（那是 uid 的地盘）、重名拒掉。
func (s *Store) UpdateUserName(id int64, name string) error {
	if _, err := strconv.ParseInt(name, 10, 64); err == nil {
		return InvalidError("名字不能是纯数字（那是 uid 的地盘）")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var taken int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM users WHERE name = ?1 AND id <> ?2", name, id).Scan(&taken); err != nil {
		return err
	}
	if taken > 0 {
		return InvalidError("名字被占了：" + name)
	}
	// ⚠ 持着 `s.mu` 就**只能**用 Locked 变体：`s.execTouch` 自己再拿一次同一把
	// `sync.Mutex`（不可重入）⇒ 当场死锁、**整个进程**再也不应答（2026-10-10 踩过）。
	return execTouchLocked(s.db, "UPDATE users SET name = ?1 WHERE id = ?2", name, id)
}

// DeleteUser：删一个人。**对会话必须显式**（R6）：`sessions` = "transfer"（会话转给 root）
// 或 "purge"（连会话一起没，消息/摘要随外键级联）。**root（uid 0）不许删**。
// 返回动了几条会话。
func (s *Store) DeleteUser(id int64, sessions string) (int, error) {
	if id == 0 {
		return 0, InvalidError("root 不许删（uid 0 恒在）")
	}
	var move string
	switch sessions {
	case "transfer":
		move = "UPDATE sessions SET owner_id = 0 WHERE owner_id = ?1"
	case "purge":
		move = "DELETE FROM sessions WHERE owner_id = ?1"
	default:
		return 0, InvalidError(`sessions 必须是 "transfer" 或 "purge"`)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.Exec(move, id)
	if err != nil {
		return 0, err
	}
	affected, _ := res.RowsAffected()
	if _, err := tx.Exec("DELETE FROM users WHERE id = ?1", id); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(affected), nil
}

// boolArg：SQLite 没有布尔列（INTEGER 0/1）。
func boolArg(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
