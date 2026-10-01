// Package config：`config/` 下几个文件的读写。
//
// 三条从 Rust 版继承的规矩：
//  1. 文件都可以**缺失**（缺了用代码里的默认值，缺文件也能跑）；
//  2. 程序**整体重写**这些文件（严格 JSON，没有注释可写）；
//  3. `config/` 与 `data/` 永不入库 —— 密钥写在这里，接口一律只回 `has_key`。
package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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
}

type ServerConfig struct {
	Host      string  `json:"host"`
	Port      uint16  `json:"port"`
	AuthToken *string `json:"auth_token"`
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

// DefaultConfig：与 Rust 版的 Default 逐字相同（开箱落到 dummy：没有上游，但立刻有回话）。
func DefaultConfig() Config {
	return Config{
		Version:  1,
		Server:   ServerConfig{Host: "127.0.0.1", Port: 8787},
		Defaults: DefaultsConfig{Provider: "dummy", Model: DummyModelID, Agent: DefaultAgentID},
		Chat:     DefaultChat(),
	}
}

const (
	DummyModelID   = "dummy" // chat::DUMMY_MODEL_ID
	DefaultAgentID = "default"
)

// LoadConfig：读 `config.json`，缺字段用默认值（`#[serde(default)]` 的等价物）。
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

// ChatConfig：`config.json` 的 chat 段（模型上下文 / 摘要触发阈值 / 标题字数 / 默认压几块）。
type ChatConfig struct {
	TitleChars           int  `json:"title_chars"`
	ModelContextTokens   int  `json:"model_context_tokens"`
	CompactTriggerTokens *int `json:"compact_trigger_tokens"`
	// CompactBlocks：压缩不给块数时压几个**对话块**（`compact_blocks`）。单位是块，不是 token。
	CompactBlocks int `json:"compact_blocks"`
}

// DefaultCompactBlocks：`compact_blocks` 的缺省（照 `AGENTS.md` 的参数那一节）。
const DefaultCompactBlocks = 10

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
	ID           string                   `json:"id"`
	Name         string                   `json:"name"`
	SystemPrompt string                   `json:"system_prompt"`
	Abilities    map[string]AbilityToggle `json:"abilities,omitempty"`
}

type AgentsConfig struct {
	Version      int     `json:"version"`
	DefaultAgent string  `json:"default_agent"`
	Agents       []Agent `json:"agents"`
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
	if err = loadJSON(paths.Config("providers.json"), &providers); err != nil {
		return
	}
	return
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
