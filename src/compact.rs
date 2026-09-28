//! Compact（压缩）：把路径上的一段收成一条摘要。
//!
//! **分两层，别混**：
//! - **机制** —— [`summarize_span`]：给它一段（两端用 id 指，**可以是 message 也可以是 summary**），
//!   它拼材料、叫内置 Agent、过闸、落库，回一条 `Summary`。二次压缩（金字塔）就是"喂摘要 id"。
//! - **策略** —— 这一轮该收哪一段：手动按钮用 [`summarize_blocks`]（最老的 N 个已闭合块），
//!   将来的自动触发按阈值挑，重摇某条摘要则直接 [`summarize_span`]。
//!
//! 谁也**不许**绕过这里自己拼摘要请求 —— 材料形状（§15）、占位符（§26）、过闸（剔 `<state>`）、
//! 落库（单事务：插摘要 + 指成员）都只在这一处，写第二份必然漂。
//!
//! 不持锁跨 await：取料在锁里、调用在锁外、落库再进锁（与 `turn::run_turn` 同一条纪律）。

use std::collections::BTreeMap;
use std::sync::{Arc, Mutex};

use uuid::Uuid;

use crate::blocks::{self, Block};
use crate::config::{ProvidersConfig, AbilitiesConfig};
use crate::model::{Conversation, Message, Summary, SummarySourceKind};
use crate::store::Store;
use crate::abilities::{self, Ability};
use crate::template;

/// 路径上的一段：用**两端元素的 id** 指。
///
/// 两端可以是 message id，也可以是 **summary id** —— 后者就是二次压缩（金字塔）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Span {
    pub begin: Uuid,
    pub end: Uuid,
}

/// 会话的**生效系统提示词** + 它算哪一层 —— 变量底子（`<state>`）要用它做前提。
///
/// 由调用方解析（`server::effective_system_prompt`），这里只管用：
/// 压缩模块不该知道 `agents.json` 长什么样。
pub struct Prompt {
    pub text: String,
    pub source: crate::world::PromptSource,
}

#[derive(Debug)]
pub enum Error {
    NotFound(&'static str),
    /// 没有可压的东西（全被覆盖了，或只剩那个开着的块）。
    NothingToDo,
    Store(String),
    Ability(String),
    /// 产出过不了闸（空了、或全是状态字段）。
    Rejected(String),
}

impl std::fmt::Display for Error {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::NotFound(what) => write!(f, "{what}不存在"),
            Self::NothingToDo => {
                write!(f, "没有可压的块（要么全被覆盖了，要么只剩那个开着的块）")
            }
            Self::Store(message) => write!(f, "存储: {message}"),
            Self::Ability(message) => write!(f, "内置 Agent: {message}"),
            Self::Rejected(message) => write!(f, "产出不合格: {message}"),
        }
    }
}

impl std::error::Error for Error {}

pub type Result<T, E = Error> = std::result::Result<T, E>;

