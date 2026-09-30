package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// liveUsageBody：2026-09-30 拿真 key 打 `GET /zen/go/v1/usage` 回的**原样**（key 不在体里）。
// 数值会变，形状不变 —— 测试钉的是**形状**（所以这里也用一份固定样例，不联网）。
const liveUsageBody = `{"usage":{"rolling":{"status":"ok","percent":3,"resetsAt":"2026-09-30T13:47:01.482Z"},` +
	`"weekly":{"status":"ok","percent":1,"resetsAt":"2026-10-05T00:00:00.000Z"},` +
	`"monthly":{"status":"ok","percent":0,"resetsAt":"2026-10-28T18:39:36.000Z"}}}`

// usageServer：一个假的用量端点。`handler` 拿到的是**已记下来**的那一发请求。
func usageServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *http.Request) {
	t.Helper()
	var seen *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Clone(r.Context())
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return server, seen
}

// 一发请求该长什么样：GET /v1/usage、Bearer 认证、带 accept、自报身份（不带会话头 —— 用量不属于任何会话）。
func TestOpencodeUsageRequestShape(t *testing.T) {
	server, _ := usageServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(liveUsageBody))
	})

	// base 故意写成 chat 用的那个（以 /v1 结尾）—— 拼接必须剥掉再补，不能拼成 /v1/v1/usage
	usage, err := OpencodeUsageAt(context.Background(), server.URL+"/v1", "sk-test-key")
	if err != nil {
		t.Fatalf("OpencodeUsageAt 出错: %v", err)
	}
	if usage.Endpoint != server.URL+"/v1/usage" {
		t.Fatalf("endpoint = %q，想要 %q", usage.Endpoint, server.URL+"/v1/usage")
	}
	if usage.Plan != "OpenCode Go" {
		t.Fatalf("plan = %q", usage.Plan)
	}
	if usage.FetchedAt.IsZero() {
		t.Fatal("FetchedAt 没填")
	}

	// 三个窗口：顺序、id、数值、状态、重置时刻
	want := []struct {
		id      string
		percent float64
		resets  string
	}{
		{"rolling-5h", 3, "2026-09-30T13:47:01.482Z"},
		{"weekly", 1, "2026-10-05T00:00:00Z"},
		{"monthly", 0, "2026-10-28T18:39:36Z"},
	}
	if len(usage.Windows) != len(want) {
		t.Fatalf("窗口数 = %d，想要 %d（%+v）", len(usage.Windows), len(want), usage.Windows)
	}
	for index, expect := range want {
		window := usage.Windows[index]
		if window.ID != expect.id || window.Percent != expect.percent {
			t.Fatalf("窗口[%d] = %+v，想要 id=%s percent=%v", index, window, expect.id, expect.percent)
		}
		if window.Label == "" {
			t.Fatalf("窗口 %s 没有 label", window.ID)
		}
		if window.Status != UsageStatusOK {
			t.Fatalf("窗口 %s status = %q", window.ID, window.Status)
		}
		resets, err := time.Parse(time.RFC3339, expect.resets)
		if err != nil || !window.ResetsAt.Equal(resets) {
			t.Fatalf("窗口 %s resetsAt = %s，想要 %s", window.ID, window.ResetsAt, expect.resets)
		}
	}
}

// 发出去的那一发（头与路径）—— 用真发一次留下的请求钉。
func TestOpencodeUsageRequestHeaders(t *testing.T) {
	var seen *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Clone(r.Context())
		_, _ = w.Write([]byte(liveUsageBody))
	}))
	defer server.Close()

	if _, err := OpencodeUsageAt(context.Background(), server.URL, "sk-test-key"); err != nil {
		t.Fatalf("出错: %v", err)
	}
	if seen.Method != http.MethodGet {
		t.Fatalf("method = %s，想要 GET", seen.Method)
	}
	if seen.URL.Path != OpencodeUsagePath {
		t.Fatalf("path = %s，想要 %s", seen.URL.Path, OpencodeUsagePath)
	}
	if got := seen.Header.Get("Authorization"); got != "Bearer sk-test-key" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := seen.Header.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept = %q", got)
	}
	if got := seen.Header.Get("User-Agent"); !strings.HasPrefix(got, "pi (") {
		t.Fatalf("User-Agent = %q（默认该是 Pi 的形状）", got)
	}
	// 用量是账号级的：不该出现会话头（实测上游也不要求）
	if got := seen.Header.Get("X-Opencode-Session"); got != "" {
		t.Fatalf("不该发会话头，得到 %q", got)
	}
}

