// Package config：`config/` 下几个文件的读写。
//
// 三条从 Rust 版继承的规矩：
//  1. 文件都可以**缺失**（缺了用代码里的默认值，缺文件也能跑）；
//  2. 程序**整体重写**这些文件（严格 JSON，没有注释可写）；
//  3. `config/` 与 `data/` 永不入库 —— 密钥写在这里，接口一律只回 `has_key`。
package config

import (
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Paths：两个目录都相对工作目录，可用环境变量覆盖（与 Rust 版同名）。
type Paths struct {
	ConfigDir string
	DataDir   string
}

// LoadPaths：目录布局。**配置住数据目录里**（`data/config/`）——
// 备份 / 搬家只要搬 `data/` 一个目录；`MICROCHAT_CONFIG_DIR` 仍可单独把配置挪走。
func LoadPaths() Paths {
	dataDir := envOr("MICROCHAT_DATA_DIR", "data")
	return Paths{
		DataDir:   dataDir,
		ConfigDir: envOr("MICROCHAT_CONFIG_DIR", filepath.Join(dataDir, "config")),
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func (p Paths) Config(name string) string { return filepath.Join(p.ConfigDir, name) }

// loadJSON：读文件到结构体；文件不存在 ⇒ 保持默认值（**不是错误**）。
func loadJSON(path string, into any) error {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, into)
}

// Config：`config.json` 整体（文件可以不存在 ⇒ 全用默认值）。
type Config struct {
	Version  int            `json:"version"`
	Server   ServerConfig   `json:"server"`
	Defaults DefaultsConfig `json:"defaults"`
	Chat     ChatConfig     `json:"chat"`
	Telegram TelegramConfig `json:"telegram"`
}

type ServerConfig struct {
	Host string `json:"host"`
	Port uint16 `json:"port"`
	// JwtKey：登录令牌的签名密钥（HMAC-SHA256，DEFINE「用户与鉴权」）。
	// 缺省 ⇒ 启动时**现铸 32 字节随机并写回本文件**（0600）：零配置能跑；换 key = 全员登出。
	// （旧的 `server.auth_token` 已退休：口令是第一个注册者自己设的。）
	JwtKey *string `json:"jwt_key"`
	// Identity：**服务器级默认特征**（以什么身份自报家门）："" = **pi**（默认）/ "microchat" / "bare" = 什么都不装。
	// 单个渠道可以用 `providers.json` 里的 `identity` 覆盖它。
	Identity string `json:"identity"`
	// RefreshModelsOnStart：**一启动就去问每个渠道有哪些模型**（默认开）。nil = 没写 = 开。
	//
	// 为什么是 `*bool`：`bool` 的零值是 `false` ⇒ "文件里没写"会**静默变成关掉**
	// （这个坑在 `abilities.enabled` 上踩过，见 AGENTS.md 的「踩过的坑」）。
	RefreshModelsOnStart *bool `json:"refresh_models_on_start"`
}

// RefreshModelsOnStart：启动时要不要自动刷一遍模型列表（`server.refresh_models_on_start`，**默认 true**）。
func (c Config) RefreshModelsOnStart() bool {
	return c.Server.RefreshModelsOnStart == nil || *c.Server.RefreshModelsOnStart
}

// WithIdentity：**服务器级默认特征**（`server.identity`）—— 渠道自己没写就跟随服务器。
//
// 一处改、所有没写 `identity` 的渠道都跟着变 ⇒ 这条规则只许有一份实现：
// HTTP 视图（`GET /providers` / 刷新那条路）、启动时的自动刷新、直操模式全都走它。
func (c Config) WithIdentity(provider Provider) Provider {
	if provider.Identity == "" {
		provider.Identity = c.Server.Identity
	}
	return provider
}

// DefaultsConfig：新建会话时的缺省（provider / model / agent）。
type DefaultsConfig struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Agent    string `json:"agent"`
}

// TelegramConfig：TG bot 那一份（单账户：只认一个 Telegram id）。
//
// BotToken 为空 ⇒ 去环境变量 `TELEGRAM_BOT_TOKEN` 找（跟渠道 key 同一条"文件优先、环境兜底"规矩）；
// 都没有 ⇒ bot 不启动（不是错误 —— 没配 token 的人本来就不用 TG）。
// AllowedID = 0 ⇒ 还没绑定（谁来都只回 id 方便绑定）；绑新的自动顶掉旧的。
// Enabled 必须显式开（`true`）：配了 token 也不自动跑 —— 连外网是显式动作，不许"配了就跑"。
type TelegramConfig struct {
	BotToken  string `json:"bot_token"`
	AllowedID int64  `json:"allowed_id"`
	Enabled   bool   `json:"enabled"`
}

// BotToken：生效的 token（文件优先，环境兜底）。
func (c Config) BotToken() string {
	if c.Telegram.BotToken != "" {
		return c.Telegram.BotToken
	}
	return os.Getenv("TELEGRAM_BOT_TOKEN")
}

// DefaultConfig：与 Rust 版的 Default 逐字相同（开箱落到 dummy：没有上游，但立刻有回话）。
func DefaultConfig() Config {
	return Config{
		Version: 1,
		// 默认对局域网开放（0.0.0.0）：手机/平板开 http://<本机IP>:8787/api/v1 就能用。
		// 没设 auth_token 时本网段人人可用 —— 要收紧就设口令（见 AGENTS 配置一节）。
		Server:   ServerConfig{Host: "0.0.0.0", Port: 8787},
		Defaults: DefaultsConfig{Provider: "dummy", Model: DummyModelID, Agent: DefaultAgentID},
		Chat:     DefaultChat(),
	}
}

const (
	DummyModelID   = "dummy" // chat::DUMMY_MODEL_ID
	DefaultAgentID = "default"
)

// LoadConfig：读 `config.json`，缺字段用默认值（`#[serde(default)]` 的等价物）。
// JWTKey：拿签名密钥；文件里没有就现铸 32 字节随机、**写回配置**（0600）再回。
// 写回失败不回退成"临时 key"——那会让每次重启全员登出，宁可启动报错。
func (c Config) JWTKey(paths Paths) ([]byte, error) {
	if c.Server.JwtKey != nil && *c.Server.JwtKey != "" {
		return []byte(*c.Server.JwtKey), nil
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	key := hex.EncodeToString(buf)
	c.Server.JwtKey = &key
	if err := SaveJSON(paths.Config("config.json"), c); err != nil {
		return nil, err
	}
	return []byte(key), nil
}

func LoadConfig(paths Paths) (Config, error) {
	config := DefaultConfig()
	if err := loadJSON(paths.Config("config.json"), &config); err != nil {
		return config, err
	}
	if config.Chat.TitleChars == 0 {
		config.Chat.TitleChars = 32
	}
	if config.Chat.ModelContextTokens == 0 {
		config.Chat.ModelContextTokens = 131072
	}
	if config.Chat.CompactBlocks == 0 {
		config.Chat.CompactBlocks = DefaultCompactBlocks
	}
	if config.Defaults.Provider == "" {
		config.Defaults.Provider = "dummy"
	}
	if config.Defaults.Model == "" {
		config.Defaults.Model = DummyModelID
	}
	if config.Defaults.Agent == "" {
		config.Defaults.Agent = DefaultAgentID
	}
	return config, nil
}

// ChatConfig：`config.json` 的 chat 段（模型上下文 / 摘要触发阈值 / 标题字数 / 默认压几块 / 思考回传）。
type ChatConfig struct {
	TitleChars         int `json:"title_chars"`
	ModelContextTokens int `json:"model_context_tokens"`
	// AutoCompact：**自动压缩**开关（2026-10-11 用户定）。**缺省 = 关** —— 这套还没实测验证过，
	// 要用的自己在设置→聊天 里打开。开 ⇒ 一轮翻 idle 之后看占用：到触发线就压，压到退出线以下
	// （口径见 chat/autocompact.go）。默认关 ⇒ 零值（false）就是诚实值，不需要三态指针。
	AutoCompact          bool `json:"auto_compact,omitempty"`
	CompactTriggerTokens *int `json:"compact_trigger_tokens"`
	// CompactBlocks：压缩不给块数时压几个**对话块**（`compact_blocks`）。单位是块，不是 token。
	CompactBlocks int `json:"compact_blocks"`
	// ReplayReasoning：出站时回传思考（replay metadata，多轮推理不断档）。
	// 缺省 = 关（省上下文；思考仍落库可看，只是不发给上游）。
	ReplayReasoning *bool `json:"replay_reasoning,omitempty"`
}

// DefaultCompactBlocks：`compact_blocks` 的缺省（2026-10-11 用户定：**3** —— 小步快跑：
// 每坨原话只嚼一次，且"抬头"那条路要凑满 N 条同层相邻顶层摘要，N 太大常常凑不齐直接报无可压缩）。
const DefaultCompactBlocks = 3

func DefaultChat() ChatConfig {
	return ChatConfig{TitleChars: 32, ModelContextTokens: 131072, CompactBlocks: DefaultCompactBlocks}
}

// AbilityToggle：**一个能力在这份人格上的开关 + 可选覆盖**（挂 `agents.json` 每个 agent 上）。
//
// 三条口径（见 `AGENTS.md`「后台任务 / 压缩 / 能力」）：
//   - **缺字段 = 默认全开** ⇒ `Enabled` 必须是 `*bool`：`bool` 的零值是 `false`，
//     那样"只写了 provider 没写 enabled"会**静默变成关掉**（这是最容易被写错的一格）；
//   - `provider` / `model` 留空 = 用**会话自己的**（辅助调用本来就要骑同一个会话）；
//   - `prompt` 留空 = 用**代码里的默认模板**（能力只能覆盖，不能新增流程）。
type AbilityToggle struct {
	Enabled  *bool   `json:"enabled,omitempty"`
	Provider *string `json:"provider,omitempty"`
	Model    *string `json:"model,omitempty"`
	// Prompt：这个能力的提示词模板（流程在代码里，模板可换）。空 = 用代码里的默认。
	Prompt *string `json:"prompt,omitempty"`
}

// Agent：会话 Agent（一份命名的人格 + 一组能力开关）。
//
// `Abilities` 的键 = 能力 id（**枚举，写在代码里**，见 `internal/abilities`）；
// 未知的键在写入与启动读取时都**报清楚**（不静默忽略 —— 写了不生效最难查）。
type Agent struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	SystemPrompt string `json:"system_prompt"`
	// FirstMessage：**首段对话**（2026-10-11 用户定）——以这个 agent 新建会话时，
	// 它作为**第一条 assistant 消息**（idx 1）落库；空 = 不启用。正文原样存档
	// （可带 `<state>` 块 —— 于是"首段设初始状态"天然成立）。
	FirstMessage string `json:"first_message,omitempty"`
	// Segments：追加段（段库里的 id 列表，**顺序 = 追加顺序**）。
	// 拼装 = base（`system_prompt`）+ 逐段 `"\n\n"` 拼接 —— **纯字符串拼接**，
	// 无替换无递归（`state.AssembleSystemPrompt`，唯一实现处）。
	Segments  []string                 `json:"segments,omitempty"`
	Abilities map[string]AbilityToggle `json:"abilities,omitempty"`
	// PrependState：出站时把当前状态表渲染成 `<current_state>` 块，贴到用户句首。
	// 缺省 = 关（只有 RP 这类 Agent 需要；一般问答用不到）。
	PrependState *bool `json:"prepend_state,omitempty"`
}

// ⚠ **`display_mode` 已删除**（2026-10-27 用户定：「只允许"你用气泡发消息，AI 回你 markdown
// 而非气泡"的模式」）。从前它是 agent 上的二选一：
//   - `chat`（默认）= 两边都是气泡；
//   - `roleplay`   = 用户气泡 + AI 纯文本。
// 现在**只有后者这一种**（而且不再是"模式"，就是界面的样子）⇒ 这个字段没有意义了，删掉。
// 老 `agents.json` 里还留着 `"display_mode": "chat"/"roleplay"` 也不报错：它成了**不认识的键**，
// 下次整份重写时自己就没了（与当年 `params` / `prompt_order` 同一条处理口径 ✓）。

// Segment：一段可复用的提示词（"system prompt segment"，用户定 2026-10-11）。
//
// 住 `agents.json` 顶层（与"没有 abilities.json"同一条思路：一个文件 = 人格 + 段，
// 卡带导出天然完整）；agent 用 `segments: [id…]` 引用，拼装 = base + 逐段拼接。
type Segment struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	// Imported：**来源标记**（2026-10-11 用户定）—— true = 这条是"卡带导入"带进来的
	// （界面显示「导入」徽标）。记的是**从哪来**，用户编辑不清标；删掉就是删掉。
	Imported bool `json:"imported,omitempty"`
}

