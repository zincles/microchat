//! 对话后端：把「会话 + 用户消息」变成助手回复。
//!
//! 三个入口，两种话术：
//! - [`Backend::Dummy`]：会话选了 **dummy 模型**（`dummy` 类型 provider 提供的虚拟模型，
//!   没有上游），立刻回 [`DUMMY_MODEL_REPLY`]——联调/测试用；
//! - [`Backend::Fallback`]：什么都没配、或配的 provider 已从 `providers.json` 消失，
//!   回 [`FALLBACK_REPLY`]，保证前端不会对着空气说话；
//! - [`Backend::OpenAiCompletion`]：真模型，走 `providers.json` 里那个 provider 的
//!   OpenAI 兼容接口（非流式）。要发出去的历史一律来自 [`crate::world::build_outgoing`]。
//!
//! 将来 anthropic / deepseek 是同一层的兄弟实现；image-gen 之类不是"对话后端"，另走一路。

use crate::config::{ProviderKind, ProvidersConfig};
use crate::providers::{self, WireMessage};
use crate::model::Conversation;
use crate::world::Outgoing;

/// dummy provider 对外暴露的虚拟模型 id。它不在数据库里——没有上游，也就没有可发现的东西。
pub const DUMMY_MODEL_ID: &str = "dummy";

/// 打到 dummy 模型的回复。
pub const DUMMY_MODEL_REPLY: &str = "（测试用空模型）";

/// 什么都没配时的兜底话术。
pub const FALLBACK_REPLY: &str = "未配置模型";

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Backend {
    /// 显式选了 dummy 模型。
    Dummy,
    /// 没有可用配置：同样由 dummy 出面，但话术不同。
    Fallback,
    OpenAiCompletion,
}

impl Backend {
    /// 选择规则：
    /// - provider 不在 `providers.json` 里（没配 / 被删）→ `Fallback`；
    /// - provider 是 `dummy` 类型 → `Dummy`（它就是为"显式选中"而存在的）；
    /// - provider 是 `openai-compat`：配了 model → `OpenAiCompletion`，没配 → `Fallback`。
    ///
    /// 必须检查 provider 是否存在：`providers.json` 是手写文件，删掉一个 provider 后
    /// 历史会话里仍留着它的名字——那种情况该回兜底话术，而不是去打一个不存在的上游。
    pub fn select(conversation: &Conversation, providers: &ProvidersConfig) -> Self {
        let Some(provider) = providers.get(&conversation.provider) else {
            return Self::Fallback;
        };
        match provider.kind {
            ProviderKind::Dummy => Self::Dummy,
            ProviderKind::OpenAiCompat => {
                if conversation.model.is_empty() {
                    Self::Fallback
                } else {
                    Self::OpenAiCompletion
                }
            }
        }
    }

    pub fn name(self) -> &'static str {
        match self {
            Self::Dummy => "dummy",
            Self::Fallback => "fallback",
            Self::OpenAiCompletion => "openai-completion",
        }
    }

}

/// 生成一条回复。dummy / fallback 立即返回，真模型走上游。
///
/// `outgoing` 是**真正要发出去的东西**：变量表已注入、历史里的状态标签已剔除，
/// 由 [`crate::world::build_outgoing`] 一次组装——真模型直接用它，
/// **不允许**再写第二条拼装路径（否则"剔除标签"会漏）。
/// 一次生成的结果：正文 + **实际发出去的请求体** + 上游报的用量。
///
/// `payload` 与 `usage` 都只服务显示（调试页/卡片脚注）；一切计算与落库只认 `reply`。
#[derive(Debug, Clone)]
pub struct Sent {
    pub reply: String,
    pub payload: serde_json::Value,
    pub usage: Option<crate::model::Usage>,
}

pub async fn complete(
    conversation: &Conversation,
    outgoing: &[Outgoing],
    providers: &ProvidersConfig,
) -> Result<String, Error> {
    complete_with(conversation, outgoing, providers, false, |_, _| {})
        .await
        .map(|sent| sent.reply)
}

