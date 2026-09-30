package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// usageBody：一份形状正确的用量响应（值随便，测试钉的是形状与路由分支）。
const usageBody = `{"usage":{"rolling":{"status":"ok","percent":6,"resetsAt":"2026-09-30T13:47:01.482Z"},` +
	`"weekly":{"status":"ok","percent":1,"resetsAt":"2026-10-05T00:00:00.000Z"},` +
	`"monthly":{"status":"ok","percent":0,"resetsAt":"2026-10-28T18:39:36.000Z"}}}`

// 非 opencode-go 的渠道 ⇒ 400 说清楚（用量端点只有 OpenCode GO 有）。
func TestProviderUsageRejectsOtherKinds(t *testing.T) {
	server, _ := newProvidersServer(t, `{"providers":[{"id":"dummy","kind":"dummy","api_key":"x"}]}`)
	recorder := call(server, "GET", "/api/v1/providers/dummy/usage", "")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("该 400，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "invalid" {
		t.Fatalf("错误码该是 invalid：%s", recorder.Body.String())
	}
}

// 没配 key ⇒ 400 说清楚（不会去问上游）。
func TestProviderUsageNeedsKey(t *testing.T) {
	t.Setenv("OPENCODE_API_KEY", "") // 别让环境变量顶上来
	server, _ := newProvidersServer(t, `{"providers":[{"id":"ocgo","kind":"opencode-go","base_url":"https://opencode.ai/zen/go/v1"}]}`)
	recorder := call(server, "GET", "/api/v1/providers/ocgo/usage", "")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("该 400，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
}

// 正常 ⇒ 200：形状 = PlanUsage（plan / windows[].label/percent/resets_at）+ provider_id。
// 上游用 httptest 假端点（渠道的 base_url 指过去），**不联网**。
func TestProviderUsageOK(t *testing.T) {
	t.Setenv("OPENCODE_API_KEY", "")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/usage" {
			t.Errorf("打错了路径：%s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("没带对 key：%q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(usageBody))
	}))
	t.Cleanup(upstream.Close)

	server, _ := newProvidersServer(t, `{"providers":[{"id":"ocgo","kind":"opencode-go","base_url":"`+upstream.URL+`/v1","api_key":"sk-test"}]}`)
	recorder := call(server, "GET", "/api/v1/providers/ocgo/usage", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("该 200，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	var view struct {
		ProviderID string `json:"provider_id"`
		Plan       string `json:"plan"`
		Windows    []struct {
			Label    string  `json:"label"`
			Percent  float64 `json:"percent"`
			ResetsAt string  `json:"resets_at"`
		} `json:"windows"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.ProviderID != "ocgo" || view.Plan != "OpenCode Go" || len(view.Windows) != 3 {
		t.Fatalf("视图 = %+v", view)
	}
	if view.Windows[0].Label != "Rolling (5h)" || view.Windows[0].Percent != 6 || view.Windows[0].ResetsAt == "" {
		t.Fatalf("第一个窗口不对：%+v", view.Windows[0])
	}
}

// 渠道不存在 ⇒ 404。
func TestProviderUsageUnknownProvider(t *testing.T) {
	server, _ := newProvidersServer(t, `{"providers":[]}`)
	recorder := call(server, "GET", "/api/v1/providers/nope/usage", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("该 404，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
}
