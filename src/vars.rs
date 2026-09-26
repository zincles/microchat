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

use crate::model::{Conversation, Message};

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
    pub scope: Scope,
    pub kind: OpKind,
    pub key: String,
    /// `Set` 为 `Some`（可以是空串），`Delete` 为 `None`。
    pub value: Option<String>,
}

/// 已落库的一条操作（`seq` = 全局自增序号，即时间序）。
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
        let (scope, kind) = match command.to_ascii_lowercase().as_str() {
            "set" => (Scope::Session, OpKind::Set),
            "del" => (Scope::Session, OpKind::Delete),
            "setglobal" => (Scope::Global, OpKind::Set),
            "delglobal" => (Scope::Global, OpKind::Delete),
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
            scope,
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
    /// 全局操作日志（所有会话共用）。
    pub global: Vec<VarOpRow>,
    /// 本会话的操作日志（"这一局改了什么"）。
    pub session: Vec<VarOpRow>,
    /// 全局现值。
    pub global_values: BTreeMap<String, String>,
    /// 生效值（全局 + 本会话）。
    pub effective: BTreeMap<String, String>,
}

impl VariableView {
    /// `rows` = 全局 + 本会话的混合日志，已按 `seq` 升序。
    pub fn build(rows: &[VarOpRow]) -> Self {
        let global = fold(rows, Scope::Global);
        let session = fold(rows, Scope::Session);
        Self {
            global: rows
                .iter()
                .filter(|row| row.scope == Scope::Global)
                .cloned()
                .collect(),
            session: rows
                .iter()
                .filter(|row| row.scope == Scope::Session)
                .cloned()
                .collect(),
            global_values: values_of(&global),
            effective: effective(&global, &session),
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

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Outgoing {
    pub role: OutgoingRole,
    pub content: String,
}

/// 组装**真正要发出去的东西**：系统提示词（+ 当前变量表）+ 历史（标签已剔除）。
///
/// 这是"剔除标签、只发当前状态"的唯一实现处：真模型后端接进来时直接用它，
/// 不会有人再写第二条路径。
pub fn build_outgoing(
    conversation: &Conversation,
    messages: &[Message],
    values: &BTreeMap<String, String>,
) -> Vec<Outgoing> {
    let mut system = conversation.system_prompt.trim().to_owned();
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
    outgoing.extend(messages.iter().map(|message| Outgoing {
        role: match message.role {
            crate::model::Role::User => OutgoingRole::User,
            crate::model::Role::Assistant => OutgoingRole::Assistant,
        },
        content: parse(&message.content).cleaned,
    }));
    outgoing
}

#[cfg(test)]
mod tests {
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

    fn conversation(prompt: &str) -> Conversation {
        Conversation {
            id: Uuid::now_v7(),
            title: String::new(),
            system_prompt: prompt.to_owned(),
            provider: "dummy".to_owned(),
            model: "dummy".to_owned(),
            agent_id: "default".to_owned(),
            created_at: 0,
            updated_at: 0,
        }
    }

    fn message(role: Role, content: &str) -> Message {
        Message {
            id: Uuid::now_v7(),
            conversation_id: Uuid::now_v7(),
            role,
            content: content.to_owned(),
            created_at: 0,
        }
    }

    #[test]
    fn parses_block_and_keeps_only_prose() {
        let text = "雨水顺着屋檐落下。\n\n<state>\nset HP = 12\ndel 火把\nsetglobal 季节 = 初冬\n</state>\n";
        let parsed = parse(text);
        assert_eq!(parsed.cleaned, "雨水顺着屋檐落下。");
        assert_eq!(
            parsed.ops,
            vec![
                VarOp {
                    scope: Scope::Session,
                    kind: OpKind::Set,
                    key: "HP".to_owned(),
                    value: Some("12".to_owned()),
                },
                VarOp {
                    scope: Scope::Session,
                    kind: OpKind::Delete,
                    key: "火把".to_owned(),
                    value: None,
                },
                VarOp {
                    scope: Scope::Global,
                    kind: OpKind::Set,
                    key: "季节".to_owned(),
                    value: Some("初冬".to_owned()),
                },
            ]
        );
        assert!(parsed.warnings.is_empty(), "{:?}", parsed.warnings);
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
        let rows = vec![
            row(1, Scope::Global, OpKind::Set, "世界", Some("临安")),
            row(2, Scope::Global, OpKind::Set, "HP", Some("10")),
            row(3, Scope::Session, OpKind::Set, "HP", Some("3")),
            row(4, Scope::Session, OpKind::Delete, "世界", None),
        ];
        let view = VariableView::build(&rows);
        assert_eq!(view.global_values.get("HP").map(String::as_str), Some("10"));
        assert_eq!(view.effective.get("HP").map(String::as_str), Some("3"));
        assert!(!view.effective.contains_key("世界"), "会话删除要连全局一起遮掉");
        assert_eq!(view.session.len(), 2);
    }

    #[test]
    fn outgoing_strips_tags_and_injects_current_table() {
        let conversation = conversation("你是客栈老板。");
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
        let outgoing = build_outgoing(&conversation, &messages, &values);

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
        let conversation = conversation("你是助手。");
        let messages = vec![message(Role::User, "在吗")];
        let outgoing = build_outgoing(&conversation, &messages, &BTreeMap::new());
        assert_eq!(outgoing[0].content, "你是助手。");
        assert_eq!(outgoing.len(), 2);
    }

    #[test]
    fn outgoing_without_system_prompt_omits_the_system_message() {
        let conversation = conversation("   ");
        let messages = vec![message(Role::User, "在吗")];
        let outgoing = build_outgoing(&conversation, &messages, &BTreeMap::new());
        assert_eq!(outgoing.len(), 1);
        assert_eq!(outgoing[0].role, OutgoingRole::User);
    }
}