/// **压缩的唯一入口（机制）**：把路径上 `span` 指的那一段收成一条摘要并落库。
///
/// - 材料形状见 §15（前情提要 + 原文含当时 `<state>`、不含思考 + 块分隔 + 触发语）；
///   两端状态与时间由占位符摆进 system（`{{state_before}}` …，见 §26）；
/// - 过闸：产出里混进 `<state>` 一律剔除并记日志；
/// - 落库走 `store::record_summary`（单事务：插摘要 + 把成员指过去）；
/// - **`messages` 一个字节都不动** —— 这是整套设计的地基。
pub async fn summarize_span(
    store: &Arc<Mutex<Store>>,
    prompt: &Prompt,
    providers: &ProvidersConfig,
    abilities: &AbilitiesConfig,
    conversation_id: Uuid,
    span: Span,
) -> Result<Summary> {
    // ① 取料（锁里）
    let material = collect(store, prompt, conversation_id, span)?;

    // ② 拼材料 + 占位符的值（纯计算）
    let (body, values) = material_for(&material);

    // ③ 叫内置 Agent（**锁外**；它可能慢）
    let run = abilities::run(
        abilities,
        Ability::Compact,
        &body,
        &values,
        &material.conversation,
        providers,
    )
    .await
    .map_err(|error| Error::Ability(error.to_string()))?;
    if !run.unknown_vars.is_empty() {
        // 不认识的变量原样留在提示词里（不改用户写的东西），但要让人知道
        eprintln!("摘要模板里有不认识的变量（未替换）：{:?}", run.unknown_vars);
    }

    // ④ 过闸：状态字段一律剔除（手改 / 重摇也走同一道闸）
    let parsed = crate::world::parse(&run.text);
    let text = parsed.cleaned.trim().to_owned();
    if text.is_empty() {
        return Err(Error::Rejected("摘要器回了空文本".to_owned()));
    }
    if parsed.cleaned != run.text {
        eprintln!("摘要产出里混进了状态字段，已剔除（会话 {conversation_id}）");
    }

    // ⑤ 落库（锁里；单事务）
    let members: Vec<Uuid> = material.slice.iter().map(|message| message.id).collect();
    let summary = Summary {
        id: Uuid::now_v7(),
        conversation_id,
        parent_summary_id: None,
        source_kind: SummarySourceKind::Message,
        text,
        blocks: material.block_count,
        tokens: crate::world::estimate_tokens(&parsed.cleaned, material.ratio) as i64,
        source_ids: members.clone(),
        provider: run.provider,
        model: run.model,
        prompt_version: run.prompt_version,
        usage: run.usage,
        dirty: false,
        created_at: now_ms(),
    };
    let mut store = store
        .lock()
        .map_err(|_| Error::Store("存储锁中毒".to_owned()))?;
    store
        .record_summary(&summary, &members)
        .map_err(|error| Error::Store(error.to_string()))?;
    Ok(summary)
}

/// **策略：手动压缩** —— 从第一条没被覆盖的消息起，取最老的至多 `limit` 个**已闭合**的块收掉。
///
/// （"N 个块"是按钮的语义；自动触发将来换个挑法，复用 [`summarize_span`]。）
pub async fn summarize_blocks(
    store: &Arc<Mutex<Store>>,
    prompt: &Prompt,
    providers: &ProvidersConfig,
    abilities: &AbilitiesConfig,
    conversation_id: Uuid,
    limit: usize,
) -> Result<Summary> {
    let span = {
        let store = store
            .lock()
            .map_err(|_| Error::Store("存储锁中毒".to_owned()))?;
        let path = path_of(&store, conversation_id)?;
        let picked = pick_blocks(&path, limit);
        if picked.is_empty() {
            return Err(Error::NothingToDo);
        }
        Span {
            begin: picked[0].first,
            end: picked[picked.len() - 1].last,
        }
    };
    summarize_span(store, prompt, providers, abilities, conversation_id, span).await
}

/// 挑出可压的块：从**第一条没被摘要覆盖的消息**起，往后取至多 `limit` 个**已闭合**的块（§18）。
///
/// （开着的那个块永不压 —— 用户刚说完还没答，它不是"一段往事"。）
pub fn pick_blocks(path: &[Message], limit: usize) -> Vec<Block> {
    let Some(start) = path.iter().position(|message| message.summary_id.is_none()) else {
        return Vec::new();
    };
    let anchor = path[start].id;
    blocks::numbered(blocks::blocks_of_path(path))
        .into_iter()
        .skip_while(|block| block.first != anchor)
        .filter(|block| block.closed)
        .take(limit)
        .collect()
}

/// 一次压缩要用到的全部东西（都在锁里取好；锁外不再碰库）。
struct Material {
    conversation: Conversation,
    /// 要收的那一段（路径上的切片）。
    slice: Vec<Message>,
    /// 前情提要：覆盖着"这一段之前那一条"的摘要正文（`None` = 从故事开头开始）。
    previous_summary: Option<String>,
    /// 两端的世界状态（**都从原始消息现演** —— §14：状态是端点的属性，不是段的属性）。
    state_before: BTreeMap<String, String>,
    state_after: BTreeMap<String, String>,
    /// 这一段覆盖了几个**对话块**（落 `summaries.blocks`）。
    block_count: i64,
    /// 两端在**当前路径**上的序号（1 起；`{{range}}` 用，写出来给人看）。
    first_ordinal: usize,
    last_ordinal: usize,
    /// 每个块的起点 id（材料里插 `— 块 N —` 用）。
    block_starts: Vec<Uuid>,
    ratio: f64,
}

