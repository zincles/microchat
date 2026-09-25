//! 领域模型与纯规则（无 IO，可直接单测）。
//!
//! id 一律 uuidv7：前 48 位是毫秒时间戳，`ORDER BY id` 即时间序，合并两个存档不撞 id。

use serde::{Deserialize, Serialize};
use uuid::Uuid;

/// 消息角色。命名与 OpenAI 线格式一致；`system` 不作为消息落库——系统提示词是
/// `Conversation.system_prompt`（可变配置 ≠ 历史）。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Role {
    User,
    Assistant,
}

impl Role {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::User => "user",
            Self::Assistant => "assistant",
        }
    }

    pub fn parse(raw: &str) -> Option<Self> {
        match raw {
            "user" => Some(Self::User),
            "assistant" => Some(Self::Assistant),
            _ => None,
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Conversation {
    pub id: Uuid,
    pub title: String,
    pub system_prompt: String,
    /// provider handle（对应 `providers.jsonc` 里的 `id`）。
    pub provider: String,
    /// 上游裸模型 id，原样存取，不拼接、不美化。
    pub model: String,
    /// 生成该会话提示词所用的 agent（`agents.jsonc` 的 `id`），仅作来源标记。
    pub agent_id: String,
    pub created_at: i64,
    pub updated_at: i64,
}

/// agent 缺省 handle。
pub const DEFAULT_AGENT_ID: &str = "default";

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Message {
    pub id: Uuid,
    pub conversation_id: Uuid,
    pub role: Role,
    pub content: String,
    pub created_at: i64,
}

/// 从上游发现的一条模型记录（尚未落库）。
#[derive(Debug, Clone, PartialEq, Default, Serialize, Deserialize)]
pub struct DiscoveredModel {
    pub upstream_id: String,
    pub upstream_name: Option<String>,
    pub owned_by: Option<String>,
    pub context_length: Option<i64>,
    pub max_output: Option<i64>,
    /// 上游声明的可调参数名（OpenRouter 的 `supported_parameters`）。
    #[serde(default)]
    pub supported_parameters: Vec<String>,
    /// 上游给的默认参数。
    #[serde(default)]
    pub default_parameters: Option<serde_json::Value>,
}

/// 已落库的模型。发现列（`upstream_*` / `context_length` / `max_output`）由刷新覆写；
/// 用户列（`display_name` / `params` / `tokenizer`）刷新永不触碰。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ModelEntry {
    pub provider: String,
    pub upstream_id: String,
    pub upstream_name: Option<String>,
    pub owned_by: Option<String>,
    pub context_length: Option<i64>,
    pub max_output: Option<i64>,
    pub display_name: Option<String>,
    /// 用户覆盖的采样参数（JSON 文本）。
    pub params: String,
    /// token 估算口径（JSON 文本）。
    pub tokenizer: String,
    /// 上游声明的参数信息（JSON 文本：`{supported, default}`）。刷新写入，用户不碰，
    /// 与 `params`（用户覆盖）分列，互不覆盖。
    pub upstream_params: String,
    pub first_seen_at: i64,
    pub last_seen_at: i64,
}

impl ModelEntry {
    /// 显示名三级回退：用户覆盖 → 上游名 → `prettify(upstream_id)`。
    pub fn resolved_name(&self) -> String {
        match (&self.display_name, &self.upstream_name) {
            (Some(name), _) if !name.is_empty() => name.clone(),
            (_, Some(name)) if !name.is_empty() => name.clone(),
            _ => prettify(&self.upstream_id),
        }
    }

    pub fn params_json(&self) -> serde_json::Value {
        serde_json::from_str(&self.params)
            .unwrap_or_else(|_| serde_json::Value::Object(Default::default()))
    }

    /// 上游声明的参数信息（`{supported, default}`）。
    pub fn upstream_params_json(&self) -> serde_json::Value {
        serde_json::from_str(&self.upstream_params)
            .unwrap_or_else(|_| serde_json::Value::Object(Default::default()))
    }
}

/// 上游 id → 显示名兜底：取路径末段，按 `-`/`_` 切词、首字母大写。
pub fn prettify(upstream_id: &str) -> String {
    let tail = upstream_id.rsplit('/').next().unwrap_or(upstream_id);
    tail.split(['-', '_'])
        .filter(|word| !word.is_empty())
        .map(|word| {
            let mut chars = word.chars();
            match chars.next() {
                Some(first) => first.to_uppercase().collect::<String>() + chars.as_str(),
                None => String::new(),
            }
        })
        .collect::<Vec<_>>()
        .join(" ")
}

/// 会话标题 = 首条用户消息**去换行**后取前 `chars` 个字符（按字符而非字节截断，CJK 安全）。
pub fn title_from(text: &str, chars: usize) -> String {
    text.replace(['\n', '\r'], " ").trim().chars().take(chars).collect()
}

#[cfg(test)]
mod tests {
    use super::{prettify, title_from, ModelEntry};

    #[test]
    fn title_truncates_chars_not_bytes() {
        let text = "汉".repeat(40);
        let title = title_from(&text, 32);
        assert_eq!(title.chars().count(), 32);
        // 32 个 CJK 字符 = 96 字节：按字节截断会切成残字或只留 10 来个字。
        assert!(title.len() > 32);
    }

    #[test]
    fn title_flattens_newlines_and_trims() {
        assert_eq!(title_from("  第一行\n第二行  ", 10), "第一行 第二行");
    }

    #[test]
    fn prettify_makes_readable_fallback_names() {
        assert_eq!(prettify("deepseek/deepseek-v4-flash"), "Deepseek V4 Flash");
        assert_eq!(prettify("gpt-4o"), "Gpt 4o");
        assert_eq!(prettify("bare-model"), "Bare Model");
    }

    #[test]
    fn resolved_name_prefers_user_override_then_upstream() {
        let mut entry = ModelEntry {
            provider: "p".to_owned(),
            upstream_id: "org/model-x".to_owned(),
            upstream_name: Some("Upstream Name".to_owned()),
            owned_by: None,
            context_length: None,
            max_output: None,
            display_name: None,
            params: "{}".to_owned(),
            tokenizer: "{}".to_owned(),
            upstream_params: "{}".to_owned(),
            first_seen_at: 0,
            last_seen_at: 0,
        };
        assert_eq!(entry.resolved_name(), "Upstream Name");

        entry.display_name = Some("我的名字".to_owned());
        assert_eq!(entry.resolved_name(), "我的名字");

        entry.display_name = Some(String::new());
        assert_eq!(entry.resolved_name(), "Upstream Name", "空覆盖等于没覆盖");

        entry.upstream_name = None;
        assert_eq!(entry.resolved_name(), "Model X");
    }
}