/// 生成一条回复——`complete` 与流式共用这一条路。
///
/// `stream = true` **且** provider 没关掉流式时走 SSE：每个增量交给 `on_chunk`
/// （**只服务动画**：界面拿它把气泡动起来；它不进库、不参与变量与出站计算）。
/// 返回值**始终是完整正文**，落库与一切计算只认它。
pub async fn complete_with(
    conversation: &Conversation,
    outgoing: &[Outgoing],
    providers: &ProvidersConfig,
    stream: bool,
    on_chunk: impl FnMut(providers::ChunkKind, &str),
) -> Result<Sent, Error> {
    let messages: Vec<WireMessage> = outgoing
        .iter()
        .map(|item| WireMessage {
            role: item.role.as_str(),
            content: item.content.clone(),
        })
        .collect();

    match Backend::select(conversation, providers) {
        Backend::Dummy => {
            // dummy 没有上游，但**载荷照样拼一份**：用与真模型同一个 `chat_body`，
            // 形状一模一样，只是不联网。调试页据此显示"本来会发出去的东西" ——
            // 没有 key 也能把装配（历史剔除、变量注入）验一遍。
            let streaming = stream
                && providers
                    .get(&conversation.provider)
                    .is_some_and(|provider| provider.stream);
            Ok(Sent {
                reply: DUMMY_MODEL_REPLY.to_owned(),
                payload: providers::Client::chat_body(&conversation.model, &messages, streaming),
                // 没发给任何上游：没有用量
                usage: None,
            })
        }
        Backend::Fallback => Ok(Sent {
            reply: FALLBACK_REPLY.to_owned(),
            payload: serde_json::Value::Null,
            usage: None,
        }),
        Backend::OpenAiCompletion => {
            // select 已经确认过 provider 存在；这里再取一次是拿配置本体（含密钥）。
            let provider = providers.get(&conversation.provider).ok_or(Error::NoProvider)?;
            let client = providers::Client::new(provider).map_err(Error::Upstream)?;
            let streaming = stream && provider.stream;
            // 载荷在这里定稿（客户端内部也走同一个构造函数，不会漂）
            let payload = providers::Client::chat_body(&conversation.model, &messages, streaming);
            let (reply, usage) = if streaming {
                client
                    .chat_completion_stream(&conversation.model, &messages, on_chunk)
                    .await
                    .map_err(Error::Upstream)?
            } else {
                client
                    .chat_completion(&conversation.model, &messages)
                    .await
                    .map_err(Error::Upstream)?
            };
            Ok(Sent {
                reply,
                payload,
                usage,
            })
        }
    }
}

#[derive(Debug)]
pub enum Error {
    /// 调上游失败：连不上、非 2xx、解析不了、回了个空的。
    Upstream(providers::Error),
    /// 选择后端时 provider 还在，真要发的时候没了（配置被改过）。
    NoProvider,
}

impl std::fmt::Display for Error {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Upstream(e) => write!(f, "{e}"),
            Self::NoProvider => write!(f, "provider 已经不在 providers.json 里了"),
        }
    }
}

impl std::error::Error for Error {}

pub type Result<T, E = Error> = std::result::Result<T, E>;

#[cfg(test)]
mod tests {
    use uuid::Uuid;

    use crate::config::{ProviderConfig, ProviderKind, ProvidersConfig};
    use crate::model::Conversation;
    use crate::world::{Outgoing, OutgoingRole};

    use super::{complete, Backend, Error, DUMMY_MODEL_ID, DUMMY_MODEL_REPLY, FALLBACK_REPLY};

    /// 起一个本地假上游。返回 (base_url, 已捕获的 (authorization, body), 句柄)。
    type Captured = std::sync::Arc<std::sync::Mutex<(String, String)>>;

    async fn stub_upstream(
        status: u16,
        payload: serde_json::Value,
    ) -> (String, Captured, tokio::task::JoinHandle<()>) {
        let captured: Captured = std::sync::Arc::new(std::sync::Mutex::new((String::new(), String::new())));
        let sink = captured.clone();
        let stub = axum::Router::new().route(
            "/v1/chat/completions",
            axum::routing::post(move |headers: axum::http::HeaderMap, body: String| {
                let sink = sink.clone();
                let payload = payload.clone();
                async move {
                    let auth = headers
                        .get("authorization")
                        .and_then(|v| v.to_str().ok())
                        .unwrap_or_default()
                        .to_owned();
                    *sink.lock().unwrap() = (auth, body);
                    (
                        axum::http::StatusCode::from_u16(status).unwrap(),
                        axum::Json(payload),
                    )
                }
            }),
        );
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        let handle = tokio::spawn(async move {
            let _ = axum::serve(listener, stub).await;
        });
        (format!("http://{addr}/v1"), captured, handle)
    }

    fn providers_with(id: &str, base_url: &str) -> ProvidersConfig {
        ProvidersConfig {
            providers: vec![ProviderConfig {
                id: id.to_owned(),
                kind: ProviderKind::OpenAiCompat,
                base_url: base_url.to_owned(),
                ..Default::default()
            }],
            ..Default::default()
        }
    }

    fn conversation(provider: &str, model: &str) -> Conversation {
        Conversation {
            id: Uuid::now_v7(),
            title: String::new(),
            system_prompt: String::new(),
            provider: provider.to_owned(),
            model: model.to_owned(),
            agent_id: crate::model::DEFAULT_AGENT_ID.to_owned(),
            current_leaf: None,
            created_at: 0,
            updated_at: 0,
        }
    }

