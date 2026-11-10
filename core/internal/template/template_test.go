package template

import (
	"strings"
	"testing"

	"microchat/internal/macro"
)

func TestRenderVariablesCommentsTrimAndIdentity(t *testing.T) {
	ctx := &Context{
		Values:    macro.Values("Alice", "旁白", "m1"),
		Variables: map[string]string{"a": "甲", "empty": "x"},
	}
	text := `{{//注释}}
{{setvar::a::乙}}{{setvar::empty::}}
甲={{getvar::a}}；空={{getvar::empty}}；未知={{getvar::missing}}；{{user_name}}/{{char}}
{{trim}}`
	got := Render(text, ctx)
	want := "甲=乙；空=；未知=；Alice/旁白"
	if strings.TrimSpace(got) != want {
		t.Fatalf("Render = %q，想要 %q", got, want)
	}
	if ctx.Variables["a"] != "乙" {
		t.Fatalf("setvar 没写入：%#v", ctx.Variables)
	}
	if _, ok := ctx.Variables["empty"]; ok {
		t.Fatal("空 setvar 应该删除变量")
	}
}

func TestRenderLastUserMessageCanRemainDynamic(t *testing.T) {
	ctx := &Context{Values: map[string]string{}}
	if got := Render("{{lastUserMessage}}", ctx); got != "{{lastUserMessage}}" {
		t.Fatalf("nil 时应保留：%q", got)
	}
	value := "你好"
	ctx.LastUserMessage = &value
	if got := Render("{{lastUserMessage}}", ctx); got != "你好" {
		t.Fatalf("给了值应替换：%q", got)
	}
}

func TestRenderDoesNotRecurse(t *testing.T) {
	ctx := &Context{Values: macro.Values("A", "B", ""), Variables: map[string]string{"x": "{{user}}"}}
	if got := Render("{{getvar::x}}", ctx); got != "A" {
		t.Fatalf("静态宏应在最后替换，但 ST 变量值不递归：%q", got)
	}
	ctx.Variables["y"] = "{{getvar::x}}"
	if got := Render("{{getvar::y}}", ctx); got != "{{getvar::x}}" {
		t.Fatalf("getvar 不应递归：%q", got)
	}
}

func TestAnalyze(t *testing.T) {
	s := Analyze("{{//x}}{{setvar::a::1}}{{getvar::a}}{{trim}}{{lastUserMessage}}{{user_name}}")
	if s.Comments != 1 || s.Trim != 1 || s.LastUser != 1 || len(s.SetVars) != 1 || len(s.GetVars) != 1 {
		t.Fatalf("Analyze = %+v", s)
	}
}
