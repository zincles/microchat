package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"microchat/internal/config"
	"microchat/internal/providers"
)

func TestRunLocalDeepSeekTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/tools/search":
			var body struct {
				Query string `json:"query"`
				Count int    `json:"count"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Query == "" || body.Count <= 0 {
				t.Errorf("search body = %+v", body)
			}
			_, _ = w.Write([]byte(`{"results":[{"title":"A","url":"https://example.com/a","snippet":"s","summary":"sum"}]}`))
		case "/v1/tools/fetch":
			_, _ = w.Write([]byte(`{"title":"Page","content":"# 内容"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	channel := config.Provider{ID: "ds", Vendor: "deepseek", BaseURL: server.URL, APIKey: "k"}

	content, sources, err := runLocalTool(context.Background(), channel, providers.ToolCall{
		ID: "call_1", Name: "web_search", Arguments: `{"query":"xAI","count":3}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "example.com/a") || len(sources) != 1 || sources[0].URL != "https://example.com/a" {
		t.Fatalf("search content=%s sources=%+v", content, sources)
	}

	content, sources, err = runLocalTool(context.Background(), channel, providers.ToolCall{
		ID: "call_2", Name: "web_fetch", Arguments: `{"url":"https://example.com/a"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "内容") || len(sources) != 1 {
		t.Fatalf("fetch content=%s sources=%+v", content, sources)
	}
}
