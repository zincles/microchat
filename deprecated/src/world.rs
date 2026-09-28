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
//! HP = 12
//! delete(湿透的火把)
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
HP = 12
delete(湿透的火把)
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
            Self::Delete => "delete",
        }
    }

    pub fn parse(raw: &str) -> Option<Self> {
        match raw {
            "set" => Some(Self::Set),
            "delete" => Some(Self::Delete),
            _ => None,
        }
    }
}

/// 一条待落库的操作。
///
/// serde 形状与 Go 的 `statelang.Statement` **逐字一致**（接口要逐字节对得上）：
/// `{"kind":"set","table":"玩家状态","key":"AA","value":"123456"}` —— 表名为空串、值是 `None` 时省略字段。
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct WorldStateOp {
    pub kind: OpKind,
    /// 哪张表（`<state 玩家状态>` ⇒ `玩家状态`；未命名块 ⇒ 空串）。
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub table: String,
    pub key: String,
    /// `Set` 为 `Some`（可以是空串），`Delete` 为 `None`。
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub value: Option<String>,
}

/// `POST /api/v1/statelang` 的响应：**解析**与**计算**分开给（字段顺序即契约）。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct StatelangView {
    /// 计算结果：折叠之后的值（删除生效、后写覆盖前写、空表消失；未命名表用空串作键）。
    pub tables: Tables,
    /// 解析结果：读出来的操作，按出现顺序（删除还没生效）。
    pub statements: Vec<WorldStateOp>,
    /// 坏行与提醒，带行号。
    pub diagnostics: Vec<Diagnostic>,
}

/// 一条**现算出来的**操作。`seq` 只在一次重演内部有意义（消息顺序 → 块内顺序），
/// 库里不保存它——变量表是读的时候从正文推出来的，见 [`WorldStateView::from_messages`]。
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct WorldWorldStateOpRow {
    pub seq: i64,
    /// 哪张表（未命名块 ⇒ 空串）。
    #[serde(default)]
    pub table: String,
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
    pub ops: Vec<WorldStateOp>,
    /// 坏行与提醒：**带行号**（整段文本里的行号，1 起），供界面直接定位。
    /// 不是致命错误：解析继续，块照旧整块剔除。字段名与 Go 版一致（接口要逐字节对得上）。
    pub diagnostics: Vec<Diagnostic>,
}

/// 一条诊断：哪一行出了什么问题。`text` 是**原样**那一行（报错时别再改字）。
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct Diagnostic {
    pub line: i64,
    pub text: String,
    pub message: String,
}

impl Parsed {
    fn bad(&mut self, line: i64, text: &str, message: impl Into<String>) {
        self.diagnostics.push(Diagnostic {
            line,
            text: text.to_owned(),
            message: message.into(),
        });
    }
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
        // `open` 是 find_tag 认下的标签，必然带 '>'；拿不到就当地没有标签
        let Some(open_end) = tag_end(text, open) else {
            break;
        };
        cleaned.push_str(&text[cursor..open]);

        let table = table_name(text, open, open_end);
        if table.contains([' ', '\t', '\r', '\n', '/', '<', '>', ';', '=']) {
            parsed.bad(
                line_of(text, open),
                "",
                format!("表名里不该出现空白或 `/` `<` `>` `;` `=`（照收，但多半是笔误）：{table}"),
            );
        }
        let base_line = line_of(text, open_end);

        match find_tag(text, open_end, true) {
            Some(close) => {
                parse_block(&text[open_end..close], base_line, &table, &mut parsed);
                cursor = tag_end(text, close).unwrap_or(text.len());
            }
            None => {
                // 没闭合：把剩下的都当块内容——绝不能把它当正文发出去。
                parsed.bad(line_of(text, open), "", "状态块没有闭合，已按到文本结尾处理");
                parse_block(&text[open_end..], base_line, &table, &mut parsed);
                cursor = text.len();
                break;
            }
        }
    }

    cleaned.push_str(&text[cursor..]);
    parsed.cleaned = cleaned.trim_end().to_owned();
    parsed
}

/// `offset` 处在第几行（1 起）——诊断要指向用户看得见的那一行。
fn line_of(text: &str, offset: usize) -> i64 {
    let offset = offset.min(text.len());
    1 + text[..offset].matches('\n').count() as i64
}

/// 标签里的表名：`<state 玩家状态>` ⇒ `玩家状态`；`<state>` ⇒ 空串（未命名表）。
fn table_name(text: &str, open: usize, open_end: usize) -> String {
    let inner = text[open + 1..open_end - 1].trim();
    // 同上：多字节标签切不出来就当没有表名，绝不 panic
    let Some(rest) = inner.get(5..) else {
        return String::new();
    };
    rest.trim().to_owned() // 去掉 `state` 前缀（大小写已在 find_tag 里放行）
}

