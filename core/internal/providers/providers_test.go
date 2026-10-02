package providers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"microchat/internal/config"
	"testing"
	"time"
)

func opencodeProvider() Provider {
	return Provider{ID: "go", Kind: KindOpenCodeGo, APIKey: "sk-test"}
}

// 头：kind 决定必需头，identity 决定自报身份，用户 headers 最后合并。
func TestHeadersPerKindAndIdentity(t *testing.T) {
	session := "01a0e965-4156-741d-a8bd-5e16f28eb90d"
	cases := []struct {
		name     string
		provider Provider
		want     map[string]string // 必须**恰好**等于
		wantUA   string            // 空 = 不该有 User-Agent
	}{
		{"opencode-go + 没写 identity ⇒ **默认 pi**", opencodeProvider(), map[string]string{
			"X-Opencode-Session": session,
			"X-Opencode-Client":  "pi",
			"Accept":             "text/event-stream",
		}, ""}, // 单独断言 UA 形状
		{"opencode-go + 显式 microchat", func() Provider {
			p := opencodeProvider()
			p.Identity = IdentityMicrochat
			return p
		}(), map[string]string{
			"X-Opencode-Session": session,
			"X-Opencode-Client":  "microchat",
			"Accept":             "text/event-stream", // 流式要 SSE
		}, "microchat/" + Version},
		{"opencode-go + pi（逐字照 Pi）", func() Provider {
			p := opencodeProvider()
			p.Identity = IdentityPi
			return p
		}(), map[string]string{
			"X-Opencode-Session": session,
			"X-Opencode-Client":  "pi",
			"Accept":             "text/event-stream",
		}, ""}, // 单独断言（格式带内核版本）
		{"openrouter：x-session-id + 归属三样", Provider{ID: "or", Kind: KindOpenRouter}, map[string]string{
			"X-Session-Id":            session,
			"Accept":                  "text/event-stream",
			"Http-Referer":            "https://github.com/zincles/microchat",
			"X-Openrouter-Title":      "microchat",
			"X-Openrouter-Categories": "cli-agent",
		}, ""},
		{"openai：会话亲和三条", Provider{ID: "oa", Kind: KindOpenAI}, map[string]string{
			"Session_Id":          session,
			"X-Client-Request-Id": session,
			"X-Session-Affinity":  session,
			"Accept":              "text/event-stream",
		}, ""},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			request, err := Build(test.provider, Request{Model: "m", SessionID: session, Stream: true})
			if err != nil {
				t.Fatal(err)
			}
			for name, want := range test.want {
				if got := request.Header.Get(name); got != want {
					t.Fatalf("头 %s = %q，想要 %q", name, got, want)
				}
			}
			gotUA := request.Header.Get("User-Agent")
			if test.wantUA != "" && gotUA != test.wantUA {
				t.Fatalf("User-Agent = %q，想要 %q", gotUA, test.wantUA)
			}
			if test.wantUA == "" && !strings.HasPrefix(gotUA, "pi (") {
				t.Fatalf("pi 身份的 UA 该是 `pi (<platform> <release>; <arch>)`，得到 %q", gotUA)
			}
			// 密钥永远只走 Authorization
			if test.provider.APIKey != "" && request.Header.Get("Authorization") != "Bearer "+test.provider.APIKey {
				t.Fatalf("Authorization = %q", request.Header.Get("Authorization"))
			}
		})
	}
}

