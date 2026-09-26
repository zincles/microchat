//! 对话后端：把「会话 + 用户消息」变成助手回复。
//!
//! 三个入口，两种话术：
//! - [`Backend::Dummy`]：会话选了 **dummy 模型**（`dummy` 类型 provider 提供的虚拟模型，
//!   没有上游），立刻回 [`DUMMY_MODEL_REPLY`]——联调/测试用；
//! - [`Backend::Fallback`]：什么都没配、或配的 provider 已从 `providers.jsonc` 消失，
//!   回 [`FALLBACK_REPLY`]，保证前端不会对着空气说话；
//! - [`Backend::OpenAiCompletion`]：真模型，占位待实现（选择规则、名字、错误码已定）。
//!
//! 将来 anthropic / deepseek 是同一层的兄弟实现；image-gen 之类不是"对话后端"，另走一路。

use crate::config::{ProviderKind, ProvidersConfig};
use crate::model::Conversation;
use crate::vars::Outgoing;

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
    /// - provider 不在 `providers.jsonc` 里（没配 / 被删）→ `Fallback`；
    /// - provider 是 `dummy` 类型 → `Dummy`（它就是为"显式选中"而存在的）；
    /// - provider 是 `openai-compat`：配了 model → `OpenAiCompletion`，没配 → `Fallback`。
    ///
    /// 必须检查 provider 是否存在：`providers.jsonc` 是手写文件，删掉一个 provider 后
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

    /// 生成一条回复。未实现的后端明确报错，绝不假装有人在说话。
    ///
    /// `outgoing` 是**真正要发出去的东西**：变量表已注入、历史里的状态标签已剔除，
    /// 由 [`crate::vars::build_outgoing`] 一次组装。真模型后端接进来时直接用它——
    /// 不允许再写第二条拼装路径（否则"剔除标签"会漏）。
    pub fn reply(self, _conversation: &Conversation, _outgoing: &[Outgoing]) -> Result<String, Error> {
        match self {
            Self::Dummy => Ok(DUMMY_MODEL_REPLY.to_owned()),
            Self::Fallback => Ok(FALLBACK_REPLY.to_owned()),
            Self::OpenAiCompletion => Err(Error::NotImplemented(self.name())),
        }
    }
}

#[derive(Debug)]
pub enum Error {
    /// 后端已被选择但尚未实现。
    NotImplemented(&'static str),
}

impl std::fmt::Display for Error {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::NotImplemented(name) => write!(f, "{name} 对话后端尚未实现"),
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

    use super::{Backend, DUMMY_MODEL_ID, DUMMY_MODEL_REPLY, FALLBACK_REPLY};

    fn conversation(provider: &str, model: &str) -> Conversation {
        Conversation {
            id: Uuid::now_v7(),
            title: String::new(),
            system_prompt: String::new(),
            provider: provider.to_owned(),
            model: model.to_owned(),
            agent_id: crate::model::DEFAULT_AGENT_ID.to_owned(),
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

    #[test]
    fn dummy_model_replies_immediately() {
        let providers = dummy_providers();
        let conversation = conversation("dummy", DUMMY_MODEL_ID);

        let backend = Backend::select(&conversation, &providers);
        assert_eq!(backend, Backend::Dummy);
        assert_eq!(backend.reply(&conversation, &[]).unwrap(), DUMMY_MODEL_REPLY);
        assert_eq!(DUMMY_MODEL_REPLY, "（测试用空模型）");
    }

    #[test]
    fn unconfigured_conversation_falls_back() {
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
        assert_eq!(backend.reply(&blank, &[]).unwrap(), FALLBACK_REPLY);
        // provider 已被从 providers.jsonc 删掉
        assert_eq!(
            Backend::select(&conversation("ghost", "m"), &providers),
            Backend::Fallback
        );
    }

    #[test]
    fn configured_conversation_selects_openai_completion() {
        let providers = providers(&["local"]);
        let conversation = conversation("local", "deepseek/deepseek-v4-flash");
        let backend = Backend::select(&conversation, &providers);

        assert_eq!(backend, Backend::OpenAiCompletion);
        let err = backend.reply(&conversation, &[]).unwrap_err();
        assert!(matches!(err, super::Error::NotImplemented("openai-completion")));
        assert!(err.to_string().contains("尚未实现"));
    }
}