/// 标签名是不是我们的块标签 —— `state` 或 `state 表名`（大小写不敏感、允许多余空白）。
///
/// 闭合标签里写不写表名都认（`</state>` 与 `</state 玩家状态>` 等价）：模型爱写对称，
/// 不认的话整块会被当成"没闭合"，把后半段正文一起吞掉。
fn is_block_tag(raw: &str, closing: bool) -> bool {
    let mut name = raw.trim();
    if closing {
        let Some(rest) = name.strip_prefix('/') else {
            return false;
        };
        name = rest.trim();
    }
    // **按字符边界切**：`<状态>` 这种多字节标签会让 `name[..5]` 直接 panic
    // （这个文件早先就在这上面栽过一次）。`get` 切不出来就是"不是我们的标签"。
    let Some(head) = name.get(..5) else {
        return false;
    };
    if !head.eq_ignore_ascii_case("state") {
        return false;
    }
    let rest = &name[5..]; // head 已经取到边界 ⇒ 这里必然也在边界上
    rest.is_empty() || rest.starts_with([' ', '\t'])
}

/// 找下一个块标签（`closing=false` 找 `<state …>`，`closing=true` 找 `</state …>`）。
fn find_tag(text: &str, from: usize, closing: bool) -> Option<usize> {
    let mut cursor = from;
    while let Some(offset) = text[cursor..].find('<') {
        let start = cursor + offset;
        // 没有 '>' ⇒ 这不是标签（正文里的 `<` 多了去了），后面也不会再有完整标签
        let Some(end) = tag_end(text, start) else {
            return None;
        };
        if is_block_tag(&text[start + 1..end - 1], closing) {
            return Some(start);
        }
        // 不是标签 ⇒ **只往前挪一个字符**，别跳到这个 `>` 后面：
        // `a < b <state>…</state>` 里那个假标签的 `>` 可能站在真标签之后，
        // 一步跳过去就会**把真状态块漏掉**（而标签泄漏进上下文是最不能接受的）。
        cursor = start + 1;
    }
    None
}

/// 标签 `<…>` 的结束下标（含 `>`）；**没有 `>` 就返回 `None`** —— 那不是标签。
///
/// 早先这里在没找到 `>` 时返回 `text.len()`，于是调用方拿 `len - 1` 去切字符串：
/// 末字是多字节（中文标点必是）时就 **panic 在 char boundary 上**。正文里的 `<`
/// 太常见（`a<b`、`<-`、`伤害 < 10`），这条路径随时会被踩到。
fn tag_end(text: &str, start: usize) -> Option<usize> {
    text[start..]
        .find('>')
        .map(|offset| start + offset + 1)
}

/// 认 `delete(键)`（括号内外多几个空格无妨）。认不出返回 `None`。
fn delete_call(line: &str) -> Option<String> {
    let open = line.find('(')?;
    if !line[..open].trim().eq_ignore_ascii_case("delete") {
        return None;
    }
    let inner = line[open + 1..].strip_suffix(')')?.trim();
    Some(inner.to_owned())
}

/// 键的规矩（一条一个原因 —— 报出来的话要能直接看懂）。
fn check_key(key: &str) -> Result<(), String> {
    if key.is_empty() {
        return Err("键为空".to_owned());
    }
    if key.chars().count() > KEY_MAX_CHARS {
        return Err(format!("键超过 {KEY_MAX_CHARS} 字符"));
    }
    if key.chars().any(char::is_whitespace) {
        return Err("键不能含空白".to_owned());
    }
    if key.chars().any(|ch| matches!(ch, '/' | '<' | '>' | '=')) {
        return Err("键不能含 `/` `<` `>` `=`".to_owned());
    }
    Ok(())
}