type AgentsConfig struct {
	Version      int       `json:"version"`
	DefaultAgent string    `json:"default_agent"`
	Segments     []Segment `json:"segments,omitempty"`
	Agents       []Agent   `json:"agents"`
}

// 内置提示词段（2026-10-11 用户定）：正文住 `segments/*.md`，**随二进制嵌入** ——
// 一字不改地随代码提交/分发（git 里可 diff、新装即得）；**只读、删不掉**（要改就"复制为自定义"）；
// 用户段**不许与内置撞 id**（`ValidateSegments` 在写入与启动读取时都报）。
//
//go:embed segments/*.md
var builtinSegmentFS embed.FS

// builtinSegmentFiles：内置段清单（顺序 = 段库里的展示顺序；id ↔ 文件的映射就在这儿）。
var builtinSegmentFiles = []struct{ ID, File string }{
	{ID: "StateLang 语法", File: "segments/statelang-syntax.md"},
	{ID: "合理的RolePlay", File: "segments/roleplay.md"},
	{ID: "在RolePlay里使用StateLang", File: "segments/statelang-in-roleplay.md"},
	{ID: "Markdown RolePlay相关", File: "segments/markdown-roleplay.md"},
}

// BuiltinSegments：全部内置段（正文现读自嵌入文件）。
func BuiltinSegments() []Segment {
	out := make([]Segment, 0, len(builtinSegmentFiles))
	for _, item := range builtinSegmentFiles {
		text, err := builtinSegmentFS.ReadFile(item.File)
		if err != nil {
			// 文件在编译期就嵌进来了（`go:embed`）—— 读不到只可能是清单里文件名写错，开发错误，直接炸。
			panic("内置提示词段读不到：" + item.File + "（" + err.Error() + "）")
		}
		out = append(out, Segment{ID: item.ID, Text: string(text)})
	}
	return out
}

