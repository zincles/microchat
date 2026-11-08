package config

import (
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
)

// ── 界面偏好（UI prefs）────────────────────────────────────────────
//
// 2026-11-01 用户定：「所有设置均存储在服务器上」—— 从前这些全在浏览器
// localStorage（`mc_theme` 那一串）。搬到服务器的理由：只有一个使用者、只听回环，
// 那么"设置跟着这台服务器走"比"跟着这个浏览器走"才对（换设备、清缓存、装成 PWA 都不丢）。
//
// **两个例外仍住 localStorage**，各有硬理由：
//   - `mc_api`（后端地址）：**存哪儿都存不到服务器自己身上** —— 它是"去哪个服务器"，
//     在拿到地址之前根本没法请求。放服务器是循环依赖。
//   - `mc_last_session`（上次看的那条会话）：这是**设备状态**，不是设置
//     （手机与桌面各自想回到自己上次看的那条）。而且它每次切会话都要写，
//     走一趟 HTTP 不值得。
//
// 文件是 `data/config/prefs.json`。它**由程序整体重写**（不是手写配置），
// 所以缺文件 = 全默认、坏字段 = 校验时点名拒绝，不猜。

// MarkdownType：**一种会被 markdown 解析的语法**（界面据此点出一排开关）。
//
// 为什么清单住后端：前端不该另抄一张表 —— 少一条就留个点不动的死开关，
// 多一条就是个后端不认识的选项（照 `providers` 中文名那条纪律：**名字只有一个来源**）。
//
// `Kind` 只有两个值：`block`（整行/整块的东西）与 `inline`（行内包一层的东西）。
// 界面按它分组，别自己猜。
type MarkdownType struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Sample string `json:"sample"`
}

// MarkdownTypes：全部**会被解析**的语法，顺序 = 界面上排开关的顺序。
//
// ⚠ 这**不是** markdown 的全集：裸 HTML 块（`<div>` 之类）不在里面也永远不会 ——
// 我们**先行转义** `&` 与 `<`（XSS 的门在那儿），所以 HTML 从来就不解析。
// 用户口径（2026-11-01）：「所有的会被 Markdown 解析的非 <div> HTML 块」。
var MarkdownTypes = []MarkdownType{
	{ID: "heading", Name: "标题", Kind: "block", Sample: "# 标题"},
	{ID: "hr", Name: "分隔线", Kind: "block", Sample: "---"},
	{ID: "list", Name: "列表", Kind: "block", Sample: "- 一项"},
	{ID: "blockquote", Name: "引用", Kind: "block", Sample: "> 引文"},
	{ID: "code_block", Name: "代码块", Kind: "block", Sample: "```"},
	{ID: "table", Name: "表格", Kind: "block", Sample: "| a | b |"},
	{ID: "bold", Name: "粗体", Kind: "inline", Sample: "**粗**"},
	{ID: "italic", Name: "斜体", Kind: "inline", Sample: "*斜*"},
	{ID: "strike", Name: "删除线", Kind: "inline", Sample: "~~删~~"},
	{ID: "inline_code", Name: "行内代码", Kind: "inline", Sample: "`代码`"},
	{ID: "link", Name: "链接", Kind: "inline", Sample: "[名](址)"},
	{ID: "image", Name: "图片", Kind: "inline", Sample: "![说明](址)"},
	//  不是标准 markdown 的语义（标准里 `__x__` 是粗体），是**我们自己的扩展**：
	// RP 模型（Discord 味）惯用它表示下划线，见 `markdown.js` 的 `underline` 扩展。
	// 它同样会被解析⇒ 同样得有开关。
	{ID: "underline", Name: "下划线", Kind: "inline", Sample: "__下划__"},
	{ID: "autolink", Name: "自动链接", Kind: "inline", Sample: "https://…"},
	{ID: "breaks", Name: "单换行就换行", Kind: "inline", Sample: "一行\n一行"},
	// 数学不归 markdown 管（是 KaTeX），但它同样"会被解析"⇒ 同一个开关面板里管它才合理。
	{ID: "katex", Name: "数学公式", Kind: "inline", Sample: "$$…$$"},
}

// markdownTypeIDs：合法 id 的集合（校验用）。
func markdownTypeIDs() map[string]bool {
	ids := make(map[string]bool, len(MarkdownTypes))
	for _, t := range MarkdownTypes {
		ids[t.ID] = true
	}
	return ids
}

