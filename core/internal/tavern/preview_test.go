package tavern

import (
	"encoding/json"
	"strings"
	"testing"
)

const sample = `{
  "name": "样例",
  "prompts": [
    {"identifier":"main","name":"主提示词","role":"system","content":"你是主持人","system_prompt":true},
    {"identifier":"marker","name":"对话历史","marker":true},
    {"identifier":"empty","name":"空条目","role":"system","content":"  "},
    {"identifier":"abs","name":"文风","role":"system","content":"<style>{{user_name}} {{user}} {{getvar::base_writing}}</style>","injection_position":1,"injection_depth":2,"injection_order":50},
    {"identifier":"user","name":"用户条目","role":"user","content":"{{char_name}}","injection_position":0},
    {"identifier":"off","name":"关闭","role":"system","content":"不应出现"}
  ],
  "prompt_order": [{"character_id":100001,"order":[
    {"identifier":"main","enabled":true},
    {"identifier":"marker","enabled":true},
    {"identifier":"empty","enabled":true},
    {"identifier":"abs","enabled":true},
    {"identifier":"user","enabled":true},
    {"identifier":"missing","enabled":true},
    {"identifier":"off","enabled":false}
  ]}]
}`

func TestParsePreview(t *testing.T) {
	got, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "样例" || got.PromptCount != 6 || got.OrderGroups != 1 || got.OrderCharacterID != "100001" {
		t.Fatalf("头部统计不对：%+v", got)
	}
	if got.OrderEntries != 7 || got.Enabled != 6 || got.WithContent != 3 || got.Relative != 2 || got.Absolute != 1 {
		t.Fatalf("计数不对：%+v", got)
	}
	if len(got.Entries) != 3 {
		t.Fatalf("entries = %+v", got.Entries)
	}
	if got.Entries[0].Identifier != "main" || got.Entries[0].Position != "relative" {
		t.Fatalf("main 条目不对：%+v", got.Entries[0])
	}
	abs := got.Entries[1]
	if abs.Identifier != "abs" || abs.Position != "absolute" || abs.Depth != 2 || abs.Order != 50 {
		t.Fatalf("absolute 条目不对：%+v", abs)
	}
	if got.Entries[2].Identifier != "user" || got.Entries[2].Role != "user" {
		t.Fatalf("user 条目不对：%+v", got.Entries[2])
	}
	if len(got.Skipped) != 4 {
		t.Fatalf("skipped = %+v", got.Skipped)
	}
	// user_name/user 已注册；getvar/char_name 未注册（char_name 是注册宏，这里只应剩 getvar）
	var unknown []string
	for _, item := range got.Macros {
		if !item.Known {
			unknown = append(unknown, item.Name)
		}
	}
	if len(unknown) != 1 || unknown[0] != "getvar::base_writing" {
		t.Fatalf("unknown macros = %v", unknown)
	}
}

func TestParseBOM(t *testing.T) {
	got, err := Parse(append([]byte{0xEF, 0xBB, 0xBF}, []byte(sample)...))
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "样例" {
		t.Fatalf("BOM 没被容忍：%+v", got)
	}
}

func TestParseNoOrderFallsBack(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"name": "无 order",
		"prompts": []any{
			map[string]any{"identifier": "a", "role": "system", "content": "A"},
			map[string]any{"identifier": "b", "role": "system", "content": "B", "enabled": false},
		},
	})
	got, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.OrderEntries != 2 || got.Enabled != 1 || got.WithContent != 1 {
		t.Fatalf("fallback 不对：%+v", got)
	}
	if !strings.Contains(strings.Join(got.Warnings, "\n"), "没有 prompt_order") {
		t.Fatalf("warnings = %v", got.Warnings)
	}
}

func TestParseBadJSON(t *testing.T) {
	if _, err := Parse([]byte(`{"prompts":`)); err == nil {
		t.Fatal("坏 JSON 应该报错")
	}
}