// 没 key ⇒ 当场报，**一个包都不发**。
func TestOpencodeUsageNeedsAPIKey(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = w.Write([]byte(liveUsageBody))
	}))
	defer server.Close()

	for _, key := range []string{"", "   "} {
		if _, err := OpencodeUsageAt(context.Background(), server.URL, key); err == nil {
			t.Fatalf("key = %q 时该报错", key)
		}
	}
	if called {
		t.Fatal("没 key 不该发请求")
	}
}

// 401：上游的 message 要原样带出来（那才是"为什么"）。
func TestOpencodeUsageAuthError(t *testing.T) {
	server, _ := usageServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"AuthError","message":"Unauthorized"}}`))
	})

	_, err := OpencodeUsageAt(context.Background(), server.URL, "sk-wrong")
	if err == nil {
		t.Fatal("401 该报错")
	}
	for _, want := range []string{"401", "Unauthorized"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误里缺 %q：%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "sk-wrong") {
		t.Fatalf("错误里漏了 key：%v", err)
	}
}

// 404：上游回的是整页 HTML —— 错误信息得缩成一行，别把整页灌进来。
func TestOpencodeUsageNotFoundKeepsErrorShort(t *testing.T) {
	server, _ := usageServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<html><head><title>x</title></head><body>" + strings.Repeat("y", 5000) + "</body></html>"))
	})

	_, err := OpencodeUsageAt(context.Background(), server.URL, "sk-test-key")
	if err == nil {
		t.Fatal("404 该报错")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("错误里缺状态码：%v", err)
	}
	if len(err.Error()) > 500 {
		t.Fatalf("错误正文太长（%d 字）——HTML 该被缩掉：%v", len(err.Error()), snippet(err.Error()))
	}
}

// 三个窗口缺一个 / 形状不对 ⇒ 报**哪一个**不对（不编半个结果）。
func TestOpencodeUsageRejectsMalformedWindows(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"没有 usage 对象", `{"ok":true}`},
		{"缺 monthly", `{"usage":{"rolling":{"status":"ok","percent":3,"resetsAt":"2026-09-30T13:47:01Z"},` +
			`"weekly":{"status":"ok","percent":1,"resetsAt":"2026-10-05T00:00:00Z"}}}`},
		{"percent 超范围", `{"usage":{"rolling":{"status":"ok","percent":150,"resetsAt":"2026-09-30T13:47:01Z"},` +
			`"weekly":{"status":"ok","percent":1,"resetsAt":"2026-10-05T00:00:00Z"},` +
			`"monthly":{"status":"ok","percent":0,"resetsAt":"2026-10-28T18:39:36Z"}}}`},
		{"status 不认识", `{"usage":{"rolling":{"status":"weird","percent":3,"resetsAt":"2026-09-30T13:47:01Z"},` +
			`"weekly":{"status":"ok","percent":1,"resetsAt":"2026-10-05T00:00:00Z"},` +
			`"monthly":{"status":"ok","percent":0,"resetsAt":"2026-10-28T18:39:36Z"}}}`},
		{"resetsAt 不是时间", `{"usage":{"rolling":{"status":"ok","percent":3,"resetsAt":"soon"},` +
			`"weekly":{"status":"ok","percent":1,"resetsAt":"2026-10-05T00:00:00Z"},` +
			`"monthly":{"status":"ok","percent":0,"resetsAt":"2026-10-28T18:39:36Z"}}}`},
		{"不是 JSON", `<html>nope</html>`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			server, _ := usageServer(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(test.body))
			})
			if _, err := OpencodeUsageAt(context.Background(), server.URL, "sk-test-key"); err == nil {
				t.Fatalf("该报错：%s", test.body)
			}
		})
	}
}

// `rate-limited` 是合法的 status（omp 的校验认它），别被自己的校验挡在外面。
func TestOpencodeUsageAcceptsRateLimited(t *testing.T) {
	server, _ := usageServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"usage":{"rolling":{"status":"rate-limited","percent":100,"resetsAt":"2026-09-30T13:47:01Z"},` +
			`"weekly":{"status":"ok","percent":42.5,"resetsAt":"2026-10-05T00:00:00Z"},` +
			`"monthly":{"status":"ok","percent":0,"resetsAt":"2026-10-28T18:39:36Z"}}}`))
	})

	usage, err := OpencodeUsageAt(context.Background(), server.URL, "sk-test-key")
	if err != nil {
		t.Fatalf("出错: %v", err)
	}
	if usage.Windows[0].Status != UsageStatusRateLimited || usage.Windows[0].Percent != 100 {
		t.Fatalf("rolling = %+v", usage.Windows[0])
	}
	if usage.Windows[1].Percent != 42.5 {
		t.Fatalf("weekly percent = %v（小数得保住）", usage.Windows[1].Percent)
	}
}

