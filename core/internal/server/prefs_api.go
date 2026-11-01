package server

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"microchat/internal/config"
)

// ── 界面偏好（`prefs.json`）────────────────────────────────────────
//
// 2026-11-01 用户定：「所有设置均存储在服务器上」。
// 从前这些全在浏览器 localStorage；现在服务器是唯一权威，浏览器只留一份**内存缓存**
// （启动时 `GET /config/prefs` 灌进来 ⇒ 页面里所有读取仍是同步的，不用把半个 App 改成 async）。
//
// 两个例外仍住浏览器，见 `config/prefs.go` 顶上的注释（`mc_api` 是循环依赖、
// `mc_last_session` 是设备状态）。

// PrefsView：`GET /config/prefs` 的形状。
//
// 在 `Prefs` 之外多带两样**只读**的东西：
//   - `markdown_types`：类型清单（名字与例子）—— 界面据此排开关，**不许前端自己抄一张**
//     （少一条就留个点不动的死开关，多一条就是个后端不认识的选项）。
//   - `defaults`：出厂值（界面"恢复默认"用；也让前端不必自己抄一份缺省）。
type PrefsView struct {
	Prefs         config.Prefs          `json:"prefs"`
	MarkdownTypes []config.MarkdownType `json:"markdown_types"`
	Defaults      config.Prefs          `json:"defaults"`
}

func (s *Server) getPrefs(w http.ResponseWriter, r *http.Request) {
	prefs, err := config.LoadPrefs(s.paths)
	if err != nil {
		// ⚠ 这里**不**回落默认值：文件坏了就明说，别让人以为自己的开关"自己全开了"。
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, PrefsView{
		Prefs:         prefs,
		MarkdownTypes: config.MarkdownTypes,
		Defaults:      config.DefaultPrefs(),
	})
}

