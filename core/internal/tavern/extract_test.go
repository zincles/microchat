package tavern

import (
	"testing"

	"microchat/internal/macro"
)

func TestExtractEffectiveEntries(t *testing.T) {
	data := []byte(`{
  "name": "提取样例",
  "prompts": [
    {"identifier":"vars","name":"变量组","role":"system","content":"{{setvar::a::}}{{setvar::b::}}"},
    {"identifier":"opt","name":"选项","role":"system","content":"{{setvar::a::甲}}{{setvar::b::乙}}"},
    {"identifier":"header","name":"标题","role":"system","content":"{{//说明}}{{trim}}"},
    {"identifier":"use","name":"使用","role":"system","content":"A={{getvar::a}} B={{getvar::b}} {{user_name}}"},
    {"identifier":"abs","name":"注入","role":"system","content":"<s>{{getvar::a}}</s>","injection_position":1,"injection_depth":2,"injection_order":100}
  ],
  "prompt_order": [{"character_id":1,"order":[
    {"identifier":"vars","enabled":true},
    {"identifier":"opt","enabled":true},
    {"identifier":"header","enabled":true},
    {"identifier":"use","enabled":true},
    {"identifier":"abs","enabled":true}
  ]}]
}`)
	got, err := Extract(data, ExtractOptions{Values: macro.Values("Alice", "旁白", "m1")})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 2 {
		t.Fatalf("entries = %+v, dropped = %+v", got.Entries, got.Dropped)
	}
	if got.Entries[0].Content != "A=甲 B=乙 Alice" {
		t.Fatalf("正文 = %q", got.Entries[0].Content)
	}
	if got.Entries[1].Position != "absolute" || got.Entries[1].Depth != 2 || got.Entries[1].Content != "<s>甲</s>" {
		t.Fatalf("绝对条目 = %+v", got.Entries[1])
	}
	if got.Variables["a"] != "甲" || got.Variables["b"] != "乙" {
		t.Fatalf("variables = %#v", got.Variables)
	}
	if len(got.Dropped) != 3 {
		t.Fatalf("dropped = %+v", got.Dropped)
	}
}

func TestExtractKeepsLastUserMessageDynamic(t *testing.T) {
	data := []byte(`{
  "name":"last",
  "prompts":[{"identifier":"u","name":"user","role":"system","content":"{{lastUserMessage}}"}],
  "prompt_order":[{"character_id":1,"order":[{"identifier":"u","enabled":true}]}]
}`)
	got, err := Extract(data, ExtractOptions{Values: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 1 || got.Entries[0].Content != "{{lastUserMessage}}" {
		t.Fatalf("entries = %+v", got.Entries)
	}
	value := "你好"
	got, err = Extract(data, ExtractOptions{Values: map[string]string{}, LastUserMessage: &value})
	if err != nil || len(got.Entries) != 1 || got.Entries[0].Content != "你好" {
		t.Fatalf("替换后 = %+v, err=%v", got.Entries, err)
	}
}
