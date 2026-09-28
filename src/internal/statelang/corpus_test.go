package statelang

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 共享语料：**同一份 JSON，Rust 与 Go 都跑**（跨语言一致性靠它，而不是靠人眼比对）。
//
// 文件住 `testdata/` —— Go 把 testdata 当作"只给测试用的数据"，永不编译、永不打包。
type corpus struct {
	Version int          `json:"version"`
	Cases   []corpusCase `json:"cases"`
}

type corpusCase struct {
	Name        string      `json:"name"`
	Input       string      `json:"input"`
	Cleaned     string      `json:"cleaned"`
	Statements  []Statement `json:"statements"`
	Diagnostics int         `json:"diagnostics"`
	// Tables 省略 ⇒ 这一组不比"算完的值"（Rust 侧暂时只比解析结果）
	Tables map[string]map[string]string `json:"tables"`
}

func TestSharedCorpus(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var data corpus
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Cases) == 0 {
		t.Fatal("语料是空的")
	}
	for _, item := range data.Cases {
		t.Run(item.Name, func(t *testing.T) {
			document := Scan(item.Input)
			if document.Cleaned != item.Cleaned {
				t.Fatalf("Cleaned = %q，语料说 %q", document.Cleaned, item.Cleaned)
			}
			if len(document.Statements) != len(item.Statements) {
				t.Fatalf("语句 = %+v，语料说 %+v", document.Statements, item.Statements)
			}
			for index, want := range item.Statements {
				got := document.Statements[index]
				if got.Kind != want.Kind || got.Key != want.Key {
					t.Fatalf("第 %d 条 = %+v，语料说 %+v", index, got, want)
				}
				if (got.Value == nil) != (want.Value == nil) ||
					(got.Value != nil && *got.Value != *want.Value) {
					t.Fatalf("第 %d 条的值 = %+v，语料说 %+v", index, got, want)
				}
			}
			if len(document.Diagnostics) != item.Diagnostics {
				t.Fatalf("诊断 %d 条，语料说 %d 条：%+v", len(document.Diagnostics), item.Diagnostics, document.Diagnostics)
			}
			for index, want := range item.Statements {
				if document.Statements[index].Table != want.Table {
					t.Fatalf("第 %d 条的 table = %q，语料说 %q", index, document.Statements[index].Table, want.Table)
				}
			}
			// 计算（折叠）的结果：删除生效、空表消失、未命名表用空串作键
			if item.Tables != nil {
				got := Tables(item.Input)
				if !sameTables(got, item.Tables) {
					t.Fatalf("Tables = %+v，语料说 %+v", got, item.Tables)
				}
			}
		})
	}
}

func sameTables(a, b map[string]map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for name, table := range a {
		other, ok := b[name]
		if !ok || len(table) != len(other) {
			return false
		}
		for key, value := range table {
			if other[key] != value {
				return false
			}
		}
	}
	return true
}
