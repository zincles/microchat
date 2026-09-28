//! 配置层：`config/` 目录下的用户手写文件（JSONC 容忍：注释、尾逗号）。
//!
//! 归属规则：
//! - `providers.json` / `agents.json` / `config.json` 由用户手写，程序**不隐式改写**
//!   （显式编辑由 API 提供，届时写回会丢注释，这一点在 UI 上要讲明）；
//! - `providers.json` 里带着 `api_key`（**只住这里**）：整个 `config/` 都在 `.gitignore`
//!   范围内，密钥不会进版本库；接口一律**不回显**它，调试页读它也会先打码；
//! - 发现所得与用户覆盖落数据库（见 `store`），`providers.json` 里没有模型的影子。

use std::collections::{BTreeMap, BTreeSet};
use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};
use serde::de::DeserializeOwned;

/// 目录布局与各文件路径。环境变量 `MICROCHAT_CONFIG_DIR` / `MICROCHAT_DATA_DIR` 可覆盖。
#[derive(Debug, Clone)]
pub struct Paths {
    pub config_dir: PathBuf,
    pub data_dir: PathBuf,
}

impl Default for Paths {
    fn default() -> Self {
        Self {
            config_dir: std::env::var_os("MICROCHAT_CONFIG_DIR")
                .map(PathBuf::from)
                .unwrap_or_else(|| PathBuf::from("config")),
            data_dir: std::env::var_os("MICROCHAT_DATA_DIR")
                .map(PathBuf::from)
                .unwrap_or_else(|| PathBuf::from("data")),
        }
    }
}

impl Paths {
    pub fn config_json(&self) -> PathBuf {
        self.config_dir.join("config.json")
    }
    pub fn providers_json(&self) -> PathBuf {
        self.config_dir.join("providers.json")
    }
    pub fn agents_json(&self) -> PathBuf {
        self.config_dir.join("agents.json")
    }
    /// **工具**的覆盖项（摘要器之类的一次性后台任务）—— **与 agents.json 分开**：
    /// 会话 Agent 是有人格的对话者，工具不是，两边的语义各自干净。
    pub fn abilities_json(&self) -> PathBuf {
        self.config_dir.join("abilities.json")
    }
    pub fn database(&self) -> PathBuf {
        self.data_dir.join("microchat.db")
    }
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(default)]
pub struct Config {
    pub version: u32,
    pub server: ServerConfig,
    pub defaults: DefaultsConfig,
    pub chat: ChatConfig,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(default)]
pub struct ServerConfig {
    pub host: String,
    pub port: u16,
    /// `Some` 时要求 `Authorization: Bearer <token>`；`None` = 本地免鉴权。
    pub auth_token: Option<String>,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(default)]
pub struct DefaultsConfig {
    /// provider handle（`providers.json` 里的 `id`）。
    pub provider: String,
    /// 上游裸模型 id，原样使用。
    pub model: String,
    /// agent handle（`agents.json` 里的 `id`）。
    pub agent: String,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(default)]
pub struct ChatConfig {
    /// 会话标题取首条用户消息的字符数。
    pub title_chars: usize,
    /// **模型上下文**：模型"有多能吃"（token）。
    /// 上游发现到 `context_length` 时**以发现值为准**；这一项是**兜底**
    /// （你库里 `deepseek` 那两行就是上游没给的情况）。
    pub model_context_tokens: usize,
    /// **摘要触发阈值**：占用越过它就 Compact（§24）。`None` ⇒ 预算 × 0.8。
    pub compact_trigger_tokens: Option<usize>,
}

impl Default for Config {
    fn default() -> Self {
        Self {
            version: 1,
            server: ServerConfig::default(),
            defaults: DefaultsConfig::default(),
            chat: ChatConfig::default(),
        }
    }
}

impl Default for ServerConfig {
    fn default() -> Self {
        Self {
            host: "127.0.0.1".to_owned(),
            port: 8787,
            auth_token: None,
        }
    }
}

impl Default for DefaultsConfig {
    fn default() -> Self {
        Self {
            // 开箱默认落到 dummy：没有上游，但立刻有回话。
            // 配好自己的 provider 后改这里（或直接在界面上选模型）。模型选择器落地前，
            // 新建会话一律用这两个默认值。
            provider: "dummy".to_owned(),
            model: crate::chat::DUMMY_MODEL_ID.to_owned(),
            agent: "default".to_owned(),
        }
    }
}