// FirstMessageOf：这个 agent 的首段对话（trim；空 = 没配）。
func (c AgentsConfig) FirstMessageOf(agentID string) string {
	agent, ok := c.Get(agentID)
	if !ok {
		return ""
	}
	return strings.TrimSpace(agent.FirstMessage)
}

// SegmentByID：按 id 找段（用户库在前，内置在后 —— 撞名被校验挡掉，正常不会两义）。
func (c AgentsConfig) SegmentByID(id string) (Segment, bool) {
	for _, segment := range c.Segments {
		if segment.ID == id {
			return segment, true
		}
	}
	for _, segment := range BuiltinSegments() {
		if segment.ID == id {
			return segment, true
		}
	}
	return Segment{}, false
}

// BuiltinSegmentByID：只在内置里找（段 CRUD 的守卫用：内置只读）。
func BuiltinSegmentByID(id string) (Segment, bool) {
	for _, segment := range BuiltinSegments() {
		if segment.ID == id {
			return segment, true
		}
	}
	return Segment{}, false
}

// ValidateSegments：段库与引用的一致性 —— **写入与启动读取时都报**（与 abilities 一个口径）：
// 段 id 非空且唯一、**不许与内置撞名**；每个 agent 引用的段必须在库**或内置**里
// （写了不生效是最难查的一类问题）。
func (c AgentsConfig) ValidateSegments() error {
	seen := map[string]bool{}
	for _, segment := range c.Segments {
		id := strings.TrimSpace(segment.ID)
		if id == "" {
			return fmt.Errorf("提示词段有空 id")
		}
		if _, builtin := BuiltinSegmentByID(id); builtin {
			return fmt.Errorf("提示词段 id 与内置段撞名：%s（内置只读，换个 id）", id)
		}
		if seen[id] {
			return fmt.Errorf("提示词段 id 重复：%s", id)
		}
		seen[id] = true
	}
	for _, agent := range c.Agents {
		for _, ref := range agent.Segments {
			if !seen[ref] {
				if _, builtin := BuiltinSegmentByID(ref); !builtin {
					return fmt.Errorf("agent %q 引用了不存在的提示词段：%s", agent.ID, ref)
				}
			}
		}
	}
	return nil
}