fn path_of(store: &Store, conversation_id: Uuid) -> Result<Vec<Message>> {
    store
        .get_conversation(conversation_id)
        .map_err(|error| Error::Store(error.to_string()))?
        .ok_or(Error::NotFound("会话"))?;
    store
        .list_messages(conversation_id)
        .map_err(|error| Error::Store(error.to_string()))
}

/// 取料：裁区间、现演两端状态、算块数、找前情提要。
fn collect(
    store: &Arc<Mutex<Store>>,
    prompt: &Prompt,
    conversation_id: Uuid,
    span: Span,
) -> Result<Material> {
    let store = store
        .lock()
        .map_err(|_| Error::Store("存储锁中毒".to_owned()))?;
    let conversation = store
        .get_conversation(conversation_id)
        .map_err(|error| Error::Store(error.to_string()))?
        .ok_or(Error::NotFound("会话"))?;
    let path = store
        .list_messages(conversation_id)
        .map_err(|error| Error::Store(error.to_string()))?;

    let begin = path
        .iter()
        .position(|message| message.id == span.begin)
        .ok_or(Error::NotFound("区间的起点"))?;
    let end = path
        .iter()
        .position(|message| message.id == span.end)
        .ok_or(Error::NotFound("区间的终点"))?;
    if end < begin {
        return Err(Error::Rejected("区间两端反了".to_owned()));
    }

    // 两端状态现演（§14）：before = 区间之前，after = 区间含末条。
    // 层数多深都一样 —— 状态只认**原始消息**，不爬摘要树。
    let view = |upto: usize| {
        crate::world::WorldStateView::from_sources(
            conversation_id,
            &prompt.text,
            prompt.source,
            &path[..upto],
        )
        .effective
    };
    let state_before = view(begin);
    let state_after = view(end + 1);

    // 前情提要：上一条消息归谁的摘要（有就用它的正文，没有就是故事开头）
    let previous_summary = begin
        .checked_sub(1)
        .and_then(|index| path.get(index))
        .and_then(|message| message.summary_id)
        .and_then(|summary_id| {
            store
                .list_summaries(conversation_id)
                .ok()
                .and_then(|summaries| {
                    summaries
                        .into_iter()
                        .find(|summary| summary.id == summary_id)
                        .map(|summary| summary.text)
                })
        });

    let slice = path[begin..=end].to_vec();
    let mut blocks_here = blocks::blocks_of_path(&slice);
    // "开着"只是"它落在这一段末尾"的假象：这一刀之后还有消息 ⇒ 它其实已经闭合
    if let Some(last) = blocks_here.last_mut()
        && !last.closed
        && end + 1 < path.len()
    {
        last.closed = true;
    }
    let block_count = blocks_here.iter().filter(|block| block.closed).count().max(1) as i64;
    let block_starts: Vec<Uuid> = blocks_here.iter().map(|block| block.first).collect();
    let ratio = store
        .get_model(&conversation.provider, &conversation.model)
        .ok()
        .flatten()
        .map(|model| crate::world::tokenizer_ratio(&model.tokenizer))
        .unwrap_or(crate::world::DEFAULT_TOKENIZER_RATIO);

    Ok(Material {
        conversation,
        slice,
        previous_summary,
        state_before,
        state_after,
        block_count,
        first_ordinal: begin + 1,
        last_ordinal: end + 1,
        block_starts,
        ratio,
    })
}

