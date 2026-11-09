// Package store：SQLite 的打开、迁移与读写。
//
// 与 Rust 版同一条纪律：**写入由一把锁串行化**（`mu`），而且**不许跨网络调用持锁** ——
// 取料在锁里、调上游在锁外、落库再进锁。
package store

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"

	_ "modernc.org/sqlite" // 纯 Go 的 SQLite 驱动（不碰 cgo）
)

type Store struct {
	db *sql.DB
	mu sync.Mutex
}

// Open 打开（必要时创建）数据库并跑完迁移。
//
// 三条连接级 pragma **挂在 DSN 上**（`_pragma=` 每建一条连接就应用一次）：
// 用 `db.Exec("PRAGMA …")` 只对**当时那一条连接**生效 —— 池子后来新开的连接就漏了
// （`foreign_keys` 漏掉 = 级联静默失效，最难查的那类）。
//
//   - `foreign_keys(1)`：messages 的两条级联全靠它（CASCADE / SET NULL）；
//   - `journal_mode(WAL)`：与 Rust 版一致（读写不互相挡）；
//   - `busy_timeout(5000)`：**多进程**时（服务在跑，同时来一个 `-debug`；或两个 `-debug`）
//     先等一会儿再报 —— SQLite 默认是 0，一撞就回 "database is locked"，
//     而打开这个库本身（`journal_mode` 要拿一下写锁）就可能撞上别的进程正在写。
func Open(path string) (*Store, error) {
	// `file:` 是 URI 形状 ⇒ 路径里的 `?` `#` 得先转义（正常路径没有，但别留个坑）
	escaped := strings.NewReplacer("?", "%3f", "#", "%23").Replace(path)
	dsn := "file:" + escaped +
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	st := &Store{db: db}
	if err := st.migrate(); err != nil {
		return nil, err
	}
	return st, nil
}

func (s *Store) Close() error { return s.db.Close() }

// migrate：照 `PRAGMA user_version` 逐条补，跑一条 +1（与 Rust 版同一个机制）。
func (s *Store) migrate() error {
	all, err := migrations()
	if err != nil {
		return err
	}
	version, err := s.UserVersion()
	if err != nil {
		return err
	}
	for i := version; i < len(all); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(all[i]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("迁移 [%d] 失败: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	if err := s.dropRetired(); err != nil {
		return err
	}
	if err := s.ensureSessionThinking(); err != nil {
		return err
	}
	if err := s.ensureSessionNativeTools(); err != nil {
		return err
	}
	if err := s.ensureMessageMetadata(); err != nil {
		return err
	}
	if err := s.ensureMessageTools(); err != nil {
		return err
	}
	return s.ensureTavernPresets()
}

// ensureSessionNativeTools：老库补 `native_tools_disabled` 列（幂等；默认 []）。
func (s *Store) ensureSessionNativeTools() error {
	var hasColumn int
	if err := s.db.QueryRow(
		"SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'native_tools_disabled'").Scan(&hasColumn); err != nil {
		return err
	}
	if hasColumn > 0 {
		return nil
	}
	_, err := s.db.Exec("ALTER TABLE sessions ADD COLUMN native_tools_disabled TEXT NOT NULL DEFAULT '[]'")
	return err
}

// ensureTavernPresets：老库（user_version 已经是 1）不会重跑 001.sql ⇒ 补这张表。
// 幂等；只建表，不碰任何已有数据。新库 001 里也有同一张表。
func (s *Store) ensureTavernPresets() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS tavern_presets (
  id         TEXT PRIMARY KEY,
  filename   TEXT NOT NULL,
  size       INTEGER NOT NULL,
  data       BLOB NOT NULL,
  created_at INTEGER NOT NULL
)`)
	return err
}

// ensureSessionThinking：老库（user_version 已经是 1）不会重跑 001.sql ⇒ 补这一列。
// 幂等：先查 pragma_table_info；新库本来就有。只加列，不碰任何数据。
func (s *Store) ensureSessionThinking() error {
	var hasColumn int
	if err := s.db.QueryRow(
		"SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'thinking_level'").Scan(&hasColumn); err != nil {
		return err
	}
	if hasColumn > 0 {
		return nil
	}
	_, err := s.db.Exec("ALTER TABLE sessions ADD COLUMN thinking_level TEXT NOT NULL DEFAULT ''")
	return err
}

// dropRetired：把**已经不用的老结构**顺手清掉（幂等，每次打开都跑一遍）。
//
// 2026-11-01 砍掉账号功能：`users` 表与 `sessions.owner_id` 不再有任何读写。
// ⚠ 新库本来就没有它们（`001.sql` 已经删了那一段），老库则**还留着** —— 而迁移靠
// `PRAGMA user_version` 拦着，改 `001.sql` 对老库**不生效**（它不会被重跑）。
// 所以这里补一把：只删"那一列/那张表在不在都行"的东西，**绝不碰会话、消息、摘要的数据**。
//
// ⚠ 两步都要判存在：`DROP COLUMN` 在 SQLite 3.35+ 才有，`DROP TABLE IF EXISTS` 则一直都有；
//
//	万一某天它已经在别处删过了，这里再跑一次也不能报错（每次打开都跑）。
func (s *Store) dropRetired() error {
	if _, err := s.db.Exec("DROP TABLE IF EXISTS users"); err != nil {
		return err
	}
	var hasColumn int
	if err := s.db.QueryRow(
		"SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'owner_id'").Scan(&hasColumn); err != nil {
		return err
	}
	if hasColumn == 0 {
		return nil // 新库：本来就没有
	}
	_, err := s.db.Exec("ALTER TABLE sessions DROP COLUMN owner_id")
	return err
}

// UserVersion：库当前的迁移版本。
func (s *Store) UserVersion() (int, error) {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return 0, err
	}
	return version, nil
}

// ensureMessageTools：老库补 message_tools 表（幂等；新库 001 里也有）。
func (s *Store) ensureMessageTools() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS message_tools (
  id         TEXT PRIMARY KEY,
  message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  seq        INTEGER NOT NULL,
  source     TEXT NOT NULL DEFAULT 'server',
  tool_name  TEXT NOT NULL,
  item_id    TEXT,
  queries    TEXT NOT NULL DEFAULT '[]',
  sources    TEXT NOT NULL DEFAULT '[]',
  arguments  TEXT NOT NULL DEFAULT '',
  result     TEXT NOT NULL DEFAULT '',
  status     TEXT NOT NULL DEFAULT 'done',
  created_at INTEGER NOT NULL
)`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("CREATE INDEX IF NOT EXISTS message_tools_by_message ON message_tools(message_id, seq)")
	return err
}
