//! 工具（subagent）：**内置的、专用的后台任务**。目前只有一个：`compact`（把 N 个对话块收成一条摘要）。
//!
//! 为什么叫"工具"而不是"子 Agent"：**工具一定是专用的，没有复用可言**（§25）。
//! 所以它的身份写死在 Rust 里（枚举变体），配置文件只**覆盖**它的行为（模板、渠道、模型），
//! 不能凭空造一个新工具 —— 想加工具就得写代码。
//!
//! 与会话 Agent（`agents.json` 里那些有人格的角色）的三条硬边界：
//!
//! 1. **输出永不进历史、不进树、不碰变量** —— 它只吐一段文本，落库由调用方决定
//!    （对摘要器：`store::record_summary`）；
//! 2. **提示词永不进会话的系统提示词** —— 两条路径不许交叉（那个归 `effective_system_prompt`）；
//! 3. **失败不阻塞任何一轮** —— 产出是派生数据，坏了大不了重跑。
//!
//! 它和会话 Agent 唯一的共同点是"都要打一次上游"：所以这里复用同一个
//! [`crate::providers::Client`]，不另开 HTTP 栈。

use crate::config::{ProviderKind, ProvidersConfig, SubAgentsConfig};
use crate::model::{Conversation, Usage};
use crate::providers::{Client, WireMessage};

/// 内置工具。
///
/// **一个变体 = 一个工具**：身份、显示名、默认模板都在代码里（专用，不复用）。
/// 想加工具就在这儿加一个变体，并补上它的默认模板与说明。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum SubAgent {
    /// 把最老的 N 个**对话块**收成一条叙事梗概（§15 的材料 + §18 的选块由调用方拼）。
    Compact,
}

impl SubAgent {
    /// 全部内置工具（界面按这个顺序列）。
    pub const ALL: [SubAgent; 1] = [SubAgent::Compact];

    /// 配置里的键，也是接口上的 id。
    pub fn id(self) -> &'static str {
        match self {
            Self::Compact => "compact",
        }
    }

    /// 显示名（界面用；写死在代码里）。
    pub fn name(self) -> &'static str {
        match self {
            Self::Compact => "摘要器",
        }
    }

    /// 一句话说明它干什么（界面上的悬停 / 副标题）。
    pub fn about(self) -> &'static str {
        match self {
            Self::Compact => "把最老的 N 个对话块收成一条叙事梗概：只写发生了什么，不写状态字段",
        }
    }

    /// 内置模板（用户没在 `subagents.json` 里覆盖时用它）。
    pub fn builtin_prompt(self) -> &'static str {
        match self {
            Self::Compact => BUILTIN_COMPACT_PROMPT,
        }
    }
}

/// 摘要器的内置模板：用户没在 `subagents.json` 里覆盖时用它。
///
/// 形状见 `IMPORTANT_DISCUSSION.md` §15：只写叙事、禁止状态字段、必保留
/// 人名/地点/承诺/未了结的线/关键因果；收到触发语才动手。
/// **材料**（前情提要 + 原文 + 两端状态）由调用方拼好，这里只管规则。
pub const BUILTIN_COMPACT_PROMPT: &str = "\
你是摘要器。你只做一件事：把「原文」这一段收成一条叙事梗概，让后面的对话能接着往下走。

规则：
· 只写叙事：发生了什么、谁在场、承诺与未了结的线索、时间地点怎么推进；
· 必须保留：人名、地点、承诺、未了结的事、关键因果；状态的变化（谁受了伤、欠了谁钱、
  什么被拿走了）也要写进去 —— 它解释了后面的因果；
· 不要逐句复述，不要写「用户说 / 助手说」，用第三人称直接讲事；
· **禁止**输出任何状态/变量字段（`<state>` 之类）—— 出现即被程序剔除并记日志；
· 中文、一段话、≤ 300 字，不要小标题、不要清单；
· 收到 \"NOW Triggers Compaction\" 时，就按本规则压缩「上文这一段」。

【程序·状态（权威，仅供参考，不要写进梗概）】
  这一段（{{range}}，共 {{blocks}} 个块）之前：{{state_before}}
  这一段之后：{{state_after}}
  （材料生成时间：{{system_time}}）";

