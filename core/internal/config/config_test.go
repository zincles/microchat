package config

import (
	"encoding/json"
	"testing"
)

// Endpoints 只透传：JSON 能进能出，校验在 providers 侧（这里不断言行为，只钉住"字段还在"）。
func TestProviderEndpointsPassthrough(t *testing.T) {
	raw := `{"id":"go","vendor":"opencode-go","endpoints":{"openai-response":"https://x.invalid/r"}}`
	var provider Provider
	if err := json.Unmarshal([]byte(raw), &provider); err != nil {
		t.Fatal(err)
	}
	if provider.Endpoints["openai-response"] != "https://x.invalid/r" {
		t.Fatalf("%+v", provider.Endpoints)
	}
	out, err := json.Marshal(provider)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	endpoints, _ := back["endpoints"].(map[string]any)
	if endpoints["openai-response"] != "https://x.invalid/r" {
		t.Fatalf("透传丢了：%s", out)
	}
}