/// 拼给摘要器的材料（§15 的形状），以及占位符的值（§26）。
///
/// ```text
/// 【前情提要·第 3–10 条】
/// <上一条摘要的正文 / "这是故事的开端。">
///
/// 【原文】
/// — 块 1 —
/// user: …
/// assistant: …          ← 原样：含当时的 <state>，不含思考
/// — 块 2 —
/// …
///
/// NOW Triggers Compaction.
/// ```
///
/// 两端状态与时间**不在这里** —— 它们由模板里的 `{{state_before}} / {{state_after}} / {{system_time}}`
/// 摆进 system（用户可以自己调格式）。
fn material_for(material: &Material) -> (String, BTreeMap<&'static str, String>) {
    let mut body = String::new();
    body.push_str("【前情提要】\n");
    body.push_str(material.previous_summary.as_deref().unwrap_or("这是故事的开端。"));
    body.push_str("\n\n【原文】\n");

    let mut block_index = 0;
    for message in &material.slice {
        if material.block_starts.contains(&message.id) {
            block_index += 1;
            body.push_str(&format!("\n— 块 {block_index} —\n"));
        }
        let who = match message.role {
            crate::model::Role::User => "user",
            crate::model::Role::Assistant => "assistant",
        };
        body.push_str(who);
        body.push_str(": ");
        // **原样**：含当时的 <state>（那是记录，能解释因果 —— §14）；思考不在正文里，天然不带
        body.push_str(&message.content);
        body.push('\n');
    }
    body.push_str("\nNOW Triggers Compaction.\n");

    let mut values: BTreeMap<&'static str, String> = BTreeMap::new();
    values.insert("system_time", template::system_time_utc());
    values.insert("blocks", material.block_count.to_string());
    values.insert(
        "range",
        format!("第 {}–{} 条", material.first_ordinal, material.last_ordinal),
    );
    values.insert("state_before", template::render_state(&material.state_before));
    values.insert("state_after", template::render_state(&material.state_after));
    (body, values)
}