/// 模板版本 = **模板正文的 64 位哈希**（§16 附）。
///
/// 改一个字的模板，版本就自己变 —— 不需要谁记得 +1（手动计数忘一次就静默错位）。
/// 用途：判定"哪些摘要出自旧模板"，好整批重做。FNV-1a：不引依赖、跨平台稳定。
pub fn prompt_version(template: &str) -> i64 {
    let mut hash: u64 = 0xcbf2_9ce4_8422_2325;
    for byte in template.as_bytes() {
        hash ^= u64::from(*byte);
        hash = hash.wrapping_mul(0x0000_0100_0000_01b3);
    }
    // 掐掉符号位：存 INTEGER，负数读起来像出事
    (hash & 0x7fff_ffff_ffff_ffff) as i64
}

/// 生效的模板：`subagents.json` 里覆盖了就用覆盖的，否则用内置的。
pub fn prompt(config: &SubAgentsConfig, subagent: SubAgent) -> String {
    config
        .get(subagent.id())
        .map(|found| found.system_prompt.clone())
        .filter(|text| !text.trim().is_empty())
        .unwrap_or_else(|| subagent.builtin_prompt().to_owned())
}

/// 生效的渠道/模型：覆盖里写了就用，留空 = 跟随会话。
pub fn route(config: &SubAgentsConfig, subagent: SubAgent, conversation: &Conversation) -> (String, String) {
    let found = config.get(subagent.id());
    (
        found
            .and_then(|found| found.provider.clone())
            .unwrap_or_else(|| conversation.provider.clone()),
        found
            .and_then(|found| found.model.clone())
            .unwrap_or_else(|| conversation.model.clone()),
    )
}

#[derive(Debug)]
pub enum Error {
    /// 会话指名的渠道不在 `providers.json` 里（或没配）。
    NoProvider,
    /// 打上游失败：连不上、非 2xx、解析不了、回了个空的。
    Upstream(crate::providers::Error),
}

impl std::fmt::Display for Error {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::NoProvider => write!(f, "没有可用的渠道（providers.json）"),
            Self::Upstream(e) => write!(f, "{e}"),
        }
    }
}

impl std::error::Error for Error {}

pub type Result<T, E = Error> = std::result::Result<T, E>;

/// 一次工具调用的产出（**文本 + 它是谁生成的**）。
///
/// `provider` / `model` / `prompt_version` 正是 `summaries` 表上那三列 ——
/// 调用方原样抄进去，审计与"整批重做"就都有了。
#[derive(Debug, Clone)]
pub struct SubAgentRun {
    pub text: String,
    /// 模板里出现但不在白名单里的 `{{name}}`（原样留着没换；界面/日志该提示）。
    pub unknown_vars: Vec<String>,
    pub provider: String,
    pub model: String,
    pub prompt_version: i64,
    pub usage: Option<Usage>,
    /// 实际发出去的请求体（调试页要用；dummy 也拼一份，见 `chat` 那边同一套做法）。
    pub payload: serde_json::Value,
}

fn wire(system_prompt: &str, material: &str) -> Vec<WireMessage> {
    vec![
        WireMessage {
            role: "system",
            content: system_prompt.to_owned(),
        },
        WireMessage {
            role: "user",
            content: material.to_owned(),
        },
    ]
}

