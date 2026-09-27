//! 变量：从消息正文里解析状态标签，折出"当前变量表"，并组装真正发给模型的东西。
//!
//! 口径（与设计讨论一致）：
//! - `<state>…</state>` 是**存档**的一部分：原文永远原样留在消息里，不删不改；
//! - 但**发给模型的历史一律剔除标签**，改为在系统提示词后注入一张"当前变量表"（每回合重算）；
//! - 解析产出的是**操作**（`set`/`del`），只追加、不回改：回溯 = 从空 fold 到第 N 步；
//! - 作用域两级：`set`/`del` 是**本会话**（session，随会话覆写），`setglobal`/`delglobal` 是**全局**
//!   （global，所有会话共用的底）。
//!
//! 标签内容只有两种形态，因为操作只有两种：
//! ```text
//! <state>
//! set HP = 12
//! del 湿透的火把
//! setglobal 季节 = 初冬
//! </state>
//! ```

use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};
use uuid::Uuid;

use crate::model::{Message, Summary};

/// 键的长度上限（字符）。
pub const KEY_MAX_CHARS: usize = 64;
/// 单个值的长度上限（字符）。不设上限的话，模型会往变量里塞整段散文，
/// 而这张表每条消息都要注入一次。
pub const VALUE_MAX_CHARS: usize = 2048;
/// 一条消息最多接受多少个操作（防模型循环刷屏）。
pub const OPS_PER_MESSAGE_MAX: usize = 32;

/// 写进 agent 系统提示词里告诉模型怎么用状态块。前端提供"复制"按钮。
pub const PROTOCOL_HINT: &str = "\
你可以在回复的末尾用一个状态块记录世界状态的变化：

<state>
set HP = 12
del 湿透的火把
setglobal 季节 = 初冬
</state>

`set` / `del` 改的是本场对话的临时状态，`setglobal` / `delglobal` 改的是所有对话共享的设定。
只在有变化时输出状态块；除状态块以外的正文照常书写。当前状态会在每次请求前告诉你，不必复述。";

/// 操作作用域。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Scope {
    /// 本会话（随会话覆写全局）。
    Session,
    /// 全局（所有会话共用的底）。
    Global,
}

impl Scope {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Session => "session",
            Self::Global => "global",
        }
    }

    pub fn parse(raw: &str) -> Option<Self> {
        match raw {
            "session" => Some(Self::Session),
            "global" => Some(Self::Global),
            _ => None,
        }
    }
}

/// 操作种类。只有两种——标签语法因此只有两种行形态。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum OpKind {
    Set,
    Delete,
}

impl OpKind {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Set => "set",
            Self::Delete => "del",
        }
    }

    pub fn parse(raw: &str) -> Option<Self> {
        match raw {
            "set" => Some(Self::Set),
            "del" => Some(Self::Delete),
            _ => None,
        }
    }
}

/// 一条待落库的操作。
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct VarOp {
    pub kind: OpKind,
    pub key: String,
    /// `Set` 为 `Some`（可以是空串），`Delete` 为 `None`。
    pub value: Option<String>,
}

/// 一条**现算出来的**操作。`seq` 只在一次重演内部有意义（消息顺序 → 块内顺序），
/// 库里不保存它——变量表是读的时候从正文推出来的，见 [`VariableView::from_messages`]。
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct VarOpRow {
    pub seq: i64,
    pub scope: Scope,
    pub kind: OpKind,
    pub key: String,
    pub value: Option<String>,
    /// 来源消息；手工/将来的注入通道可能为空。
    pub message_id: Option<Uuid>,
    pub conversation_id: Option<Uuid>,
    pub created_at: i64,
}

/// 解析结果。
#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub struct Parsed {
    /// 剔除状态块后的正文——**这才是发给模型的历史**。
    pub cleaned: String,
    pub ops: Vec<VarOp>,
    /// 被忽略的行/异常，进调试视图；不影响消息落库。
    pub warnings: Vec<String>,
}

/// 从正文里解析状态块。**无论块里写了什么，块本身一定被剔除**——
/// 标签泄漏进上下文比丢掉一条操作严重得多。
pub fn parse(text: &str) -> Parsed {
    let mut parsed = Parsed::default();
    let mut cleaned = String::with_capacity(text.len());
    let mut cursor = 0usize;

    loop {
        let Some(open) = find_tag(text, cursor, false) else {
            break;
        };
        let open_end = tag_end(text, open);
        cleaned.push_str(&text[cursor..open]);

        match find_tag(text, open_end, true) {
            Some(close) => {
                parse_block(&text[open_end..close], &mut parsed);
                cursor = tag_end(text, close);
            }
            None => {
                // 没闭合：把剩下的都当块内容——绝不能把它当正文发出去。
                parsed
                    .warnings
                    .push("状态块没有闭合，已按到消息结尾处理".to_owned());
                parse_block(&text[open_end..], &mut parsed);
                cursor = text.len();
                break;
            }
        }
    }

    cleaned.push_str(&text[cursor..]);
    parsed.cleaned = cleaned.trim_end().to_owned();
    parsed
}

/// 找下一个 `<state>`（`closing=false`）或 `</state>`（`closing=true`）的起始下标。
/// 名字大小写不敏感、允许标签内多余空白：`<State >` 也认。
fn find_tag(text: &str, from: usize, closing: bool) -> Option<usize> {
    let mut cursor = from;
    while let Some(offset) = text[cursor..].find('<') {
        let start = cursor + offset;
        let end = tag_end(text, start);
        if end <= start {
            return None; // 没有 '>'，后面不会再有完整标签
        }
        let name = text[start + 1..end - 1].trim();
        let matched = if closing {
            name.eq_ignore_ascii_case("/state")
        } else {
            name.eq_ignore_ascii_case("state")
        };
        if matched {
            return Some(start);
        }
        cursor = end;
    }
    None
}

/// 标签 `<…>` 的结束下标（含 `>`）；没有 `>` 时返回 `text.len()`。
fn tag_end(text: &str, start: usize) -> usize {
    text[start..]
        .find('>')
        .map_or(text.len(), |offset| start + offset + 1)
}