// putPrefs：**整份替换**（前端总是把完整的一份发上来 —— 它是缓存，不是补丁）。
//
// 为什么不用 PATCH：偏好是一组互相独立的开关，客户端手里就有全量；整份替换省掉
// "没给的字段算不动还是算清空"这类歧义（`PATCH /sessions` 用指针就是为了这个，
// 这里没必要付那份复杂度）。`Background` 是**唯一**的例外：它涉及服务器上的文件，
// 走自己那条上传/删除的口子，所以 PUT 里它只允许**保持不变**（见下）。
func (s *Server) putPrefs(w http.ResponseWriter, r *http.Request) {
	// `background` 用**指针**接：`nil`（没传）= 不动它。
	//
	// 为什么给它开这个特例：整份替换下"没传 = 清空"是对的，但背景图是**一张图**，
	// 任何一个只改了主题、顺手 PUT 一份不完整偏好的客户端都会把壁纸抹掉 ——
	// 那是静默的数据丢失（与 `PATCH /sessions` 里 `system_prompt` 用指针同一个道理）。
	// 要清空就显式给 `"background": {}`。
	var req struct {
		config.Prefs
		Background *config.Background `json:"background"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	incoming := req.Prefs
	if err := incoming.Markdown.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return
	}
	// 背景图分两种来源，规则不同：
	//   - `url`（外部地址）与空（不用背景）是**纯元信息** ⇒ 客户端可以直接设；
	//   - `file`（上传的那张）**只能由上传端点产生** ⇒ 这里收到 `kind=file` 一律以盘上为准，
	//     免得凭空登记一张服务器上根本不存在的图（那会让 `GET /background` 永远 404）。
	current, err := config.LoadPrefs(s.paths)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	switch {
	case req.Background == nil:
		// 没传 ⇒ 不动（见上面那段注释）。
		incoming.Background = current.Background
	case req.Background.Kind == "file":
		// 伪造不了：保留现状（要换图就走上传端点）。
		incoming.Background = current.Background
	case req.Background.Kind == "":
		// 显式清空。若原来是上传的那张，顺手把文件删掉（否则盘上留一张谁都不认领的图）。
		incoming.Background = config.Background{}
		if current.Background.Kind == "file" {
			if err := os.Remove(config.BackgroundPath(s.paths)); err != nil && !os.IsNotExist(err) {
				writeStoreError(w, err)
				return
			}
		}
	default:
		incoming.Background = *req.Background
	}
	// 校验**合并之后**的那一份：合并前那一格是嵌入式结构体的零值，
	// 在那儿校验等于校验"空" —— 坏地址会一路滑到 `SavePrefs` 之后的回读才炸成 500
	// （本该是 422 + 一句人话；这个 500/422 的错位是注入测试抓出来的）。
	if err := incoming.Background.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return
	}

	if err := config.SavePrefs(s.paths, incoming); err != nil {
		writeStoreError(w, err)
		return
	}
	prefs, err := config.LoadPrefs(s.paths) // 回读：把 Normalize 之后的样子给客户端
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, PrefsView{
		Prefs:         prefs,
		MarkdownTypes: config.MarkdownTypes,
		Defaults:      config.DefaultPrefs(),
	})
}

// maxBackgroundBytes：上传背景图的上限。
//
// 前端在**浏览器里**已经压到长边 1920 / JPEG 0.85（一般 300–700KB）；
// 这里留到 4MB 是给"原样保留的小图"与将来的格式变化留余量。
// 超了明说，别让 `io.ReadAll` 把内存吃干净。
const maxBackgroundBytes = 4 << 20

// putBackground：上传自定义背景图（原始字节，`Content-Type` 由我们现判）。
//
// 用**原始 body** 而不是 multipart/表单：调用方只有我们自己的前端，
// 传的就是一张图的字节流，多一层编码只会多一处出错的地方。
func (s *Server) putBackground(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBackgroundBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid", "读上传内容失败："+err.Error())
		return
	}
	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, "invalid", "上传内容为空")
		return
	}
	if len(body) > maxBackgroundBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "invalid",
			"图片太大（上限 4MB；前端会先压到长边 1920，压完还超就是这个文件本身有问题）")
		return
	}
	// 只收图片：`Content-Type` 现判（不信客户端说的）。存下来再服务时用的也是它。
	mime := http.DetectContentType(body)
	if !isImageMime(mime) {
		writeError(w, http.StatusUnprocessableEntity, "invalid",
			"这不是图片（识别成 "+mime+"）；只收常见位图")
		return
	}

	path := config.BackgroundPath(s.paths)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		writeStoreError(w, err)
		return
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		writeStoreError(w, err)
		return
	}

	// 铸一枚版本号：图换了但地址不变，前端靠它破缓存（也靠它知道"该重取了"）。
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	prefs, err := config.LoadPrefs(s.paths)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	prefs.Background = config.Background{Kind: "file", Version: hex.EncodeToString(buf)}
	if err := config.SavePrefs(s.paths, prefs); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, prefs.Background)
}

// deleteBackground：删掉上传的那张（同时把 `background` 清空）。
// 幂等：没设过也回 204（"它现在没有图"是事实，不是错误）。
func (s *Server) deleteBackground(w http.ResponseWriter, r *http.Request) {
	prefs, err := config.LoadPrefs(s.paths)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	// 只删**服务器上那张**：`kind=url` 的是外部地址，没有文件可删。
	if prefs.Background.Kind == "file" {
		if err := os.Remove(config.BackgroundPath(s.paths)); err != nil && !os.IsNotExist(err) {
			writeStoreError(w, err)
			return
		}
	}
	prefs.Background = config.Background{}
	if err := config.SavePrefs(s.paths, prefs); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// getBackground：服务器上那张图（没有 ⇒ 404）。
//
// 缓存分两层：`ETag` 用 `Background.Version`（换图必变），`Cache-Control` 给长
// `max-age` —— 但**必须配 `no-cache` 的语义**（`must-revalidate`），否则换图后
// 浏览器拿着旧的 `max-age` 一直不回来问。所以给的是 `private, max-age=0, must-revalidate`
// + ETag：每次回来问一句，没变就 304（一个来回，几字节）。
func (s *Server) getBackground(w http.ResponseWriter, r *http.Request) {
	prefs, err := config.LoadPrefs(s.paths)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if prefs.Background.Kind != "file" {
		writeError(w, http.StatusNotFound, "not_found", "没有自定义背景图")
		return
	}
	if match := r.Header.Get("If-None-Match"); match != "" && match == `"`+prefs.Background.Version+`"` {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body, err := os.ReadFile(config.BackgroundPath(s.paths))
	if err != nil {
		if os.IsNotExist(err) {
			// 元信息说有、盘上没有：照实回 404（别 500 —— 它不是服务器坏了，是文件没了）。
			writeError(w, http.StatusNotFound, "not_found", "背景图文件不见了（重新上传一张）")
			return
		}
		writeStoreError(w, err)
		return
	}
	w.Header().Set("Content-Type", http.DetectContentType(body))
	w.Header().Set("ETag", `"`+prefs.Background.Version+`"`)
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// isImageMime：`DetectContentType` 认得出的常见位图。
// 显式白名单而不是 `strings.HasPrefix(mime, "image/")` —— 后者会把将来某个
// 我们没想过的 `image/*` 一起放进来；这几样是浏览器都吃得下的。
func isImageMime(mime string) bool {
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp":
		return true
	}
	return false
}