fn parse_block(body: &str, base_line: i64, table: &str, parsed: &mut Parsed) {
    // `;` 当作换行（口径：一行一个变量，`A=1; B=2` 与分两行等价）。
    // 因此**值里不能出现 `;`** —— 与"值里不能有空格"是同一类规矩：宁可报出来，也别猜。
    for (index, raw) in body.replace(';', "\n").lines().enumerate() {
        let line_number = base_line + index as i64;
        let line = raw.trim();
        if line.is_empty() {
            continue;
        }
        if parsed.ops.len() >= OPS_PER_MESSAGE_MAX {
            parsed.bad(
                line_number,
                line,
                format!("操作数超过上限 {OPS_PER_MESSAGE_MAX}，其余已忽略"),
            );
            return;
        }

        // **删除**：`delete(键)` —— 唯一的形态（调用形态，与 `键 = 值` 并列，最像样板代码）。
        // 老写法（`del 键`、`del(A)`、`delete 键`）一律**报错**，不做兼容：
        // 与 `set` 同一条纪律 —— 语言只有一个规范形态。
        if let Some(key) = delete_call(line) {
            if let Err(reason) = check_key(&key) {
                parsed.bad(line_number, line, reason);
                continue;
            }
            parsed.ops.push(WorldStateOp {
                kind: OpKind::Delete,
                table: table.to_owned(),
                key,
                value: None,
            });
            continue;
        }

        // 赋值**只有一种形态**：`键 = 值`（`set` 已砍）。行首那个"词"到空白或 `(` 为止，
        // 好让 `del(A)` 这种没空格的写法也能落到专门那条报错上。
        let head = line
            .split(|ch: char| ch.is_whitespace() || ch == '(')
            .next()
            .unwrap_or(line);
        match head.to_ascii_lowercase().as_str() {
            "set" => {
                parsed.bad(line_number, line, "`set` 写法已砍掉，直接写 `键 = 值`");
                continue;
            }
            "del" | "delete" => {
                parsed.bad(
                    line_number,
                    line,
                    "删除要写成 `delete(键)`（`del 键` / `delete 键` 都不认了）",
                );
                continue;
            }
            // 全局变量不再从消息里写：它住手写的 config/（别静默丢掉用户写过的东西）。
            "setglobal" | "delglobal" => {
                parsed.bad(
                    line_number,
                    line,
                    format!("`{head}` 不从消息里写全局变量了，请挪到 config/ 的全局状态里"),
                );
                continue;
            }
            _ => {}
        }
        if !line.contains('=') {
            parsed.bad(line_number, line, format!("不认识的操作 `{head}`，已忽略"));
            continue;
        }

        // 只在**第一个** `=` 处切 —— 值里可以有 `=`
        let (key, raw_value) = line.split_once('=').expect("赋值必然带 `=`");
        let key = key.trim();
        let value = raw_value.trim().to_owned();

        if let Err(reason) = check_key(key) {
            parsed.bad(line_number, line, reason);
            continue;
        }
        // **一行只能有一个变量**：值里出现空白 ⇒ 多半是把两条写在一行了（`A=1 B=2`）。
        // 这里刻意**报错而不是静默删空格** —— 删空格会把 `A = 你好 世界` 悄悄改成 `你好世界`。
        if value.chars().any(char::is_whitespace) {
            parsed.bad(
                line_number,
                line,
                "值里不能有空格（一行只能有一个变量，多个变量请用 `;` 或换行分开）",
            );
            continue;
        }
        let chars = value.chars().count();
        if chars > VALUE_MAX_CHARS {
            parsed.bad(
                line_number,
                line,
                format!("值超过 {VALUE_MAX_CHARS} 字符（{chars}），已忽略：{key}"),
            );
            continue;
        }

        parsed.ops.push(WorldStateOp {
            kind: OpKind::Set,
            table: table.to_owned(),
            key: key.to_owned(),
            value: Some(value),
        });
    }
}

/// 折叠结果：**一层**的状态（键 → 值）。
///
/// **没有墓碑**（2026-09-28 用户定稿）：`delete` 就是把这个键从**本层**拿掉，不留痕迹。
/// 代价与用法见 `IMPORTANT_DISCUSSION.md` §38：删**提示词底子**里的键 = 它会从下面漏回来
/// （"删"只作用于自己那一层）⇒ 不想被删掉的键，别写进底子，写进第一条消息。
pub type Tables = BTreeMap<String, BTreeMap<String, String>>;

/// 从空开始按 `seq` 顺序执行操作（`rows` 必须已按 `seq` 升序）。**只折叠这一层**。
///
/// 按**表**分开折：不同表里的同名键互不影响。算完为空的表自动消失（§37）。
pub fn fold(rows: &[WorldWorldStateOpRow], scope: Scope) -> Tables {
    let mut tables = Tables::new();
    for row in rows.iter().filter(|row| row.scope == scope) {
        let table = tables.entry(row.table.clone()).or_default();
        match row.kind {
            OpKind::Set if !clears(row.value.as_deref()) => {
                table.insert(row.key.clone(), row.value.clone().unwrap_or_default());
            }
            // `delete(键)` 与"赋成空值"（`键 =`）效果相同（§37）：清掉这个键
            _ => {
                table.remove(&row.key);
            }
        }
    }
    tables.retain(|_, table| !table.is_empty());
    tables
}

/// 生效值 = 全局打底，本会话覆写，**按表各自合并**。
/// 本会话删掉的键会从全局漏回来 —— 这就是"无墓碑"的语义（§38）。
pub fn effective(global: &Tables, session: &Tables) -> Tables {
    let mut merged = global.clone();
    for (name, table) in session {
        let target = merged.entry(name.clone()).or_default();
        for (key, value) in table {
            target.insert(key.clone(), value.clone());
        }
    }
    merged.retain(|_, table| !table.is_empty());
    merged
}