fn parse_block(body: &str, parsed: &mut Parsed) {
    for raw in body.lines() {
        let line = raw.trim();
        if line.is_empty() {
            continue;
        }
        if parsed.ops.len() >= OPS_PER_MESSAGE_MAX {
            parsed
                .warnings
                .push(format!("操作数超过上限 {OPS_PER_MESSAGE_MAX}，其余已忽略"));
            return;
        }

        let (command, rest) = line
            .split_once(char::is_whitespace)
            .unwrap_or((line, ""));
        let kind = match command.to_ascii_lowercase().as_str() {
            "set" => OpKind::Set,
            "del" => OpKind::Delete,
            // 全局变量不再从消息里写：它住手写的 config/variables.json。
            // 老消息里残留的 setglobal/delglobal 明确报出来——别静默丢掉用户写过的东西。
            "setglobal" | "delglobal" => {
                parsed.warnings.push(format!(
                    "`{command}` 不从消息里写全局变量了，请挪到 config/variables.json：{line}"
                ));
                continue;
            }
            other => {
                parsed
                    .warnings
                    .push(format!("不认识的操作 `{other}`，已忽略：{line}"));
                continue;
            }
        };

        let (key, value) = match kind {
            OpKind::Set => match rest.split_once('=') {
                // 只在**第一个** `=` 处切，值里可以有 `=`
                Some((key, value)) => (key.trim(), Some(value.trim().to_owned())),
                None => {
                    parsed
                        .warnings
                        .push(format!("缺少 `=`，已忽略：{line}"));
                    continue;
                }
            },
            OpKind::Delete => (rest.trim(), None),
        };

        if let Err(reason) = check_key(key) {
            parsed.warnings.push(format!("{reason}：{line}"));
            continue;
        }
        if let Some(value) = &value {
            let chars = value.chars().count();
            if chars > VALUE_MAX_CHARS {
                parsed
                    .warnings
                    .push(format!("值超过 {VALUE_MAX_CHARS} 字符（{chars}），已忽略：{key}"));
                continue;
            }
        }

        parsed.ops.push(VarOp {
            kind,
            key: key.to_owned(),
            value,
        });
    }
}

fn check_key(key: &str) -> Result<(), String> {
    if key.is_empty() {
        return Err("键为空".to_owned());
    }
    if key.chars().count() > KEY_MAX_CHARS {
        return Err(format!("键超过 {KEY_MAX_CHARS} 字符"));
    }
    if key
        .chars()
        .any(|c| c.is_whitespace() || matches!(c, '/' | '<' | '>' | '='))
    {
        return Err("键不能含空白或 `/` `<` `>` `=`".to_owned());
    }
    Ok(())
}

/// 折叠结果：`None` 是**墓碑**（显式删除），它必须能和"没设置过"区分开，
/// 否则本会话删掉的键会从全局底下漏回来。
pub type Dict = BTreeMap<String, Option<String>>;

/// 从空开始按 `seq` 顺序执行操作（`rows` 必须已按 `seq` 升序）。
pub fn fold(rows: &[VarOpRow], scope: Scope) -> Dict {
    let mut dict = Dict::new();
    for row in rows.iter().filter(|row| row.scope == scope) {
        match row.kind {
            OpKind::Set => {
                dict.insert(row.key.clone(), row.value.clone());
            }
            OpKind::Delete => {
                dict.insert(row.key.clone(), None);
            }
        }
    }
    dict
}

/// 生效值 = 全局打底，本会话覆写（本会话的 `del` 同样覆写全局）。
pub fn effective(global: &Dict, session: &Dict) -> BTreeMap<String, String> {
    let mut merged = global.clone();
    for (key, value) in session {
        merged.insert(key.clone(), value.clone());
    }
    merged
        .into_iter()
        .filter_map(|(key, value)| value.map(|value| (key, value)))
        .collect()
}

/// 注入给模型的变量表；空表返回 `None`（不注入空块）。
pub fn render_table(values: &BTreeMap<String, String>) -> Option<String> {
    if values.is_empty() {
        return None;
    }
    let mut out = String::from("当前变量:");
    for (key, value) in values {
        out.push_str(&format!("\n  {key} = {value}"));
    }
    Some(out)
}

/// 面板/接口要的一份快照。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct VariableView {
    /// 全局那几张（来自 `config/variables.json`，所有会话共用）。
    pub global: Vec<VarOpRow>,
    /// 本会话正文里累积出来的操作（"这一局改了什么"）。
    pub session: Vec<VarOpRow>,
    /// 全局现值。
    pub global_values: BTreeMap<String, String>,
    /// 生效值（全局 + 本会话）。
    pub effective: BTreeMap<String, String>,
}

/// 生效的那份 system prompt 是从哪儿来的——决定它写的 `<state>` 块算"全局底子"
/// 还是"本会话的起始状态"。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum PromptSource {
    /// 会话没写自己的提示词，用的是 agent 的：这份底子所有用同一 agent 的会话共享。
    Agent,
    /// 会话自己写了提示词（覆盖了 agent 的）：这份底子只属于这条会话。
    Conversation,
}

impl VariableView {
    /// **现演一份快照**：库里只存正文与提示词，变量表是每次顺着它们重演出来的。
    ///
    /// 顺序（也就是 fold 的顺序）：
    /// 1. 生效的 system prompt 里的 `<state>` 块 —— 这是**底子**，和消息里同一套语法；
    /// 2. 然后是每条消息正文里的块，按消息顺序（`list_messages` 的 rowid 序）。
    ///
    /// ![来源] 底子来自 agent 的提示词时算全局（`Scope::Global`，别的会话共享），
    /// 来自会话自己的提示词时算本会话（`Scope::Session`）。本会话的 `del` 写墓碑，
    /// 能把全局同名的键挡住，不会从底下漏回来。
    ///
    /// 没有派生表 ⇒ 编辑/删除消息、改提示词**都不需要**"重算变量"：文本改了，现演结果自然变。
    pub fn from_sources(
        conversation: Uuid,
        system_prompt: &str,
        source: PromptSource,
        messages: &[Message],
    ) -> Self {
        let prompt_scope = match source {
            PromptSource::Agent => Scope::Global,
            PromptSource::Conversation => Scope::Session,
        };

        let mut global_rows: Vec<VarOpRow> = Vec::new();
        let mut session_rows: Vec<VarOpRow> = Vec::new();

        // 1) 底子：系统提示词里的块（没有块就是空底子，很正常）。
        for op in parse(system_prompt).ops {
            let row = VarOpRow {
                seq: 0, // 下面按作用域重排
                scope: prompt_scope,
                kind: op.kind,
                key: op.key,
                value: op.value,
                message_id: None,
                conversation_id: Some(conversation),
                created_at: 0,
            };
            match prompt_scope {
                Scope::Global => global_rows.push(row),
                Scope::Session => session_rows.push(row),
            }
        }

        // 2) 然后才是消息，按顺序追加。
        for message in messages {
            for op in parse(&message.content).ops {
                session_rows.push(VarOpRow {
                    seq: 0,
                    scope: Scope::Session,
                    kind: op.kind,
                    key: op.key,
                    value: op.value,
                    message_id: Some(message.id),
                    conversation_id: Some(conversation),
                    created_at: message.created_at,
                });
            }
        }

        for (index, row) in global_rows.iter_mut().enumerate() {
            row.seq = index as i64;
        }
        for (index, row) in session_rows.iter_mut().enumerate() {
            row.seq = index as i64;
        }

        let global = fold(&global_rows, Scope::Global);
        let session = fold(&session_rows, Scope::Session);
        Self {
            global_values: values_of(&global),
            effective: effective(&global, &session),
            global: global_rows,
            session: session_rows,
        }
    }
}

