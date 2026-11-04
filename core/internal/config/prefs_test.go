package config

import (
	"path/filepath"
	"testing"
)

// 2026-11-02：`DefaultPrefs()` 曾把多格 `*bool` 指向同一个局部变量（`on` / `off`）。
// JSON 解码时它们互相改写 —— `command_menu:true` 会把 `payload_viz` 也改成 true，
// 后一行 `payload_viz:false` 又把它改回来；最终值取决于字段顺序。
// 表现就是"开关点不动、刷新就回去"（前端设置页实测到的）。
//
// 这条用**混合值**验：只要共用指针，`Preview=false` / `CommandMenu=true` 就至少错一格。
func TestPrefsBoolFieldsRoundTripIndependently(t *testing.T) {
	paths := Paths{DataDir: t.TempDir()}
	paths.ConfigDir = filepath.Join(paths.DataDir, "config")

	want := Prefs{
		Markdown:      MarkdownPrefs{Enabled: new(true)},
		Preview:       new(false),
		CommandMenu:   new(true),
		ChromeHide:    new(true),
		PayloadViz:    new(false),
		BarLeftHidden: new(true),
	}
	if err := SavePrefs(paths, want); err != nil {
		t.Fatalf("SavePrefs: %v", err)
	}
	got, err := LoadPrefs(paths)
	if err != nil {
		t.Fatalf("LoadPrefs: %v", err)
	}

	check := func(name string, got *bool, want bool) {
		t.Helper()
		if got == nil {
			t.Fatalf("%s 读回来是 nil", name)
		}
		if *got != want {
			t.Fatalf("%s 读回来是 %v，应为 %v（多格共用了同一个指针？）", name, *got, want)
		}
	}
	check("preview", got.Preview, false)
	check("command_menu", got.CommandMenu, true)
	check("chrome_hide", got.ChromeHide, true)
	check("payload_viz", got.PayloadViz, false)
	check("bar_left_hidden", got.BarLeftHidden, true)
}

// 缺文件时返回的是默认值；而且这些默认值的指针必须彼此独立 ——
// 否则界面上拨一个开关会顺手把别的开关也改掉。
func TestDefaultPrefsBoolPointersAreDistinct(t *testing.T) {
	def := DefaultPrefs()
	if def.Preview == nil || def.PayloadPreview == nil || def.CommandMenu == nil || def.ChromeHide == nil ||
		def.PayloadViz == nil || def.BarLeftHidden == nil {
		t.Fatal("默认值里有 nil 的布尔指针")
	}
	*def.PayloadPreview = false
	if !*def.Preview {
		t.Fatal("payload_preview 与 preview 共用了同一个指针")
	}
	*def.PayloadPreview = true
	*def.Preview = false
	if !*def.ChromeHide {
		t.Fatal("preview 与 chrome_hide 共用了同一个指针")
	}
	*def.CommandMenu = true
	if *def.PayloadViz || *def.BarLeftHidden {
		t.Fatal("command_menu 与 payload_viz / bar_left_hidden 共用了同一个指针")
	}
}
