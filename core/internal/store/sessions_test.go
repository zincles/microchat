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