// MarkdownPrefs：**逐类型**的渲染开关。
//
// 为什么是"关掉哪些"而不是一排布尔：`bool` 的零值是 `false` ⇒ 文件里没写会**静默变成关掉**
// （`abilities.enabled` 上踩过这个坑，见 `AGENTS.md`）。用 `Disabled` 列表则是
// 「缺字段 = 空 = 全开」，新加的语法类型自动跟着开 —— 加一条 `MarkdownTypes` 不用改任何文件。
type MarkdownPrefs struct {
	// Enabled：总开关。nil = 没写 = **开**（保持 2026-11-01 之前的现状：一直在渲染）。
	Enabled *bool `json:"enabled,omitempty"`
	// Disabled：**关掉的那几类**（id 取自 `MarkdownTypes`）；空 = 全开。
	// 未知 id 在写入与读取时都报错、不静默忽略 —— 写了不生效最难查。
	Disabled []string `json:"disabled,omitempty"`
}

// MarkdownOn：总开关的值（缺省开）。
func (m MarkdownPrefs) MarkdownOn() bool { return m.Enabled == nil || *m.Enabled }

// TypeOn：某一类语法要不要渲染（不在 `Disabled` 里就是要）。
func (m MarkdownPrefs) TypeOn(id string) bool {
	for _, off := range m.Disabled {
		if off == id {
			return false
		}
	}
	return true
}

// Validate：坏 id 点名拒绝（先验后写 —— 与 `model_filter` 同一条纪律）。
func (m MarkdownPrefs) Validate() error {
	known := markdownTypeIDs()
	seen := make(map[string]bool, len(m.Disabled))
	for _, id := range m.Disabled {
		if !known[id] {
			names := make([]string, 0, len(MarkdownTypes))
			for _, t := range MarkdownTypes {
				names = append(names, t.ID)
			}
			sort.Strings(names)
			return fmt.Errorf("markdown.disabled 里有不认识的类型 %q（认识的：%s）",
				id, strings.Join(names, " / "))
		}
		if seen[id] {
			return fmt.Errorf("markdown.disabled 里 %q 写了两遍", id)
		}
		seen[id] = true
	}
	return nil
}

// Normalize：把 `Disabled` 收成**规范形态** —— 按类型清单的顺序、去掉重复。
//
// 为什么排序：同一个开关状态必须写出同一份文件，否则"改一下再改回来"会留下
// 两份内容不同、含义相同的 JSON，diff 里全是噪声。
func (m MarkdownPrefs) Normalize() MarkdownPrefs {
	out := MarkdownPrefs{Enabled: m.Enabled}
	if len(m.Disabled) == 0 {
		return out
	}
	off := make(map[string]bool, len(m.Disabled))
	for _, id := range m.Disabled {
		off[id] = true
	}
	for _, t := range MarkdownTypes {
		if off[t.ID] {
			out.Disabled = append(out.Disabled, t.ID)
		}
	}
	return out
}

