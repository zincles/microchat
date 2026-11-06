package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// 酒馆原始导入：**逐字节**存/取；格式校验只做闸门，不重写内容。
func TestTavernPresetRoundTripIsByteExact(t *testing.T) {
	box := newSandbox(t, 0)
	// BOM + 不规则空白 + 转义 + 中文 + 结尾空行：任何 Unmarshal→Marshal 都会变样。
	raw := []byte("\xEF\xBB\xBF { \"name\" : \"十四行诗\", \"nested\" : {\"a\":1}, \"esc\" : \"\\u003c\" }\n\n")
	req := httptest.NewRequest("POST", "/api/v1/tavern/presets?name="+url.QueryEscape("十四行诗.json"), bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	box.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("该 201，得到 %d：%s", rec.Code, rec.Body.String())
	}
	var meta struct {
		ID       string `json:"id"`
		Filename string `json:"filename"`
		Size     int64  `json:"size"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.ID == "" || meta.Size != int64(len(raw)) || meta.Filename != "十四行诗.json" {
		t.Fatalf("元数据不对：%+v", meta)
	}

	rec = call(box.server, "GET", "/api/v1/tavern/presets/"+meta.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("下载该 200，得到 %d", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), raw) {
		t.Fatalf("下载字节变了：got %q want %q", rec.Body.Bytes(), raw)
	}
	if rec.Header().Get("Content-Disposition") == "" {
		t.Fatal("下载缺 Content-Disposition（导出文件名）")
	}

	rec = call(box.server, "GET", "/api/v1/tavern/presets", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("列表该 200，得到 %d", rec.Code)
	}
	var list []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list) != 1 {
		t.Fatalf("列表不对：%v %s", err, rec.Body.String())
	}
	if _, leaked := list[0]["data"]; leaked {
		t.Fatal("列表不该带原始正文")
	}
}

func TestTavernPresetRejectsInvalidJSON(t *testing.T) {
	box := newSandbox(t, 0)
	rec := call(box.server, "POST", "/api/v1/tavern/presets", `{"name":`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("坏 JSON 该 422，得到 %d：%s", rec.Code, rec.Body.String())
	}
}

func TestTavernPresetDelete(t *testing.T) {
	box := newSandbox(t, 0)
	rec := call(box.server, "POST", "/api/v1/tavern/presets", `{"a":1}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("该 201，得到 %d", rec.Code)
	}
	var meta struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &meta)
	rec = call(box.server, "DELETE", "/api/v1/tavern/presets/"+meta.ID, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("删除该 204，得到 %d", rec.Code)
	}
	rec = call(box.server, "GET", "/api/v1/tavern/presets/"+meta.ID, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("删后该 404，得到 %d", rec.Code)
	}
}
