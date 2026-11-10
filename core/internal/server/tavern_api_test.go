package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"microchat/internal/config"
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

// 从 ST 预设导入的提示词片段要记住来源；删预设时可选一并删段。
// 2026-11-11 用户定：段要能区分自编写与导入，删除预设时提醒关联段。
func TestTavernPresetImportedSegments(t *testing.T) {
	box := newDummySandboxWith(t, true)
	uploadPreset := func(name string) string {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/v1/tavern/presets?name="+url.QueryEscape(name), bytes.NewReader([]byte(`{"a":1}`)))
		rec := httptest.NewRecorder()
		box.server.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("上传预设该 201，得到 %d：%s", rec.Code, rec.Body.String())
		}
		var meta struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil || meta.ID == "" {
			t.Fatalf("预设元数据不对：%v %s", err, rec.Body.String())
		}
		return meta.ID
	}
	createSegment := func(id, presetID string) config.Segment {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"id": id, "text": "段正文", "imported": true, "preset_id": presetID})
		rec := authCall(box.server, "POST", "/api/v1/segments", string(body), testSysToken)
		if rec.Code != http.StatusCreated {
			t.Fatalf("建导入段该 201，得到 %d：%s", rec.Code, rec.Body.String())
		}
		return decodeBody[config.Segment](t, rec)
	}
	hasSegment := func(id string) bool {
		t.Helper()
		list := decodeBody[struct {
			Segments []config.Segment `json:"segments"`
		}](t, authCall(box.server, "GET", "/api/v1/agents", "", testSysToken))
		for _, s := range list.Segments {
			if s.ID == id {
				return true
			}
		}
		return false
	}

	// ① 来源随段一起存：Imported 强制为 true，PresetID 原样，PresetName 是文件名快照
	presetID := uploadPreset("十四行诗.json")
	segment := createSegment("from-st", presetID)
	if !segment.Imported || segment.PresetID != presetID || segment.PresetName != "十四行诗.json" {
		t.Fatalf("导入段的来源不对：%+v", segment)
	}
	if !hasSegment("from-st") {
		t.Fatal("导入段没进段库")
	}
	// 不存在的来源预设拒绝
	bad, _ := json.Marshal(map[string]any{"id": "from-nowhere", "text": "x", "preset_id": "nope"})
	if rec := authCall(box.server, "POST", "/api/v1/segments", string(bad), testSysToken); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("来源预设不存在该 422，得到 %d（%s）", rec.Code, rec.Body.String())
	}

	// ② 不带 segments 参数 ⇒ 409，让界面先问用户
	if rec := authCall(box.server, "DELETE", "/api/v1/tavern/presets/"+presetID, "", testSysToken); rec.Code != http.StatusConflict {
		t.Fatalf("有导入段时删预设该 409，得到 %d（%s）", rec.Code, rec.Body.String())
	}
	// ③ keep ⇒ 只删预设，段与来源快照都留着
	if rec := authCall(box.server, "DELETE", "/api/v1/tavern/presets/"+presetID+"?segments=keep", "", testSysToken); rec.Code != http.StatusNoContent {
		t.Fatalf("保留段的删除该 204，得到 %d（%s）", rec.Code, rec.Body.String())
	}
	if !hasSegment("from-st") {
		t.Fatal("segments=keep 不该删段")
	}

	// ④ delete ⇒ 预设与段一起没
	presetID2 := uploadPreset("第二份.json")
	createSegment("from-st-2", presetID2)
	if rec := authCall(box.server, "DELETE", "/api/v1/tavern/presets/"+presetID2+"?segments=delete", "", testSysToken); rec.Code != http.StatusNoContent {
		t.Fatalf("一并删除该 204，得到 %d（%s）", rec.Code, rec.Body.String())
	}
	if hasSegment("from-st-2") {
		t.Fatal("segments=delete 该删掉导入段")
	}

	// ⑤ 段还被 Agent 引用 ⇒ 拒绝一并删（不能顺手改人格）
	presetID3 := uploadPreset("第三份.json")
	createSegment("from-st-3", presetID3)
	if rec := authCall(box.server, "POST", "/api/v1/agents", `{"name":"用段的","segments":["from-st-3"]}`, testSysToken); rec.Code != http.StatusCreated {
		t.Fatalf("建引用 Agent 该 201，得到 %d（%s）", rec.Code, rec.Body.String())
	}
	rec := authCall(box.server, "DELETE", "/api/v1/tavern/presets/"+presetID3+"?segments=delete", "", testSysToken)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "用段的") {
		t.Fatalf("被引用时该 400 并点名 Agent，得到 %d（%s）", rec.Code, rec.Body.String())
	}
	if !hasSegment("from-st-3") {
		t.Fatal("被引用时不该删段")
	}
}
