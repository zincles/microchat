package tg

import (
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
)

// unknownCommandReply 只看形状（斜杠开头、非空）：
//   - 命中 ⇒ 回复含原文与可用清单；
//   - 不命中 ⇒ ok=false，交给文本入口真发一轮。
//
// 注意语义边界：/start 这类已知命令在这里**也**返回 true —— 函数不认名单，
// 已知命令靠命令表派发接走，根本到不了这儿。
func TestUnknownCommandReply(t *testing.T) {
	reply, ok := unknownCommandReply("/foo")
	if !ok {
		t.Fatal("斜杠命令该命中")
	}
	for _, want := range []string{"/foo", "/start", "/status", "/help"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("回复该含 %q：%q", want, reply)
		}
	}

	// 已知命令同样命中（形状判定，名单不在这儿管）。
	if _, ok := unknownCommandReply("/start"); !ok {
		t.Fatal("/start 也命中：函数只管形状，已知由处理器顺序兜")
	}

	// 不是斜杠开头 / 空串 ⇒ 不命中，交给文本入口真发一轮。
	for _, text := range []string{"hello", "", "  /foo", "hello /foo"} {
		if reply, ok := unknownCommandReply(text); ok {
			t.Fatalf("%q 不该命中：%q", text, reply)
		}
	}

	// 光一个斜杠、两个斜杠：形状够格 ⇒ 命中（没有可猜的近似命令）。
	for _, text := range []string{"/", "//"} {
		reply, ok := unknownCommandReply(text)
		if !ok {
			t.Fatalf("%q 该命中（斜杠开头非空）", text)
		}
		if !strings.Contains(reply, text) {
			t.Fatalf("%q 的回复该含原文：%q", text, reply)
		}
	}
}

// commandScopes：四个批量作用域，类型各自正确（清旧菜单挨个招呼用）。
func TestCommandScopes(t *testing.T) {
	scopes := commandScopes()
	if len(scopes) != 4 {
		t.Fatalf("该四个作用域：%d", len(scopes))
	}
	want := map[string]bool{
		"default": false, "all_private_chats": false,
		"all_group_chats": false, "all_chat_administrators": false,
	}
	for _, scope := range scopes {
		switch scope.(type) {
		case *models.BotCommandScopeDefault:
			want["default"] = true
		case *models.BotCommandScopeAllPrivateChats:
			want["all_private_chats"] = true
		case *models.BotCommandScopeAllGroupChats:
			want["all_group_chats"] = true
		case *models.BotCommandScopeAllChatAdministrators:
			want["all_chat_administrators"] = true
		default:
			t.Fatalf("不认识的作用域类型：%T", scope)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("缺作用域：%s", name)
		}
	}
}

// startupGreeting：绑了人才发；没绑（<=0）不发。
func TestStartupGreeting(t *testing.T) {
	if _, ok := startupGreeting(0); ok {
		t.Fatal("没绑不该发")
	}
	if _, ok := startupGreeting(-1); ok {
		t.Fatal("负数不该发")
	}
	text, ok := startupGreeting(12345)
	if !ok || text == "" {
		t.Fatalf("绑了该发：%q %v", text, ok)
	}
	if !strings.Contains(text, "/status") {
		t.Fatalf("问候该提 /status：%q", text)
	}
}