// ctx 超时 = **真的**会断（本仓硬规矩：不配总超时 = 界面转一辈子）。
func TestOpencodeUsageHonoursContext(t *testing.T) {
	server, _ := usageServer(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		_, _ = w.Write([]byte(liveUsageBody))
	})

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := OpencodeUsageAt(ctx, server.URL, "sk-test-key")
	if err == nil {
		t.Fatal("ctx 超时该报错")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("错误该是 DeadlineExceeded：%v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("等了 %s 才回来 —— ctx 没生效", elapsed)
	}
}

// 真打一次（**只有**显式给了 key 才跑；`-short` 也跳）。
// 只读：GET /v1/usage 不改任何东西。
func TestOpencodeUsageLive(t *testing.T) {
	if testing.Short() {
		t.Skip("-short：跳过真联网")
	}
	key := os.Getenv("OPENCODE_API_KEY")
	if key == "" {
		t.Skip("没有 OPENCODE_API_KEY：跳过（解析已由 httptest 钉住）")
	}

	usage, err := OpencodeUsage(context.Background(), key)
	if err != nil {
		t.Fatalf("真打失败: %v", err)
	}
	if len(usage.Windows) != 3 {
		t.Fatalf("窗口数 = %d，想要 3", len(usage.Windows))
	}
	for _, window := range usage.Windows {
		if window.Percent < 0 || window.Percent > 100 {
			t.Fatalf("窗口 %s percent = %v", window.ID, window.Percent)
		}
		if window.ResetsAt.IsZero() {
			t.Fatalf("窗口 %s 没有重置时刻", window.ID)
		}
	}
	// 不外泄 key：只打形状
	t.Logf("plan=%s endpoint=%s windows=%d rolling=%.1f%% weekly=%.1f%% monthly=%.1f%%",
		usage.Plan, usage.Endpoint, len(usage.Windows),
		usage.Windows[0].Percent, usage.Windows[1].Percent, usage.Windows[2].Percent)
}

// usageURL：剥 `/v1` 的那条规矩（配置里写的多半是 chat 的 base）。
func TestUsageURLJoining(t *testing.T) {
	cases := []struct{ base, want string }{
		{"", OpencodeGoUsageURL + OpencodeUsagePath},
		{"https://opencode.ai/zen/go", "https://opencode.ai/zen/go/v1/usage"},
		{"https://opencode.ai/zen/go/", "https://opencode.ai/zen/go/v1/usage"},
		{"https://opencode.ai/zen/go/v1", "https://opencode.ai/zen/go/v1/usage"},
		{"https://opencode.ai/zen/go/v1/", "https://opencode.ai/zen/go/v1/usage"},
		{"http://127.0.0.1:8080/zen/go/v1", "http://127.0.0.1:8080/zen/go/v1/usage"},
	}
	for _, test := range cases {
		if got := usageURL(test.base); got != test.want {
			t.Fatalf("usageURL(%q) = %q，想要 %q", test.base, got, test.want)
		}
	}
}