/// 跑一次工具：`material` 是**程序拼好的材料**（不是历史、不是会话）。
///
/// 非流式：摘要不需要动画，一次拿全反而简单。渠道与模型默认跟随会话
/// （`subagents.json` 里留空时），可以在配置里换成更便宜的。
pub async fn run(
    config: &SubAgentsConfig,
    subagent: SubAgent,
    material: &str,
    values: &std::collections::BTreeMap<&'static str, String>,
    conversation: &Conversation,
    providers: &ProvidersConfig,
) -> Result<SubAgentRun> {
    let raw = prompt(config, subagent);
    // 占位符在这里展开（**只扫一遍**）；版本仍按**替换前**的正文算
    let rendered = crate::template::render(&raw, values);
    let template = rendered.text;
    let (provider_id, model) = route(config, subagent, conversation);
    let provider = providers
        .get(&provider_id)
        .ok_or(Error::NoProvider)?;
    let messages = wire(&template, material);
    let payload = Client::chat_body(&model, &messages, false);
    let version = prompt_version(&raw);

    // dummy 没有上游，但照样拼一份载荷：整条链路在没有 key 的环境里也能验（与 chat 那边一个道理）。
    if provider.kind == ProviderKind::Dummy {
        return Ok(SubAgentRun {
            text: crate::chat::DUMMY_MODEL_REPLY.to_owned(),
            unknown_vars: rendered.unknown,
            provider: provider_id,
            model,
            prompt_version: version,
            usage: None,
            payload,
        });
    }

    let client = Client::new(provider).map_err(Error::Upstream)?;
    let (text, usage) = client
        .chat_completion(&model, &messages)
        .await
        .map_err(Error::Upstream)?;
    Ok(SubAgentRun {
        text,
        unknown_vars: rendered.unknown,
        provider: provider_id,
        model,
        prompt_version: version,
        usage,
        payload,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::config::{ProviderConfig, ProviderKind as Kind, SubAgentOverride};

    /// 身份、显示名、说明、内置模板都写死在代码里 —— 这就是"专用"的意思。
    #[test]
    fn tools_are_defined_in_code() {
        assert_eq!(SubAgent::ALL.len(), 1, "目前只有摘要器一个");
        let subagent = SubAgent::Compact;
        assert_eq!(subagent.id(), "compact");
        assert_eq!(subagent.name(), "摘要器");
        assert!(!subagent.about().is_empty());
        assert!(subagent.builtin_prompt().contains("摘要器"));
    }

    /// 版本号是模板正文的哈希：稳定、可区分、永远非负。
    #[test]
    fn prompt_version_tracks_the_template_text() {
        let base = prompt_version(SubAgent::Compact.builtin_prompt());
        assert_eq!(base, prompt_version(SubAgent::Compact.builtin_prompt()), "同一份文本 ⇒ 同一个版本");
        assert!(base > 0, "掐掉符号位，永远非负");
        let edited = format!("{}\n· 另外别忘了天气。", SubAgent::Compact.builtin_prompt());
        assert_ne!(base, prompt_version(&edited), "改一个字就该变");
        assert_ne!(prompt_version(""), prompt_version(" "), "空白也算内容");
    }

    /// 覆盖项：没写 / 写空 ⇒ 用内置；写了就用写的；渠道模型留空 ⇒ 跟随会话。
    #[test]
    fn overrides_replace_the_builtin_prompt_and_route() {
        let conversation = conversation("别处", "big-model");
        let empty = SubAgentsConfig::default();
        assert_eq!(prompt(&empty, SubAgent::Compact), SubAgent::Compact.builtin_prompt());
        assert_eq!(
            route(&empty, SubAgent::Compact, &conversation),
            ("别处".to_owned(), "big-model".to_owned()),
            "没覆盖 ⇒ 跟随会话"
        );

        let mut config = SubAgentsConfig::default();
        config.subagents.insert(
            "compact".to_owned(),
            SubAgentOverride {
                system_prompt: "压".to_owned(),
                provider: Some("cheap".to_owned()),
                model: Some("small".to_owned()),
                ..Default::default()
            },
        );
        assert_eq!(prompt(&config, SubAgent::Compact), "压");
        assert_eq!(
            route(&config, SubAgent::Compact, &conversation),
            ("cheap".to_owned(), "small".to_owned())
        );

        // 模板写空 ⇒ 回落内置（免得手滑删光就得到一个空 system）
        config.subagents.get_mut("compact").unwrap().system_prompt = "   ".to_owned();
        assert_eq!(prompt(&config, SubAgent::Compact), SubAgent::Compact.builtin_prompt());

        // 不认识的键不影响任何工具
        config.subagents.insert("没这个工具".to_owned(), SubAgentOverride::default());
        assert_eq!(prompt(&config, SubAgent::Compact), SubAgent::Compact.builtin_prompt());
    }

    fn conversation(provider: &str, model: &str) -> Conversation {
        Conversation {
            id: uuid::Uuid::now_v7(),
            title: String::new(),
            system_prompt: String::new(),
            provider: provider.to_owned(),
            model: model.to_owned(),
            agent_id: "default".to_owned(),
            current_leaf: None,
            created_at: 0,
            updated_at: 0,
        }
    }

    /// 本地假上游：非流式，回一段固定正文。
    ///
    /// **不收捕获**：调用方要验的请求体从 `SubAgentRun::payload` 就能拿到（那就是发出去的那一份）。
    async fn stub_upstream(reply: &'static str) -> String {
        let stub = axum::Router::new().route(
            "/v1/chat/completions",
            axum::routing::post(move || async move {
                axum::Json(serde_json::json!({
                    "choices": [{ "message": { "role": "assistant", "content": reply } }],
                    "usage": { "prompt_tokens": 11, "completion_tokens": 3, "total_tokens": 14 },
                }))
            }),
        );
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        tokio::spawn(async move {
            let _ = axum::serve(listener, stub).await;
        });
        format!("http://{addr}/v1")
    }

    fn providers_with(id: &str, base_url: &str) -> ProvidersConfig {
        ProvidersConfig {
            providers: vec![ProviderConfig {
                id: id.to_owned(),
                kind: Kind::OpenAiCompat,
                base_url: base_url.to_owned(),
                ..Default::default()
            }],
            ..Default::default()
        }
    }

    /// 全链路：模板当 system、材料当 user；产出带 provider/model/prompt_version/usage。
    #[tokio::test]
    async fn run_sends_template_and_material_and_reports_meta() {
        let base = stub_upstream("人进房间吃饭，吃饱了离开。").await;
        let providers = providers_with("stub", &base);
        let conversation = conversation("stub", "m");
        let mut values: std::collections::BTreeMap<&'static str, String> =
            std::collections::BTreeMap::new();
        values.insert("range", "第 1–8 条".to_owned());
        let run = run(
            &SubAgentsConfig::default(),
            SubAgent::Compact,
            "【材料】第 1–10 轮",
            &values,
            &conversation,
            &providers,
        )
        .await
        .unwrap();

        assert_eq!(run.text, "人进房间吃饭，吃饱了离开。");
        assert_eq!(run.provider, "stub");
        assert_eq!(run.model, "m", "没覆盖 ⇒ 跟随会话");
        assert_eq!(
            run.prompt_version,
            prompt_version(SubAgent::Compact.builtin_prompt()),
            "版本按**替换前**的模板正文算（否则每轮都变）"
        );
        assert!(run.unknown_vars.is_empty());
        assert!(
            !run.payload["messages"][0]["content"]
                .as_str()
                .unwrap()
                .contains("{{"),
            "已知变量都该被换掉"
        );
        assert!(
            run.payload["messages"][0]["content"]
                .as_str()
                .unwrap()
                .contains("第 1–8 条"),
            "{{range}} 该被换成值"
        );
        assert_eq!(run.usage.as_ref().map(|usage| usage.prompt_tokens), Some(11));

        // 发出去的就是这一份
        assert_eq!(run.payload["stream"], false, "摘要不流式");
        assert_eq!(run.payload["messages"][0]["role"], "system");
        // 发出去的 system 是**替换后**的那份；模板正文的版本另算（见上）
        let rendered = crate::template::render(SubAgent::Compact.builtin_prompt(), &values).text;
        assert_eq!(run.payload["messages"][0]["content"], rendered);
        assert_eq!(run.payload["messages"][1]["role"], "user");
        assert_eq!(run.payload["messages"][1]["content"], "【材料】第 1–10 轮");
    }

    /// 覆盖里换了渠道与模型（压缩是体力活，可以挑便宜的）。
    #[tokio::test]
    async fn overrides_can_switch_provider_and_model() {
        let base = stub_upstream("梗概").await;
        let providers = providers_with("cheap", &base);
        let mut config = SubAgentsConfig::default();
        config.subagents.insert(
            "compact".to_owned(),
            SubAgentOverride {
                system_prompt: "压".to_owned(),
                provider: Some("cheap".to_owned()),
                model: Some("small-model".to_owned()),
                ..Default::default()
            },
        );
        let conversation = conversation("别处", "big-model");

        let run = run(
            &config,
            SubAgent::Compact,
            "材料",
            &std::collections::BTreeMap::new(),
            &conversation,
            &providers,
        )
            .await
            .unwrap();
        assert_eq!(run.provider, "cheap");
        assert_eq!(run.model, "small-model");
        assert_eq!(run.payload["model"], "small-model");
        assert_eq!(run.payload["messages"][0]["content"], "压");
    }

    /// 渠道不存在 ⇒ `NoProvider`（不 panic、不假装成功）。
    #[tokio::test]
    async fn missing_provider_is_an_error() {
        let providers = ProvidersConfig::default();
        let conversation = conversation("没配过", "m");
        let err = run(
            &SubAgentsConfig::default(),
            SubAgent::Compact,
            "材料",
            &std::collections::BTreeMap::new(),
            &conversation,
            &providers,
        )
        .await
        .unwrap_err();
        assert!(matches!(err, Error::NoProvider));
    }
}
