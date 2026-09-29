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
	return nil
}

// UserVersion：库当前的迁移版本。
func (s *Store) UserVersion() (int, error) {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return 0, err
	}
	return version, nil
}