/// 把折叠结果里的墓碑滤掉，得到"现值"。
pub fn values_of(dict: &Dict) -> BTreeMap<String, String> {
    dict.iter()
        .filter_map(|(key, value)| value.clone().map(|value| (key.clone(), value)))
        .collect()
}

/// 发给模型的一条消息。角色含 `System`——系统提示词不落库，但必须出现在请求里。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum OutgoingRole {
    System,
    User,
    Assistant,
}

impl OutgoingRole {
    /// 发往 OpenAI 兼容上游时 `messages[].role` 的取值。
    pub fn as_str(&self) -> &'static str {
        match self {
            Self::System => "system",
            Self::User => "user",
            Self::Assistant => "assistant",
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Outgoing {
    pub role: OutgoingRole,
    pub content: String,
}

/// 装配时的一"段"：**原文**，或者一条**摘要**（§20 行走的产出）。
enum Part<'a> {
    Message(&'a Message),
    /// 有摘要覆盖 ⇒ 这一段**不发明文**（Mask），发摘要的正文。
    Summary(&'a Summary),
}

/// 装配时的行走算法（§20）：**能取粗的不取精**。
///
/// ```text
/// 从路径根逐条往前走。站在某条消息 m 上：
///   S = m.summary_id
///   ├─ S 为空  → 发 m 的原文，往前一步
///   └─ S 非空  → P = S.parent_summary_id
///        ├─ P 为空 → 用 S，跳过 S 覆盖的那一串
///        └─ P 非空 → 往后**瞄一眼**：紧接着那串消息的 summary_id 是否全 ∈ P 的孩子？
///                     ├─ 是  → 用 P，跳过整串
///                     └─ 否  → 用 S，跳过它覆盖的那一串
/// ```
///
/// **"跳过"靠继续读 `summary_id` 判断，不存范围** —— 这就是不需要"两端见证"的原因。
/// "必须先瞄一眼"是因为 P 未必真的完整涵盖 S（分支/编辑都可能让覆盖变残，`dirty` 与剪枝让它
/// 极少发生但不是零）：不瞄就"直接用 P"，一旦 P 盖不住 S 就会**漏掉一段且不报错**。
/// 从 `index` 起，"眼前这一串"有多长：一条条往前数，条件不满足就停。
fn run_len(messages: &[Message], index: usize, keep: impl Fn(&Message) -> bool) -> usize {
    messages[index..]
        .iter()
        .take_while(|message| keep(message))
        .count()
}

fn walk<'a>(messages: &'a [Message], summaries: &'a [Summary]) -> Vec<Part<'a>> {
    let summary_of = |id: Uuid| summaries.iter().find(|summary| summary.id == id);

    let mut parts = Vec::new();
    let mut index = 0;
    while index < messages.len() {
        let message = &messages[index];

        // ① 没被覆盖（或指针悬空）⇒ 发原文，往前一步
        let Some(summary) = message.summary_id.and_then(summary_of) else {
            parts.push(Part::Message(message));
            index += 1;
            continue;
        };

        // ② 它自己覆盖的那一串（摘要覆盖的总是连续几条 —— 块绝不被劈开）
        let covered_by_self = run_len(messages, index, |m| m.summary_id == Some(summary.id)).max(1);

        // ③ 能再上一层吗：**往后瞄一眼** —— 从这儿起，父摘要 P 的孩子们覆盖的那一串就在眼前吗？
        //    （算出来的长度就是"眼前真实存在的"，所以这一步既完成了检查、也给出了跳过的长度）
        let promoted = summary.parent_summary_id.and_then(summary_of).map(|parent| {
            let belongs = |m: &Message| {
                m.summary_id
                    .and_then(summary_of)
                    .and_then(|own| own.parent_summary_id)
                    == Some(parent.id)
            };
            let run = run_len(messages, index, belongs);
            // 关键：**P 的孩子们必须一个不缺地就在眼前** —— 只看 run 会拿一条只盖了一半的
            // 父摘要去顶（它声称涵盖 S1+S2，可这一串里只有 S1），那就会少发东西。
            let total = messages.iter().filter(|m| belongs(m)).count();
            (parent, run, total)
        });

        match promoted {
            Some((parent, run, total)) if run > 0 && run == total => {
                parts.push(Part::Summary(parent));
                index += run;
            }
            // 父摘要盖不住眼前这一串（分支/编辑弄残过）⇒ 老实用它自己，别漏内容
            _ => {
                parts.push(Part::Summary(summary));
                index += covered_by_self;
            }
        }
    }
    parts
}

/// 组装**真正要发出去的东西**：系统提示词（+ 当前变量表）+ 历史（标签已剔除、有摘要就不发明文）。
///
/// 这是"剔除标签、只发当前状态、**按摘要收拢**"的唯一实现处 —— 真模型后端直接用它，
/// 不会有人再写第二条路径。摘要在**装配**里取代原文（Mask），**存放处一个字节都不动**。
pub fn build_outgoing(
    system_prompt: &str,
    messages: &[Message],
    summaries: &[Summary],
    values: &BTreeMap<String, String>,
) -> Vec<Outgoing> {
    // 提示词里的 `<state>` 块和消息里一个待遇：**原样留在存档里，发出去时剔除**。
    let mut system = parse(system_prompt).cleaned.trim().to_owned();
    if let Some(table) = render_table(values) {
        if !system.is_empty() {
            system.push_str("\n\n");
        }
        system.push_str(&table);
    }

    let mut outgoing = Vec::with_capacity(messages.len() + 1);
    if !system.is_empty() {
        outgoing.push(Outgoing {
            role: OutgoingRole::System,
            content: system,
        });
    }
    for part in walk(messages, summaries) {
        match part {
            // 正文为空的**不发出去**：生成中的占位消息（Assistant、空正文）就在历史里躺着，
            // 而"空一条、再说一句"对上游是纯噪音；整句都是 `<state>` 块的消息同理（剔除后为空）。
            Part::Message(message) => {
                let content = parse(&message.content).cleaned;
                if content.trim().is_empty() {
                    continue;
                }
                outgoing.push(Outgoing {
                    role: match message.role {
                        crate::model::Role::User => OutgoingRole::User,
                        crate::model::Role::Assistant => OutgoingRole::Assistant,
                    },
                    content,
                });
            }
            // 摘要：不是谁说的话，是**程序摆给模型的前情** ⇒ 用 user 角色 + 明写的抬头，
            // 免得模型把它当成"用户刚说的一句"。
            Part::Summary(summary) => {
                let text = summary.text.trim();
                if text.is_empty() {
                    continue;
                }
                outgoing.push(Outgoing {
                    role: OutgoingRole::User,
                    content: format!("【前情提要·{} 块】\n{text}", summary.blocks.max(1)),
                });
            }
        }
    }
    outgoing
}

/// `models.tokenizer` 里 ratio 的缺省值。
pub const DEFAULT_TOKENIZER_RATIO: f64 = 1.3;

/// **唯一**的 token 估算处：`tokens = ceil(字符数 / ratio)`，`ratio` = 每个 token 多少字符。
///
/// 只用于**排预算与显示占用** —— 绝不参与计费、也绝不参与任何正确性判断。
/// 单一比率必然粗糙（实测：中文正文 26 字 ≈ 27 token，≈1.0 字/token；思考段 201 字 ≈ 92 token，
/// ≈2.2 —— BPE 会合并），所以预算要留 25% 余量。别处一律调它，不许再写第二处。
pub fn estimate_tokens(text: &str, ratio: f64) -> usize {
    if text.is_empty() {
        return 0;
    }
    let ratio = if ratio.is_finite() && ratio > 0.0 && ratio <= 100.0 {
        ratio
    } else {
        DEFAULT_TOKENIZER_RATIO
    };
    ((text.chars().count() as f64 / ratio).ceil() as usize).max(1)
}

/// 从 `models.tokenizer` 的 JSON 文本里解出比率；解不出（或值离谱）就用缺省 ——
/// 一个写坏的配置不该把预算算崩。
pub fn tokenizer_ratio(tokenizer_json: &str) -> f64 {
    #[derive(serde::Deserialize)]
    struct Tokenizer {
        #[serde(default)]
        ratio: Option<f64>,
    }
    serde_json::from_str::<Tokenizer>(tokenizer_json)
        .ok()
        .and_then(|tokenizer| tokenizer.ratio)
        .filter(|ratio| ratio.is_finite() && *ratio > 0.0 && *ratio <= 100.0)
        .unwrap_or(DEFAULT_TOKENIZER_RATIO)
}

/// `max_output` 缺席时的输出预留。
pub const DEFAULT_MAX_OUTPUT: i64 = 4096;

/// 模型能吃多少、这个数凭什么 —— **优先顺序只在这里写一遍**：
/// 用户覆盖（`models.context_override`）> 上游发现（`models.context_length`）> 设置兜底。
pub fn resolve_context(
    overridden: Option<i64>,
    discovered: Option<i64>,
) -> (Option<i64>, CtxLenSource) {
    match (overridden, discovered) {
        (Some(value), _) => (Some(value), CtxLenSource::Override),
        (None, Some(value)) => (Some(value), CtxLenSource::Model),
        (None, None) => (None, CtxLenSource::Setting),
    }
}

/// 上下文长度是从哪来的（显示用：让人知道顶栏那个数凭什么）。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum CtxLenSource {
    /// 用户为该模型填的覆盖值（`models.context_override`）—— 最优先。
    Override,
    /// 上游发现所得（`models.context_length`）。
    Model,
    /// 设置里的兜底（`chat.model_context_tokens`）—— 前两者都没有时用。
    Setting,
}

/// 预算口径：算"占用 / 触发"要用的全部外部数字，**只在这里算一遍**。
///
/// 两个数字，各管一件事（§24）：
/// - **模型上下文**（发现值优先，设置兜底）＝ 模型能吃多少；
/// - **压缩阈值** ＝ 吃到多少就该 Compact（没设 ⇒ 预算 × 0.8）。
#[derive(Debug, Clone, Copy, PartialEq, Serialize, Deserialize)]
pub struct Budget {
    /// 每 token 多少字符（`models.tokenizer`，缺省 1.3）。
    pub ratio: f64,
    /// 模型声明的上下文长度（上游常常不给 ⇒ `None` = 用 `model_context` 兜底）。
    pub ctx_len: Option<i64>,
    /// 上面那个数是从哪来的（显示用）。
    pub ctx_source: CtxLenSource,
    /// 输出预留（`models.max_output`；`None` ⇒ 按 [`DEFAULT_MAX_OUTPUT`] 算）。
    pub max_output: Option<i64>,
    /// **模型上下文**（`chat.model_context_tokens`）：上游没报时的兜底。
    pub model_context: usize,
    /// **摘要触发阈值**（`chat.compact_trigger_tokens`；`None` ⇒ 预算 × 0.8）。
    pub trigger: Option<usize>,
}

impl Budget {
    /// 真正可用的上下文：**发现值优先**，没有才用设置里的兜底。
    pub fn ctx_tokens(&self) -> i64 {
        self.ctx_len.unwrap_or(self.model_context as i64)
    }

    /// `budget = 可用上下文 − 输出预留`（**没有别的上限** —— 别吃到爆由触发阈值负责）。
    pub fn budget_tokens(&self) -> usize {
        let reserve = self.max_output.unwrap_or(DEFAULT_MAX_OUTPUT).max(0);
        (self.ctx_tokens() - reserve).max(0) as usize
    }

    /// 触发阈值：用户设了就用用户设的（例：1M 模型设 500K）；没设 ⇒ **预算 × 0.8**。
    pub fn trigger_tokens(&self) -> usize {
        self.trigger.unwrap_or(self.budget_tokens() * 8 / 10)
    }
}

/// 一次出站的占用数字（`GET /conversations/{id}/context` 的返回）。
///
/// **全是估算**，只服务显示与"要不要 Compact"的判断。`last_prompt_tokens` 是唯一
/// 来自上游的地面真相（上一轮 `usage` 报的）——摆出来是为了让"估算漂没漂"一眼可见。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct ContextUsage {
    /// 下一次出站的全部文本（系统提示词 + 变量表 + 全历史）的估算。
    pub used_tokens: usize,
    pub budget_tokens: usize,
    pub trigger_tokens: usize,
    /// 相对预算的剩余；**超了就照实报负数**（夹到 0 会把"超了多少"藏起来）。
    pub remaining_tokens: i64,
    pub ctx_len: Option<i64>,
    /// 上面那个上下文长度是从哪来的：模型的发现值，还是设置里的兜底。
    pub ctx_len_source: CtxLenSource,
    pub max_output: Option<i64>,
    pub ratio: f64,
    /// 恒 `true`：这是估算，不是上游计数。
    pub estimated: bool,
    pub last_prompt_tokens: Option<u64>,
    pub over_budget: bool,
}

/// 算这一次出站的占用（**纯函数**：外部数字都由 `budget` / `last_prompt_tokens` 带进来）。
pub fn context_usage(
    outgoing: &[Outgoing],
    budget: &Budget,
    last_prompt_tokens: Option<u64>,
) -> ContextUsage {
    let used_tokens = outgoing
        .iter()
        .map(|item| estimate_tokens(&item.content, budget.ratio))
        .sum();
    let budget_tokens = budget.budget_tokens();
    ContextUsage {
        used_tokens,
        budget_tokens,
        trigger_tokens: budget.trigger_tokens(),
        remaining_tokens: budget_tokens as i64 - used_tokens as i64,
        ctx_len: budget.ctx_len,
        ctx_len_source: budget.ctx_source,
        max_output: budget.max_output,
        ratio: budget.ratio,
        estimated: true,
        last_prompt_tokens,
        over_budget: used_tokens > budget_tokens,
    }
}

#[cfg(test)]
mod tests {