impl Default for ChatConfig {
    fn default() -> Self {
        Self {
            title_chars: 32,
            // 兜底用"现代模型的保守下限"；上游报了就以上游的为准（你这边的模型基本都是 100 万）。
            model_context_tokens: 131_072,
            compact_trigger_tokens: None,
        }
    }
}

/// provider 适配器类型。新协议（anthropic / google）在这里加变体。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default, Deserialize, Serialize)]
pub enum ProviderKind {
    #[default]
    #[serde(rename = "openai-compat")]
    OpenAiCompat,
    /// 没有上游：存在的意义是被显式选中，对话直接走 dummy 兜底。
    #[serde(rename = "dummy")]
    Dummy,
}

/// 单次上游请求的超时。
///
/// 没有超时的 reqwest 客户端 = 上游卡住就永远挂着，界面上只有一个转不完的"等待…"。
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Timeouts {
    /// 建连超时（秒）。
    pub connect_secs: u64,
    /// 一次请求的总超时（秒）——非流式：从发出去到整段回来。
    /// **流式落地后这个要换成"读超时"**（首字节 + 两次增量之间的间隔），
    /// 否则长回答会被总时长砍掉。
    pub total_secs: u64,
}

impl Default for Timeouts {
    fn default() -> Self {
        Self {
            connect_secs: 15,
            total_secs: 300,
        }
    }
}

impl Timeouts {
    /// 全等于默认值时就不写进文件——`providers.json` 保持干净（不塞一堆等于默认值的字段）。
    pub fn is_default(&self) -> bool {
        *self == Self::default()
    }
}

/// `skip_serializing_if` 用：`true`（默认值）不写进文件，`false` 才写。
fn is_true(value: &bool) -> bool {
    *value
}

/// `providers.json` 里的单个 provider：只有连接信息，没有模型（模型是发现所得，在库里）。
#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(default)]
pub struct ProviderConfig {
    pub id: String,
    /// 显示名（可选）：前端下拉里显示它，缺省时回退到 `id`。
    /// 不写进文件时保持文件干净（`skip_serializing_if`），用户手写也能被读到。
    #[serde(skip_serializing_if = "Option::is_none")]
    pub name: Option<String>,
    pub kind: ProviderKind,
    pub base_url: String,
    /// 额外 HTTP 头，如 OpenRouter 的 `X-Title`。
    pub headers: BTreeMap<String, String>,
    /// API 密钥。**和 provider 住在一起**（不另开一个文件）：`config/` 整块在忽略范围内，
    /// 所以它不会进版本库；接口绝不回显（只回 `has_key`），调试页读它也会先打码。
    /// 空串 = 没配。
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub api_key: String,
    /// 超时可单独覆盖；等于默认值时不写进文件（`providers.json` 保持干净）。
    #[serde(skip_serializing_if = "Timeouts::is_default")]
    pub timeouts: Timeouts,
    /// 走流式（SSE）。**默认开**：界面能边生成边显示。少数上游不支持、或流式有问题时，
    /// 在这个 provider 上写 `"stream": false` 退回非流式——行为只是"不播动画"。
    /// 默认值走结构体的 `Default`（`#[serde(default)]`），不写进文件（写出来只会在默认时显得吵）。
    #[serde(skip_serializing_if = "is_true")]
    pub stream: bool,
    /// 把上游的"思考"也留档（`messages.reasoning`）。**默认开**：事后想看还有。
    /// 关掉只影响留档——流式动画照样会显示思考（那是另一条缓冲，见 `turn.rs`）。
    #[serde(skip_serializing_if = "is_true")]
    pub store_reasoning: bool,
    /// 保留未知字段：API 写回文件时不能丢用户自己加的东西。
    #[serde(flatten)]
    pub extra: BTreeMap<String, serde_json::Value>,
}

