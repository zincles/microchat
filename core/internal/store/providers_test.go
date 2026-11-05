package store

import (
	"errors"
	"testing"
)

// 改名 = 把库里的**全部引用**一起搬：会话、模型（含用户覆盖列）、路由表、上次刷新时间、摘要审计列。
//
// 少搬任何一张都会静默出错：会话指向不存在的渠道 ⇒ 发消息回落 fallback；
// 模型不搬 ⇒ 渠道详情一个模型都没有、用户覆盖也跟着"消失"（刷新也找不回来）。
func TestRenameProviderMovesEveryReference(t *testing.T) {
	st, err := Open(t.TempDir() + "/microchat.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	const (
		oldID = "old"
		newID = "new"
	)
	now := int64(1_700_000_000_000)
	// 模型 + 用户覆盖（改名后覆盖必须还在 —— 那是不可再发现的数据）
	if _, _, _, err := st.RefreshDiscovered(oldID, []Discovered{{UpstreamID: "m1"}}, now); err != nil {
		t.Fatal(err)
	}
	display := "我的显示名"
	contextOverride := int64(1234)
	if err := st.SetModelOverride(oldID, "m1", &display, &contextOverride, nil); err != nil {
		t.Fatal(err)
	}
	// 会话（活引用）
	session, err := st.CreateSession(oldID, "m1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	// 摘要审计列
	if _, err := st.db.Exec(
		`INSERT INTO summaries (id, session_id, source_kind, text, provider, model, prompt_version, created_at)
		 VALUES ('sum1', ?1, 'message', '前情', ?2, 'm1', 1, ?3)`,
		session.ID, oldID, now); err != nil {
		t.Fatal(err)
	}
	// 路由表（按需建，不在 001 里）
	if err := st.ReplaceModelRoutes(oldID, map[string]string{"m1": "openai-response"}, now); err != nil {
		t.Fatal(err)
	}

	if err := st.RenameProvider(oldID, newID); err != nil {
		t.Fatalf("改名失败：%v", err)
	}

	// 模型：旧标识下一条不剩，新标识下带着用户覆盖
	if rows, err := st.ListModels(oldID); err != nil || len(rows) != 0 {
		t.Fatalf("旧标识下还有模型：%v %v", rows, err)
	}
	rows, err := st.ListModels(newID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("新标识下的模型 = %v %v", rows, err)
	}
	if rows[0].DisplayName == nil || *rows[0].DisplayName != display {
		t.Fatalf("用户覆盖丢了：%+v", rows[0])
	}
	if rows[0].ContextOverride == nil || *rows[0].ContextOverride != contextOverride {
		t.Fatalf("上下文覆盖丢了：%+v", rows[0])
	}
	// 上次刷新时间跟着走
	if at, err := st.LastRefreshAt(newID); err != nil || at == nil || *at != now {
		t.Fatalf("last_refresh_at 没搬：%v %v", at, err)
	}
	if at, _ := st.LastRefreshAt(oldID); at != nil {
		t.Fatalf("旧标识还留着 last_refresh_at：%v", *at)
	}
	// 会话
	got, err := st.GetSession(session.ID)
	if err != nil || got.Provider != newID {
		t.Fatalf("会话的渠道没搬：%+v %v", got, err)
	}
	// 路由
	if api, ok := st.ModelRoute(newID, "m1"); !ok || api != "openai-response" {
		t.Fatalf("路由没搬：%q %v", api, ok)
	}
	if _, ok := st.ModelRoute(oldID, "m1"); ok {
		t.Fatal("旧标识还留着路由")
	}
	// 摘要审计列
	var summaryProvider string
	if err := st.db.QueryRow("SELECT provider FROM summaries WHERE id = 'sum1'").Scan(&summaryProvider); err != nil {
		t.Fatal(err)
	}
	if summaryProvider != newID {
		t.Fatalf("摘要的 provider = %q", summaryProvider)
	}
}

// 目标标识在库里已有数据 ⇒ 拒绝（不合并、不覆盖）。拒绝之后必须**什么都没动**。
func TestRenameProviderRefusesToMerge(t *testing.T) {
	st, err := Open(t.TempDir() + "/microchat.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := int64(1_700_000_000_000)
	if _, _, _, err := st.RefreshDiscovered("old", []Discovered{{UpstreamID: "m1"}}, now); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := st.RefreshDiscovered("taken", []Discovered{{UpstreamID: "m1"}}, now); err != nil {
		t.Fatal(err)
	}
	session, err := st.CreateSession("old", "m1", "", "")
	if err != nil {
		t.Fatal(err)
	}

	if err := st.RenameProvider("old", "taken"); !errors.Is(err, ErrProviderDataConflict) {
		t.Fatalf("该报冲突，得到 %v", err)
	}
	// 事务回滚：两边都还是原样
	if rows, err := st.ListModels("old"); err != nil || len(rows) != 1 {
		t.Fatalf("旧标识的模型被动了：%v %v", rows, err)
	}
	if got, err := st.GetSession(session.ID); err != nil || got.Provider != "old" {
		t.Fatalf("会话被动了：%+v %v", got, err)
	}
	if rows, err := st.ListModels("taken"); err != nil || len(rows) != 1 {
		t.Fatalf("目标标识的数据被动了：%v %v", rows, err)
	}
}

// 同一个 id 改名 = 什么都不做（幂等），不报冲突。
func TestRenameProviderSameIDIsNoop(t *testing.T) {
	st, err := Open(t.TempDir() + "/microchat.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, _, _, err := st.RefreshDiscovered("same", []Discovered{{UpstreamID: "m1"}}, 1); err != nil {
		t.Fatal(err)
	}
	if err := st.RenameProvider("same", "same"); err != nil {
		t.Fatalf("同名改名该是空操作，得到 %v", err)
	}
	if rows, err := st.ListModels("same"); err != nil || len(rows) != 1 {
		t.Fatalf("数据被动了：%v %v", rows, err)
	}
}