    /// 预算/触发的公式：**模型上下文（发现优先，设置兜底）− 输出预留**；
    /// 触发阈值缺省 = 预算 × 0.8（§14：自动压缩在 80% 触发）。
    #[test]
    fn budget_follows_the_documented_formula() {
        let base = Budget {
            ratio: 1.3,
            ctx_len: Some(1_000_000),
            ctx_source: CtxLenSource::Model,
            max_output: Some(8192),
            model_context: 131_072,
            trigger: None,
        };
        assert_eq!(base.budget_tokens(), 991_808, "100 万 − 8192");
        assert_eq!(base.trigger_tokens(), 793_446, "没设阈值 ⇒ 预算 × 0.8");

        // 上游没报 ⇒ 用设置里的兜底
        let fallback = Budget {
            ctx_len: None,
            ctx_source: CtxLenSource::Setting,
            ..base
        };
        assert_eq!(fallback.ctx_tokens(), 131_072);
        assert_eq!(fallback.budget_tokens(), 122_880, "131072 − 8192");

        // 输出预留缺省 4096
        let no_reserve = Budget {
            max_output: None,
            ..base
        };
        assert_eq!(no_reserve.budget_tokens(), 995_904, "100 万 − 4096");

        // 用户设了阈值 ⇒ 以用户的为准（例：1M 模型设 500K）
        let user_trigger = Budget {
            trigger: Some(500_000),
            ..base
        };
        assert_eq!(user_trigger.trigger_tokens(), 500_000);
    }

