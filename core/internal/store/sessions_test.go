package store

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestSessionNativeToolsDisabledRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	session, err := st.CreateSession("xai", "grok-4.7", "", "t")
	if err != nil {
		t.Fatal(err)
	}
	if len(session.NativeToolsDisabled) != 0 {
		t.Fatalf("新会话默认应为空：%+v", session.NativeToolsDisabled)
	}
	if err := st.SetSessionNativeToolsDisabled(session.ID, []string{"x_search", "code_execution"}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetSession(session.ID)
	if err != nil || got == nil {
		t.Fatalf("GetSession: %v %+v", err, got)
	}
	if !reflect.DeepEqual(got.NativeToolsDisabled, []string{"x_search", "code_execution"}) {
		t.Fatalf("disabled = %+v", got.NativeToolsDisabled)
	}
	copied, err := st.CopySession(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(copied.NativeToolsDisabled, got.NativeToolsDisabled) {
		t.Fatalf("Copy 没带工具开关：%+v", copied.NativeToolsDisabled)
	}
}

func TestSessionFavoriteRoundTripAndOrdering(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	first, err := st.CreateSession("p", "m", "", "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.CreateSession("p", "m", "", "second")
	if err != nil {
		t.Fatal(err)
	}
	third, err := st.CreateSession("p", "m", "", "third")
	if err != nil {
		t.Fatal(err)
	}
	// 使普通会话时间较新，并让收藏会话时间较旧，验证收藏始终优先。
	if _, err := st.db.Exec("UPDATE sessions SET updated_at = CASE id WHEN ?1 THEN 1 WHEN ?2 THEN 3 WHEN ?3 THEN 2 END", first.ID, second.ID, third.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSessionFavorite(first.ID, true); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetSession(first.ID)
	if err != nil || got == nil || !got.Favorite || got.UpdatedAt != 1 {
		t.Fatalf("收藏状态/更新时间 = %+v, %v", got, err)
	}
	list, err := st.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].ID != first.ID || list[1].ID != second.ID || list[2].ID != third.ID {
		t.Fatalf("排序 = %+v", list)
	}
	if err := st.SetSessionFavorite(first.ID, false); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetSession(first.ID)
	if err != nil || got == nil || got.Favorite || got.UpdatedAt != 1 {
		t.Fatalf("取消收藏状态/更新时间 = %+v, %v", got, err)
	}
}

func TestNewAndCopiedSessionsAreNotFavorite(t *testing.T) {
	st := openTemp(t)
	session, err := st.CreateSession("p", "m", "", "")
	if err != nil || session.Favorite {
		t.Fatalf("新会话默认未收藏：%+v, %v", session, err)
	}
	if err := st.SetSessionFavorite(session.ID, true); err != nil {
		t.Fatal(err)
	}
	copied, err := st.CopySession(session.ID)
	if err != nil || copied.Favorite {
		t.Fatalf("复制会话默认未收藏：%+v, %v", copied, err)
	}
}
