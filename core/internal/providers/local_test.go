package providers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// 本地假上游（dummy / fallback）：**不联网**，把这句确定性的话分片吐出来 ——
// 分片与停顿是刻意的（不然 `pending` / `streaming` / 耗时 这些状态根本观察不到）。
func TestLocalDummyStreamsDeterministically(t *testing.T) {
	provider := Provider{ID: "dummy", Vendor: VendorDummy}
	started := time.Now()
	var deltas []Delta
	result, err := NewClient(provider).Chat(context.Background(), provider, Request{
		Model: "dummy", SessionID: "01a0e965-4156-741d-a8bd-5e16f28eb90d",
		Messages: []ChatMessage{{Role: "user", Content: "在吗"}},
	}, "（测试用空模型）", func(delta Delta) { deltas = append(deltas, delta) })
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "（测试用空模型）" {
		t.Fatalf("正文 = %q", result.Text)
	}
	if len(result.Usage) != 0 {
		t.Fatalf("没发给任何上游 ⇒ 没有用量：%s", result.Usage)
	}
	if len(deltas) != len([]rune(result.Text)) {
		t.Fatalf("该一字一片：%d 片 / %d 字", len(deltas), len([]rune(result.Text)))
	}
	for _, delta := range deltas {
		if delta.Reasoning != "" {
			t.Fatalf("dummy 不吐思考：%+v", delta)
		}
	}
	// 真的花了一点时间（不是瞬时返回）—— 这就是 `pending`/`streaming` 能被看见的前提
	if elapsed := time.Since(started); elapsed < localFirstDelay {
		t.Fatalf("本地假上游该先「想」一下再吐（耗时 %s）", elapsed)
	}
	if last, ok := LastSent(); !ok || !strings.HasSuffix(last.URL, "/chat/completions") {
		t.Fatalf("本地路径也要留档（调试页看得到本来会发什么）：%+v（%v）", last, ok)
	}
}

// 本地假上游同样**可以被按停**：ctx 一取消就退出，不留半句。
func TestLocalDummyStopsOnCancel(t *testing.T) {
	provider := Provider{ID: "dummy", Vendor: VendorDummy}
	ctx, cancel := context.WithCancel(context.Background())
	// 收到第一片就按停（模拟用户按 /stop）
	result, err := NewClient(provider).Chat(ctx, provider, Request{
		Model: "dummy", SessionID: "s1",
	}, "（测试用空模型）", func(Delta) { cancel() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("该回 context.Canceled：%v", err)
	}
	if result.Text != "" {
		t.Fatalf("被停的那一轮不该有整段正文：%q", result.Text)
	}
}

// **发之前**先留档（含请求头，密钥打码）—— `/debug/last-payload` 靠它，之前它一直是空的。
func TestLastPayloadIsRecordedBeforeSending(t *testing.T) {
	stub := newStubUpstream(t, 200, "text/event-stream", "data: {\"choices\":[{\"delta\":{\"content\":\"好\"}}]}\n\ndata: [DONE]\n\n")
	provider := Provider{
		ID: "stub", Vendor: VendorOpenCodeGo, BaseURL: stub.baseURL(), APIKey: "sk-super-secret",
	}
	session := "01a0e965-4156-741d-a8bd-5e16f28eb90d"
	if _, err := NewClient(provider).Chat(context.Background(), provider, Request{
		Model: "deepseek-v4.1-flash", SessionID: session,
		Messages: []ChatMessage{{Role: "user", Content: "在吗"}},
	}, "", nil); err != nil {
		t.Fatal(err)
	}
	payload, ok := LastSent()
	if !ok {
		t.Fatal("发过就该留档")
	}
	if payload.Method != "POST" || !strings.HasSuffix(payload.URL, "/chat/completions") {
		t.Fatalf("载荷 = %+v", payload)
	}
	// 头也要回显（网关拒的往往是头不是体）—— 但密钥必须打码
	if payload.Headers["Authorization"] != "Bearer ***" {
		t.Fatalf("密钥该打码：%q", payload.Headers["Authorization"])
	}
	if payload.Headers["X-Opencode-Session"] != session {
		t.Fatalf("会话头该原样（就是调用的 SessionID）：%q", payload.Headers["X-Opencode-Session"])
	}
	if !strings.Contains(string(payload.Body), `"stream":true`) {
		t.Fatalf("体里该是流式：%s", payload.Body)
	}
}