    /// 优先顺序只在一处：用户覆盖 > 上游发现 > 设置兜底。
    #[test]
    fn resolve_context_prefers_override_then_discovery() {
        assert_eq!(
            resolve_context(Some(2_000_000), Some(1_000_000)),
            (Some(2_000_000), CtxLenSource::Override)
        );
        assert_eq!(
            resolve_context(None, Some(1_000_000)),
            (Some(1_000_000), CtxLenSource::Model)
        );
        assert_eq!(
            resolve_context(None, None),
            (None, CtxLenSource::Setting)
        );
    }

    /// 造一条摘要（装配测试用）。
    fn summary(id: Uuid, text: &str, parent: Option<Uuid>, blocks: i64) -> Summary {
        Summary {
            id,
            conversation_id: Uuid::now_v7(),
            parent_summary_id: parent,
            source_kind: crate::model::SummarySourceKind::Message,
            text: text.to_owned(),
            blocks,
            tokens: 1,
            source_ids: Vec::new(),
            provider: "dummy".to_owned(),
            model: "dummy".to_owned(),
            prompt_version: 1,
            usage: None,
            dirty: false,
            created_at: 0,
        }
    }

    /// **装配时 Mask（§20 行走）**：有摘要覆盖的那一段不发明文，发摘要。
    ///
    /// 布局：`m1 m2 → S1`，`m3 m4 → S2`，`S1 + S2 → S12`（第二层），`m5 m6` 没被覆盖。
    /// 能取粗的不取精 ⇒ 该直接吃 `S12`（把 1–4 全跳过），再发 m5 m6 的原文。
    #[test]
    fn assembly_masks_covered_messages_with_the_coarsest_summary() {
        let conversation = Uuid::now_v7();
        let message = |role, content: &str| Message {
            id: Uuid::now_v7(),
            conversation_id: conversation,
            role,
            content: content.to_owned(),
            reasoning: None,
            reasoning_ms: None,
            duration_ms: None,
            usage: None,
            parent_id: None,
            summary_id: None,
            created_at: 0,
        };
        let s1 = summary(Uuid::now_v7(), "前两句的梗概", None, 1);
        let s2 = summary(Uuid::now_v7(), "中两句的梗概", None, 1);
        let s12 = summary(Uuid::now_v7(), "四句的总梗概", None, 2);

        let mut messages = vec![
            message(Role::User, "第一句"),
            message(Role::Assistant, "第一答"),
            message(Role::User, "第三句"),
            message(Role::Assistant, "第三答"),
            message(Role::User, "第五句"),
            message(Role::Assistant, "第五答"),
        ];
        messages[0].summary_id = Some(s1.id);
        messages[1].summary_id = Some(s1.id);
        messages[2].summary_id = Some(s2.id);
        messages[3].summary_id = Some(s2.id);
        let summaries = vec![
            Summary {
                parent_summary_id: Some(s12.id),
                ..s1.clone()
            },
            Summary {
                parent_summary_id: Some(s12.id),
                ..s2.clone()
            },
            s12.clone(),
        ];

        let outgoing = build_outgoing("你是助手", &messages, &summaries, &BTreeMap::new());
        let contents: Vec<&str> = outgoing
            .iter()
            .map(|item| item.content.as_str())
            .collect();
        assert_eq!(outgoing.len(), 4, "系统 + 一条粗摘要 + 两条原文：{contents:?}");
        assert!(contents[0].contains("你是助手"));
        assert!(
            contents[1].contains("四句的总梗概"),
            "该用最粗的那条（S12）：{contents:?}"
        );
        assert!(contents[1].contains("前情提要"));
        assert_eq!(contents[2], "第五句");
        assert_eq!(contents[3], "第五答");
        assert!(
            !contents.iter().any(|text| text.contains("第一句")),
            "被覆盖的原文必须不再发"
        );
    }