impl Default for ProviderConfig {
    fn default() -> Self {
        Self {
            id: String::new(),
            name: None,
            kind: ProviderKind::default(),
            base_url: String::new(),
            headers: BTreeMap::new(),
            api_key: String::new(),
            timeouts: Timeouts::default(),
            stream: true,
            store_reasoning: true,
            extra: BTreeMap::new(),
        }
    }
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(default)]
pub struct ProvidersConfig {
    pub version: u32,
    pub providers: Vec<ProviderConfig>,
}

impl Default for ProvidersConfig {
    fn default() -> Self {
        Self {
            version: 1,
            providers: Vec::new(),
        }
    }
}

impl ProvidersConfig {
    pub fn load(path: &Path) -> Result<Self> {
        let cfg: Self = load_json(path)?;
        cfg.validate()?;
        Ok(cfg)
    }

    pub fn get(&self, id: &str) -> Option<&ProviderConfig> {
        self.providers.iter().find(|p| p.id == id)
    }

    /// 显式写回——和 agents 一样：只在 API 被调用时发生，且整体重写（注释会丢）。
    /// 这个文件里有 `api_key`，所以写完把权限收紧（`write_json_private`）。
    pub fn save(&self, path: &Path) -> Result<()> {
        write_json_private(path, self)
    }

    pub fn validate(&self) -> Result<()> {
        let mut seen = BTreeSet::new();
        for provider in &self.providers {
            if provider.id.is_empty() {
                return Err(Error::Parse("provider.id 不能为空".to_owned()));
            }
            match provider.kind {
                ProviderKind::OpenAiCompat => {
                    if !provider.base_url.starts_with("http://")
                        && !provider.base_url.starts_with("https://")
                    {
                        return Err(Error::Parse(format!(
                            "{} 的 base_url 必须以 http:// 或 https:// 开头",
                            provider.id
                        )));
                    }
                }
                ProviderKind::Dummy => {
                    if !provider.base_url.is_empty() {
                        return Err(Error::Parse(format!(
                            "{} 是 dummy 类型，不该填 base_url",
                            provider.id
                        )));
                    }
                }
            }
            if !seen.insert(&provider.id) {
                return Err(Error::Parse(format!("provider id 重复: {}", provider.id)));
            }
        }
        Ok(())
    }
}

/// **工具**的覆盖项（`config/abilities.json`）。
///
/// 工具的身份、显示名、默认模板都在代码里（`crate::abilities::Tool` 的枚举变体）：
/// **工具一定是专用的，没有复用可言**（§25）——所以这里只能**覆盖**某个已有工具的行为，
/// 不能凭空造一个新工具（想加工具就得写 Rust）。
#[derive(Debug, Clone, Default, Deserialize, Serialize)]
#[serde(default)]
pub struct AbilityOverride {
    /// 模板正文。**空 = 用内置模板**；非空时它的哈希就是 `prompt_version`。
    pub system_prompt: String,
    /// 留空 = 跟随会话的渠道。
    pub provider: Option<String>,
    /// 留空 = 跟随会话的模型。
    pub model: Option<String>,
    pub params: serde_json::Value,
    /// 保留未知字段：写回文件时不丢用户数据。
    #[serde(flatten)]
    pub extra: BTreeMap<String, serde_json::Value>,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(default)]
pub struct AbilitiesConfig {
    pub version: u32,
    /// 键 = `Tool::id()`。**没有这个键 = 该工具全用内置**（文件不存在也能跑）。
    pub abilities: BTreeMap<String, AbilityOverride>,
}

impl Default for AbilitiesConfig {
    fn default() -> Self {
        Self {
            version: 1,
            abilities: BTreeMap::new(),
        }
    }
}

impl AbilitiesConfig {
    pub fn load(path: &Path) -> Result<Self> {
        load_json(path)
    }

    pub fn save(&self, path: &Path) -> Result<()> {
        write_json_pretty(path, self)
    }

    pub fn get(&self, id: &str) -> Option<&AbilityOverride> {
        self.abilities.get(id)
    }
}

/// agent 预设。本轮只消费 `system_prompt`；`params` / `prompt_order` 是已定型的格式位，
/// 留给"模型参数"与"提示词组合"落地时填充。
#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(default)]
pub struct Agent {
    pub id: String,
    pub name: String,
    pub system_prompt: String,
    pub params: serde_json::Value,
    pub prompt_order: Vec<serde_json::Value>,
    /// 保留未知字段：API 显式写回文件时不丢用户数据。
    #[serde(flatten)]
    pub extra: BTreeMap<String, serde_json::Value>,
}

