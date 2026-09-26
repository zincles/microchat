//! OpenAI-compatible 上游客户端。
//!
//! 当前能力是模型发现：`GET {base_url}/models`。解析刻意宽松——只有 `id` 是必需的，
//! `name`（OpenRouter 扩展）、`context_length`、`top_provider.max_completion_tokens`
//! 给就给、不给就空，缺 `id` 的条目直接丢弃而不是整体失败。

use std::collections::BTreeMap;

use serde::Deserialize;

use crate::config::ProviderConfig;
use crate::model::DiscoveredModel;

#[derive(Debug)]
pub enum Error {
    Http(reqwest::Error),
    /// 上游返回非 2xx。
    Status(u16),
    /// 上游返回非 2xx 且带了正文。对话接口专用：只报状态码没法排查（密钥错？模型名错？）。
    Upstream { status: u16, body: String },
    /// 上游 200，但 `choices[].message.content` 里一个字都没有。
    Empty,
}

impl std::fmt::Display for Error {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Http(e) => write!(f, "上游请求失败: {e}"),
            Self::Status(code) => write!(f, "上游返回 HTTP {code}"),
            Self::Upstream { status, body } => write!(f, "上游返回 HTTP {status}：{body}"),
            Self::Empty => write!(f, "上游没有返回任何内容"),
        }
    }
}

impl std::error::Error for Error {}

pub type Result<T, E = Error> = std::result::Result<T, E>;

pub struct Client {
    http: reqwest::Client,
    base_url: String,
    api_key: Option<String>,
    headers: BTreeMap<String, String>,
}

impl Client {
    pub fn new(provider: &ProviderConfig) -> Result<Self> {
        // 密钥和 provider 住在一起：空串 = 没配（探活/无需鉴权的上游都合法）
        let api_key = (!provider.api_key.is_empty()).then(|| provider.api_key.clone());
        Self::build(
            &provider.base_url,
            api_key,
            provider.headers.clone(),
            provider.timeouts.clone(),
        )
    }

    /// 从 **url + apikey** 直接构造，不经过 `providers.json`——供"先探测再保存"使用。
    pub fn from_parts(
        base_url: &str,
        api_key: Option<String>,
        headers: BTreeMap<String, String>,
    ) -> Result<Self> {
        Self::build(
            base_url,
            api_key,
            headers,
            crate::config::Timeouts::default(),
        )
    }

    fn build(
        base_url: &str,
        api_key: Option<String>,
        headers: BTreeMap<String, String>,
        timeouts: crate::config::Timeouts,
    ) -> Result<Self> {
        let http = reqwest::Client::builder()
            .connect_timeout(std::time::Duration::from_secs(timeouts.connect_secs.max(1)))
            .timeout(std::time::Duration::from_secs(timeouts.total_secs.max(1)))
            .build()
            .map_err(Error::Http)?;
        Ok(Self {
            http,
            base_url: base_url.trim_end_matches('/').to_owned(),
            api_key,
            headers,
        })
    }

    /// `GET {base_url}/models`。缺 `id` 的条目被丢弃。
    pub async fn list_models(&self) -> Result<Vec<DiscoveredModel>> {
        let mut req = self.http.get(format!("{}/models", self.base_url));
        if let Some(key) = &self.api_key {
            req = req.bearer_auth(key);
        }
        for (name, value) in &self.headers {
            req = req.header(name, value);
        }

        let resp = req.send().await.map_err(Error::Http)?;
        if !resp.status().is_success() {
            return Err(Error::Status(resp.status().as_u16()));
        }
        let body: ModelsResponse = resp.json().await.map_err(Error::Http)?;
        Ok(body.data.into_iter().filter_map(WireModel::into_model).collect())
    }

    /// `POST {base_url}/chat/completions`，非流式。返回助手消息的正文。
    ///
    /// `messages` 必须来自 [`crate::vars::build_outgoing`]——那是"剔除状态标签、
    /// 注入当前变量表"的唯一实现处，别在这儿二次拼装。
    pub async fn chat_completion(&self, model: &str, messages: &[WireMessage]) -> Result<String> {
        let mut req = self
            .http
            .post(format!("{}/chat/completions", self.base_url))
            .json(&serde_json::json!({
                "model": model,
                "messages": messages,
                "stream": false,
            }));
        if let Some(key) = &self.api_key {
            req = req.bearer_auth(key);
        }
        for (name, value) in &self.headers {
            req = req.header(name, value);
        }

        let resp = req.send().await.map_err(Error::Http)?;
        let status = resp.status().as_u16();
        let text = resp.text().await.map_err(Error::Http)?;
        if !(200..300).contains(&status) {
            // 上游的错误正文对排查很关键（密钥、模型名、额度），但要截断：
            // 别把上游返回的整页 HTML 一路灌到前端。
            return Err(Error::Upstream {
                status,
                body: snippet(&text),
            });
        }
        let parsed: ChatResponse = serde_json::from_str(&text).map_err(|_| Error::Upstream {
            status,
            body: snippet(&text),
        })?;
        parsed
            .choices
            .into_iter()
            .find_map(|c| c.message.and_then(|m| m.content))
            .filter(|text| !text.is_empty())
            .ok_or(Error::Empty)
    }
}