fn now_ms() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis() as i64)
        .unwrap_or(0)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::Role;

    fn store_with_turns(turns: usize) -> (Arc<Mutex<Store>>, Uuid) {
        let mut store = Store::open_in_memory().unwrap();
        let conversation = store.create_conversation("dummy", "dummy", "").unwrap();
        let mut parent = None;
        for index in 0..turns {
            let user = store
                .insert_message(
                    conversation.id,
                    Role::User,
                    &format!("<state>set 回合 = {index}</state>第 {index} 问"),
                    parent,
                )
                .unwrap();
            let assistant = store
                .insert_message(conversation.id, Role::Assistant, &format!("第 {index} 答"), Some(user.id))
                .unwrap();
            parent = Some(assistant.id);
        }
        (Arc::new(Mutex::new(store)), conversation.id)
    }

    fn prompt() -> Prompt {
        Prompt {
            text: String::new(),
            source: crate::world::PromptSource::Agent,
        }
    }

    /// 手动压缩（dummy 全链路）：压 N 个块 ⇒ 一条摘要 + 成员指好 + **messages 一字不动**。
    #[tokio::test]
    async fn summarize_blocks_runs_the_whole_chain() {
        let (store, conversation_id) = store_with_turns(3);
        // dummy 也得在 providers 里有一条（与线上同一套选择规则：Backend::select 认它）
        let providers = ProvidersConfig {
            providers: vec![crate::config::ProviderConfig {
                id: "dummy".to_owned(),
                kind: crate::config::ProviderKind::Dummy,
                ..Default::default()
            }],
            ..Default::default()
        };
        let before = {
            let store = store.lock().unwrap();
            store.list_all_messages(conversation_id).unwrap()
        };

        let summary = summarize_blocks(
            &store,
            &prompt(),
            &providers,
            &AbilitiesConfig::default(),
            conversation_id,
            2,
        )
        .await
        .unwrap();

        assert_eq!(summary.blocks, 2, "两块");
        assert_eq!(summary.text, crate::chat::DUMMY_MODEL_REPLY);
        assert_eq!(summary.source_kind, SummarySourceKind::Message);
        assert!(summary.prompt_version > 0, "带模板版本");
        assert!(summary.tokens > 0);
        assert!(!summary.dirty);

        let after = {
            let store = store.lock().unwrap();
            store.list_all_messages(conversation_id).unwrap()
        };
        assert_eq!(after.len(), before.len());
        assert_eq!(
            after
                .iter()
                .map(|message| message.content.clone())
                .collect::<Vec<_>>(),
            before
                .iter()
                .map(|message| message.content.clone())
                .collect::<Vec<_>>(),
            "压缩只许插摘要，不许碰 messages"
        );
        assert_eq!(after[0].summary_id, Some(summary.id));
        assert_eq!(after[3].summary_id, Some(summary.id));
        assert_eq!(after[4].summary_id, None, "开着的那个块没被覆盖");

        // 再压一次：只剩第三块（开着的）⇒ 没有可压的
        let again = summarize_blocks(
            &store,
            &prompt(),
            &providers,
            &AbilitiesConfig::default(),
            conversation_id,
            5,
        )
        .await;
        assert!(matches!(again, Err(Error::NothingToDo)), "{again:?}");
    }

    /// 挑块：起于第一条未被覆盖的消息，只取**已闭合**的块。
    #[test]
    fn pick_blocks_starts_at_first_uncovered() {
        use Role::{Assistant as A, User as U};
        let mut store = Store::open_in_memory().unwrap();
        let conversation = store.create_conversation("dummy", "dummy", "").unwrap();
        let mut parent = None;
        for role in [U, A, U, A, U, A] {
            let message = store
                .insert_message(conversation.id, role, "说了点什么", parent)
                .unwrap();
            parent = Some(message.id);
        }
        let path = store.list_messages(conversation.id).unwrap();
        let picked = pick_blocks(&path, 10);
        assert_eq!(picked.len(), 2, "三个块里最后那个开着 ⇒ 只挑到两个");
        assert_eq!(picked[0].first, path[0].id);
    }

    /// 材料：形状对（前情提要 / 原文 / 块分隔 / 触发语），变量值齐。
    #[test]
    fn material_has_the_documented_shape_and_values() {
        let material = Material {
            conversation: Conversation {
                id: Uuid::now_v7(),
                title: String::new(),
                system_prompt: String::new(),
                provider: "dummy".to_owned(),
                model: "dummy".to_owned(),
                agent_id: "default".to_owned(),
                current_leaf: None,
                created_at: 0,
                updated_at: 0,
            },
            slice: vec![
                Message {
                    id: Uuid::now_v7(),
                    conversation_id: Uuid::now_v7(),
                    role: Role::User,
                    content: "<state>set HP = 1</state>开门".to_owned(),
                    reasoning: None,
                    reasoning_ms: None,
                    duration_ms: None,
                    usage: None,
                    parent_id: None,
                    summary_id: None,
                    created_at: 0,
                },
                Message {
                    id: Uuid::now_v7(),
                    conversation_id: Uuid::now_v7(),
                    role: Role::Assistant,
                    content: "门开了".to_owned(),
                    reasoning: Some("不该出现".to_owned()),
                    reasoning_ms: None,
                    duration_ms: None,
                    usage: None,
                    parent_id: None,
                    summary_id: None,
                    created_at: 0,
                },
            ],
            previous_summary: Some("此前的事".to_owned()),
            state_before: BTreeMap::new(),
            state_after: {
                let mut state = BTreeMap::new();
                state.insert("HP".to_owned(), "1".to_owned());
                state
            },
            block_count: 1,
            first_ordinal: 1,
            last_ordinal: 2,
            block_starts: Vec::new(),
            ratio: 1.3,
        };
        let (body, values) = material_for(&material);
        assert!(body.starts_with("【前情提要】\n此前的事"));
        assert!(body.contains("【原文】"));
        assert!(body.contains("user: <state>set HP = 1</state>开门"), "正文原样，含当时的 <state>");
        assert!(!body.contains("不该出现"), "思考不进材料");
        assert!(body.ends_with("NOW Triggers Compaction.\n"));
        assert_eq!(values.get("blocks").unwrap(), "1");
        assert_eq!(values.get("state_after").unwrap(), "HP = 1");
        assert_eq!(values.get("state_before").unwrap(), "（空）");
        assert!(values.get("system_time").unwrap().ends_with(" UTC"));
    }
}
