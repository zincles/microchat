//! 对话后端：把「会话 + 用户消息」变成助手回复。
//!
//! 目前只有 [`Backend::Dummy`] 落地：没有可用模型时的兜底，固定回一句，
//! 保证前端不会对着空气说话、也不会静默失败。
//!
//! [`Backend::OpenAiCompletion`] 只占位：选择规则、名字、错误码都已定好，
//! 实现随下一轮的 `chat/completions` + SSE 一起补。将来 anthropic / deepseek
//! 是同一层的兄弟实现；image-gen 之类不是"对话后端"，另走一路。

use crate::config::{ProviderKind, ProvidersConfig};
use crate::model::Conversation;

/// dummy 的固定回复。
pub const DUMMY_REPLY: &str = "未配置模型";

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Backend {
    Dummy,
    OpenAiCompletion,
}

impl Backend {
    /// 选择规则：会话的 provider 仍存在于 `providers.jsonc`，且**类型是 openai-compat**、
    /// 会话配了 model → `OpenAiCompletion`；其余一律 `Dummy`。
    ///
    /// 必须检查 provider 是否仍然存在：`providers.jsonc` 是手写文件，删掉一个 provider 后，
    /// 历史会话里仍留着它的名字——那种情况该回兜底话术，而不是去打一个不存在的上游。
    /// `dummy` 类型的 provider 被显式选中时也走兜底——那正是它存在的意义。
    pub fn select(conversation: &Conversation, providers: &ProvidersConfig) -> Self {
        let Some(provider) = providers.get(&conversation.provider) else {
            return Self::Dummy;
        };
        match provider.kind {
            ProviderKind::Dummy => Self::Dummy,
            ProviderKind::OpenAiCompat => {
                if conversation.model.is_empty() {
                    Self::Dummy
                } else {
                    Self::OpenAiCompletion
                }
            }
        }
    }

    pub fn name(self) -> &'static str {
        match self {
            Self::Dummy => "dummy",
            Self::OpenAiCompletion => "openai-completion",
        }
    }

    /// 生成一条回复。未实现的后端明确报错，绝不假装有人在说话。
    pub fn reply(self, _conversation: &Conversation, _user_text: &str) -> Result<String, Error> {
        match self {
            Self::Dummy => Ok(DUMMY_REPLY.to_owned()),
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

    use crate::config::{ProviderConfig, ProvidersConfig};
    use crate::model::Conversation;

    use super::{Backend, DUMMY_REPLY};

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

    #[test]
    fn unconfigured_conversation_falls_back_to_dummy() {
        let providers = providers(&["local"]);
        // 没配 model
        assert_eq!(
            Backend::select(&conversation("local", ""), &providers),
            Backend::Dummy
        );
        // 什么都没配
        assert_eq!(
            Backend::select(&conversation("", ""), &providers),
            Backend::Dummy
        );
        // provider 已被从 providers.jsonc 删掉
        assert_eq!(
            Backend::select(&conversation("ghost", "m"), &providers),
            Backend::Dummy
        );
    }

    #[test]
    fn configured_conversation_selects_openai_completion() {
        let providers = providers(&["local"]);
        assert_eq!(
            Backend::select(&conversation("local", "deepseek/deepseek-v4-flash"), &providers),
            Backend::OpenAiCompletion
        );
    }

    #[test]
    fn dummy_provider_is_always_dummy() {
        let providers = ProvidersConfig {
            providers: vec![ProviderConfig {
                id: "offline".to_owned(),
                kind: crate::config::ProviderKind::Dummy,
                base_url: String::new(),
                ..Default::default()
            }],
            ..Default::default()
        };
        // 即便会话"配了"模型，dummy provider 也只兜底
        assert_eq!(
            Backend::select(&conversation("offline", "some-model"), &providers),
            Backend::Dummy
        );
    }

    #[test]
    fn dummy_speaks_and_unimplemented_backend_refuses() {
        let conv = conversation("", "");
        assert_eq!(Backend::Dummy.reply(&conv, "你好").unwrap(), DUMMY_REPLY);
        assert_eq!(DUMMY_REPLY, "未配置模型");

        let err = Backend::OpenAiCompletion.reply(&conv, "你好").unwrap_err();
        assert!(matches!(err, super::Error::NotImplemented("openai-completion")));
        assert!(err.to_string().contains("尚未实现"));
    }
}