/// 正文片段：压掉换行、截到 300 字符，够看清楚原因就行。
fn snippet(text: &str) -> String {
    let flat: String = text.split_whitespace().collect::<Vec<_>>().join(" ");
    if flat.chars().count() <= 300 {
        return flat;
    }
    flat.chars().take(300).collect::<String>() + "…"
}

/// 发往上游的一条消息。`role` 取 OpenAI 的取值：system / user / assistant。
#[derive(Debug, Clone, serde::Serialize)]
pub struct WireMessage {
    pub role: &'static str,
    pub content: String,
}

#[derive(Deserialize)]
struct ChatResponse {
    #[serde(default)]
    choices: Vec<ChatChoice>,
}

#[derive(Deserialize)]
struct ChatChoice {
    #[serde(default)]
    message: Option<WireAssistant>,
}

#[derive(Deserialize)]
struct WireAssistant {
    #[serde(default)]
    content: Option<String>,
}

#[derive(Deserialize)]
struct ModelsResponse {
    #[serde(default)]
    data: Vec<WireModel>,
}

#[derive(Deserialize)]
struct WireModel {
    id: Option<String>,
    name: Option<String>,
    owned_by: Option<String>,
    context_length: Option<i64>,
    /// OpenRouter 等：该模型接受的采样参数名。
    #[serde(default)]
    supported_parameters: Vec<String>,
    /// OpenRouter 等：上游给的默认参数。
    #[serde(default)]
    default_parameters: Option<serde_json::Value>,
    #[serde(default)]
    top_provider: Option<WireTopProvider>,
}

#[derive(Deserialize)]
struct WireTopProvider {
    max_completion_tokens: Option<i64>,
}

impl WireModel {
    fn into_model(self) -> Option<DiscoveredModel> {
        let upstream_id = self.id?;
        Some(DiscoveredModel {
            upstream_id,
            upstream_name: self.name,
            owned_by: self.owned_by,
            context_length: self.context_length,
            max_output: self.top_provider.and_then(|top| top.max_completion_tokens),
            supported_parameters: self.supported_parameters,
            default_parameters: self.default_parameters,
        })
    }
}

#[cfg(test)]
mod tests {
    use axum::body::Bytes;
    use axum::http::{HeaderMap, StatusCode};
    use axum::{routing::get, Router};

    use super::*;

    async fn upstream() -> String {
        let app = Router::new().route(
            "/v1/models",
            get(|headers: HeaderMap| async move {
                if headers.get("authorization").and_then(|v| v.to_str().ok())
                    != Some("Bearer sk-test")
                {
                    return (StatusCode::UNAUTHORIZED, Bytes::new());
                }
                let body = serde_json::json!({
                    "object": "list",
                    "data": [
                        { "id": "deepseek/deepseek-v4-flash", "name": "DeepSeek V4 Flash",
                          "owned_by": "deepseek", "context_length": 131072,
                          "supported_parameters": ["temperature", "top_p", "max_tokens"],
                          "default_parameters": { "temperature": 0.8 },
                          "top_provider": { "max_completion_tokens": 8192 } },
                        { "id": "bare-model" },
                        { "name": "缺 id，应被丢弃" }
                    ]
                });
                (StatusCode::OK, Bytes::from(body.to_string()))
            }),
        );
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        tokio::spawn(async move {
            axum::serve(listener, app).await.unwrap();
        });
        format!("http://{addr}/v1")
    }

    fn provider(base_url: String) -> ProviderConfig {
        ProviderConfig {
            id: "test".to_owned(),
            base_url,
            ..Default::default()
        }
    }

    #[tokio::test]
    async fn discovers_models_and_skips_idless_entries() {
        let mut provider = provider(upstream().await);
        provider.api_key = "sk-test".to_owned();
        let client = Client::new(&provider).unwrap();
        let models = client.list_models().await.unwrap();

        assert_eq!(models.len(), 2, "缺 id 的条目应被丢弃");
        assert_eq!(models[0].upstream_id, "deepseek/deepseek-v4-flash");
        assert_eq!(models[0].upstream_name.as_deref(), Some("DeepSeek V4 Flash"));
        assert_eq!(models[0].context_length, Some(131072));
        assert_eq!(models[0].max_output, Some(8192));
        assert_eq!(
            models[0].supported_parameters,
            ["temperature", "top_p", "max_tokens"]
        );
        assert_eq!(models[0].default_parameters.as_ref().unwrap()["temperature"], 0.8);

        assert_eq!(models[1].upstream_id, "bare-model");
        assert_eq!(models[1].upstream_name, None, "名称可缺");
        assert!(models[1].supported_parameters.is_empty(), "参数也可缺");
    }

    #[tokio::test]
    async fn from_parts_works_without_a_configured_provider() {
        let client =
            Client::from_parts(&upstream().await, Some("sk-test".to_owned()), BTreeMap::new())
                .unwrap();
        let models = client.list_models().await.unwrap();
        assert_eq!(models.len(), 2);
    }

    #[tokio::test]
    async fn missing_api_key_surfaces_upstream_status() {
        // 没配密钥（api_key 空）→ 上游会回 401，错误要如实浮上来
        let client = Client::new(&provider(upstream().await)).unwrap();
        let err = client.list_models().await.unwrap_err();
        assert!(matches!(err, Error::Status(401)), "got {err:?}");
    }
}
