package server

// ── 界面偏好（`GET/PUT /config/prefs` + 背景图三条）──────────────────
//
// 2026-11-01 用户定：「所有设置均存储在服务器上」。
// 这里钉的是**接口与文件两头**的口径，尤其是：
//   - 缺字段 = 全开（不是"全关"）—— `bool` 零值那个坑的反面；
//   - 未知的 markdown 类型**点名拒绝**，不静默忽略；
//   - 背景图走自己的口子，`PUT /config/prefs` 不许凭空把它改掉。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"microchat/internal/config"
)

// prefsSandbox：一个只有配置目录的沙箱（偏好不碰库）。
func prefsSandbox(t *testing.T) (*Server, config.Paths) {
	t.Helper()
	dir := t.TempDir()
	paths := config.Paths{ConfigDir: dir, DataDir: dir}
	return newTestServer(nil, config.DefaultConfig(), paths), paths
}

func getPrefsView(t *testing.T, server *Server) PrefsView {
	t.Helper()
	recorder := call(server, "GET", "/api/v1/config/prefs", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /config/prefs 该 200，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	return decodeBody[PrefsView](t, recorder)
}

// PNG 的最小合法头（`DetectContentType` 靠签名认，不靠内容完整性）。
var tinyPNG = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
}

func TestPrefsDefaultsWhenMissing(t *testing.T) {
	server, _ := prefsSandbox(t)
	view := getPrefsView(t, server)

	// ① 文件缺失 ⇒ 全默认，且**渲染是开着的**（2026-11-01 之前一直如此，迁移不该改现状）。
	if !view.Prefs.Markdown.MarkdownOn() {
		t.Fatal("缺文件时 markdown 总开关该是开")
	}
	if len(view.Prefs.Markdown.Disabled) != 0 {
		t.Fatalf("缺文件时不该有关掉的类型，得到 %v", view.Prefs.Markdown.Disabled)
	}

	// ② 每一个语法类型都默认是开的。
	for _, typ := range view.MarkdownTypes {
		if !view.Prefs.Markdown.TypeOn(typ.ID) {
			t.Fatalf("缺文件时 %q 该是开的", typ.ID)
		}
	}

	// ③ 类型清单由后端给（界面据此排开关）：至少含用户点名的那几样。
	want := map[string]bool{"bold": false, "italic": false, "heading": false, "strike": false}
	for _, typ := range view.MarkdownTypes {
		if _, ok := want[typ.ID]; ok {
			want[typ.ID] = true
		}
		if typ.Name == "" || typ.Kind == "" || typ.Sample == "" {
			t.Fatalf("类型 %q 的三格（名称/类别/例子）都得有：%+v", typ.ID, typ)
		}
		if typ.Kind != "block" && typ.Kind != "inline" {
			t.Fatalf("类型 %q 的 kind 只认 block/inline，得到 %q", typ.ID, typ.Kind)
		}
	}
	for id, seen := range want {
		if !seen {
			t.Fatalf("类型清单里缺 %q —— 用户点名的语法必须有开关", id)
		}
	}

	// ④ 原来那些偏好的缺省与前端 localStorage 的旧缺省逐个对齐（迁移不改现状）。
	if view.Prefs.Preview == nil || !*view.Prefs.Preview {
		t.Fatal("preview 缺省该是开（旧 localStorage 是 !== \"0\"）")
	}
	if view.Prefs.ChromeHide == nil || !*view.Prefs.ChromeHide {
		t.Fatal("chrome_hide 缺省该是开")
	}
	if view.Prefs.CommandMenu == nil || *view.Prefs.CommandMenu {
		t.Fatal("command_menu 缺省该是关（旧值是 === \"1\"）")
	}
	if view.Prefs.PayloadViz == nil || *view.Prefs.PayloadViz {
		t.Fatal("payload_viz 缺省该是关")
	}
}