func (c AgentsConfig) Get(id string) (Agent, bool) {
	for _, agent := range c.Agents {
		if agent.ID == id {
			return agent, true
		}
	}
	return Agent{}, false
}

// DefaultAgentID 的内置 agent：`agents.json` 里没有 `default` 时补上它。
//
// 名字与提示词**照抄旧版**——它会进"生效的系统提示词"，两版必须一模一样。
func BuiltinDefaultAgent() Agent {
	return Agent{ID: "default", Name: "默认助手", SystemPrompt: "You are a helpful assistant."}
}

// Effective：生效的 agent 列表（文件里的那些；没有 `default` 就把内置的插在最前）。
func (c AgentsConfig) Effective() []Agent {
	agents := make([]Agent, 0, len(c.Agents)+1)
	hasDefault := false
	for _, agent := range c.Agents {
		if agent.ID == "default" {
			hasDefault = true
		}
	}
	if !hasDefault {
		agents = append(agents, BuiltinDefaultAgent())
	}
	return append(agents, c.Agents...)
}

// Resolve：按 id 解析（含内置默认）。找不到 ⇒ 空提示词（旧版也是静默的）。
func (c AgentsConfig) Resolve(id string) (Agent, bool) {
	for _, agent := range c.Effective() {
		if agent.ID == id {
			return agent, true
		}
	}
	return Agent{}, false
}