    /// 父摘要自称"我盖着 S1+S2"，可眼前这一串其实**不完整**（中间夹着没被覆盖的消息）⇒ 不能用它：
    /// 否则中间那段会被少发，而且**不报错**（这正是 §20 "必须先瞄一眼"要防的事）。
    #[test]
    fn assembly_falls_back_when_the_parent_summary_does_not_cover() {
        let conversation = Uuid::now_v7();
        let message = |content: &str| Message {
            id: Uuid::now_v7(),
            conversation_id: conversation,
            role: Role::User,
            content: content.to_owned(),
            reasoning: None,
            reasoning_ms: None,
            duration_ms: None,
            usage: None,
            parent_id: None,
            summary_id: None,
            created_at: 0,
        };
        let s1 = summary(Uuid::now_v7(), "前两句的梗概", None, 1);
        let s2 = summary(Uuid::now_v7(), "后两句的梗概", None, 1);
        let s12 = summary(Uuid::now_v7(), "四句的总梗概", None, 2);

        // m1 m2 → s1；**m3 没被覆盖**；m4 m5 → s2；s1 与 s2 都挂在 s12 下
        let mut messages = vec![
            message("第一句"),
            message("第二句"),
            message("第三句"),
            message("第四句"),
            message("第五句"),
        ];
        messages[0].summary_id = Some(s1.id);
        messages[1].summary_id = Some(s1.id);
        messages[3].summary_id = Some(s2.id);
        messages[4].summary_id = Some(s2.id);
        let summaries = vec![
            Summary {
                parent_summary_id: Some(s12.id),
                ..s1.clone()
            },
            Summary {
                parent_summary_id: Some(s12.id),
                ..s2.clone()
            },
            s12,
        ];

        let outgoing = build_outgoing("你是助手", &messages, &summaries, &BTreeMap::new());
        let contents: Vec<&str> = outgoing
            .iter()
            .map(|item| item.content.as_str())
            .collect();
        assert_eq!(
            outgoing.len(),
            4,
            "系统 + s1 + 那句没被覆盖的原文 + s2：{contents:?}"
        );
        assert!(contents[1].contains("前两句的梗概"));
        assert_eq!(contents[2], "第三句", "夹在中间的那句必须照发");
        assert!(contents[3].contains("后两句的梗概"), "{contents:?}");
        assert!(
            !contents.iter().any(|text| text.contains("四句的总梗概")),
            "它盖不全，就不该被用上"
        );
    }

    /// **会话提示词永不展开模板变量**（§26）：那是内置 Agent 的待遇。
    ///
    /// 理由：会话提示词在请求的**最前面**，它一变，前缀缓存整条废。
    #[test]
    fn session_prompt_is_never_templated() {
        let system_prompt = "你是客栈老板。{{system_time}} 与 {{我自己编的}} 都别动。";
        let outgoing = build_outgoing(
            system_prompt,
            &[message(Role::User, "在吗")],
            &[],
            &BTreeMap::new(),
        );
        let first = &outgoing[0].content;
        assert!(
            first.contains("{{system_time}}"),
            "会话提示词里的大括号必须原样发出去：{first}"
        );
        assert!(first.contains("{{我自己编的}}"));
    }

    /// 越界要**照实报负数**，不能夹到 0 —— 那会把"超了多少"藏起来。
    #[test]
    fn context_usage_reports_over_budget_honestly() {
        let outgoing = vec![
            Outgoing {
                role: OutgoingRole::System,
                content: "你是助手".to_owned(),
            },
            Outgoing {
                role: OutgoingRole::User,
                content: "他推开门，屋里只有一盏灯在闪".to_owned(),
            },
        ];
        let budget = Budget {
            ratio: 1.3,
            ctx_len: None,
            ctx_source: CtxLenSource::Setting,
            max_output: None,
            model_context: 10,
            trigger: None,
        };
        let usage = context_usage(&outgoing, &budget, Some(7));
        assert!(usage.used_tokens > 10, "两条加起来该超过 10");
        assert!(usage.over_budget);
        assert_eq!(
            usage.remaining_tokens,
            usage.budget_tokens as i64 - usage.used_tokens as i64
        );
        assert!(usage.remaining_tokens < 0);
        assert_eq!(usage.last_prompt_tokens, Some(7), "实测值原样带回");
        assert!(usage.estimated, "再像也是估算");
    }

    /// 估算只用于排预算/显示。这里钉的是**公式行为**，不是精确 token 数 ——
    /// 实测偏差（中文正文 ≈1.0 字/token、思考段 ≈2.2）说明单一比率必然 ±2×，
    /// 正因如此预算要留 25% 余量（§19.A）。
    #[test]
    fn estimate_tokens_is_chars_over_ratio() {
        assert_eq!(estimate_tokens("", 1.3), 0, "空文本 = 0，不是 1");
        assert_eq!(
            estimate_tokens("他推开门，屋里只有一盏灯在闪", 1.3),
            11,
            "14 字 ÷ 1.3 ⇒ 11"
        );
        assert_eq!(estimate_tokens("1234567890", 2.0), 5);
        assert_eq!(estimate_tokens("a", 1000.0), 1, "离谱的比率换回缺省，且至少算 1");
        assert_eq!(estimate_tokens("abcdefghij", 0.0), 8, "0 也是离谱值");
    }

