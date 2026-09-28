package store

import (
	"testing"

	"microchat/internal/model"
)

func pointer(value string) *string { return &value }

// 当前路径 = 从 current_leaf 沿 parent_id 回溯到根（正序）。
// 三种边角都在：正常链、指针悬空、以及**成环**（导入坏数据时可能碰上）。
func TestPathFrom(t *testing.T) {
	build := func(id string, parent *string) model.Message {
		return model.Message{ID: id, ParentID: parent}
	}
	all := []model.Message{
		build("a", nil),
		build("b", pointer("a")),
		build("b2", pointer("a")), // b 的兄弟（分支）
		build("c", pointer("b")),
	}
	cases := []struct {
		name string
		leaf *string
		want []string
	}{
		{"整条链", pointer("c"), []string{"a", "b", "c"}},
		{"切到兄弟", pointer("b2"), []string{"a", "b2"}},
		{"没有尾巴", nil, nil},
		{"悬空指针当到根", pointer("missing"), nil},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			path := pathFrom(all, test.leaf)
			ids := make([]string, 0, len(path))
			for _, message := range path {
				ids = append(ids, message.ID)
			}
			if len(ids) != len(test.want) {
				t.Fatalf("路径 = %v，想要 %v", ids, test.want)
			}
			for index := range test.want {
				if ids[index] != test.want[index] {
					t.Fatalf("路径 = %v，想要 %v", ids, test.want)
				}
			}
		})
	}
}

// 成环时**必须停**（指针数据坏掉也得打得开会话）。
//
// 注意口径：这个闸是**终止**保险，不是去重 —— 与 Rust 版逐字一致
// （`path.len() > all.len()` 时才 break），所以环上的节点会出现两次、
// 最多返回 len(all)+1 条。要的是"不会转死"，不是"结果多漂亮"。
func TestPathFromCycle(t *testing.T) {
	all := []model.Message{
		{ID: "a", ParentID: pointer("b")},
		{ID: "b", ParentID: pointer("a")},
	}
	path := pathFrom(all, pointer("a"))
	if len(path) > len(all)+1 {
		t.Fatalf("闸没起作用：%d 条（上限 %d）", len(path), len(all)+1)
	}
}

// 分支信息：分组按 parent_id（整棵树），组内顺序 = 生成先后（rowid）。
func TestBranchInfo(t *testing.T) {
	st := openTemp(t)
	conversationID := "01a00000-0000-7000-8000-000000000000"
	if _, err := st.db.Exec(
		`INSERT INTO conversations (id, title, provider, model, agent_id, created_at, updated_at)
		 VALUES (?1, '', 'dummy', 'dummy', 'default', 1, 1)`, conversationID); err != nil {
		t.Fatal(err)
	}
	insert := func(id string, parent any) {
		if _, err := st.db.Exec(
			`INSERT INTO messages (id, conversation_id, role, content, parent_id, created_at)
			 VALUES (?1, ?2, 'user', 'x', ?3, 1)`, id, conversationID, parent); err != nil {
			t.Fatal(err)
		}
	}
	insert("m1", nil)
	insert("m2", "m1")
	insert("m3", "m1") // 第二条候选回复（分支）
	insert("m4", "m2")

	info, err := st.BranchInfo(conversationID)
	if err != nil {
		t.Fatal(err)
	}
	if got := info["m1"]; got.Index != 1 || got.Total != 1 {
		t.Fatalf("m1 = %+v，想要 1/1", got)
	}
	if got := info["m2"]; got.Index != 1 || got.Total != 2 || len(got.Siblings) != 2 {
		t.Fatalf("m2 = %+v，想要 1/2", got)
	}
	if got := info["m3"]; got.Index != 2 || got.Total != 2 {
		t.Fatalf("m3 = %+v，想要 2/2", got)
	}
}