// Provider：一个渠道（密钥就写在这一条里；接口只回 has_key）。
//
// `Vendor`（找谁）× `Protocol`（说什么话）优先；`Kind` 是废弃的只读入别名
// （老配置/旧字面量照常读，包内 `EffectiveVendor` 收敛；`Vendor` 非空时 `Kind` 被忽略）。
type Provider struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Kind     string            `json:"kind"`     // deprecated：只读入；Vendor 非空时忽略
	Vendor   string            `json:"vendor"`   // 新口径：找谁（空 = 看 Kind）
	Protocol string            `json:"protocol"` // 新口径：说什么话（空 = 缺省 chat）
	BaseURL  string            `json:"base_url"`
	Headers  map[string]string `json:"headers"`
	APIKey   string            `json:"api_key"`
	// ModelFilter：**模型筛选器**（2026-10-22 用户定）—— 发现到的模型里，只有 id **包含**这段子串的
	// 才导入；空（或纯空白）= 不筛，全都要。
	//
	// 聚合站的模型动辄几百条（OpenRouter 实测 470 个），真正常用的就几个 ⇒ 用一段子串把列表切小。
	// 语义**只有一份**：`registry.ValidateModelFilter` / `registry.FilterModels`（别在别处再写一遍）：
	//   - **子串**匹配模型 id（`upstream_id`；`openai/` 这类前缀也一起比）：`gpt` 命中
	//     `openai/gpt-5`、`gpt-6`、`openai/gpt-5.6-sol`；
	//   - **不分大小写**（id 里大小写混着：`meta-llama/Llama-3.3-70B`）；
	//   - **不支持通配符 / 布尔**：`*` 会被**拒绝**（它没有"全部"的意思；要全部就留空）。
	//
	// ⚠ 它与"刷新时没见到的删行"是同一套账：改了筛选器**要再刷一次**才生效 —— 刷完不匹配的旧行会被删掉
	// （模型是可再发现的数据，刷新就回来；但那些行上的用户覆盖（display_name / context_override）也跟着没）。
	ModelFilter string `json:"model_filter"`
	// Identity：空 = 跟随服务器默认（最终默认是 pi）。
	Identity string `json:"identity"`
	// ClientUAOverride：直接覆写 UA 字符串（默认空）。**优先级最高** —— 高过身份与服务器默认。
	ClientUAOverride string `json:"client_ua_override"`
	// SessionHeader：会话 id 透传成哪个头。nil = 按 kind 默认（opencode 系 = `x-opencode-session`）；
	// "" = 明确不发；其它 = 用这个名字。
	SessionHeader *string `json:"session_header"`
	// ReasoningField：回传思考用哪个字段名。nil = 按 kind 默认；"" = 不回传。
	ReasoningField *string `json:"reasoning_field"`
	// AllowTools：允许工具透传（默认关）。开了才把 tools / tool_calls 原样发给上游。
	AllowTools *bool `json:"allow_tools"`
	// ToolResultName：工具结果消息里带不带函数名（对应 Pi 的 requiresToolResultName）。
	ToolResultName *bool     `json:"tool_result_name"`
	Stream         *bool     `json:"stream"`          // 缺省 = 开
	StoreReasoning *bool     `json:"store_reasoning"` // 缺省 = 开
	Timeouts       *Timeouts `json:"timeouts"`
	// Endpoints：预设专用（protocol → 整条 URL 的端点表）；只透传，校验在 providers 侧 ——
	// 配置里写了会被忽略+log（不断老配置）。
	Endpoints map[string]string `json:"endpoints,omitempty"`
}