// bare：什么都不装（Go 会补自己的 UA）—— 但仍要求会话 id（那是路由必需，不属于伪装）。
func TestBareIdentityStillNeedsSession(t *testing.T) {
	p := opencodeProvider()
	p.Identity = IdentityBare
	request, err := Build(p, Request{Model: "m", SessionID: "abc", Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	if ua := request.Header.Get("User-Agent"); ua != "" {
		t.Fatalf("bare 不该自报身份，得到 %q", ua)
	}
	if request.Header.Get("x-opencode-session") != "abc" {
		t.Fatal("会话头仍要发（路由必需，不是伪装）")
	}
}

// 用户 headers 是最后一句话。
func TestUserHeadersWin(t *testing.T) {
	p := opencodeProvider()
	p.Headers = map[string]string{"x-opencode-client": "mine", "X-Extra": "1"}
	request, err := Build(p, Request{Model: "m", SessionID: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if got := request.Header.Get("x-opencode-client"); got != "mine" {
		t.Fatalf("用户该能覆盖：%q", got)
	}
	if request.Header.Get("X-Extra") != "1" {
		t.Fatal("用户自定义头要带上")
	}
}

// 体：照 Pi 的条件（这是他那边最容易抄错的部分）。
func TestBodyShape(t *testing.T) {
	session := "01a0e965-4156-741d-a8bd-5e16f28eb90d"
	messages := []ChatMessage{{Role: "user", Content: "在吗"}}
	decode := func(t *testing.T, request *http.Request) map[string]any {
		t.Helper()
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatal(err)
		}
		return parsed
	}

	t.Run("openai + auto：发 prompt_cache_key，不发 24h", func(t *testing.T) {
		request, err := Build(Provider{ID: "oa", Kind: KindOpenAI}, Request{
			Model: "m", Messages: messages, SessionID: session, Stream: true, MaxTokens: 64})
		if err != nil {
			t.Fatal(err)
		}
		body := decode(t, request)
		if body["prompt_cache_key"] != session {
			t.Fatalf("prompt_cache_key = %v", body["prompt_cache_key"])
		}
		if _, exists := body["prompt_cache_retention"]; exists {
			t.Fatal("只有 long 才发 24h")
		}
		if body["store"] != false {
			t.Fatal("OpenAI 该发 store:false")
		}
		if body["max_completion_tokens"] != float64(64) {
			t.Fatalf("max_completion_tokens = %v", body["max_completion_tokens"])
		}
		if body["stream"] != true {
			t.Fatal("一直是流式")
		}
	})

	t.Run("cache=none：子调用不写缓存（照 Pi）", func(t *testing.T) {
		request, err := Build(Provider{ID: "oa", Kind: KindOpenAI}, Request{
			Model: "m", Messages: messages, SessionID: session, CacheRetention: CacheNone, Stream: true})
		if err != nil {
			t.Fatal(err)
		}
		body := decode(t, request)
		if _, exists := body["prompt_cache_key"]; exists {
			t.Fatal("none 就不该发 prompt_cache_key")
		}
		if _, exists := body["prompt_cache_retention"]; exists {
			t.Fatal("none 更不该发 24h")
		}
	})

	t.Run("opencode-go：没有缓存参数，但有 stream_options", func(t *testing.T) {
		request, err := Build(opencodeProvider(), Request{
			Model: "deepseek-v4-flash", Messages: messages, SessionID: session, Stream: true})
		if err != nil {
			t.Fatal(err)
		}
		body := decode(t, request)
		if _, exists := body["prompt_cache_key"]; exists {
			t.Fatal("OpenCode 不是 api.openai.com ⇒ 不发 prompt_cache_key")
		}
		if _, exists := body["store"]; exists {
			t.Fatal("OpenCode 不发 store")
		}
		usage, ok := body["stream_options"].(map[string]any)
		if !ok || usage["include_usage"] != true {
			t.Fatalf("stream_options = %v", body["stream_options"])
		}
	})

	t.Run("opencode 系一律用 max_tokens（chat 体，Pi 生成器强制）", func(t *testing.T) {
		for _, p := range []Provider{
			{ID: "go", Vendor: VendorOpenCodeGo},
			{ID: "oc", Vendor: VendorOpenCode},
		} {
			request, err := Build(p, Request{
				Model: "m", Messages: messages, SessionID: session, Stream: true, MaxTokens: 64})
			if err != nil {
				t.Fatal(err)
			}
			body := decode(t, request)
			if body["max_tokens"] != float64(64) {
				t.Fatalf("%s max_tokens = %v", p.Vendor, body["max_tokens"])
			}
			if _, exists := body["max_completion_tokens"]; exists {
				t.Fatalf("%s 不该发 max_completion_tokens：%v", p.Vendor, body)
			}
		}
	})

	t.Run("long：发 24h（能支持时）", func(t *testing.T) {
		request, err := Build(Provider{ID: "oa", Kind: KindOpenAI}, Request{
			Model: "m", Messages: messages, SessionID: session, CacheRetention: CacheLong, Stream: true})
		if err != nil {
			t.Fatal(err)
		}
		body := decode(t, request)
		if body["prompt_cache_retention"] != "24h" {
			t.Fatalf("long 该发 24h：%v", body["prompt_cache_retention"])
		}
	})
}

// 会话 id 必填：忘了传就在**构造期**报，别让它变成"上游看来时好时坏"的静默 bug。
func TestSessionIDIsRequired(t *testing.T) {
	if _, err := Build(opencodeProvider(), Request{Model: "m"}); err == nil {
		t.Fatal("空 SessionID 必须报错")
	}
	if _, err := Build(opencodeProvider(), Request{Model: "m", SessionID: "   "}); err == nil {
		t.Fatal("空白 SessionID 也算没传")
	}
}

// 真发一发到假上游：断言**服务端实际收到**的头与体（这才是网关的视角）。
func TestWhatTheGatewaySees(t *testing.T) {
	var seenHeaders http.Header
	var seenBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHeaders = r.Header.Clone()
		_ = json.NewDecoder(r.Body).Decode(&seenBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"好"}}]}`))
	}))
	defer upstream.Close()

	session := "01a0e965-4156-741d-a8bd-5e16f28eb90d"
	provider := Provider{ID: "fake", Kind: KindOpenCodeGo, BaseURL: upstream.URL, APIKey: "sk-x"}
	request, err := Build(provider, Request{
		Model: "deepseek-v4-flash", SessionID: session, Stream: true,
		Messages: []ChatMessage{{Role: "user", Content: "在吗"}}})
	if err != nil {
		t.Fatal(err)
	}
	response, err := NewClient(provider).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("状态码 = %d", response.StatusCode)
	}

	if got := seenHeaders.Get("x-opencode-session"); got != session {
		t.Fatalf("网关看到 x-opencode-session = %q", got)
	}
	if got := seenHeaders.Get("x-opencode-client"); got != "pi" {
		t.Fatalf("网关看到 x-opencode-client = %q", got)
	}
	if got := seenHeaders.Get("User-Agent"); !strings.HasPrefix(got, "pi (") {
		t.Fatalf("网关看到 User-Agent = %q（默认 = Pi 的形状）", got)
	}
	if got := seenHeaders.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("网关看到 Accept = %q", got)
	}
	if seenBody["model"] != "deepseek-v4-flash" || seenBody["stream"] != true {
		t.Fatalf("网关看到体 = %v", seenBody)
	}
}

// 调试回显：头**连密钥一起打码**（否则调试页会把密钥漏在响应里）。
func TestSnapshotMasksSecrets(t *testing.T) {
	request, err := Build(opencodeProvider(), Request{Model: "m", SessionID: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := Snapshot(request, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if payload.Headers["Authorization"] != "Bearer ***" {
		t.Fatalf("密钥没打码：%q", payload.Headers["Authorization"])
	}
	if payload.Headers["X-Opencode-Session"] != "abc" {
		t.Fatal("会话头该原样回显（它就是排查要看的）")
	}
	if !strings.HasSuffix(payload.URL, "/chat/completions") {
		t.Fatalf("URL = %q", payload.URL)
	}
	if len(payload.Body) == 0 {
		t.Fatal("体该记下来")
	}
}

// 回归：**配置里的每个字段都必须真的流到请求上**。
//
// 这条测试是为了"identity 声明了却从没被转过去 ⇒ 配置里写 `"identity":"pi"` 被静默忽略"
// 那类 bug 立的 —— 手搓 `providers.Provider{...}` 漏一个字段，就是静默失效。
func TestFromConfigCarriesEveryField(t *testing.T) {
	configured := config.Provider{
		ID: "ocgo", Name: "Go 套餐", Kind: "opencode-go",
		BaseURL: "https://opencode.ai/zen/go/v1", APIKey: "sk-x",
		Headers: map[string]string{"X-Extra": "1"}, Identity: "pi",
	}
	provider := FromConfig(configured)
	request, err := Build(provider, Request{Model: "deepseek-v4-flash", SessionID: "s-1", Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	// identity 必须活到请求上（默认 microchat，配了 pi 就得是 pi）
	if ua := request.Header.Get("User-Agent"); !strings.HasPrefix(ua, "pi (") {
		t.Fatalf("identity 被丢了：User-Agent = %q", ua)
	}
	if request.Header.Get("x-opencode-client") != "pi" {
		t.Fatalf("x-opencode-client = %q", request.Header.Get("x-opencode-client"))
	}
	if request.Header.Get("X-Extra") != "1" {
		t.Fatal("用户 headers 被丢了")
	}
	if request.Header.Get("Authorization") != "Bearer sk-x" {
		t.Fatal("密钥被丢了")
	}
	if request.URL.String() != "https://opencode.ai/zen/go/v1/chat/completions" {
		t.Fatalf("端点 = %q", request.URL.String())
	}
	// 默认（什么都没配）⇒ **Pi 的形状**
	request, err = Build(FromConfig(config.Provider{ID: "x", Kind: "opencode-go"}), Request{Model: "m", SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if ua := request.Header.Get("User-Agent"); !strings.HasPrefix(ua, "pi (") {
		t.Fatalf("默认身份 = %q（该是 Pi 的形状）", ua)
	}
}

// 预设清单：界面拿它生成"选一个内置 provider"的下拉；顺序固定。
func TestPresets(t *testing.T) {
	list := Presets()
	if len(list) < 5 {
		t.Fatalf("预设太少：%d", len(list))
	}
	byKind := map[Kind]PresetInfo{}
	for _, item := range list {
		byKind[item.Kind] = item
	}
	goPreset, ok := byKind[KindOpenCodeGo]
	if !ok {
		t.Fatal("必须有 opencode-go")
	}
	if goPreset.SessionHeader != "x-opencode-session" {
		t.Fatalf("会话头 = %q", goPreset.SessionHeader)
	}
	if !goPreset.NeedsKey || goPreset.KeyEnv != "OPENCODE_API_KEY" {
		t.Fatalf("密钥口径 = %+v", goPreset)
	}
	if goPreset.BaseURL != "https://opencode.ai/zen/go/v1" {
		t.Fatalf("端点 = %q", goPreset.BaseURL)
	}
	if len(goPreset.Identities) != 3 {
		t.Fatalf("三种身份都该可选：%v", goPreset.Identities)
	}
	if dummy := byKind[KindDummy]; dummy.NeedsKey {
		t.Fatal("dummy 不需要密钥")
	}
	// DeepSeek 官方：端点 + 密钥 env + 官方思考字段（`reasoning_content`）
	deepseek, ok := byKind[KindDeepseek]
	if !ok {
		t.Fatal("必须有 deepseek")
	}
	if deepseek.BaseURL != "https://api.deepseek.com" {
		t.Fatalf("端点 = %q", deepseek.BaseURL)
	}
	if !deepseek.NeedsKey || deepseek.KeyEnv != "DEEPSEEK_API_KEY" {
		t.Fatalf("密钥口径 = %+v", deepseek)
	}
	if deepseek.SessionHeader != "" {
		t.Fatalf("DeepSeek 官方不发会话头：%q", deepseek.SessionHeader)
	}
}

// DeepSeek 官方：拼出来的那一发（端点、stream_options、思考回传字段）。
func TestDeepseekBuild(t *testing.T) {
	messages := []ChatMessage{
		{Role: "user", Content: "在吗"},
		{Role: "assistant", Content: "在。", Reasoning: "他大概想问路。"},
	}
	request, err := Build(Provider{ID: "ds", Kind: KindDeepseek}, Request{
		Model: "deepseek-chat", SessionID: "s", Stream: true, Messages: messages})
	if err != nil {
		t.Fatal(err)
	}
	if request.URL.String() != "https://api.deepseek.com/chat/completions" {
		t.Fatalf("端点 = %q", request.URL.String())
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Messages      []map[string]any `json:"messages"`
		Stream        bool             `json:"stream"`
		StreamOptions map[string]any   `json:"stream_options"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	if !parsed.Stream || parsed.StreamOptions["include_usage"] != true {
		t.Fatalf("流式该带 stream_options.include_usage：%s", body)
	}
	if got := parsed.Messages[1]["reasoning_content"]; got != "他大概想问路。" {
		t.Fatalf("assistant 该用官方字段名回传思考：%+v", parsed.Messages[1])
	}
	// 密钥来自环境变量（没配 key 也能用 `DEEPSEEK_API_KEY` 顶上）
	t.Setenv("DEEPSEEK_API_KEY", "sk-test")
	wire := ApplyPreset(Provider{ID: "ds", Kind: KindDeepseek})
	if wire.APIKey != "sk-test" {
		t.Fatalf("密钥该从环境变量来：%q", wire.APIKey)
	}
	if wire.BaseURL != "https://api.deepseek.com" {
		t.Fatalf("端点该从预设来：%q", wire.BaseURL)
	}
}