impl Default for Agent {
    fn default() -> Self {
        Self {
            id: String::new(),
            name: String::new(),
            system_prompt: String::new(),
            params: serde_json::Value::Object(Default::default()),
            prompt_order: Vec::new(),
            extra: BTreeMap::new(),
        }
    }
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(default)]
pub struct AgentsConfig {
    pub version: u32,
    pub default_agent: String,
    pub agents: Vec<Agent>,
}

impl Default for AgentsConfig {
    fn default() -> Self {
        Self {
            version: 1,
            default_agent: "default".to_owned(),
            agents: Vec::new(),
        }
    }
}

/// 内置默认 agent：`agents.json` 里没有 `default` 时补上它。
///
/// 与 dummy 模型对称：不来自配置文件，但系统**永远**有一个可用身份。
/// 它只带一段提示词，别的一概没有。
pub fn builtin_default_agent() -> Agent {
    Agent {
        id: crate::model::DEFAULT_AGENT_ID.to_owned(),
        name: "默认助手".to_owned(),
        system_prompt: "You are a helpful assistant.".to_owned(),
        ..Default::default()
    }
}

impl AgentsConfig {
    /// 生效的 agent 列表：文件里的那些；文件里没有 `default` 时，把内置默认插在最前。
    pub fn effective(&self) -> Vec<Agent> {
        let mut agents = self.agents.clone();
        if !agents
            .iter()
            .any(|agent| agent.id == crate::model::DEFAULT_AGENT_ID)
        {
            agents.insert(0, builtin_default_agent());
        }
        agents
    }

    /// 按 id 解析（含内置默认）。返回克隆：结果可能来自"合成"而不是文件。
    pub fn resolve(&self, id: &str) -> Option<Agent> {
        self.effective().into_iter().find(|agent| agent.id == id)
    }

    pub fn load(path: &Path) -> Result<Self> {
        let cfg: Self = load_json(path)?;
        cfg.validate()?;
        Ok(cfg)
    }

    pub fn get(&self, id: &str) -> Option<&Agent> {
        self.agents.iter().find(|a| a.id == id)
    }

    /// 显式写回——程序唯一会写用户文件的地方，且只在 API 被调用时发生。
    /// 整体重写会丢掉注释：想保留注释就别用界面改，二选一。
    pub fn save(&self, path: &Path) -> Result<()> {
        write_json_pretty(path, self)
    }

    /// 默认 agent：`default_agent` 指定的那个；文件里没有这个 id，就退回**内置默认**
    /// （`effective()` 里补的那个），最后才考虑文件里的第一个。
    ///
    /// 别写成 `get(...).or_else(first)`：那会把"没配 default_agent"变成
    /// "随便挑一个文件里的 agent"——新建会话可能带着某个测试 agent 的提示词出门。
    pub fn default_agent(&self) -> Option<Agent> {
        // 空串也算"没配"（文件被整体重写时很容易留下一个空串），否则会掉到
        // "取文件里第一个"——那可能是某个测试 agent。
        let named = self.default_agent.trim();
        if !named.is_empty() {
            if let Some(agent) = self.resolve(named) {
                return Some(agent);
            }
        }
        self.resolve(crate::model::DEFAULT_AGENT_ID)
            .or_else(|| self.agents.first().cloned())
    }

    pub fn validate(&self) -> Result<()> {
        let mut seen = BTreeSet::new();
        for agent in &self.agents {
            if agent.id.is_empty() {
                return Err(Error::Parse("agent.id 不能为空".to_owned()));
            }
            if !seen.insert(&agent.id) {
                return Err(Error::Parse(format!("agent id 重复: {}", agent.id)));
            }
        }
        Ok(())
    }
}

/// 写 `providers.json` 这类**含密钥**的文件：写完立刻把权限收紧到 0600。
///
/// 密钥住在这个文件里，`chmod 600` 只是最低限度的卫生（同一个用户当然读得到），
/// 真正要紧的是"不回显、不打进版本库、不出现在调试页"。
fn write_json_private(path: &Path, value: &impl Serialize) -> Result<()> {
    write_json_pretty(path, value)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o600));
    }
    Ok(())
}