// Background：自定义背景图。
//
// 本地上传的那张**不住 prefs.json**（一张压过的 JPEG 也能到 700KB，塞进 JSON 里
// 既臃肿又没法用 `curl` 看）：它落 `data/prefs/background.<ext>`，这里只记元信息。
// `Kind` = "" ⇒ 没设；"url" ⇒ 用 `Value` 里的地址；"file" ⇒ 用服务器上那个文件。
type Background struct {
	Kind string `json:"kind,omitempty"`
	// Value：只有 `kind=url` 用它（上传的那张在服务器上，`Kind=file` 时这里是空的）。
	Value  string `json:"value,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	// Version：本地上传图的**内容标识**（上传时铸）。前端据此决定要不要重取
	// —— 地址不变但图换了，光看 URL 是发现不了的。
	Version string `json:"version,omitempty"`
}

// Validate：`url` 必须是 http/https（`javascript:` 之类一律拒 —— 它最终会进 CSS 的 `url()`）。
func (b Background) Validate() error {
	switch b.Kind {
	case "":
		return nil
	case "url":
		if b.Value == "" {
			return fmt.Errorf("background.value 在 kind=url 时不能为空")
		}
		u, err := url.Parse(b.Value)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("background.value 只收 http/https 地址")
		}
		return nil
	case "file":
		if b.Version == "" {
			return fmt.Errorf("background.version 在 kind=file 时不能为空")
		}
		return nil
	default:
		return fmt.Errorf("background.kind 只认 url / file（收到 %q）", b.Kind)
	}
}

// Prefs：`prefs.json` 整体。
//
// 字段名用 snake_case，与 `config.json` 其余部分一致。
type Prefs struct {
	Markdown MarkdownPrefs `json:"markdown"`

	// —— 外观 ——
	Theme     string `json:"theme,omitempty"`
	TextScale string `json:"text_scale,omitempty"`
	// FontBody / FontMono：自定义字族（2026-11-04 用户定）。
	// 标准字体用于全站，等宽字体用于代码块；空 = 用前端内置的系统字族。
	FontBody   string     `json:"font_body,omitempty"`
	FontMono   string     `json:"font_mono,omitempty"`
	Background Background `json:"background,omitempty"`

	// —— 输入 ——
	Send    string `json:"send,omitempty"`
	Preview *bool  `json:"preview,omitempty"`
	// PayloadPreview：进入上下文载荷 tab 时要不要请求并显示载荷（2026-11-04 加）。
	// 与输入框的 `Preview` 分开：一个管输入预演，一个管载荷面板；默认都开。
	PayloadPreview *bool `json:"payload_preview,omitempty"`
	CommandMenu    *bool `json:"command_menu,omitempty"`

	// —— 聊天 ——
	ChromeHide    *bool `json:"chrome_hide,omitempty"`
	PayloadViz    *bool `json:"payload_viz,omitempty"`
	BarLeftHidden *bool `json:"bar_left_hidden,omitempty"`

	// —— 更新 ——
	// UpdateAutoCheck：每 5 分钟查一次 GitHub 最新提交；发现落后时顶部出现更新条。
	// 缺字段 = 开（旧 prefs.json 不用改）。
	UpdateAutoCheck *bool `json:"update_auto_check,omitempty"`
}

// DefaultPrefs：**全默认**（文件缺失时就是它）。
//
// 每一项的默认值都与前端从前的 localStorage 缺省**逐个对齐**（`!== "0"` 的那些 = true，
// `=== "1"` 的那些 = false）—— 迁移不该悄悄改掉任何人的现状。
func DefaultPrefs() Prefs {
	return Prefs{
		Markdown:  MarkdownPrefs{}, // Enabled 为 nil ⇒ 开；Disabled 空 ⇒ 全开
		Theme:     "",
		TextScale: "",
		FontBody:  "",
		FontMono:  "",
		Send:      "",
		// ⚠ 每一格都要**独立**的指针（2026-11-02 修）。从前写的是 `on := true; off := false`
		// 然后所有 `&on` / `&off` 共用同一个变量 —— JSON 解码 `command_menu:true` 会把那个
		// 共享的 `off` 改成 true，连带 `payload_viz` / `bar_left_hidden` 一起变；下一行
		// `payload_viz:false` 又把它改回来。最终值取决于字段在 JSON 里的顺序，
		// 表现是"开关怎么点都存不住"（实测：界面 on、服务端 off）。
		// `new(true)` 每次调用都分配一个新变量（Go 1.26）。
		Preview:         new(true),
		PayloadPreview:  new(true),
		CommandMenu:     new(false),
		ChromeHide:      new(true),
		PayloadViz:      new(false),
		BarLeftHidden:   new(false),
		UpdateAutoCheck: new(true),
	}
}

// LoadPrefs：读 `prefs.json`（**唯一入口**：HTTP 与启动都走它）。
//
// 文件缺失 ⇒ 全默认（不是错）。坏 `disabled` ⇒ 报错且**不悄悄改成人家的值**：
// 宁可让用户看见"这文件有问题"，也别让他的开关莫名其妙地自己全开。
func LoadPrefs(paths Paths) (Prefs, error) {
	prefs := DefaultPrefs()
	if err := loadJSON(paths.Config("prefs.json"), &prefs); err != nil {
		return prefs, err
	}
	if err := prefs.Markdown.Validate(); err != nil {
		return prefs, err
	}
	if err := prefs.Background.Validate(); err != nil {
		return prefs, err
	}
	prefs.Markdown = prefs.Markdown.Normalize()
	return prefs, nil
}

// SavePrefs：整体写回（0600；目录 0755）。
func SavePrefs(paths Paths, prefs Prefs) error {
	prefs.Markdown = prefs.Markdown.Normalize()
	return SaveJSON(paths.Config("prefs.json"), prefs)
}

// BackgroundPath：本地上传那张图的存放路径（**唯一**算式 —— 读、写、删、服务都走它）。
//
// 固定文件名 `background.bin` 而不是"带版本号的文件名"：换图时覆盖同一个文件，
// 省得去清理旧图；缓存靠 `Background.Version` 破（前端拿它当查询参数）。
// 也不存扩展名 —— 类型在**服务那一刻**由 `http.DetectContentType` 现判
// （存了扩展名就要维护"什么类型配什么后缀"，那是个没有收益的额外状态）。
func BackgroundPath(paths Paths) string {
	return filepath.Join(paths.DataDir, "prefs", "background.bin")
}