/// 空值算不算"清掉"：`键 =` 与 `delete(键)` 效果相同（§37）—— 于是这门语言里**存不下空串**。
fn clears(value: Option<&str>) -> bool {
    value.is_none_or(str::is_empty)
}

/// 未命名表（老写法 `<state>…</state>` 的键都住这儿）—— 界面与老接口沿用它的扁平静态。
pub fn unnamed_table(tables: &Tables) -> BTreeMap<String, String> {
    tables.get("").cloned().unwrap_or_default()
}

/// 注入给模型的变量表：**按表分组**；未命名表不写表头（保持老样子）；空 ⇒ `None`。
pub fn render_table(tables: &Tables) -> Option<String> {
    if tables.is_empty() {
        return None;
    }
    let mut out = String::from("当前变量:");
    for (name, table) in tables {
        if table.is_empty() {
            continue;
        }
        if name.is_empty() {
            for (key, value) in table {
                out.push_str(&format!("\n  {key} = {value}"));
            }
            continue;
        }
        out.push_str(&format!("\n  〔{name}〕"));
        for (key, value) in table {
            out.push_str(&format!("\n    {key} = {value}"));
        }
    }
    Some(out)
}

/// **解析 + 计算**：一段文本 ⇒ `{表: {键: 值}}`。
///
/// 这就是 `POST /api/v1/statelang` 的主体，也是给外部工具的那个函数：
/// 删除生效、后写覆盖前写、空表消失。要"只解析不计算"就用 `parse(text).ops`。
pub fn tables_of(text: &str) -> Tables {
    let mut tables = Tables::new();
    for op in parse(text).ops {
        let table = tables.entry(op.table.clone()).or_default();
        match op.kind {
            OpKind::Set if !clears(op.value.as_deref()) => {
                table.insert(op.key.clone(), op.value.clone().unwrap_or_default());
            }
            // 空值 = 清掉（§37）。注意解析照实报告 set（值是空串），动手的是**计算**。
            _ => {
                table.remove(&op.key);
            }
        }
    }
    tables.retain(|_, table| !table.is_empty());
    tables
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct WorldStateView {
    /// 全局那几张（来自底子提示词/`config/`，所有会话共用）。
    pub global: Vec<WorldWorldStateOpRow>,
    /// 本会话正文里累积出来的操作（"这一局改了什么"）。
    pub session: Vec<WorldWorldStateOpRow>,
    /// 全局现值。
    pub global_values: BTreeMap<String, String>,
    /// 生效值（全局 + 本会话）—— **未命名表**那一层（老写法与老前端沿用它的扁平静态）。
    pub effective: BTreeMap<String, String>,
    /// **全部分层之后的表**（表名 → 键 → 值；未命名表用空串作键）。
    /// 新前端与外部工具用这个 —— `effective` 只是为了兼容老写法。
    #[serde(default)]
    pub tables: Tables,
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

impl WorldStateView {
    /// **现演一份快照**：库里只存正文与提示词，变量表是每次顺着它们重演出来的。
    ///
    /// 顺序（也就是 fold 的顺序）：
    /// 1. 生效的 system prompt 里的 `<state>` 块 —— 这是**底子**，和消息里同一套语法；
    /// 2. 然后是每条消息正文里的块，按消息顺序（`list_messages` 的 rowid 序）。
    ///
    /// ![来源] 底子来自 agent 的提示词时算全局（`Scope::Global`，别的会话共享），
    /// 来自会话自己的提示词时算本会话（`Scope::Session`）。
    /// **没有墓碑**（§38）：本会话删掉底子里的键，值会从底子漏回来 —— "删"只作用于自己那一层。
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

        let mut global_rows: Vec<WorldWorldStateOpRow> = Vec::new();
        let mut session_rows: Vec<WorldWorldStateOpRow> = Vec::new();

        // 1) 底子：系统提示词里的块（没有块就是空底子，很正常）。
        for op in parse(system_prompt).ops {
            let row = WorldWorldStateOpRow {
                seq: 0, // 下面按作用域重排
                table: op.table.clone(),
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
                session_rows.push(WorldWorldStateOpRow {
                    seq: 0,
                    table: op.table.clone(),
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
        let merged = effective(&global, &session);
        Self {
            global_values: unnamed_table(&global),
            effective: unnamed_table(&merged),
            tables: merged,
            global: global_rows,
            session: session_rows,
        }
    }
}

/// 把折叠结果里的墓碑滤掉，得到"现值"。

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
    tables: &Tables,
) -> Vec<Outgoing> {
    // 提示词里的 `<state>` 块和消息里一个待遇：**原样留在存档里，发出去时剔除**。
    let mut system = parse(system_prompt).cleaned.trim().to_owned();
    if let Some(table) = render_table(tables) {
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

    /// 正文里的孤立 `<` **不是标签**：不许 panic，也不许动它一个字。
    ///
    /// 曾经这里会 panic（`end - 1` 落在多字节字符中间）——中文正文里 `<` 太常见了。
    #[test]
    fn a_lone_angle_bracket_is_not_a_tag() {
        for text in [
            "他去买 3 < 5 的东西",
            "伤害 < 10 且 > 5",
            "他看了一眼 <- 那个箭头",
            "结尾就是一个小于号 <",
            "只有一个 <",
        ] {
            let parsed = parse(text);
            assert_eq!(parsed.cleaned, text.trim_end(), "{text:?} 应当原样留下");
            assert!(parsed.ops.is_empty(), "{text:?} 不该解析出操作");
        }
    }

    /// 真正的状态块照旧：块被剔除，操作照读。
    #[test]
    fn a_real_state_block_still_works_next_to_angle_brackets() {
        let parsed = parse("他买了 3 < 5 个苹果<state>苹果 = 3</state>然后走了");
        assert_eq!(parsed.cleaned, "他买了 3 < 5 个苹果然后走了");
        assert_eq!(parsed.ops.len(), 1);
        assert_eq!(parsed.ops[0].key, "苹果");
    }

    /// **会话提示词永不展开占位符**（§26）：那是内置 Agent 的待遇。
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
            msg("<state>HP = 1</state>"),
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
            msg("<state>HP = 12\n火把 = 1</state>我点亮了火把", 1000),
            msg("我继续往前走", 2000),
            msg("<state>delete(火把)</state>火把烧完了", 3000),
        ];
        // 底子在**系统提示词**里，和消息同一套语法；来自 agent ⇒ 算全局底子
        let system_prompt = "你是跑团主持人。\n<state>季节 = 初冬\nHP = 99</state>";

        let view = WorldStateView::from_sources(
            conversation,
            system_prompt,
            PromptSource::Agent,
            &messages,
        );
        assert_eq!(view.effective["HP"], "12", "本会话覆写底子里的同名键");
        assert_eq!(view.effective["季节"], "初冬", "底子还在");
        assert!(!view.effective.contains_key("火把"), "最后一句把它删了（底子里没有它 ⇒ 不会再出现）");
        assert_eq!(view.global_values.len(), 2);
        assert_eq!(view.global.len(), 2, "底子那两条算全局");
        assert_eq!(view.session.len(), 3, "三条操作都来自正文");
        assert!(view.global[0].message_id.is_none(), "底子不来自某条消息");
        assert_eq!(view.session[0].message_id, Some(messages[0].id), "能追到是哪句写的");
        assert_eq!(view.session[0].created_at, 1000);
        assert_eq!(view.session[2].kind, OpKind::Delete);

        // 回溯 = 只喂到第 N 条：这就是"从空 fold 到第 N 条"
        let rewind =
            WorldStateView::from_sources(conversation, system_prompt, PromptSource::Agent, &messages[..1]);
        assert_eq!(rewind.effective["火把"], "1", "第一句之后火把还在");
        assert_eq!(rewind.session.len(), 2);
    }

    /// 会话自己写了提示词（覆盖了 agent 的）：它的底子只属于这条会话，不外溢成全局。
    #[test]
    fn conversation_prompt_base_is_session_scoped() {
        let view = WorldStateView::from_sources(
            Uuid::now_v7(),
            "<state>地点 = 客栈</state>你是客栈老板。",
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

    fn row(seq: i64, scope: Scope, kind: OpKind, key: &str, value: Option<&str>) -> WorldWorldStateOpRow {
        WorldWorldStateOpRow {
            seq,
            table: String::new(),
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
        let text = "雨水顺着屋檐落下。\n\n<state>\nHP = 12\ndelete(火把)\n</state>\n";
        let parsed = parse(text);
        assert_eq!(parsed.cleaned, "雨水顺着屋檐落下。");
        assert_eq!(
            parsed.ops,
            vec![
                WorldStateOp {
                    kind: OpKind::Set,
                    table: String::new(),
                    key: "HP".to_owned(),
                    value: Some("12".to_owned()),
                },
                WorldStateOp {
                    kind: OpKind::Delete,
                    table: String::new(),
                    key: "火把".to_owned(),
                    value: None,
                },
            ]
        );
        assert!(parsed.diagnostics.is_empty(), "{:?}", parsed.diagnostics);
    }

    /// `setglobal` / `delglobal` 已废弃（全局状态住手写的 `config/`）。
    /// 老消息里残留的写法要**明确报出来**——不能静默丢掉用户写过的东西。
    #[test]
    fn retired_global_commands_warn_instead_of_writing() {
        let parsed = parse("<state>\nsetglobal 季节 = 初冬\ndelglobal 世界\n</state>");
        assert!(parsed.ops.is_empty(), "不该再产出操作：{:?}", parsed.ops);
        assert_eq!(parsed.diagnostics.len(), 2, "{:?}", parsed.diagnostics);
        assert!(
            parsed.diagnostics[0].message.contains("config/"),
            "要说清该搬去哪：{:?}",
            parsed.diagnostics
        );
    }

    /// 值里**可以有 `=`**（只在第一个 `=` 处切），但**不能有空格** ——
    /// 后者是"一行一个变量"那条规矩的代价（§36：宁可报出来，也不静默删空格）。
    #[test]
    fn value_may_contain_equals_but_not_spaces() {
        let parsed = parse("<state>\n状态 = 血量=50/100=危险\n</state>");
        assert_eq!(parsed.ops.len(), 1);
        assert_eq!(parsed.ops[0].key, "状态");
        assert_eq!(parsed.ops[0].value.as_deref(), Some("血量=50/100=危险"));
        assert!(parsed.diagnostics.is_empty(), "{:?}", parsed.diagnostics);

        // 带空格 ⇒ 按"一行一个变量"拒绝，并**原样保留**在告警里（没有静默改字）
        let spacey = parse("<state>\n状态 = 血量 50/100 = 危险\n</state>");
        assert!(spacey.ops.is_empty());
        assert!(
            spacey.diagnostics.iter().any(|d| d.message.contains("一行只能有一个变量")),
            "{:?}",
            spacey.diagnostics
        );
        assert!(
            spacey.diagnostics[0].text.contains("血量 50/100 = 危险"),
            "告警要原样带上那一行（在 text 里）：{:?}",
            spacey.diagnostics
        );
    }

    /// §36/§37 口径：裸赋值 `A = 1`、`delete(AA)`、`;` 当换行；`set`/`del` 已砍（会报错）。
    #[test]
    fn accepts_bare_assignment_delete_call_and_semicolons() {
        let parsed = parse("<state>\nAA = 123456\nB = 234\ndelete(AA)\nC = 3\ndelete(D)\nE=5; F=6\n</state>");
        let shape: Vec<(OpKind, &str, Option<&str>)> = parsed
            .ops
            .iter()
            .map(|op| (op.kind, op.key.as_str(), op.value.as_deref()))
            .collect();
        assert_eq!(
            shape,
            vec![
                (OpKind::Set, "AA", Some("123456")),
                (OpKind::Set, "B", Some("234")),
                (OpKind::Delete, "AA", None),
                (OpKind::Set, "C", Some("3")),
                (OpKind::Delete, "D", None),
                (OpKind::Set, "E", Some("5")),
                (OpKind::Set, "F", Some("6")),
            ],
            "告警：{:?}",
            parsed.diagnostics
        );
        assert!(parsed.diagnostics.is_empty(), "{:?}", parsed.diagnostics);
    }

    /// 一行塞两个变量 ⇒ **报出来**，不当成"值里有空格"收下。
    #[test]
    fn one_variable_per_line_is_enforced() {
        let parsed = parse("<state>\nA=1 B=2\n</state>");
        assert!(parsed.ops.is_empty(), "不能收下：{:?}", parsed.ops);
        assert_eq!(parsed.diagnostics.len(), 1, "{:?}", parsed.diagnostics);
        assert!(
            parsed.diagnostics[0].message.contains("一行只能有一个变量"),
            "{:?}",
            parsed.diagnostics
        );
    }

    /// 砍掉的写法要给**说得清**的报错（不是"键不能含空白"这种莫名其妙的话）。
    #[test]
    fn retired_forms_explain_themselves() {
        for line in ["set A = 1", "del A", "del(A)", "delete A"] {
            let parsed = parse(&format!("<state>\n{line}\n</state>"));
            assert!(parsed.ops.is_empty(), "{line} 不该收下：{:?}", parsed.ops);
            assert_eq!(parsed.diagnostics.len(), 1, "{line} 诊断 = {:?}", parsed.diagnostics);
            let message = &parsed.diagnostics[0].message;
            assert!(
                message.contains("砍掉") || message.contains("delete(键)"),
                "{line} 的报错说不清：{message}"
            );
        }
        // 括号内外留空格仍然认（同一个形态，不是第二种写法）
        let parsed = parse("<state>\ndelete ( 想法 )\n</state>");
        assert_eq!(parsed.ops.len(), 1, "{:?}", parsed.diagnostics);
        assert_eq!(parsed.ops[0].kind, OpKind::Delete);
        assert_eq!(parsed.ops[0].key, "想法");
    }

    /// **共享语料**：同一份 JSON，Go 侧（`src/internal/statelang`）也跑。
    ///
    /// 跨语言一致性靠它守着，而不是靠人眼比对两份实现 —— 改语法时两边一起红，才叫对账。
    #[test]
    fn shared_corpus_agrees_with_go() {
        let raw = include_str!("../../src/internal/statelang/testdata/cases.json");
        let data: serde_json::Value = serde_json::from_str(raw).expect("语料得是合法 JSON");
        let cases = data["cases"].as_array().expect("cases 得是数组");
        assert!(!cases.is_empty(), "语料是空的");
        for case in cases {
            let name = case["name"].as_str().unwrap();
            let input = case["input"].as_str().unwrap();
            let parsed = parse(input);
            assert_eq!(
                parsed.cleaned,
                case["cleaned"].as_str().unwrap(),
                "「{name}」cleaned 不一致"
            );
            let want = case["statements"].as_array().unwrap();
            assert_eq!(parsed.ops.len(), want.len(), "「{name}」语句条数不一致：{:?}", parsed.ops);
            for (index, expected) in want.iter().enumerate() {
                let op = &parsed.ops[index];
                assert_eq!(
                    op.kind.as_str(),
                    expected["kind"].as_str().unwrap(),
                    "「{name}」第 {index} 条 kind"
                );
                assert_eq!(op.key, expected["key"].as_str().unwrap(), "「{name}」第 {index} 条 key");
                assert_eq!(
                    op.value.as_deref(),
                    expected["value"].as_str(),
                    "「{name}」第 {index} 条 value"
                );
                assert_eq!(
                    op.table,
                    expected["table"].as_str().unwrap_or(""),
                    "「{name}」第 {index} 条 table"
                );
            }
            // 计算（折叠）的结果：删除生效、空表消失、未命名表用空串作键
            if let Some(expected) = case.get("tables") {
                let got = serde_json::to_value(tables_of(input)).unwrap();
                assert_eq!(&got, expected, "「{name}」tables 不一致");
            }
            assert_eq!(
                parsed.diagnostics.len() as i64,
                case["diagnostics"].as_i64().unwrap(),
                "「{name}」诊断条数不一致：{:?}",
                parsed.diagnostics
            );
        }
    }

    /// 命名表：同名键在不同表里互不影响；空表自动消失；删除只作用于自己那张表。
    #[test]
    fn tables_are_namespaced() {
        let tables = tables_of(
            "<state A>K = 1</state><state B>K = 2; delete(K)</state><state 临时>X = 1; delete(X)</state>",
        );
        assert_eq!(tables.len(), 1, "只剩 A：{tables:?}");
        assert_eq!(tables["A"]["K"], "1");
        assert!(!tables.contains_key("临时"), "算完为空的表要消失");
    }

    /// 注入给模型的表**按表分组**（未命名表不写表头）—— 模型每轮都看到正确的形状。
    #[test]
    fn render_table_groups_by_table() {
        let tables = Tables::from([
            (
                String::new(),
                BTreeMap::from([("HP".to_owned(), "10".to_owned())]),
            ),
            (
                "玩家状态".to_owned(),
                BTreeMap::from([("心情".to_owned(), "疲惫".to_owned())]),
            ),
        ]);
        let rendered = render_table(&tables).expect("非空");
        assert!(rendered.contains("  HP = 10"), "{rendered}");
        assert!(rendered.contains("〔玩家状态〕"), "{rendered}");
        assert!(rendered.contains("    心情 = 疲惫"), "{rendered}");
        assert!(!rendered.contains("〔〕"), "未命名表不该有表头：{rendered}");
    }

    /// 表名写坏：**照收**（不改归属）但报一条，别静默。
    #[test]
    fn bad_table_name_is_reported_but_kept() {
        let parsed = parse("<state 玩 家>HP = 1</state>");
        assert_eq!(parsed.ops.len(), 1);
        assert_eq!(parsed.ops[0].table, "玩 家");
        assert!(
            parsed.diagnostics.iter().any(|d| d.message.contains("表名")),
            "{:?}",
            parsed.diagnostics
        );
    }

    /// 多字节标签**绝不能让解析器 panic**（真栽过：`name[..5]` 切在字符中间）。
    #[test]
    fn multibyte_tags_do_not_panic() {
        for text in ["<状态>HP = 1</状态>", "正文<标签>x</标签>", "<statе>HP = 1</statе>"] {
            let parsed = parse(text);
            let _ = parsed.cleaned; // 只要不 panic
        }
    }

    /// 空值 = 清掉（§37）：**计算**动手，**解析**照实报告 —— 这条界线就是"解析 ≠ 计算"。
    #[test]
    fn empty_value_clears_the_key() {
        let parsed = parse("<state 玩家状态>想法 = ;HP = 10</state>");
        assert_eq!(parsed.ops.len(), 2);
        assert_eq!(parsed.ops[0].kind, OpKind::Set, "解析该如实报告 set");
        assert_eq!(parsed.ops[0].value.as_deref(), Some(""));

        let tables = tables_of("<state 玩家状态>想法 = ;HP = 10</state>");
        assert!(!tables["玩家状态"].contains_key("想法"), "{tables:?}");
        assert_eq!(tables["玩家状态"]["HP"], "10");

        assert!(tables_of("<state>X = </state>").is_empty(), "清空之后的表该消失");
        assert_eq!(tables_of("<state>HP = 1;HP = ;HP = 9</state>")[""]["HP"], "9");
    }

    #[test]
    fn bad_lines_warn_but_block_never_leaks() {
        let text = "正文\n<state>\n乱写一行\n缺等号\n坏/键 = 1\n长 = ";
        let long = "x".repeat(VALUE_MAX_CHARS + 1);
        let text = format!("{text}{long}\n</state>");
        let parsed = parse(&text);

        assert_eq!(parsed.cleaned, "正文", "块必须被整块剔除");
        for warning in [
            "不认识的操作",
            "键不能含",
            "值超过",
        ] {
            assert!(
                parsed.diagnostics.iter().any(|d| d.message.contains(warning)),
                "缺少告警 `{warning}`：{:?}",
                parsed.diagnostics
            );
        }
    }

    #[test]
    fn unclosed_block_is_swallowed_to_the_end() {
        let parsed = parse("先说话\n<state>\nHP = 1");
        assert_eq!(parsed.cleaned, "先说话");
        assert_eq!(parsed.ops.len(), 1);
        assert_eq!(parsed.diagnostics.len(), 1);
    }

    #[test]
    fn tag_lookup_tolerates_case_and_spaces() {
        // 标签大小写与多余空白宽容（语句本身不区分大小写，但 `set` 已经砍了）
        let parsed = parse("正文\n<State >\nHP = 3\n</STATE>");
        assert_eq!(parsed.cleaned, "正文");
        assert_eq!(parsed.ops[0].value.as_deref(), Some("3"));
    }

    #[test]
    fn multiple_blocks_accumulate() {
        let parsed = parse("A\n<state>\na = 1\n</state>\nB\n<state>\nb = 2\n</state>");
        assert_eq!(parsed.cleaned, "A\n\nB");
        assert_eq!(parsed.ops.len(), 2);
    }

    #[test]
    fn ops_are_capped_per_message() {
        let body: String = (0..OPS_PER_MESSAGE_MAX + 5)
            .map(|index| format!("k{index} = {index}\n"))
            .collect();
        let parsed = parse(&format!("<state>\n{body}</state>"));
        assert_eq!(parsed.ops.len(), OPS_PER_MESSAGE_MAX);
        assert!(parsed.diagnostics.iter().any(|d| d.message.contains("上限")));
    }

    #[test]
    fn fold_applies_in_order_and_delete_leaves_no_trace() {
        let rows = vec![
            row(1, Scope::Session, OpKind::Set, "HP", Some("1")),
            row(2, Scope::Session, OpKind::Set, "HP", Some("6")),
            row(3, Scope::Session, OpKind::Set, "位置", Some("客栈")),
            row(4, Scope::Session, OpKind::Delete, "位置", None),
        ];
        let tables = fold(&rows, Scope::Session);
        let table = tables.get("").expect("未命名表");
        assert_eq!(table.get("HP").map(String::as_str), Some("6"), "后写覆盖前写");
        assert_eq!(table.get("位置"), None, "删除就是直接删掉，不留墓碑");
        assert_eq!(table.len(), 1);
    }

    #[test]
    fn session_overrides_global_but_delete_cannot_erase_the_base() {
        let messages = vec![message(
            Role::User,
            "<state>HP = 3\ndelete(世界)</state>我把地图烧了",
        )];
        // 全局底子由 agent 的提示词提供（会话没有覆盖提示词时就是它）
        let view = WorldStateView::from_sources(
            Uuid::now_v7(),
            "<state>世界 = 临安\nHP = 10</state>你是说书人。",
            PromptSource::Agent,
            &messages,
        );

        assert_eq!(view.global_values.get("HP").map(String::as_str), Some("10"));
        assert_eq!(view.effective.get("HP").map(String::as_str), Some("3"));
        assert_eq!(
            view.effective["世界"], "临安",
            "**删只作用于本层**：底子里有同名键 ⇒ 值漏回来（§38 无墓碑的代价）"
        );
        assert_eq!(view.session.len(), 2);
    }

    #[test]
    fn outgoing_strips_tags_and_injects_current_table() {
        // 提示词里也能写 `<state>`（底子），发出去时同样要剔除
        let system_prompt = "你是客栈老板。\n<state>季节 = 初冬</state>";
        let messages = vec![
            message(Role::User, "我要住店"),
            message(
                Role::Assistant,
                "客官里面请。\n<state>\nHP = 12\n房号 = 天字三号\n</state>",
            ),
        ];
        let values = BTreeMap::from([
            ("HP".to_owned(), "12".to_owned()),
            ("房号".to_owned(), "天字三号".to_owned()),
        ]);
        let tables = Tables::from([(String::new(), values)]);
        let outgoing = build_outgoing(system_prompt, &messages, &[], &tables);

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