#[derive(Debug)]
pub enum Error {
    Io(std::io::Error),
    Parse(String),
}

impl From<std::io::Error> for Error {
    fn from(e: std::io::Error) -> Self {
        Self::Io(e)
    }
}

impl std::fmt::Display for Error {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Io(e) => write!(f, "读取配置失败: {e}"),
            Self::Parse(e) => write!(f, "解析配置失败: {e}"),
        }
    }
}

impl std::error::Error for Error {}

pub type Result<T, E = Error> = std::result::Result<T, E>;

/// 读 JSONC；文件不存在 → `T::default()`。前端设置也复用它。
pub fn load_json<T: DeserializeOwned + Default>(path: &Path) -> Result<T> {
    match std::fs::read_to_string(path) {
        Ok(text) => serde_json::from_str(&text)
            .map_err(|e| Error::Parse(e.to_string())),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(T::default()),
        Err(e) => Err(Error::Io(e)),
    }
}

/// 同目录 tmp + rename 原子替换，避免半截文件；无需 fsync（用户配置，丢了重写即可）。
pub fn write_json_pretty<T: Serialize>(path: &Path, value: &T) -> Result<()> {
    let text = serde_json::to_string_pretty(value)
        .map_err(|e| Error::Parse(format!("序列化失败: {e}")))?;
    if let Some(dir) = path.parent() {
        if !dir.as_os_str().is_empty() {
            std::fs::create_dir_all(dir)?;
        }
    }
    let tmp = path.with_extension("jsonc.tmp");
    std::fs::write(&tmp, text)?;
    std::fs::rename(&tmp, path)?;
    Ok(())
}

impl Config {
    /// 读取配置；文件不存在 → 全默认。
    pub fn load(path: &Path) -> Result<Self> {
        load_json(path)
    }

    /// 解析一份配置文本（**严格 JSON**：注释与尾逗号会报错）。
    pub fn parse(text: &str) -> Result<Self> {
        serde_json::from_str(text)
            .map_err(|e| Error::Parse(e.to_string()))
    }
}

#[cfg(test)]
mod tests {
    use std::path::Path;

    use super::{AgentsConfig, Config, ProvidersConfig};

    #[test]
    fn missing_file_yields_defaults() {
        let cfg = Config::load(Path::new("/nonexistent/microchat.json")).unwrap();
        assert_eq!(cfg.server.port, 8787);
        assert_eq!(cfg.chat.title_chars, 32);
        assert!(cfg.server.auth_token.is_none());
    }

    #[test]
    fn invalid_json_reports_parse_error() {
        assert!(Config::parse("{ nope").is_err());
    }