    /// 坏配置不该把预算算崩：解不出来、或值离谱，都回落到缺省比率。
    #[test]
    fn tokenizer_ratio_falls_back_on_garbage() {
        assert_eq!(tokenizer_ratio(r#"{"kind":"approx","ratio":1.3}"#), 1.3);
        assert_eq!(tokenizer_ratio(r#"{"kind":"approx"}"#), DEFAULT_TOKENIZER_RATIO);
        assert_eq!(tokenizer_ratio(r#"{"ratio":-1}"#), DEFAULT_TOKENIZER_RATIO);
        assert_eq!(tokenizer_ratio("{}"), DEFAULT_TOKENIZER_RATIO);
        assert_eq!(tokenizer_ratio("不是 JSON"), DEFAULT_TOKENIZER_RATIO);
    }

    /// 正文为空的**不发出去**：生成中的占位消息（空正文）就在历史里躺着，
    /// 而"空一条、再说一句"对上游是纯噪音；整句只有 `<state>` 块的消息同理。
    #[test]
    fn outgoing_skips_empty_bodies() {
        use crate::model::{Message, Role};

        let conversation = Uuid::now_v7();
        let msg = |content: &str| Message {
            id: Uuid::now_v7(),
            conversation_id: conversation,
            role: Role::Assistant,
            content: content.to_owned(),
            reasoning: None,
            duration_ms: None,
            reasoning_ms: None,
            usage: None,
            parent_id: None,
            summary_id: None,
            created_at: 0,
        };
        let messages = vec![
            msg(""),
            msg("   "),
            msg("<state>set HP = 1</state>"),
            msg("真的说了点什么"),
        ];

        let outgoing = build_outgoing("你是助手", &messages, &[], &BTreeMap::new());
        assert_eq!(outgoing.len(), 2, "只留系统提示词与那句真的");
        assert_eq!(outgoing[1].content, "真的说了点什么");
    }

    /// 变量表是**从正文现演**的：顺序 = 消息顺序，能追到哪句写的，切一刀就是回溯。
    #[test]
    fn view_is_derived_from_message_bodies_in_order() {
        use crate::model::{Message, Role};

        let conversation = Uuid::now_v7();
        let msg = |content: &str, created_at: i64| Message {
            id: Uuid::now_v7(),
            conversation_id: conversation,
            role: Role::User,
            content: content.to_owned(),
            reasoning: None,
            duration_ms: None,
            reasoning_ms: None,
            usage: None,
            parent_id: None,
            summary_id: None,
            created_at,
        };
        let messages = vec![
            msg("<state>set HP = 12\nset 火把 = 1</state>我点亮了火把", 1000),
            msg("我继续往前走", 2000),
            msg("<state>del 火把</state>火把烧完了", 3000),
        ];
        // 底子在**系统提示词**里，和消息同一套语法；来自 agent ⇒ 算全局底子
        let system_prompt = "你是跑团主持人。\n<state>set 季节 = 初冬\nset HP = 99</state>";

        let view = VariableView::from_sources(
            conversation,
            system_prompt,
            PromptSource::Agent,
            &messages,
        );
        assert_eq!(view.effective["HP"], "12", "本会话覆写底子里的同名键");
        assert_eq!(view.effective["季节"], "初冬", "底子还在");
        assert!(!view.effective.contains_key("火把"), "最后一句把它删了（墓碑挡住）");
        assert_eq!(view.global_values.len(), 2);
        assert_eq!(view.global.len(), 2, "底子那两条算全局");
        assert_eq!(view.session.len(), 3, "三条操作都来自正文");
        assert!(view.global[0].message_id.is_none(), "底子不来自某条消息");
        assert_eq!(view.session[0].message_id, Some(messages[0].id), "能追到是哪句写的");
        assert_eq!(view.session[0].created_at, 1000);
        assert_eq!(view.session[2].kind, OpKind::Delete);

        // 回溯 = 只喂到第 N 条：这就是"从空 fold 到第 N 条"
        let rewind =
            VariableView::from_sources(conversation, system_prompt, PromptSource::Agent, &messages[..1]);
        assert_eq!(rewind.effective["火把"], "1", "第一句之后火把还在");
        assert_eq!(rewind.session.len(), 2);
    }

    /// 会话自己写了提示词（覆盖了 agent 的）：它的底子只属于这条会话，不外溢成全局。
    #[test]
    fn conversation_prompt_base_is_session_scoped() {
        let view = VariableView::from_sources(
            Uuid::now_v7(),
            "<state>set 地点 = 客栈</state>你是客栈老板。",
            PromptSource::Conversation,
            &[],
        );
        assert!(view.global.is_empty(), "会话自带的提示词不产生全局行");
        assert_eq!(view.session.len(), 1);
        assert_eq!(view.effective["地点"], "客栈");
        assert_eq!(view.session[0].created_at, 0, "提示词没有时间，0 = 不适用");
    }

    use super::*;
    use crate::model::Role;

    fn row(seq: i64, scope: Scope, kind: OpKind, key: &str, value: Option<&str>) -> VarOpRow {
        VarOpRow {
            seq,
            scope,
            kind,
            key: key.to_owned(),
            value: value.map(str::to_owned),
            message_id: None,
            conversation_id: None,
            created_at: 0,
        }
    }

    fn message(role: Role, content: &str) -> Message {
        Message {
            id: Uuid::now_v7(),
            conversation_id: Uuid::now_v7(),
            role,
            content: content.to_owned(),
            created_at: 0,
            reasoning: None,
            duration_ms: None,
            reasoning_ms: None,
            usage: None,
            parent_id: None,
            summary_id: None,
        }
    }

    #[test]
    fn parses_block_and_keeps_only_prose() {
        let text = "雨水顺着屋檐落下。\n\n<state>\nset HP = 12\ndel 火把\n</state>\n";
        let parsed = parse(text);
        assert_eq!(parsed.cleaned, "雨水顺着屋檐落下。");
        assert_eq!(
            parsed.ops,
            vec![
                VarOp {
                    kind: OpKind::Set,
                    key: "HP".to_owned(),
                    value: Some("12".to_owned()),
                },
                VarOp {
                    kind: OpKind::Delete,
                    key: "火把".to_owned(),
                    value: None,
                },
            ]
        );
        assert!(parsed.warnings.is_empty(), "{:?}", parsed.warnings);
    }

    /// `setglobal` / `delglobal` 已废弃（全局变量住手写的 `config/variables.json`）。
    /// 老消息里残留的写法要**明确报出来**——不能静默丢掉用户写过的东西。
    #[test]
    fn retired_global_commands_warn_instead_of_writing() {
        let parsed = parse("<state>\nsetglobal 季节 = 初冬\ndelglobal 世界\n</state>");
        assert!(parsed.ops.is_empty(), "不该再产出操作：{:?}", parsed.ops);
        assert_eq!(parsed.warnings.len(), 2, "{:?}", parsed.warnings);
        assert!(
            parsed.warnings[0].contains("config/variables.json"),
            "要说清该搬去哪：{:?}",
            parsed.warnings
        );
    }

    #[test]
    fn value_may_contain_equals_and_spaces() {
        let parsed = parse("<state>\nset 状态 = 血量 50/100 = 危险\n</state>");
        assert_eq!(parsed.ops.len(), 1);
        assert_eq!(parsed.ops[0].key, "状态");
        assert_eq!(parsed.ops[0].value.as_deref(), Some("血量 50/100 = 危险"));
    }

    #[test]
    fn bad_lines_warn_but_block_never_leaks() {
        let text = "正文\n<state>\n乱写一行\nset 缺等号\nset 坏/键 = 1\nset 长 = ";
        let long = "x".repeat(VALUE_MAX_CHARS + 1);
        let text = format!("{text}{long}\n</state>");
        let parsed = parse(&text);

        assert_eq!(parsed.cleaned, "正文", "块必须被整块剔除");
        for warning in [
            "不认识的操作",
            "缺少 `=`",
            "键不能含空白",
            "值超过",
        ] {
            assert!(
                parsed.warnings.iter().any(|line| line.contains(warning)),
                "缺少告警 `{warning}`：{:?}",
                parsed.warnings
            );
        }
    }

    #[test]
    fn unclosed_block_is_swallowed_to_the_end() {
        let parsed = parse("先说话\n<state>\nset HP = 1");
        assert_eq!(parsed.cleaned, "先说话");
        assert_eq!(parsed.ops.len(), 1);
        assert_eq!(parsed.warnings.len(), 1);
    }

    #[test]
    fn tag_lookup_tolerates_case_and_spaces() {
        let parsed = parse("正文\n<State >\nSET HP = 3\n</STATE>");
        assert_eq!(parsed.cleaned, "正文");
        assert_eq!(parsed.ops[0].value.as_deref(), Some("3"));
    }

    #[test]
    fn multiple_blocks_accumulate() {
        let parsed = parse("A\n<state>\nset a = 1\n</state>\nB\n<state>\nset b = 2\n</state>");
        assert_eq!(parsed.cleaned, "A\n\nB");
        assert_eq!(parsed.ops.len(), 2);
    }

    #[test]
    fn ops_are_capped_per_message() {
        let body: String = (0..OPS_PER_MESSAGE_MAX + 5)
            .map(|index| format!("set k{index} = {index}\n"))
            .collect();
        let parsed = parse(&format!("<state>\n{body}</state>"));
        assert_eq!(parsed.ops.len(), OPS_PER_MESSAGE_MAX);
        assert!(parsed.warnings.iter().any(|line| line.contains("上限")));
    }

    #[test]
    fn fold_applies_in_order_and_delete_writes_a_tombstone() {
        let rows = vec![
            row(1, Scope::Session, OpKind::Set, "HP", Some("1")),
            row(2, Scope::Session, OpKind::Set, "HP", Some("6")),
            row(3, Scope::Session, OpKind::Set, "位置", Some("客栈")),
            row(4, Scope::Session, OpKind::Delete, "位置", None),
        ];
        let dict = fold(&rows, Scope::Session);
        assert_eq!(dict.get("HP"), Some(&Some("6".to_owned())));
        assert_eq!(dict.get("位置"), Some(&None), "删除要留墓碑");
        assert_eq!(values_of(&dict).len(), 1);
    }

    #[test]
    fn session_overrides_global_including_deletes() {
        let messages = vec![message(
            Role::User,
            "<state>set HP = 3\ndel 世界</state>我把地图烧了",
        )];
        // 全局底子由 agent 的提示词提供（会话没有覆盖提示词时就是它）
        let view = VariableView::from_sources(
            Uuid::now_v7(),
            "<state>set 世界 = 临安\nset HP = 10</state>你是说书人。",
            PromptSource::Agent,
            &messages,
        );

        assert_eq!(view.global_values.get("HP").map(String::as_str), Some("10"));
        assert_eq!(view.effective.get("HP").map(String::as_str), Some("3"));
        assert!(!view.effective.contains_key("世界"), "会话删除要连全局一起遮掉");
        assert_eq!(view.session.len(), 2);
    }

    #[test]
    fn outgoing_strips_tags_and_injects_current_table() {
        // 提示词里也能写 `<state>`（底子），发出去时同样要剔除
        let system_prompt = "你是客栈老板。\n<state>set 季节 = 初冬</state>";
        let messages = vec![
            message(Role::User, "我要住店"),
            message(
                Role::Assistant,
                "客官里面请。\n<state>\nset HP = 12\nset 房号 = 天字三号\n</state>",
            ),
        ];
        let values = BTreeMap::from([
            ("HP".to_owned(), "12".to_owned()),
            ("房号".to_owned(), "天字三号".to_owned()),
        ]);
        let outgoing = build_outgoing(system_prompt, &messages, &[], &values);

        assert_eq!(outgoing[0].role, OutgoingRole::System);
        assert_eq!(
            outgoing[0].content,
            "你是客栈老板。\n\n当前变量:\n  HP = 12\n  房号 = 天字三号"
        );
        assert_eq!(outgoing[1].content, "我要住店");
        assert_eq!(
            outgoing[2].content, "客官里面请。",
            "历史里的状态块必须被剔除"
        );
        assert!(
            !outgoing.iter().any(|item| item.content.contains("<state>")),
            "任何一条都不该带着标签发出去"
        );
    }

    #[test]
    fn outgoing_without_variables_has_no_empty_table() {
        let system_prompt = "你是助手。";
        let messages = vec![message(Role::User, "在吗")];
        let outgoing = build_outgoing(system_prompt, &messages, &[], &BTreeMap::new());
        assert_eq!(outgoing[0].content, "你是助手。");
        assert_eq!(outgoing.len(), 2);
    }

    #[test]
    fn outgoing_without_system_prompt_omits_the_system_message() {
        let system_prompt = "   ";
        let messages = vec![message(Role::User, "在吗")];
        let outgoing = build_outgoing(system_prompt, &messages, &[], &BTreeMap::new());
        assert_eq!(outgoing.len(), 1);
        assert_eq!(outgoing[0].role, OutgoingRole::User);
    }
}