    fn providers(ids: &[&str]) -> ProvidersConfig {
        ProvidersConfig {
            providers: ids
                .iter()
                .map(|id| ProviderConfig {
                    id: (*id).to_owned(),
                    base_url: "http://127.0.0.1:9/v1".to_owned(),
                    ..Default::default()
                })
                .collect(),
            ..Default::default()
        }
    }

    fn dummy_providers() -> ProvidersConfig {
        ProvidersConfig {
            providers: vec![ProviderConfig {
                id: "dummy".to_owned(),
                kind: ProviderKind::Dummy,
                base_url: String::new(),
                ..Default::default()
            }],
            ..Default::default()
        }
    }

    #[tokio::test]
    async fn dummy_model_replies_immediately() {
        let providers = dummy_providers();
        let conversation = conversation("dummy", DUMMY_MODEL_ID);

        let backend = Backend::select(&conversation, &providers);
        assert_eq!(backend, Backend::Dummy);
        let reply = complete(&conversation, &[], &providers)
            .await
            .unwrap();
        assert_eq!(reply, DUMMY_MODEL_REPLY);
        assert_eq!(DUMMY_MODEL_REPLY, "（测试用空模型）");
    }

    #[tokio::test]
    async fn unconfigured_conversation_falls_back() {
        let providers = providers(&["local"]);
        // 没配 model
        assert_eq!(
            Backend::select(&conversation("local", ""), &providers),
            Backend::Fallback
        );
        // 什么都没配
        let blank = conversation("", "");
        let backend = Backend::select(&blank, &providers);
        assert_eq!(backend, Backend::Fallback);
        let reply = complete(&blank, &[], &providers).await.unwrap();
        assert_eq!(reply, FALLBACK_REPLY);
        // provider 已被从 providers.json 删掉
        assert_eq!(
            Backend::select(&conversation("ghost", "m"), &providers),
            Backend::Fallback
        );
    }

    #[tokio::test]
    async fn configured_conversation_talks_to_its_upstream() {
        let (base_url, captured, upstream) = stub_upstream(
            200,
            serde_json::json!({
                "choices": [{ "message": { "role": "assistant", "content": "上游说你好" } }]
            }),
        )
        .await;
        let mut providers = providers_with("stub", &base_url);
        // 密钥和 provider 住在一起（就是 providers.json 里的 api_key）
        providers.providers[0].api_key = "sk-test".to_owned();
        let outgoing = vec![
            Outgoing { role: OutgoingRole::System, content: "你是助手".to_owned() },
            Outgoing { role: OutgoingRole::User, content: "在吗".to_owned() },
        ];

        let conversation = conversation("stub", "deepseek/deepseek-v4-flash");
        assert_eq!(
            Backend::select(&conversation, &providers),
            Backend::OpenAiCompletion
        );
        let reply = complete(&conversation, &outgoing, &providers)
            .await
            .unwrap();
        assert_eq!(reply, "上游说你好");

        let (auth, body) = captured.lock().unwrap().clone();
        assert_eq!(auth, "Bearer sk-test", "密钥要按 Bearer 发出去");
        let sent: serde_json::Value = serde_json::from_str(&body).unwrap();
        assert_eq!(sent["model"], "deepseek/deepseek-v4-flash");
        assert_eq!(sent["stream"], false);
        assert_eq!(sent["messages"][0]["role"], "system");
        assert_eq!(sent["messages"][0]["content"], "你是助手");
        assert_eq!(sent["messages"][1]["role"], "user");
        assert_eq!(sent["messages"][1]["content"], "在吗");
        upstream.abort();
    }

    #[tokio::test]
    async fn upstream_error_body_is_surfaced() {
        let (base_url, _captured, upstream) = stub_upstream(
            401,
            serde_json::json!({ "error": { "message": "invalid api key" } }),
        )
        .await;
        let providers = providers_with("stub", &base_url);

        let err = complete(
            &conversation("stub", "m"),
            &[],
            &providers,
        )
        .await
        .unwrap_err();
        assert!(matches!(
            err,
            Error::Upstream(crate::providers::Error::Upstream { status: 401, .. })
        ));
        assert!(err.to_string().contains("invalid api key"), "上游原话要带出来：{err}");
        upstream.abort();
    }

    /// 连不上就必须报错——绝不能假装有人在说话（这是"未实现"那版的老毛病）。
    #[tokio::test]
    async fn unreachable_upstream_is_an_error_not_a_fake_reply() {
        let providers = providers_with("local", "http://127.0.0.1:1/v1");
        let err = complete(&conversation("local", "m"), &[], &providers)
            .await
            .unwrap_err();
        assert!(matches!(err, Error::Upstream(crate::providers::Error::Http(_))));
    }
}
