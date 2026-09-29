package store

import (
	"embed"
	"fmt"
	"sort"
)

// migrations：一个迁移一个 .sql 文件，按文件名排序执行，序号即 `user_version`。
//
// **开发阶段没有向后兼容**：改形状就是改 `001.sql`，然后把 `data/microchat.db` 删掉重来
// （用户 2026-09-29 定）。等真要发版时再开始追加 002、003……
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
