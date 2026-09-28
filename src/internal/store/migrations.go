package store

import (
	"embed"
	"fmt"
	"sort"
)

// migrations 照搬自 `src/store.rs` 的 `MIGRATIONS` —— **一字不改**（生成而非手抄；
// 每个迁移一个 .sql 文件，按文件名排序执行，序号即 `user_version`）。
//
//go:embed migrations/*.sql
var migrationFS embed.FS

func migrations() ([]string, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, name := range names {
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		out = append(out, string(body))
	}
	return out, nil
}
