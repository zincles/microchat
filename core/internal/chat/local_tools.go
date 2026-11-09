package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"microchat/internal/config"
	"microchat/internal/model"
	"microchat/internal/providers"
)

// DeepSeek 的联网搜索走**客户端** Search/Fetch API。它不作为服务端工具发；
// 这里只给模型 function 声明，执行在我们自己手里，结果再以 role:"tool" 回填。
func deepseekLocalTools() []providers.Tool {
	return []providers.Tool{
		providers.FunctionTool("web_search", "Search the web. Returns titles, URLs and snippets.",
			json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Search query, max 128 characters"},"count":{"type":"integer","minimum":1,"maximum":50,"description":"Number of results, default 5"}},"required":["query"],"additionalProperties":false}`)),
		providers.FunctionTool("web_fetch", "Fetch one HTTP(S) page and return its Markdown content.",
			json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","description":"HTTP or HTTPS URL"}},"required":["url"],"additionalProperties":false}`)),
	}
}

type localSearchResult struct {
	Title       string  `json:"title"`
	URL         string  `json:"url"`
	Snippet     string  `json:"snippet"`
	Summary     string  `json:"summary"`
	SiteName    *string `json:"site_name"`
	Date        *string `json:"date"`
	Authority   int     `json:"authority"`
	RerankScore float64 `json:"rerank_score"`
}

type localSearchResponse struct {
	Results []localSearchResult `json:"results"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type localFetchResponse struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

const maxLocalToolResult = 64 << 10

// runLocalTool：执行一个客户端 function call，返回给模型的**字符串**（JSON 优先）与来源。
func runLocalTool(ctx context.Context, channel config.Provider, call providers.ToolCall) (string, []model.MessageToolSource, error) {
	wire := providers.ApplyPreset(providers.FromConfig(channel))
	base := strings.TrimRight(wire.BaseURL, "/")
	if base == "" {
		return "", nil, fmt.Errorf("工具 %s：渠道没有 base_url", call.Name)
	}
	client := &http.Client{Timeout: 45 * time.Second}
	switch call.Name {
	case "web_search":
		var args struct {
			Query string `json:"query"`
			Count int    `json:"count"`
		}
		if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
			return "", nil, fmt.Errorf("web_search 参数不是 JSON：%w", err)
		}
		args.Query = strings.TrimSpace(args.Query)
		if args.Query == "" {
			return "", nil, fmt.Errorf("web_search 缺少 query")
		}
		if args.Count <= 0 {
			args.Count = 5
		}
		if args.Count > 50 {
			args.Count = 50
		}
		body, _ := json.Marshal(map[string]any{"query": args.Query, "count": args.Count, "need_content": false})
		raw, err := doDeepSeekTool(ctx, client, base+"/v1/tools/search", wire.APIKey, body)
		if err != nil {
			return "", nil, err
		}
		var parsed localSearchResponse
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return "", nil, fmt.Errorf("web_search 解析响应失败：%w", err)
		}
		if parsed.Error != nil {
			return "", nil, fmt.Errorf("web_search 上游错误：%s", parsed.Error.Message)
		}
		sources := make([]model.MessageToolSource, 0, len(parsed.Results))
		compact := make([]map[string]any, 0, len(parsed.Results))
		for _, result := range parsed.Results {
			if result.URL == "" {
				continue
			}
			sources = append(sources, model.MessageToolSource{Type: "url", URL: result.URL, Title: result.Title})
			compact = append(compact, map[string]any{
				"title": result.Title, "url": result.URL, "snippet": result.Snippet,
				"summary": result.Summary, "site_name": result.SiteName, "date": result.Date,
				"authority": result.Authority, "rerank_score": result.RerankScore,
			})
		}
		out, _ := json.Marshal(map[string]any{"query": args.Query, "results": compact})
		return truncateToolResult(string(out)), sources, nil
	case "web_fetch":
		var args struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
			return "", nil, fmt.Errorf("web_fetch 参数不是 JSON：%w", err)
		}
		args.URL = strings.TrimSpace(args.URL)
		if !strings.HasPrefix(args.URL, "https://") && !strings.HasPrefix(args.URL, "http://") {
			return "", nil, fmt.Errorf("web_fetch 只接受 http(s) URL")
		}
		body, _ := json.Marshal(map[string]any{"url": args.URL})
		raw, err := doDeepSeekTool(ctx, client, base+"/v1/tools/fetch", wire.APIKey, body)
		if err != nil {
			return "", nil, err
		}
		var parsed localFetchResponse
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return "", nil, fmt.Errorf("web_fetch 解析响应失败：%w", err)
		}
		out, _ := json.Marshal(map[string]any{"title": parsed.Title, "content": truncateToolResult(parsed.Content)})
		return string(out), []model.MessageToolSource{{Type: "url", URL: args.URL, Title: parsed.Title}}, nil
	default:
		return "", nil, fmt.Errorf("未知本地工具 %q", call.Name)
	}
}

func doDeepSeekTool(ctx context.Context, client *http.Client, url, key string, body []byte) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("工具上游 %d：%s", response.StatusCode, truncateToolResult(string(raw)))
	}
	return raw, nil
}

func truncateToolResult(text string) string {
	if len(text) <= maxLocalToolResult {
		return text
	}
	return text[:maxLocalToolResult] + "\n…（结果已截断）"
}
