// Package store：SQLite 的打开、迁移与读写。
//
// 与 Rust 版同一条纪律：**写入由一把锁串行化**（`mu`），而且**不许跨网络调用持锁** ——
// 取料在锁里、调上游在锁外、落库再进锁。
package store

import (
	"database/sql"
	"fmt"
	"sync"

	_ "modernc.org/sqlite" // 纯 Go 的 SQLite 驱动（不碰 cgo）
)

type Store struct {
	db *sql.DB
	mu sync.Mutex
}

// Open 打开（必要时创建）数据库并跑完迁移。
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// 外键与 WAL：与 Rust 版一致（messages 的两条级联全靠它）
	for _, pragma := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
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
