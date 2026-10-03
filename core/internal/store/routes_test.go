package store

import "testing"

// Replace 整批替换 + ModelRoute 查一行：第二批把旧行清掉，别家渠道不动；空表 ⇒ 清干净。
func TestReplaceAndModelRoute(t *testing.T) {
	st := openTemp(t)
	// 表不住 001：Replace 用之前自建，调用方不用记这一步。
	if _, ok := st.ModelRoute("opencode", "a"); ok {
		t.Fatal("空表该查不到")
	}
	if err := st.ReplaceModelRoutes("opencode",
		map[string]string{"a": "openai-responses", "b": "anthropic-messages"}, 7); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceModelRoutes("opencode-go",
		map[string]string{"a": "google-generative-ai"}, 7); err != nil {
		t.Fatal(err)
	}
	if api, ok := st.ModelRoute("opencode", "a"); !ok || api != "openai-responses" {
		t.Fatalf("a = %q,%v", api, ok)
	}
	if api, ok := st.ModelRoute("opencode", "b"); !ok || api != "anthropic-messages" {
		t.Fatalf("b = %q,%v", api, ok)
	}
	// 替换语义：第二批没带的 a/b 该没了，别家渠道的 a 还在
	if err := st.ReplaceModelRoutes("opencode",
		map[string]string{"c": "openai-completions"}, 8); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.ModelRoute("opencode", "a"); ok {
		t.Fatal("替换后 a 该没了")
	}
	if _, ok := st.ModelRoute("opencode", "b"); ok {
		t.Fatal("替换后 b 该没了")
	}
	if api, ok := st.ModelRoute("opencode", "c"); !ok || api != "openai-completions" {
		t.Fatalf("c = %q,%v", api, ok)
	}
	if api, ok := st.ModelRoute("opencode-go", "a"); !ok || api != "google-generative-ai" {
		t.Fatalf("别家渠道不该动：%q,%v", api, ok)
	}
	// 空表 ⇒ 该渠道清干净
	if err := st.ReplaceModelRoutes("opencode", map[string]string{}, 9); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.ModelRoute("opencode", "c"); ok {
		t.Fatal("空替换后 c 该没了")
	}
}
