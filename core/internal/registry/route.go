// Package registry 的 route.go：models.dev 快照 ⇒ 模型走哪条 API。
//
// 模型口径归 registry 管，拉一份公开快照也归它 —— 这是 providers 之外唯一碰网络的地方（注释写清这一句）。
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// ModelsDevURL：公开快照地址（顶层 225 个 provider 节；只要 opencode 与 opencode-go 两节）。
const ModelsDevURL = "https://models.dev/api.json"

// 快照里要的两个节名。
var modelsDevSections = []string{"opencode", "opencode-go"}

// NpmToAPI：快照里 `provider.npm` ⇒ 发请求走哪条 API（照 Pi `generate-models.ts`）。
// 其余（含空）⇒ openai-completions（走 provider 缺省）。
func NpmToAPI(npm string) string {
	switch npm {
	case "@ai-sdk/openai":
		return "openai-responses"
	case "@ai-sdk/anthropic":
		return "anthropic-messages"
	case "@ai-sdk/google":
		return "google-generative-ai"
	default:
		return "openai-completions"
	}
}

// modelsDevModel：快照里一个模型的形状 —— 只要 `provider.npm` 这一格。
// provider 缺字段 / null 都见过 ⇒ RawMessage 先接住再拆；
// 万一哪天冒出字符串之类的形状，照缺省 completions 算，不把整份快照崩掉。
type modelsDevModel struct {
	Provider json.RawMessage `json:"provider"`
}

// npmOf：provider 格 ⇒ npm 字符串（拆不出 ⇒ ""，调用方按缺省走）。
func npmOf(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var obj struct {
		NPM *string `json:"npm"`
	}
	if json.Unmarshal(raw, &obj) != nil || obj.NPM == nil {
		return ""
	}
	return *obj.NPM
}

// ParseModelsDev：快照 ⇒ `provider节 → {modelID: api}`；只要 opencode 与 opencode-go 两节；坏 JSON ⇒ 错。
func ParseModelsDev(data []byte) (map[string]map[string]string, error) {
	var top map[string]map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, err
	}
	out := map[string]map[string]string{}
	for _, section := range modelsDevSections {
		sec, ok := top[section]
		if !ok {
			continue
		}
		raw, ok := sec["models"]
		if !ok {
			continue
		}
		var models map[string]modelsDevModel
		if err := json.Unmarshal(raw, &models); err != nil {
			return nil, fmt.Errorf("%s/models: %w", section, err)
		}
		routes := make(map[string]string, len(models))
		for id, m := range models {
			routes[id] = NpmToAPI(npmOf(m.Provider))
		}
		out[section] = routes
	}
	return out, nil
}

// FetchModelsDev：GET 快照 ⇒ 同 ParseModelsDev 的形状。client 由调用方给
// （超时由调用方定，别用 http.DefaultClient）；非 200 ⇒ 错。
func FetchModelsDev(ctx context.Context, client *http.Client) (map[string]map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ModelsDevURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("models.dev 回了 %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	return ParseModelsDev(data)
}