type Timeouts struct {
	ConnectSeconds float64 `json:"connect_seconds"`
	TotalSeconds   float64 `json:"total_seconds"`
}

type ProvidersConfig struct {
	Version   int        `json:"version"`
	Providers []Provider `json:"providers"`
}

func (c ProvidersConfig) Get(id string) (Provider, bool) {
	for _, provider := range c.Providers {
		if provider.ID == id {
			return provider, true
		}
	}
	return Provider{}, false
}

// Load 全部读一遍（任何一个文件缺了都照跑）。
func Load(paths Paths) (chat ChatConfig, agents AgentsConfig, providers ProvidersConfig, err error) {
	chat = DefaultChat()
	err = loadJSON(paths.Config("config.json"), &struct {
		Chat *ChatConfig `json:"chat"`
	}{})
	if err != nil {
		return
	}
	// config.json 里 chat 段可能缺字段 ⇒ 缺的用默认
	var raw struct {
		Chat *ChatConfig `json:"chat"`
	}
	if e := loadJSON(paths.Config("config.json"), &raw); e == nil && raw.Chat != nil {
		chat = *raw.Chat
		if chat.TitleChars == 0 {
			chat.TitleChars = 32
		}
		if chat.ModelContextTokens == 0 {
			chat.ModelContextTokens = 131072
		}
		if chat.CompactBlocks == 0 {
			chat.CompactBlocks = DefaultCompactBlocks
		}
	}
	if err = loadJSON(paths.Config("agents.json"), &agents); err != nil {
		return
	}
	if providers, err = LoadProviders(paths); err != nil {
		return
	}
	return
}

// DefaultProviders：全新装（还没有 providers.json）的内置渠道表（目前只有 dummy 一条）。
// 为什么必须有它：config.Defaults 默认就是 `dummy/dummy`（"开箱立刻有回话"），可全新装没有
// providers.json ⇒ SelectBackend 找不到渠道 ⇒ 落 fallback ⇒ 第一句话回"未配置模型"
// （2026-10-14 走查实测咬到）。**只认"文件缺失"这一种情况** —— 用户真把渠道删光了
// （文件在、列表空）就尊重他，回 fallback 是正确行为。
func DefaultProviders() ProvidersConfig {
	return ProvidersConfig{Version: 1, Providers: []Provider{{ID: "dummy", Vendor: "dummy"}}}
}

// LoadProviders：读 `providers.json` 的唯一入口（`Load` 与 HTTP 层都走它，口径不许分家）。
func LoadProviders(paths Paths) (ProvidersConfig, error) {
	raw, err := os.ReadFile(paths.Config("providers.json"))
	if errors.Is(err, os.ErrNotExist) {
		return DefaultProviders(), nil
	}
	if err != nil {
		return ProvidersConfig{}, err
	}
	var providers ProvidersConfig
	if err := json.Unmarshal(raw, &providers); err != nil {
		return ProvidersConfig{}, err
	}
	return providers, nil
}

// SaveJSON：整体写回（0600 —— 文件里有密钥）。
func SaveJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false) // 与 Rust 版的 serde 一致：文件里别出现 \u003c
	if err := encoder.Encode(value); err != nil {
		return err
	}
	body := bytes.TrimRight(buffer.Bytes(), "\n")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(body, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
