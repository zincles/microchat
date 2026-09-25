//! 配置层：`config/` 目录下的用户手写文件（JSONC 容忍：注释、尾逗号）。
//!
//! 归属规则：
//! - `providers.jsonc` / `agents.jsonc` / `config.jsonc` 由用户手写，程序**不隐式改写**
//!   （显式编辑由 API 提供，届时写回会丢注释，这一点在 UI 上要讲明）；
//! - `secrets.json` 存放 API 密钥，`{"<provider_id>": "sk-…"}`，结构上与可分享文件隔离；
//! - 发现所得与用户覆盖落数据库（见 `store`），`providers.jsonc` 里没有模型的影子。

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
    pub fn config_jsonc(&self) -> PathBuf {
        self.config_dir.join("config.jsonc")
    }
    pub fn providers_jsonc(&self) -> PathBuf {
        self.config_dir.join("providers.jsonc")
    }
    pub fn agents_jsonc(&self) -> PathBuf {
        self.config_dir.join("agents.jsonc")
    }
    pub fn secrets_json(&self) -> PathBuf {
        self.config_dir.join("secrets.json")
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
    /// provider handle（`providers.jsonc` 里的 `id`）。
    pub provider: String,
    /// 上游裸模型 id，原样使用。
    pub model: String,
    /// agent handle（`agents.jsonc` 里的 `id`）。
    pub agent: String,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(default)]
pub struct ChatConfig {
    /// 会话标题取首条用户消息的字符数。
    pub title_chars: usize,
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
            provider: "openrouter".to_owned(),
            model: "deepseek/deepseek-v4-flash".to_owned(),
            agent: "default".to_owned(),
        }
    }
}

impl Default for ChatConfig {
    fn default() -> Self {
        Self { title_chars: 32 }
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

/// `providers.jsonc` 里的单个 provider：只有连接信息，没有模型（模型是发现所得，在库里）。
#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(default)]
pub struct ProviderConfig {
    pub id: String,
    pub kind: ProviderKind,
    pub base_url: String,
    /// 额外 HTTP 头，如 OpenRouter 的 `X-Title`。
    pub headers: BTreeMap<String, String>,
    /// 保留未知字段：API 写回文件时不能丢用户自己加的东西。
    #[serde(flatten)]
    pub extra: BTreeMap<String, serde_json::Value>,
}

impl Default for ProviderConfig {
    fn default() -> Self {
        Self {
            id: String::new(),
            kind: ProviderKind::default(),
            base_url: String::new(),
            headers: BTreeMap::new(),
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
        let cfg: Self = load_jsonc(path)?;
        cfg.validate()?;
        Ok(cfg)
    }

    pub fn get(&self, id: &str) -> Option<&ProviderConfig> {
        self.providers.iter().find(|p| p.id == id)
    }

    /// 显式写回——和 agents 一样：只在 API 被调用时发生，且整体重写（注释会丢）。
    pub fn save(&self, path: &Path) -> Result<()> {
        write_json_pretty(path, self)
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

impl AgentsConfig {
    pub fn load(path: &Path) -> Result<Self> {
        let cfg: Self = load_jsonc(path)?;
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

    /// 默认 agent：`default_agent` 指定的那个，找不到则取第一个。
    pub fn default_agent(&self) -> Option<&Agent> {
        self.get(&self.default_agent).or_else(|| self.agents.first())
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

/// `secrets.json`：`{"<provider_id>": "sk-…"}`。缺失视为空。
pub fn load_secrets(path: &Path) -> Result<BTreeMap<String, String>> {
    load_jsonc(path)
}

/// 写 `secrets.json`。只在 API 被显式调用时写，写完立刻把权限收紧到 0600。
pub fn save_secrets(path: &Path, secrets: &BTreeMap<String, String>) -> Result<()> {
    write_json_pretty(path, secrets)?;
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
pub fn load_jsonc<T: DeserializeOwned + Default>(path: &Path) -> Result<T> {
    match std::fs::read_to_string(path) {
        Ok(text) => jsonc_parser::parse_to_serde_value(&text, &jsonc_parser::ParseOptions::default())
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
        load_jsonc(path)
    }

    /// JSONC 解析。`ParseOptions::default()` 已允许注释与尾逗号。
    pub fn parse(text: &str) -> Result<Self> {
        jsonc_parser::parse_to_serde_value(text, &jsonc_parser::ParseOptions::default())
            .map_err(|e| Error::Parse(e.to_string()))
    }
}

#[cfg(test)]
mod tests {
    use std::path::Path;

    use super::{AgentsConfig, Config, ProvidersConfig};

    #[test]
    fn missing_file_yields_defaults() {
        let cfg = Config::load(Path::new("/nonexistent/microchat.jsonc")).unwrap();
        assert_eq!(cfg.server.port, 8787);
        assert_eq!(cfg.chat.title_chars, 32);
        assert!(cfg.server.auth_token.is_none());
    }

    #[test]
    fn jsonc_tolerates_comments_and_trailing_commas() {
        let cfg = Config::parse(
            r#"{
                // 服务端绑定
                "server": { "port": 9000, "auth_token": "s3cret", },
                "chat": { "title_chars": 16 },
            }"#,
        )
        .unwrap();
        assert_eq!(cfg.server.port, 9000);
        assert_eq!(cfg.server.auth_token.as_deref(), Some("s3cret"));
        assert_eq!(cfg.chat.title_chars, 16);
        assert_eq!(cfg.server.host, "127.0.0.1", "未给出的字段应回落默认值");
    }

    #[test]
    fn invalid_json_reports_parse_error() {
        assert!(Config::parse("{ nope").is_err());
    }

    #[test]
    fn providers_parse_with_comments_and_default_kind() {
        let cfg: ProvidersConfig = jsonc_parser::parse_to_serde_value(
            r#"{
                // 本地 Ollama
                "providers": [
                    { "id": "local", "base_url": "http://127.0.0.1:11434/v1" },
                    { "id": "openrouter", "base_url": "https://openrouter.ai/api/v1",
                      "headers": { "X-Title": "microchat" }, },
                ],
            }"#,
            &jsonc_parser::ParseOptions::default(),
        )
        .unwrap();
        cfg.validate().unwrap();
        assert_eq!(cfg.providers.len(), 2);
        assert_eq!(cfg.get("openrouter").unwrap().headers["X-Title"], "microchat");
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
        let path = dir.join("providers.jsonc");

        let config: ProvidersConfig = jsonc_parser::parse_to_serde_value(
            r#"{ "providers": [ {
                    "id": "local",
                    "kind": "openai-compat",
                    "base_url": "http://127.0.0.1:9/v1",
                    "note": "我的备注"
                } ] }"#,
            &jsonc_parser::ParseOptions::default(),
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
    fn agents_keep_unknown_fields_and_resolve_default() {
        let cfg: AgentsConfig = jsonc_parser::parse_to_serde_value(
            r#"{
                "default_agent": "跑团",
                "agents": [
                    { "id": "跑团", "name": "跑团 GM", "system_prompt": "你是 GM",
                      "params": { "temperature": 0.9 }, "note": "自定义字段" },
                    { "id": "写作" },
                ],
            }"#,
            &jsonc_parser::ParseOptions::default(),
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