    #[test]
    fn providers_parse_and_kind_defaults_to_openai_compat() {
        let cfg: ProvidersConfig = serde_json::from_str(
            r#"{
                "providers": [
                    { "id": "local", "base_url": "http://127.0.0.1:11434/v1" },
                    { "id": "openrouter", "name": "OpenRouter",
                      "base_url": "https://openrouter.ai/api/v1",
                      "headers": { "X-Title": "microchat" } }
                ]
            }"#,
        )
        .unwrap();
        cfg.validate().unwrap();
        assert_eq!(cfg.providers.len(), 2);
        assert_eq!(cfg.get("openrouter").unwrap().headers["X-Title"], "microchat");
        // 显示名可选：写了读得到，没写就是 None（客户端回退到 id）
        assert_eq!(cfg.get("openrouter").unwrap().name.as_deref(), Some("OpenRouter"));
        assert_eq!(cfg.get("local").unwrap().name, None);
        // 没写 kind ⇒ 默认 openai-compat
        assert_eq!(cfg.get("local").unwrap().kind, super::ProviderKind::OpenAiCompat);
    }

    /// 严格 JSON：注释与尾逗号都不接受——配置文件会被程序整体重写，注释留不住，
    /// 与其留个"写了也会丢"的假象，不如当场报错。
    #[test]
    fn config_rejects_comments_and_trailing_commas() {
        let parse = |text: &str| serde_json::from_str::<ProvidersConfig>(text);
        assert!(parse(r#"{ // 注释
 "providers": [] }"#).is_err());
        assert!(parse(r#"{ "providers": [], }"#).is_err());
        assert!(parse(r#"{ "providers": [] }"#).is_ok());
    }

    #[test]
    fn default_agent_falls_back_to_the_builtin_not_the_first_file_agent() {
        let cfg: AgentsConfig = serde_json::from_str(
            r#"{ "agents": [ { "id": "ze", "name": "测试", "system_prompt": "hi" } ] }"#,
        )
        .unwrap();
        // 没配 default_agent（默认值 "default"），文件里也没有 default
        // → 该用**内置默认**，而不是"随便挑第一个"
        assert_eq!(
            cfg.default_agent().map(|agent| agent.id),
            Some(crate::model::DEFAULT_AGENT_ID.to_owned())
        );
    }

    #[test]
    fn duplicate_provider_id_is_rejected() {
        let cfg = ProvidersConfig {
            providers: vec![
                super::ProviderConfig {
                    id: "dup".to_owned(),
                    base_url: "http://a/v1".to_owned(),
                    ..Default::default()
                },
                super::ProviderConfig {
                    id: "dup".to_owned(),
                    base_url: "http://b/v1".to_owned(),
                    ..Default::default()
                },
            ],
            ..Default::default()
        };
        assert!(cfg.validate().is_err());
    }

    #[test]
    fn provider_base_url_must_carry_a_scheme() {
        let config = ProvidersConfig {
            providers: vec![super::ProviderConfig {
                id: "bad".to_owned(),
                base_url: "127.0.0.1:11434/v1".to_owned(),
                ..Default::default()
            }],
            ..Default::default()
        };
        assert!(config.validate().is_err(), "缺 scheme 的 base_url 应被拒");
    }

    #[test]
    fn provider_write_back_keeps_kind_spelling_and_unknown_fields() {
        let dir = std::env::temp_dir().join(format!("microchat-cfg-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("providers.json");

        let config: ProvidersConfig = serde_json::from_str(
            r#"{ "providers": [ {
                    "id": "local",
                    "kind": "openai-compat",
                    "base_url": "http://127.0.0.1:9/v1",
                    "note": "我的备注"
                } ] }"#,
        )
        .unwrap();
        config.validate().unwrap();
        config.save(&path).unwrap();

        let reread = ProvidersConfig::load(&path).unwrap();
        assert_eq!(reread.providers[0].kind, super::ProviderKind::OpenAiCompat);
        assert_eq!(reread.providers[0].extra["note"], "我的备注", "未知字段不能丢");
        assert!(
            std::fs::read_to_string(&path).unwrap().contains("openai-compat"),
            "写回后的 kind 拼写必须与文档一致"
        );

        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn builtin_agent_fills_in_when_file_lacks_default() {
        let mut config = AgentsConfig::default();
        assert_eq!(config.effective().len(), 1);
        assert_eq!(
            config.resolve("default").unwrap().system_prompt,
            "You are a helpful assistant."
        );

        // 文件里已经有 default → 不重复插入，且以文件为准
        config.agents.push(super::Agent {
            id: "default".to_owned(),
            name: "我的默认".to_owned(),
            ..Default::default()
        });
        assert_eq!(config.effective().len(), 1);
        assert_eq!(config.resolve("default").unwrap().name, "我的默认");
    }

    #[test]
    fn agents_keep_unknown_fields_and_resolve_default() {
        let cfg: AgentsConfig = serde_json::from_str(
            r#"{
                "default_agent": "跑团",
                "agents": [
                    { "id": "跑团", "name": "跑团 GM", "system_prompt": "你是 GM",
                      "params": { "temperature": 0.9 }, "note": "自定义字段" },
                    { "id": "写作" }
                ]
            }"#,
        )
        .unwrap();
        cfg.validate().unwrap();

        let gm = cfg.default_agent().unwrap();
        assert_eq!(gm.id, "跑团");
        assert_eq!(gm.system_prompt, "你是 GM");
        assert_eq!(gm.params["temperature"], 0.9);
        assert_eq!(gm.extra["note"], "自定义字段", "未知字段必须保留");
    }
}
