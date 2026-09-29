package providers

import "testing"

// 一条规则覆盖所有情况：**系统提示词一致的共用一个 id**；不同的按 (会话, 提示词) 确定性派生。
func TestSessionIDFor(t *testing.T) {
	conversation := "01a0e965-4156-741d-a8bd-5e16f28eb90d"
	mainPrompt := "你是跑团主持人。<state>季节 = 初冬</state>"

	// 主聊天 / 同提示词的任何扩展 ⇒ 就是会话 id 本身（与 Pi 同构）
	if got := SessionIDFor(conversation, mainPrompt, mainPrompt); got != conversation {
		t.Fatalf("同提示词该共用一个 id，得到 %q", got)
	}
	// 提示词不同的辅助调用 ⇒ 派生一个（且**稳定**）
	summaryPrompt := "把下面这段收成叙事："
	first := SessionIDFor(conversation, summaryPrompt, mainPrompt)
	second := SessionIDFor(conversation, summaryPrompt, mainPrompt)
	if first != second {
		t.Fatalf("派生必须可重现：%q vs %q", first, second)
	}
	if first == conversation {
		t.Fatal("不同提示词不该蹭主线的 id")
	}
	if len(first) != 36 {
		t.Fatalf("该长得像个 UUID：%q", first)
	}
	// 不同提示词 / 不同会话 ⇒ 不同 id
	if other := SessionIDFor(conversation, "另一个提示词", mainPrompt); other == first {
		t.Fatal("不同提示词该得到不同 id")
	}
	if other := SessionIDFor("别的会话", summaryPrompt, mainPrompt); other == first {
		t.Fatal("不同会话该得到不同 id")
	}
	// 没有会话上下文（将来的离线任务）：仍然**确定性**，绝不随机
	offline := SessionIDFor("", summaryPrompt, "")
	if offline != SessionIDFor("", summaryPrompt, "") {
		t.Fatal("没有会话上下文时也必须稳定")
	}
}

// 派生出来的 id 要能直接用在线上（网关接受的是 UUID 形状的稳定串）。
func TestDerivedSessionIDIsUsableOnTheWire(t *testing.T) {
	provider := opencodeProvider()
	id := SessionIDFor("01a0e965-4156-741d-a8bd-5e16f28eb90d", "摘要提示词", "主提示词")
	request, err := Build(provider, Request{Model: "m", SessionID: id, Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := request.Header.Get("x-opencode-session"); got != id {
		t.Fatalf("线上会话头 = %q", got)
	}
}
