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
    /// SSE 流本身出问题（连接断了、分帧坏了、某个 chunk 解析不了）。
    Stream(String),
    /// 上游 200，但 `choices[].message.content` 里一个字都没有。
    Empty,
}

impl std::fmt::Display for Error {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Http(e) => write!(f, "上游请求失败: {e}"),
            Self::Status(code) => write!(f, "上游返回 HTTP {code}"),
            Self::Upstream { status, body } => write!(f, "上游返回 HTTP {status}：{body}"),
            Self::Stream(message) => write!(f, "流式中断: {message}"),
            Self::Empty => write!(f, "上游没有返回任何内容"),
        }
    }
}

impl std::error::Error for Error {}

pub type Result<T, E = Error> = std::result::Result<T, E>;

/// 流式增量的**种类**：正式正文，还是"思考"。
///
/// 推理型模型（DeepSeek V4 系、各家 thinking 模型）会先一连串吐 `reasoning`、
/// 再吐 `content`。两种都交给调用方——但**只有 `Content` 会进最终消息**，
/// `Reasoning` 纯粹是"让界面别看起来死着"。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ChunkKind {
    Content,
    Reasoning,
}

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

    /// `POST {base_url}/chat/completions`，**流式**（SSE）。返回完整正文，每个增量交给 `on_chunk`。
    ///
    /// SSE 解析交给 `eventsource-stream`（成熟库）：分帧、多行 `data:`、`[DONE]` 都归它管，
    /// 这里只做"把 `delta.content` 拼起来"。**增量只服务动画**——落库与一切计算都只认
    /// 这里返回的完整正文。
    pub async fn chat_completion_stream(
        &self,
        model: &str,
        messages: &[WireMessage],
        mut on_chunk: impl FnMut(ChunkKind, &str),
    ) -> Result<String, Error> {
        use eventsource_stream::Eventsource;
        use futures_util::StreamExt;

        let mut req = self
            .http
            .post(format!("{}/chat/completions", self.base_url))
            .json(&Self::chat_body(model, messages, true));
        if let Some(key) = &self.api_key {
            req = req.bearer_auth(key);
        }
        for (name, value) in &self.headers {
            req = req.header(name, value);
        }

        let resp = req.send().await.map_err(Error::Http)?;
        let status = resp.status().as_u16();
        if !(200..300).contains(&status) {
            // 密钥错/模型名错这类问题要在**开流之前**说清楚，别等流里冒出一堆解析错误
            let text = resp.text().await.map_err(Error::Http)?;
            return Err(Error::Upstream {
                status,
                body: snippet(&text),
            });
        }

        let mut stream = resp.bytes_stream().eventsource();
        let mut full = String::new();
        while let Some(event) = stream.next().await {
            let event = event.map_err(|e| Error::Stream(e.to_string()))?;
            let data = event.data.trim();
            if data == "[DONE]" {
                break;
            }
            if data.is_empty() {
                continue;
            }
            // 心跳/注释帧解析不了就跳过，不打断整条流
            let Ok(chunk) = serde_json::from_str::<ChatStreamChunk>(data) else {
                continue;
            };
            for choice in chunk.choices {
                let Some(delta) = choice.delta else {
                    continue;
                };
                // 思考与正文分开报：前者只服务动画，后者才是最终消息的内容
                if let Some(reasoning) = delta.reasoning.filter(|text| !text.is_empty()) {
                    on_chunk(ChunkKind::Reasoning, &reasoning);
                }
                if let Some(content) = delta.content.filter(|text| !text.is_empty()) {
                    full.push_str(&content);
                    on_chunk(ChunkKind::Content, &content);
                }
            }
        }

        if full.is_empty() {
            return Err(Error::Empty);
        }
        Ok(full)
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

    /// `/chat/completions` 的**请求体**——"发出去什么"只在这一处构造。
    ///
    /// `pub` 是为了调试：调用方拿它留档（调试页要原样看最近一次发了什么），
    /// 而不是自己再拼一份（拼两份迟早会漂）。
    pub fn chat_body(model: &str, messages: &[WireMessage], stream: bool) -> serde_json::Value {
        serde_json::json!({
            "model": model,
            "messages": messages,
            "stream": stream,
        })
    }

    /// `POST {base_url}/chat/completions`，**非流式**。返回助手消息的正文。
    ///
    /// `messages` 必须来自 [`crate::vars::build_outgoing`]——那是"剔除状态标签、
    /// 注入当前变量表"的唯一实现处，别在这儿二次拼装。
    pub async fn chat_completion(&self, model: &str, messages: &[WireMessage]) -> Result<String, Error> {
        let mut req = self
            .http
            .post(format!("{}/chat/completions", self.base_url))
            .json(&Self::chat_body(model, messages, false));
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

/// 流式 chunk：`data: {"choices":[{"delta":{"content":"你"}}]}`。
///
/// 宽容解析：`delta` 可能只有 `role` 没有 `content`（首帧）、`content` 可能是空的
/// （有些上游发心跳），这几种情况都当"这一帧没有正文"跳过。
#[derive(Deserialize)]
struct ChatStreamChunk {
    #[serde(default)]
    choices: Vec<StreamChoice>,
}

#[derive(Deserialize)]
struct StreamChoice {
    #[serde(default)]
    delta: Option<StreamDelta>,
}

#[derive(Deserialize)]
struct StreamDelta {
    #[serde(default)]
    content: Option<String>,
    /// 推理型模型先吐"思考"，字段名各家不一：OpenRouter 系叫 `reasoning`，
    /// DeepSeek 官方叫 `reasoning_content`——两个都认。
    #[serde(default, alias = "reasoning_content")]
    reasoning: Option<String>,
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

    /// 一个"只会说 SSE 的"假上游：正文原样当流吐回去。
    async fn sse_upstream(body: &'static str) -> String {
        let app = Router::new().route(
            "/v1/chat/completions",
            axum::routing::post(move || async move {
                (
                    StatusCode::OK,
                    [("content-type", "text/event-stream")],
                    body,
                )
            }),
        );
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        tokio::spawn(async move {
            axum::serve(listener, app).await.unwrap();
        });
        format!("http://{addr}/v1")
    }

    /// 流式解析要能扛住上游的各种花样：首帧只有 `role`、心跳注释、空 `content`、
    /// 以及 `[DONE]`。增量**只给有正文的那些**，返回值是完整正文。
    #[tokio::test]
    async fn streaming_skips_noise_and_returns_the_full_text() {
        let base = sse_upstream(concat!(
            "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n",
            ": 这是心跳注释，不该被当数据\n\n",
            "data: {\"choices\":[{\"delta\":{\"content\":\"你\"}}]}\n\n",
            "data: not json at all\n\n",
            "data: {\"choices\":[{\"delta\":{\"content\":\"\"}}]}\n\n",
            "data: {\"choices\":[{\"delta\":{\"content\":\"，世界\"}}]}\n\n",
            "data: [DONE]\n\n",
        ))
        .await;
        let client = Client::new(&provider(base)).unwrap();

        let mut chunks: Vec<String> = Vec::new();
        let mut kinds: Vec<ChunkKind> = Vec::new();
        let full = client
            .chat_completion_stream("m", &[], |kind, delta| {
                kinds.push(kind);
                chunks.push(delta.to_owned());
            })
            .await
            .unwrap();

        assert_eq!(full, "你，世界");
        assert_eq!(chunks, vec!["你".to_owned(), "，世界".to_owned()], "空帧与杂音都不给增量");
        assert_eq!(kinds, vec![ChunkKind::Content, ChunkKind::Content]);
    }

    /// 推理型模型先吐 `reasoning`（OpenRouter 系）或 `reasoning_content`（DeepSeek 官方）——
    /// 两种都要认，且**只有正文进最终消息**。
    #[tokio::test]
    async fn streaming_reports_reasoning_separately_from_content() {
        let base = sse_upstream(concat!(
            "data: {\"choices\":[{\"delta\":{\"reasoning\":\"先想\"}}]}\n\n",
            "data: {\"choices\":[{\"delta\":{\"reasoning\":\"一下\"}}]}\n\n",
            "data: {\"choices\":[{\"delta\":{\"content\":\"答案\"}}]}\n\n",
            "data: [DONE]\n\n",
        ))
        .await;
        let client = Client::new(&provider(base)).unwrap();

        let mut log: Vec<(ChunkKind, String)> = Vec::new();
        let full = client
            .chat_completion_stream("m", &[], |kind, delta| log.push((kind, delta.to_owned())))
            .await
            .unwrap();

        assert_eq!(full, "答案", "思考不进最终消息");
        assert_eq!(
            log,
            vec![
                (ChunkKind::Reasoning, "先想".to_owned()),
                (ChunkKind::Reasoning, "一下".to_owned()),
                (ChunkKind::Content, "答案".to_owned()),
            ]
        );
    }

    /// 流里一个字都没有 → `Error::Empty`（当成失败，别落一条空回复）。
    #[tokio::test]
    async fn streaming_without_any_content_is_an_error() {
        let base = sse_upstream("data: [DONE]\n\n").await;
        let client = Client::new(&provider(base)).unwrap();
        let err = client
            .chat_completion_stream("m", &[], |_, _| {})
            .await
            .unwrap_err();
        assert!(matches!(err, Error::Empty), "got {err:?}");
    }

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