func openTemp(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir() + "/microchat.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// 删除后的 leaf 退位规则（本项目踩过的坑，必须守住）：
//  1. 退到"上文下**还活着的最新一个孩子**"（删单条时 = 它的上一条兄弟）；
//  2. 只退到父亲是不够的 —— 界面上会看到"整条分支都没了"，看起来像一次删除删掉了好几个分支；
//  3. 实在没有孩子可退，才退到上文本身。
func TestDeleteLeafFallback(t *testing.T) {
	build := func(t *testing.T, leaf string) (*Store, string) {
		t.Helper()
		st := openTemp(t)
		conversationID := "01a00000-0000-7000-8000-00000000000c"
		if _, err := st.db.Exec(
			`INSERT INTO conversations (id, title, provider, model, agent_id, current_leaf, created_at, updated_at)
			 VALUES (?1, '', 'dummy', 'dummy', 'default', ?2, 1, 1)`, conversationID, leaf); err != nil {
			t.Fatal(err)
		}
		insert := func(id string, parent any) {
			if _, err := st.db.Exec(
				`INSERT INTO messages (id, conversation_id, role, content, parent_id, created_at)
				 VALUES (?1, ?2, 'user', 'x', ?3, 1)`, id, conversationID, parent); err != nil {
				t.Fatal(err)
			}
		}
		// u1 ─┬─ a1
		//     └─ a2 ── u2 ─┬─ a3
		//                  └─ a4(leaf)
		insert("u1", nil)
		insert("a1", "u1")
		insert("a2", "u1")
		insert("u2", "a2")
		insert("a3", "u2")
		insert("a4", "u2")
		return st, conversationID
	}
	leafOf := func(t *testing.T, st *Store, conversationID string) string {
		t.Helper()
		conversation, err := st.GetConversation(conversationID)
		if err != nil || conversation == nil {
			t.Fatalf("读会话失败: %v", err)
		}
		if conversation.CurrentLeaf == nil {
			return ""
		}
		return *conversation.CurrentLeaf
	}

	t.Run("删当前尾巴 ⇒ 退到上一条兄弟", func(t *testing.T) {
		st, conversationID := build(t, "a4")
		if _, err := st.DeleteMessage(conversationID, "a4"); err != nil {
			t.Fatal(err)
		}
		if got := leafOf(t, st, conversationID); got != "a3" {
			t.Fatalf("leaf = %q，想要 a3", got)
		}
	})

	t.Run("删带子树的那条 ⇒ 退到还活着的兄弟", func(t *testing.T) {
		st, conversationID := build(t, "a4")
		deleted, err := st.DeleteMessage(conversationID, "a2") // a2+u2+a3+a4
		if err != nil {
			t.Fatal(err)
		}
		if deleted != 4 {
			t.Fatalf("删了 %d 条，想要 4", deleted)
		}
		if got := leafOf(t, st, conversationID); got != "a1" {
			t.Fatalf("leaf = %q，想要 a1", got)
		}
	})

	t.Run("删除全部 ⇒ 退到上文", func(t *testing.T) {
		st, conversationID := build(t, "a4")
		deleted, err := st.DeleteSiblings(conversationID, "a3") // a3 + a4
		if err != nil {
			t.Fatal(err)
		}
		if deleted != 2 {
			t.Fatalf("删了 %d 条，想要 2", deleted)
		}
		if got := leafOf(t, st, conversationID); got != "u2" {
			t.Fatalf("leaf = %q，想要 u2（退到上文）", got)
		}
	})

	t.Run("删不是当前尾巴的兄弟 ⇒ leaf 不动", func(t *testing.T) {
		st, conversationID := build(t, "a4")
		if _, err := st.DeleteMessage(conversationID, "a1"); err != nil {
			t.Fatal(err)
		}
		if got := leafOf(t, st, conversationID); got != "a4" {
			t.Fatalf("leaf = %q，想要 a4（不该动）", got)
		}
	})
}