// 旧命名（`openai-compat`）必须仍按 OpenAI 兼容对待 —— 否则老配置会静默失去 stream_options。
func TestLegacyOpenAICompatKind(t *testing.T) {
	request, err := Build(Provider{ID: "old", Kind: KindOpenAICompat, BaseURL: "https://example.invalid/v1"},
		Request{Model: "m", SessionID: "s", Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed["stream_options"]; !ok {
		t.Fatal("老 kind 也该发 stream_options（否则流式下拿不到 usage）")
	}
	if request.URL.String() != "https://example.invalid/v1/chat/completions" {
		t.Fatalf("端点 = %q", request.URL.String())
	}
}

// Accept 按流式区分：**流式要 text/event-stream**（SSE 的正确语义；omp 那份日志就这么发）。
func TestAcceptFollowsStreaming(t *testing.T) {
	provider := opencodeProvider()
	streaming, err := Build(provider, Request{Model: "m", SessionID: "s", Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := streaming.Header.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("流式 Accept = %q", got)
	}
	plain, err := Build(provider, Request{Model: "m", SessionID: "s", Stream: false})
	if err != nil {
		t.Fatal(err)
	}
	if got := plain.Header.Get("Accept"); got != "application/json" {
		t.Fatalf("非流式 Accept = %q", got)
	}
}

// reasoning_effort：只在我们显式给了时才发（各家取值集合不同 ⇒ 原样透传，不映射）。
func TestReasoningEffortPassthrough(t *testing.T) {
	provider := opencodeProvider()
	request, err := Build(provider, Request{Model: "m", SessionID: "s", Stream: true, ReasoningEffort: strPtr("max")})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(request.Body)
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["reasoning_effort"] != "max" {
		t.Fatalf("reasoning_effort = %v", parsed["reasoning_effort"])
	}
	// 没给就不发
	request, err = Build(provider, Request{Model: "m", SessionID: "s", Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(request.Body)
	again := map[string]any{}
	if err := json.Unmarshal(body, &again); err != nil {
		t.Fatal(err)
	}
	if _, exists := again["reasoning_effort"]; exists {
		t.Fatal("没给就不该发 reasoning_effort")
	}
}

func strPtr(value string) *string { return &value }

// 思考**回传上游**：assistant 消息带着当时的思考时要补回同一个消息里（字段名随 provider）。
func TestReasoningReplay(t *testing.T) {
	messages := []ChatMessage{
		{Role: "user", Content: "在吗"},
		{Role: "assistant", Content: "在。", Reasoning: "他大概想问路。"},
	}
	request, err := Build(opencodeProvider(), Request{Model: "m", SessionID: "s", Stream: true, Messages: messages})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(request.Body)
	var parsed struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	if got := parsed.Messages[1]["reasoning_content"]; got != "他大概想问路。" {
		t.Fatalf("assistant 那条该带上思考：%+v", parsed.Messages[1])
	}
	if _, exists := parsed.Messages[0]["reasoning_content"]; exists {
		t.Fatal("user 那条不该有思考")
	}
	// 不支持回传的 kind（标准 OpenAI 兼容）⇒ 一个字都不发
	plain, err := Build(Provider{ID: "cmp", Kind: KindOpenAICompat, BaseURL: "https://x.invalid/v1"},
		Request{Model: "m", SessionID: "s", Messages: messages})
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(plain.Body)
	parsed.Messages = nil
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	for _, message := range parsed.Messages {
		if _, exists := message["reasoning_content"]; exists {
			t.Fatalf("这个 kind 不该回传思考：%+v", message)
		}
	}
}

// 主选三种：界面新建渠道时只该看到它们（其余是便利预设）。
func TestPrimaryPresets(t *testing.T) {
	primary := map[Kind]bool{}
	for _, item := range Presets() {
		if item.Primary {
			primary[item.Kind] = true
		}
	}
	for _, want := range []Kind{KindDummy, KindOpenAICompat, KindOpenCodeGo} {
		if !primary[want] {
			t.Fatalf("%s 该是主选：%+v", want, primary)
		}
	}
	if len(primary) != 3 {
		t.Fatalf("主选只该有三种：%+v", primary)
	}
}

// 三个渠道级选项：UA 覆写（优先最高）、会话头（nil/""/自定义）、思考字段（nil/""/自定义）。
func TestProviderLevelOverrides(t *testing.T) {
	base := func() Provider { return Provider{ID: "p", Kind: KindOpenCodeGo, APIKey: "k"} }
	build := func(t *testing.T, provider Provider, messages []ChatMessage) (*http.Request, map[string]any) {
		t.Helper()
		request, err := Build(provider, Request{Model: "m", SessionID: "sess", Stream: true, Messages: messages})
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(request.Body)
		var parsed map[string]any
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatal(err)
		}
		return request, parsed
	}

	t.Run("UA 覆写优先最高（高过 identity 与服务器默认）", func(t *testing.T) {
		provider := base()
		provider.Identity = IdentityBare // 本来什么都不发
		provider.ClientUAOverride = "oc_agent/9.9"
		request, _ := build(t, provider, nil)
		if got := request.Header.Get("User-Agent"); got != "oc_agent/9.9" {
			t.Fatalf("UA 覆写没生效：%q", got)
		}
	})

	t.Run("会话头：默认发、可关、可改名", func(t *testing.T) {
		request, _ := build(t, base(), nil) // nil = 按 kind（opencode 系发）
		if request.Header.Get("x-opencode-session") != "sess" {
			t.Fatal("opencode-go 默认该发会话头")
		}
		off := ""
		provider := base()
		provider.SessionHeader = &off
		request, _ = build(t, provider, nil)
		if request.Header.Get("x-opencode-session") != "" {
			t.Fatal("空串 = 明确不发")
		}
		other := "x-my-session"
		provider.SessionHeader = &other
		request, _ = build(t, provider, nil)
		if request.Header.Get("x-my-session") != "sess" || request.Header.Get("x-opencode-session") != "" {
			t.Fatal("改名后该只发新的那个名字")
		}
	})

	t.Run("思考字段：默认 reasoning_content、可关、可改名", func(t *testing.T) {
		messages := []ChatMessage{{Role: "assistant", Content: "苹果。", Reasoning: "想一下"}}
		_, body := build(t, base(), messages)
		first := body["messages"].([]any)[0].(map[string]any)
		if first["reasoning_content"] != "想一下" {
			t.Fatalf("opencode-go 默认该回传：%+v", first)
		}
		off := ""
		provider := base()
		provider.ReasoningField = &off
		_, body = build(t, provider, messages)
		first = body["messages"].([]any)[0].(map[string]any)
		if _, exists := first["reasoning_content"]; exists {
			t.Fatal("空串 = 不回传")
		}
		custom := "reasoning"
		provider.ReasoningField = &custom
		_, body = build(t, provider, messages)
		first = body["messages"].([]any)[0].(map[string]any)
		if first["reasoning"] != "想一下" {
			t.Fatalf("改名后该用新名字：%+v", first)
		}
	})
}

// `reasoning_content` **必须存在**的规则按 vendor 走：vendor==deepseek 必补空串；
// custom/openai-compat 保留旧的模型名启发式；其余 vendor 不补（Pi 的解法：`openai-completions.ts:1379`）。
func TestReasoningFieldPresenceForDeepSeek(t *testing.T) {
	assistant := ChatMessage{Role: "assistant", Content: "苹果。"} // 这条**没有**思考
	decode := func(t *testing.T, provider Provider, model string) map[string]any {
		t.Helper()
		request, err := Build(provider, Request{
			Model: model, SessionID: "s", Stream: true,
			Messages: []ChatMessage{{Role: "user", Content: "在吗"}, assistant},
		})
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(request.Body)
		var parsed struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatal(err)
		}
		return parsed.Messages[1]
	}

	deepseek := Provider{ID: "ds", Vendor: VendorDeepseek}
	// vendor==deepseek：字段必须在（哪怕空着，模型名无关）
	item := decode(t, deepseek, "whatever-model")
	value, exists := item["reasoning_content"]
	if !exists {
		t.Fatalf("deepseek vendor 该补上这个字段：%+v", item)
	}
	if value != "" {
		t.Fatalf("没有思考时该是空串：%+v", item)
	}
	// 其余 vendor：不带就是不带（别乱塞字段 —— 有些上游见到不认识的字段会 400）
	item = decode(t, opencodeProvider(), "deepseek-v4-flash")
	if _, exists := item["reasoning_content"]; exists {
		t.Fatalf("opencode-go vendor 不该按模型名塞这个字段：%+v", item)
	}
	item = decode(t, deepseek, "deepseek-chat")
	if _, exists := item["reasoning_content"]; !exists {
		t.Fatalf("deepseek vendor 该有这个字段：%+v", item)
	}
	// custom/openai-compat 保留旧的模型名启发式
	legacy := Provider{ID: "c", Vendor: VendorCustom, BaseURL: "https://x.invalid/v1"}
	item = decode(t, legacy, "deepseek-v4-flash")
	if _, exists := item["reasoning_content"]; !exists {
		t.Fatalf("custom + deepseek 模型名该补上这个字段：%+v", item)
	}
	item = decode(t, legacy, "claude-sonnet-5")
	if _, exists := item["reasoning_content"]; exists {
		t.Fatalf("custom + 非 deepseek 模型名不该塞这个字段：%+v", item)
	}
	// 整块关掉：reasoning_field = "" ⇒ 什么都不做
	off := ""
	provider := Provider{ID: "ds", Vendor: VendorDeepseek}
	provider.ReasoningField = &off
	request, err := Build(provider, Request{Model: "deepseek-chat", SessionID: "s",
		Messages: []ChatMessage{{Role: "user", Content: "在吗"}, assistant}})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(request.Body)
	if strings.Contains(string(body), "reasoning_content") {
		t.Fatalf("关掉之后一个字都不该发：%s", body)
	}
}

// Validate：vendor×protocol 兼容矩阵（错配 loud error；占位 protocol 永远"暂不支持"）。
func TestValidateVendorProtocolMatrix(t *testing.T) {
	chatOK := []Vendor{VendorOpenAICompat, VendorDummy, VendorOpenAI, VendorOpenRouter, VendorDeepseek,
		VendorOpenCodeGo, VendorOpenCode, VendorOllama, VendorLMStudio, VendorCustom}
	for _, vendor := range chatOK {
		provider := Provider{ID: "p", Vendor: vendor, BaseURL: "https://x.invalid/v1"}
		if err := provider.Validate(); err != nil {
			t.Fatalf("vendor %q × chat 该过：%v", vendor, err)
		}
	}
	// custom 必须有 base_url
	if err := (Provider{ID: "c", Vendor: VendorCustom}).Validate(); err == nil {
		t.Fatal("custom 没给 base_url 该报错")
	}
	// systemone 只收 typesafe/openrouter/custom，且 base_url 非空
	for _, vendor := range []Vendor{VendorTypesafe, VendorOpenRouter, VendorCustom} {
		provider := Provider{ID: "p", Vendor: vendor, Protocol: ProtocolSystemOne, BaseURL: "https://x.invalid/systemone"}
		if err := provider.Validate(); err != nil {
			t.Fatalf("vendor %q × systemone 该过：%v", vendor, err)
		}
	}
	for _, vendor := range []Vendor{VendorDummy, VendorOpenAI, VendorDeepseek, VendorOpenCodeGo, VendorOllama} {
		provider := Provider{ID: "p", Vendor: vendor, Protocol: ProtocolSystemOne, BaseURL: "https://x.invalid/s"}
		if err := provider.Validate(); err == nil {
			t.Fatalf("vendor %q × systemone 该错配报错", vendor)
		}
	}
	// dummy + systemone 报错
	if err := (Provider{ID: "d", Vendor: VendorDummy, Protocol: ProtocolSystemOne}).Validate(); err == nil {
		t.Fatal("dummy + systemone 该报错")
	}
	// systemone 要求 base_url 非空
	if err := (Provider{ID: "t", Vendor: VendorTypesafe, Protocol: ProtocolSystemOne}).Validate(); err == nil {
		t.Fatal("systemone 没给 base_url 该报错")
	}
	// 占位 protocol 永远"暂不支持"（openai-response 已开放给 opencode 系/openrouter/custom，见 TestValidateResponseMatrix）
	for _, protocol := range []Protocol{ProtocolAnthropicMessages, ProtocolGeminiGenerateContent} {
		err := (Provider{ID: "p", Vendor: VendorOpenAI, Protocol: protocol, BaseURL: "https://x.invalid/v1"}).Validate()
		if err == nil || !strings.Contains(err.Error(), "暂不支持") {
			t.Fatalf("protocol %q 该报暂不支持：%v", protocol, err)
		}
	}
	// Build 入口先 Normalize＋Validate：protocol 非 chat 直接报错
	systemone := Provider{ID: "t", Vendor: VendorTypesafe, Protocol: ProtocolSystemOne, BaseURL: "https://x.invalid/s"}
	if _, err := Build(systemone, Request{Model: "m", SessionID: "s"}); err == nil {
		t.Fatal("systemone 走 Build 该报错")
	}
	// 别名读入：Kind 照常编译且收敛（openai-compat→custom）
	legacy := Provider{ID: "old", Kind: KindOpenAICompat, BaseURL: "https://x.invalid/v1"}
	if legacy.EffectiveVendor() != VendorCustom {
		t.Fatalf("openai-compat 该映射到 custom：%q", legacy.EffectiveVendor())
	}
	if err := legacy.Validate(); err != nil {
		t.Fatalf("别名输入该能过 Validate：%v", err)
	}
	// 都空→custom
	if (Provider{ID: "e"}).EffectiveVendor() != VendorCustom {
		t.Fatal("都空该兜到 custom")
	}
	if (Provider{ID: "e"}).EffectiveProtocol() != ProtocolChatCompletion {
		t.Fatal("空 protocol 该缺省 chat")
	}
}

// BuildSystemOne：verbatim URL、头体形状、错配报错。
func TestBuildSystemOne(t *testing.T) {
	state := map[string]any{"turn": 1}
	questions := map[string]any{"q1": "在吗"}
	provider := Provider{ID: "t", Vendor: VendorTypesafe, Protocol: ProtocolSystemOne,
		BaseURL: "https://api.typesafe.ai/v1/systemone?token=abc", APIKey: "sk-x",
		Headers: map[string]string{"X-Extra": "1"}}
	request, err := BuildSystemOne(provider, "m", state, questions)
	if err != nil {
		t.Fatal(err)
	}
	// URL 原样（不拼路径）
	if request.URL.String() != "https://api.typesafe.ai/v1/systemone?token=abc" {
		t.Fatalf("URL 该 verbatim：%q", request.URL.String())
	}
	// 头：只有 Content-Type + Accept + Authorization + 用户覆盖（SessionID 不进头）
	if got := request.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := request.Header.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept = %q", got)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer sk-x" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := request.Header.Get("X-Extra"); got != "1" {
		t.Fatalf("用户 headers 该覆盖：%q", got)
	}
	if request.Header.Get("X-Opencode-Session") != "" || request.Header.Get("User-Agent") != "" {
		t.Fatal("systemone 不发会话头与 UA")
	}
	// 体：只有 model/state/questions
	body, _ := io.ReadAll(request.Body)
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["model"] != "m" {
		t.Fatalf("model = %v", parsed["model"])
	}
	if _, ok := parsed["state"]; !ok {
		t.Fatal("state 该在")
	}
	if _, ok := parsed["questions"]; !ok {
		t.Fatal("questions 该在")
	}
	if len(parsed) != 3 {
		t.Fatalf("体该只有三个键：%v", parsed)
	}
	// 没 key 就不发 Authorization
	bare, err := BuildSystemOne(Provider{ID: "t", Vendor: VendorTypesafe, Protocol: ProtocolSystemOne,
		BaseURL: "https://x.invalid/s"}, "m", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bare.Header.Get("Authorization") != "" {
		t.Fatal("没 key 就不该发 Authorization")
	}
	// 错配报错：dummy + systemone、chat 走 BuildSystemOne
	if _, err := BuildSystemOne(Provider{ID: "d", Vendor: VendorDummy, Protocol: ProtocolSystemOne},
		"m", nil, nil); err == nil {
		t.Fatal("dummy + systemone 该报错")
	}
	if _, err := BuildSystemOne(Provider{ID: "c", Vendor: VendorCustom, BaseURL: "https://x.invalid/v1"},
		"m", nil, nil); err == nil {
		t.Fatal("chat protocol 走 BuildSystemOne 该报错")
	}
	// typesafe 预设：端点 + key env 回退（TYPESAFE_API_KEY 优先，JEV_API_KEY 兜底）
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("JEV_API_KEY", "sk-jev")
	wire := ApplyPreset(Provider{ID: "t", Vendor: VendorTypesafe})
	if wire.BaseURL != "https://api.typesafe.ai/v1/systemone" {
		t.Fatalf("typesafe 端点 = %q", wire.BaseURL)
	}
	if wire.APIKey != "sk-jev" {
		t.Fatalf("JEV_API_KEY 该兜底：%q", wire.APIKey)
	}
	t.Setenv("TYPESAFE_API_KEY", "sk-typesafe")
	wire = ApplyPreset(Provider{ID: "t", Vendor: VendorTypesafe})
	if wire.APIKey != "sk-typesafe" {
		t.Fatalf("TYPESAFE_API_KEY 优先：%q", wire.APIKey)
	}
}

// 工具透传：**默认关**（宁可少发）；开了才原样进请求体，回程消息也照 Pi 的形状带回去。
func TestToolPassthrough(t *testing.T) {
	yes := true
	tools := []Tool{{
		Type: "function",
		Function: ToolFunction{
			Name: "roll_dice", Description: "掷骰子",
			Parameters: json.RawMessage(`{"type":"object","properties":{"sides":{"type":"integer"}}}`),
		},
	}}
	assistant := ChatMessage{Role: "assistant", Content: "",
		ToolCalls: json.RawMessage(`[{"id":"call_1","type":"function","function":{"name":"roll_dice","arguments":"{\"sides\":20}"}}]`)}
	result := ChatMessage{Role: "tool", Content: "17", ToolCallID: "call_1", Name: "roll_dice"}
	messages := []ChatMessage{{Role: "user", Content: "掷个 d20"}, assistant, result}

	build := func(t *testing.T, provider Provider, request Request) map[string]any {
		t.Helper()
		request.SessionID = "s"
		httpRequest, err := Build(provider, request)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(httpRequest.Body)
		var parsed map[string]any
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatal(err)
		}
		return parsed
	}

	t.Run("默认关：不发工具定义，但历史照旧回放（否则结果消息会成孤儿）", func(t *testing.T) {
		body := build(t, opencodeProvider(), Request{Model: "m", Messages: messages, Tools: tools})
		sent, ok := body["tools"].([]any)
		if !ok || len(sent) != 0 {
			t.Fatalf("关掉时该是空数组（历史里有工具 ⇒ 参数必须在）：%v", body["tools"])
		}
		wire := body["messages"].([]any)
		if _, exists := wire[1].(map[string]any)["tool_calls"]; !exists {
			t.Fatal("历史里的 tool_calls 是存档的一部分，必须回放")
		}
		if _, exists := wire[2].(map[string]any)["tool_call_id"]; !exists {
			t.Fatal("工具结果也得回放，否则配对断了")
		}
	})

	t.Run("默认关、历史里也没工具 ⇒ 连这个参数都不出现", func(t *testing.T) {
		body := build(t, opencodeProvider(), Request{Model: "m",
			Messages: []ChatMessage{{Role: "user", Content: "在吗"}}, Tools: tools})
		if _, exists := body["tools"]; exists {
			t.Fatal("没有工具历史就不该有这个参数")
		}
	})

	t.Run("开了：tools + tool_choice + 回程消息全照 Pi 的形状", func(t *testing.T) {
		provider := opencodeProvider()
		provider.AllowTools = &yes
		body := build(t, provider, Request{Model: "m", Messages: messages, Tools: tools,
			ToolChoice: json.RawMessage(`"auto"`)})
		sent, ok := body["tools"].([]any)
		if !ok || len(sent) != 1 {
			t.Fatalf("tools = %v", body["tools"])
		}
		function := sent[0].(map[string]any)["function"].(map[string]any)
		if function["name"] != "roll_dice" {
			t.Fatalf("工具形状不对：%+v", function)
		}
		if _, exists := function["strict"]; exists {
			t.Fatal("没声明 strict 就不该发（有些上游会 400）")
		}
		if body["tool_choice"] != "auto" {
			t.Fatalf("tool_choice = %v", body["tool_choice"])
		}
		// 回程：assistant 带 tool_calls；结果消息带 tool_call_id（name 默认不带）
		wire := body["messages"].([]any)
		if _, exists := wire[1].(map[string]any)["tool_calls"]; !exists {
			t.Fatalf("assistant 的 tool_calls 该原样带回：%+v", wire[1])
		}
		result := wire[2].(map[string]any)
		if result["tool_call_id"] != "call_1" {
			t.Fatalf("结果消息该指回调用 id：%+v", result)
		}
		if _, exists := result["name"]; exists {
			t.Fatal("默认不带 name（Pi 的 requiresToolResultName 才带）")
		}
	})

	t.Run("要求带名字的上游：结果消息补 name", func(t *testing.T) {
		provider := opencodeProvider()
		provider.AllowTools = &yes
		provider.ToolResultName = &yes
		wire := build(t, provider, Request{Model: "m", Messages: messages, Tools: tools})["messages"].([]any)
		if wire[2].(map[string]any)["name"] != "roll_dice" {
			t.Fatalf("该带函数名：%+v", wire[2])
		}
	})

	t.Run("历史里出现过工具、这次没给 tools ⇒ 发空的 tools（Pi 的做法）", func(t *testing.T) {
		provider := opencodeProvider()
		provider.AllowTools = &yes
		body := build(t, provider, Request{Model: "m", Messages: messages}) // 没给 Tools
		sent, ok := body["tools"].([]any)
		if !ok || len(sent) != 0 {
			t.Fatalf("该发一个空数组：%v", body["tools"])
		}
		// 历史里没工具时，连这个空数组都不发
		plain := build(t, provider, Request{Model: "m", Messages: []ChatMessage{{Role: "user", Content: "在吗"}}})
		if _, exists := plain["tools"]; exists {
			t.Fatal("没有工具历史就不该有这个参数")
		}
	})
}

// 探针实测的坑：**非流式请求不许带 stream_options**（网关直接 400）。
func TestStreamOptionsOnlyWhenStreaming(t *testing.T) {
	streaming, err := Build(opencodeProvider(), Request{Model: "m", SessionID: "s", Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(streaming.Body)
	if !strings.Contains(string(body), "stream_options") {
		t.Fatal("流式该带 stream_options（不然拿不到 usage）")
	}
	plain, err := Build(opencodeProvider(), Request{Model: "m", SessionID: "s", Stream: false})
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(plain.Body)
	if strings.Contains(string(body), "stream_options") {
		t.Fatalf("非流式不该带它：%s", body)
	}
}