func TestPrefsRoundTrip(t *testing.T) {
	server, paths := prefsSandbox(t)

	// ⚠ 故意按**清单的反序**发（image 在后、table 在前）：这样"有没有归一化"才看得出区别。
	// 第一次我按正序发，于是去掉 Normalize 的注入**照样通过** —— 那个测试当时是假的。
	body := `{"markdown":{"disabled":["image","table"]},"theme":"dark","text_scale":"1.1","send":"mod-enter"}`
	recorder := call(server, "PUT", "/api/v1/config/prefs", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PUT 该 200，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	view := decodeBody[PrefsView](t, recorder)
	if view.Prefs.Markdown.TypeOn("table") || view.Prefs.Markdown.TypeOn("image") {
		t.Fatalf("写进去的两类该是关的，得到 %v", view.Prefs.Markdown.Disabled)
	}
	if !view.Prefs.Markdown.TypeOn("bold") {
		t.Fatal("没点名的类型该保持开")
	}

	// ① 落盘了（不是只在内存里）。
	raw, err := os.ReadFile(paths.Config("prefs.json"))
	if err != nil {
		t.Fatalf("prefs.json 没写出来：%v", err)
	}
	var onDisk config.Prefs
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	if len(onDisk.Markdown.Disabled) != 2 {
		t.Fatalf("盘上该有两项关闭，得到 %v", onDisk.Markdown.Disabled)
	}

	// ② 再 GET 一次（新 Server 实例也一样 —— 权威在文件，不在进程内缓存）。
	if got := getPrefsView(t, server); got.Prefs.Theme != "dark" {
		t.Fatalf("回读的 theme 该是 dark，得到 %q", got.Prefs.Theme)
	}

	// ③ Normalize：写出的顺序 = **类型清单的顺序**（同一个状态必须写出同一份文件，
	//    否则"改一下再改回来"会留下两份内容不同、含义相同的 JSON，diff 里全是噪声）。
	//    从清单推导期望顺序，别在测试里硬编码一个会随清单变化而错的位置。
	wantOrder := make([]string, 0, 2)
	for _, typ := range view.MarkdownTypes {
		if typ.ID == "table" || typ.ID == "image" {
			wantOrder = append(wantOrder, typ.ID)
		}
	}
	if len(onDisk.Markdown.Disabled) != 2 ||
		onDisk.Markdown.Disabled[0] != wantOrder[0] || onDisk.Markdown.Disabled[1] != wantOrder[1] {
		t.Fatalf("落盘该按清单顺序排（期望 %v），得到 %v", wantOrder, onDisk.Markdown.Disabled)
	}
}

func TestPrefsRejectsUnknownMarkdownType(t *testing.T) {
	server, paths := prefsSandbox(t)

	recorder := call(server, "PUT", "/api/v1/config/prefs",
		`{"markdown":{"disabled":["table","blink"]}}`)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("未知类型该 422，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	// 报错要**点名**（说清楚哪个、以及认识哪些）—— 不然用户只能猜。
	body := recorder.Body.String()
	if !bytes.Contains([]byte(body), []byte("blink")) {
		t.Fatalf("错误信息该点名 blink：%s", body)
	}
	if !bytes.Contains([]byte(body), []byte("table")) {
		t.Fatalf("错误信息该把认识的类型列出来：%s", body)
	}

	// 先验后写：坏请求**一个字都不许落盘**（否则半个状态下线了）。
	if _, err := os.Stat(paths.Config("prefs.json")); !os.IsNotExist(err) {
		t.Fatal("422 之后不该留下 prefs.json")
	}
}

func TestPrefsRejectsDuplicateType(t *testing.T) {
	server, _ := prefsSandbox(t)
	recorder := call(server, "PUT", "/api/v1/config/prefs", `{"markdown":{"disabled":["bold","bold"]}}`)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("重复的 id 该 422，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
}

// 盘上的 prefs.json 被手改坏了（未知类型）⇒ GET 要**明说**，不许悄悄回一份全默认。
//
// 为什么这条重要：悄悄回落等于"用户的开关自己全开了"，而他一点线索都没有。
// 这条必须**直接写文件**来测 —— 走 PUT 的话 `putPrefs` 自己那道校验会先把它挡在门外，
// 就测不到 `LoadPrefs` 这一层的校验了（我第一次就是这么写的，注入证明了它什么都没测到）。
func TestPrefsBrokenFileIsReportedNotSilentlyDefaulted(t *testing.T) {
	server, paths := prefsSandbox(t)
	if err := os.WriteFile(paths.Config("prefs.json"),
		[]byte(`{"markdown":{"disabled":["blink"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	recorder := call(server, "GET", "/api/v1/config/prefs", "")
	if recorder.Code == http.StatusOK {
		t.Fatalf("坏文件该报错，不该回一份默认值：%s", recorder.Body.String())
	}
	if !bytes.Contains(recorder.Body.Bytes(), []byte("blink")) {
		t.Fatalf("错误信息该点名坏掉的那个 id：%s", recorder.Body.String())
	}
}

func TestPrefsMasterSwitch(t *testing.T) {
	server, _ := prefsSandbox(t)

	recorder := call(server, "PUT", "/api/v1/config/prefs", `{"markdown":{"enabled":false}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PUT 该 200，得到 %d", recorder.Code)
	}
	view := decodeBody[PrefsView](t, recorder)
	if view.Prefs.Markdown.MarkdownOn() {
		t.Fatal("总开关写了 false，回读该是 false")
	}

	// 总开关关着、逐类型仍全开 —— 两件事互不覆盖（界面把总开关一拨，逐类型的勾选要留着）。
	for _, typ := range view.MarkdownTypes {
		if !view.Prefs.Markdown.TypeOn(typ.ID) {
			t.Fatalf("总开关不该顺手清掉逐类型的值，但 %q 变关了", typ.ID)
		}
	}

	// 再打开（`enabled:true`）⇒ 回到全开。
	recorder = call(server, "PUT", "/api/v1/config/prefs", `{"markdown":{"enabled":true}}`)
	view = decodeBody[PrefsView](t, recorder)
	if !view.Prefs.Markdown.MarkdownOn() {
		t.Fatal("写回 true 之后该是开的")
	}
}

func TestBackgroundUploadServeDelete(t *testing.T) {
	server, paths := prefsSandbox(t)
	base := "/api/v1/config/prefs/background"

	// ① 没上传过 ⇒ 404（"没有图"是事实，不是错）。
	if recorder := call(server, "GET", base, ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("没图时 GET 该 404，得到 %d", recorder.Code)
	}

	// ② 上传：原始字节进去。
	put := httptest.NewRequest("PUT", base, bytes.NewReader(tinyPNG))
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, put)
	if recorder.Code != http.StatusOK {
		t.Fatalf("上传该 200，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	bg := decodeBody[config.Background](t, recorder)
	if bg.Kind != "file" || bg.Version == "" {
		t.Fatalf("上传后该登记成 kind=file 且带版本号，得到 %+v", bg)
	}

	// ③ 落盘 + 能被取回来（Content-Type 由服务端现判）。
	if _, err := os.Stat(config.BackgroundPath(paths)); err != nil {
		t.Fatalf("图没落到 %s：%v", config.BackgroundPath(paths), err)
	}
	fetched := call(server, "GET", base, "")
	if fetched.Code != http.StatusOK {
		t.Fatalf("取图该 200，得到 %d", fetched.Code)
	}
	if ct := fetched.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type 该是 image/png（现判的），得到 %q", ct)
	}
	if !bytes.Equal(fetched.Body.Bytes(), tinyPNG) {
		t.Fatal("取回来的字节与上传的不一致")
	}

	// ④ ETag 条件请求 ⇒ 304（换图才会变）。
	conditional := httptest.NewRequest("GET", base, nil)
	conditional.Header.Set("If-None-Match", fetched.Header().Get("ETag"))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, conditional)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("带对 ETag 该 304，得到 %d", rec.Code)
	}

	// ⑤ `PUT /config/prefs` **不许**把背景图说没（它归上传那条口子管）。
	prefsPut := call(server, "PUT", "/api/v1/config/prefs", `{"theme":"dark"}`)
	if prefsPut.Code != http.StatusOK {
		t.Fatalf("PUT prefs 该 200，得到 %d", prefsPut.Code)
	}
	if got := decodeBody[PrefsView](t, prefsPut).Prefs.Background; got.Kind != "file" {
		t.Fatalf("改主题不该顺手删掉背景图，得到 %+v", got)
	}

	// ⑥ 删：204，之后 404。
	if recorder := call(server, "DELETE", base, ""); recorder.Code != http.StatusNoContent {
		t.Fatalf("删图该 204，得到 %d", recorder.Code)
	}
	if recorder := call(server, "GET", base, ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("删完 GET 该 404，得到 %d", recorder.Code)
	}
	// 删两次也回 204（幂等）。
	if recorder := call(server, "DELETE", base, ""); recorder.Code != http.StatusNoContent {
		t.Fatalf("重复删该 204（幂等），得到 %d", recorder.Code)
	}
}

// 没传 `background` = **不动它**（不是清空）。
//
// 整份替换语义下"没传 = 清空"本来是对的，但背景图是一张**要人重新上传**的东西：
// 任何一个只改主题、顺手 PUT 一份不完整偏好的客户端都会把壁纸抹掉 —— 静默的数据丢失。
// 所以 `background` 单开指针语义（`nil` = 不动），要清空得显式给 `{}`。
func TestPrefsOmittedBackgroundIsUntouched(t *testing.T) {
	server, paths := prefsSandbox(t)
	base := "/api/v1/config/prefs/background"

	put := httptest.NewRequest("PUT", base, bytes.NewReader(tinyPNG))
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, put)
	if recorder.Code != http.StatusOK {
		t.Fatalf("上传该 200，得到 %d", recorder.Code)
	}
	version := decodeBody[config.Background](t, recorder).Version

	// ① 只改主题、**不带** background ⇒ 图还在，版本号也没变。
	if r := call(server, "PUT", "/api/v1/config/prefs", `{"theme":"breeze"}`); r.Code != http.StatusOK {
		t.Fatalf("PUT 该 200，得到 %d", r.Code)
	}
	bg := getPrefsView(t, server).Prefs.Background
	if bg.Kind != "file" || bg.Version != version {
		t.Fatalf("没传 background 时说图没了/变了：%+v", bg)
	}
	if _, err := os.Stat(config.BackgroundPath(paths)); err != nil {
		t.Fatal("文件不该被删")
	}

	// ② 显式给 `{}` 才是清空 —— 并且文件一起删掉（不留孤儿）。
	if r := call(server, "PUT", "/api/v1/config/prefs", `{"background":{}}`); r.Code != http.StatusOK {
		t.Fatalf("清空该 200，得到 %d", r.Code)
	}
	if bg := getPrefsView(t, server).Prefs.Background; bg.Kind != "" {
		t.Fatalf("显式清空之后该没有背景，得到 %+v", bg)
	}
	if _, err := os.Stat(config.BackgroundPath(paths)); !os.IsNotExist(err) {
		t.Fatal("清空之后文件该被删掉（否则盘上留一张谁都不认领的图）")
	}
}

// `url` 型背景是纯元信息 ⇒ 可以直接由 `PUT /config/prefs` 设（它不涉及服务器上的文件）。
func TestPrefsAcceptsUrlBackground(t *testing.T) {
	server, _ := prefsSandbox(t)
	recorder := call(server, "PUT", "/api/v1/config/prefs",
		`{"background":{"kind":"url","value":"https://example.com/bg.jpg"}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("url 背景该 200，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	bg := decodeBody[PrefsView](t, recorder).Prefs.Background
	if bg.Kind != "url" || bg.Value != "https://example.com/bg.jpg" {
		t.Fatalf("url 背景没存对：%+v", bg)
	}
}

func TestBackgroundRejectsNonImage(t *testing.T) {
	server, _ := prefsSandbox(t)
	put := httptest.NewRequest("PUT", "/api/v1/config/prefs/background",
		bytes.NewReader([]byte("<?php system($_GET[0]); ?>")))
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, put)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("非图片该 422，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
}

func TestBackgroundRejectsExternalUrlScheme(t *testing.T) {
	server, _ := prefsSandbox(t)
	// `javascript:` 会进 CSS 的 url() —— 必须挡在写入处。
	recorder := call(server, "PUT", "/api/v1/config/prefs",
		`{"background":{"kind":"url","value":"javascript:alert(1)"}}`)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("javascript: 该 422，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	// http/https 照收。
	recorder = call(server, "PUT", "/api/v1/config/prefs",
		`{"background":{"kind":"url","value":"https://example.com/a.png"}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("https 地址该 200，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
}
