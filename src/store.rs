//! SQLite 存储：会话与消息的唯一权威来源。
//!
//! 口径（与建模讨论一致）：
//! - id 一律 uuidv7，字符串落库，`ORDER BY id` 即时间序；
//! - 消息只追加；分支（`parent_id`）留待 Phase 3 加列 + 按主键顺序回填，历史无损；
//! - `insert_message` 在**单事务**内完成「更新会话活跃时间 + 插消息」，杜绝两步不一致；
//! - 迁移由 `PRAGMA user_version` 驱动，格式升级不丢数据。

use std::path::Path;

use std::collections::{BTreeMap, BTreeSet};

use rusqlite::{params, types::Type, Connection, OptionalExtension, Row};
use serde::{Deserialize, Serialize};
use uuid::Uuid;

use crate::model::{
    Conversation, DiscoveredModel, Message, ModelEntry, Role, Summary, SummarySourceKind, Usage,
    DEFAULT_AGENT_ID,
};

const MIGRATIONS: [&str; 11] = [r#"
CREATE TABLE conversations (
  id            TEXT PRIMARY KEY,
  title         TEXT NOT NULL DEFAULT '',
  system_prompt TEXT NOT NULL DEFAULT '',
  endpoint      TEXT NOT NULL,
  model         TEXT NOT NULL,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
);

CREATE TABLE messages (
  id              TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  role            TEXT NOT NULL CHECK (role IN ('user','assistant')),
  content         TEXT NOT NULL,
  created_at      INTEGER NOT NULL
);

CREATE INDEX messages_by_conv ON messages(conversation_id, id);
"#, r#"
-- 词汇与 providers.json 对齐：endpoint → provider。
ALTER TABLE conversations RENAME COLUMN endpoint TO provider;
-- agent 来源标记。
ALTER TABLE conversations ADD COLUMN agent_id TEXT NOT NULL DEFAULT 'default';

-- 发现所得与用户覆盖同表分列：刷新只写发现列，用户列永不被动。
-- provider 用 providers.json 里的 handle，无外键（配置在文件里，库不做约束）。
CREATE TABLE models (
  provider       TEXT NOT NULL,
  upstream_id    TEXT NOT NULL,
  upstream_name  TEXT,
  owned_by       TEXT,
  context_length INTEGER,
  max_output     INTEGER,
  display_name   TEXT,
  params         TEXT NOT NULL DEFAULT '{}',
  tokenizer      TEXT NOT NULL DEFAULT '{"kind":"approx","ratio":1.3}',
  first_seen_at  INTEGER NOT NULL,
  last_seen_at   INTEGER NOT NULL,
  PRIMARY KEY (provider, upstream_id)
);

-- provider 的刷新簿记。放库里而不是 providers.json：那个文件应用只读不写。
CREATE TABLE provider_state (
  provider        TEXT PRIMARY KEY,
  last_refresh_at INTEGER NOT NULL
);
"#, r#"
-- 上游声明的参数信息（supported/default），与用户覆盖的 params 分列。
ALTER TABLE models ADD COLUMN upstream_params TEXT NOT NULL DEFAULT '{}';
"#, r#"
-- 变量操作日志：只追加，永不回改。
-- 生效值 = 从空 fold 到此刻；回溯 = fold 到第 N 条（seq 就是 id）。
-- 删除写成墓碑行（op='del'），这样"没设置过"与"显式删掉"能区分开——
-- 否则本会话删掉的键会从全局底下漏回来。
CREATE TABLE variable_ops (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  scope           TEXT NOT NULL CHECK (scope IN ('session','global')),
  -- session 作用域必填；global 为 NULL（全局操作不属于任何会话）
  conversation_id TEXT REFERENCES conversations(id) ON DELETE CASCADE,
  -- 来源消息。手工注入/将来的通道可能为空。
  message_id      TEXT,
  op              TEXT NOT NULL CHECK (op IN ('set','del')),
  key             TEXT NOT NULL,
  value           TEXT,
  created_at      INTEGER NOT NULL
);

CREATE INDEX variable_ops_by_conv ON variable_ops(conversation_id, id);
"#, r#"
-- 变量操作日志整张表拆掉：它本来就是从消息正文推出来的东西。
-- 现在**库里只存正文**，变量表在读取时顺着消息重演（vars::VariableView::from_sources）；
-- 全局变量搬去 system prompt。`config/variables.json` 与 `setglobal` 都已废弃。
DROP TABLE IF EXISTS variable_ops;
"#, r#"
-- 消息从"一条线"变成一棵树：每条记下父亲（自引用、级联），会话记下"当前走到的尾巴"。
-- 顺序不再靠 rowid 的插入序，而是**从 current_leaf 沿 parent_id 回溯出来的当前路径**。
ALTER TABLE messages ADD COLUMN parent_id TEXT REFERENCES messages(id) ON DELETE CASCADE;
ALTER TABLE conversations ADD COLUMN current_leaf TEXT;
CREATE INDEX messages_by_parent ON messages(parent_id);

-- 老数据本来就串成一条链：按 rowid 补上父亲，再把每条链的尾巴设成 current_leaf。
UPDATE messages SET parent_id = (
    SELECT prev.id FROM messages prev
    WHERE prev.conversation_id = messages.conversation_id AND prev.rowid < messages.rowid
    ORDER BY prev.rowid DESC LIMIT 1
);
UPDATE conversations SET current_leaf = (
    SELECT m.id FROM messages m
    WHERE m.conversation_id = conversations.id ORDER BY m.rowid DESC LIMIT 1
);
"#, r#"
-- 推理型模型的"思考"（reasoning / reasoning_content）：**只留档**。
-- 它不进历史（不回喂给模型）、不扫 <state>、不可编辑；界面上默认折叠。
ALTER TABLE messages ADD COLUMN reasoning TEXT;
"#, r#"
-- 每条回复的"这一轮花了多久"与用量（上行/下行/缓存/思考）。用量是各家字段归一化之后
-- 的 JSON（只服务显示，不参与任何计算）；老消息与 dummy 都是 NULL。
ALTER TABLE messages ADD COLUMN duration_ms INTEGER;
ALTER TABLE messages ADD COLUMN usage TEXT;
"#, r#"
-- 思考用时（受理 → 第一段正文）。界面上显示成「思考（2.1s）」。
ALTER TABLE messages ADD COLUMN reasoning_ms INTEGER;
"#, r#"
-- 摘要把多个对话块里的消息收拢成一条叙事梗概（Compact）。
-- 它是**派生数据**：只往这张表插行，messages 的正文与树结构一个字节都不动。
-- 深度 = 指针链（messages.summary_id → parent_summary_id），**不存 level**；
-- 覆盖范围**不存**（成员靠 messages.summary_id 反查，范围靠位置得出；source_ids 只是审计副本）。
CREATE TABLE summaries (
  id                TEXT PRIMARY KEY,
  conversation_id   TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  -- 合并进了哪条更高层的摘要；NULL = 顶层。
  parent_summary_id TEXT REFERENCES summaries(id),
  -- 成员是消息还是摘要。**同质**：一条摘要的成员不许混。
  source_kind       TEXT NOT NULL CHECK (source_kind IN ('message','summary')),
  text              TEXT NOT NULL,
  -- 覆盖了几个**对话块**（显示 + "≥N 块"判定 + 日志）。块本身不入库。
  blocks            INTEGER NOT NULL DEFAULT 0,
  tokens            INTEGER NOT NULL DEFAULT 0,
  -- 当时到底吃的是什么（消息 id 或摘要 id 的 JSON 数组）：审计 + 整批重做。
  source_ids        TEXT NOT NULL DEFAULT '[]',
  provider          TEXT NOT NULL,
  model             TEXT NOT NULL,
  prompt_version    INTEGER NOT NULL,
  usage             TEXT,
  -- 被覆盖的消息被编辑过就置 1；界面显示"已过期 · 重新生成"，装配时照用不误。
  dirty             INTEGER NOT NULL DEFAULT 0,
  created_at        INTEGER NOT NULL
);

CREATE INDEX summaries_by_conv ON summaries(conversation_id, id);
CREATE INDEX summaries_by_parent ON summaries(parent_summary_id);

-- 消息指向收拢它的摘要（压缩**只写这一格**）。
-- 删摘要时指针自动断开；"孤立摘要"（反查不到成员）是**推导**出来的，不存列。
ALTER TABLE messages ADD COLUMN summary_id TEXT REFERENCES summaries(id) ON DELETE SET NULL;
CREATE INDEX messages_by_summary ON messages(summary_id);
"#, r#"
-- 用户为"这个模型能吃多少"填的覆盖值（token）。**用户列**：刷新只写发现列，永不碰它。
-- 优先顺序：context_override > context_length（发现） > config.json 的 chat.model_context_tokens（兜底）。
ALTER TABLE models ADD COLUMN context_override INTEGER;
"#];

#[derive(Debug)]
pub enum Error {
    Db(rusqlite::Error),
    NotFound,
    /// 调用方传入的数据不合法（例如 params 不是合法 JSON）。
    Invalid(String),
}

impl From<rusqlite::Error> for Error {
    fn from(e: rusqlite::Error) -> Self {
        Self::Db(e)
    }
}

impl std::fmt::Display for Error {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Db(e) => write!(f, "sqlite: {e}"),
            Self::NotFound => write!(f, "not found"),
            Self::Invalid(e) => write!(f, "invalid input: {e}"),
        }
    }
}

impl std::error::Error for Error {}

pub type Result<T, E = Error> = std::result::Result<T, E>;

/// 库内条数快照，给调试页用。
#[derive(Debug, Clone, Copy, Serialize, Deserialize)]
pub struct StoreStats {
    pub conversations: i64,
    pub messages: i64,
    pub models: i64,
}

pub struct Store {
    conn: Connection,
}

impl Store {
    /// 打开（或创建）文件库。父目录须已存在。
    pub fn open(path: &Path) -> Result<Self> {
        Self::init(Connection::open(path)?)
    }

    pub fn open_in_memory() -> Result<Self> {
        Self::init(Connection::open_in_memory()?)
    }

    fn init(mut conn: Connection) -> Result<Self> {
        conn.pragma_update(None, "foreign_keys", "ON")?;
        // journal_mode 会回一行结果，只能用查询式 PRAGMA 设置。
        let _: String = conn.query_row("PRAGMA journal_mode = WAL", [], |r| r.get(0))?;
        conn.pragma_update(None, "synchronous", "FULL")?; // 断电不丢，别改 NORMAL
        migrate(&mut conn)?;
        Ok(Self { conn })
    }

    pub fn create_conversation(
        &mut self,
        provider: &str,
        model: &str,
        system_prompt: &str,
    ) -> Result<Conversation> {
        let now = now_ms();
        let conv = Conversation {
            current_leaf: None,
            id: Uuid::now_v7(),
            title: String::new(),
            system_prompt: system_prompt.to_owned(),
            provider: provider.to_owned(),
            model: model.to_owned(),
            agent_id: DEFAULT_AGENT_ID.to_owned(),
            created_at: now,
            updated_at: now,
        };
        self.conn.execute(
            "INSERT INTO conversations
             (id, title, system_prompt, provider, model, agent_id, created_at, updated_at)
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)",
            params![
                conv.id.to_string(),
                conv.title,
                conv.system_prompt,
                conv.provider,
                conv.model,
                conv.agent_id,
                conv.created_at,
                conv.updated_at
            ],
        )?;
        Ok(conv)
    }

    /// 追加一条消息（id 现场生成）。接在 `parent_id` 下面（`None` = 这条会话的第一条），
    /// 并把会话的"当前尾巴"移到它身上——发送就是沿当前分支往前走一步。
    pub fn insert_message(
        &mut self,
        conversation_id: Uuid,
        role: Role,
        content: &str,
        parent_id: Option<Uuid>,
    ) -> Result<Message> {
        self.insert_message_with_id(
            conversation_id,
            Uuid::now_v7(),
            role,
            content,
            parent_id,
            None,
            None,
            None,
            None,
        )
    }

    /// 指定 id 追加，其余同 [`Self::insert_message`]。
    ///
    /// `reasoning` 是推理型模型的"思考"（**只留档**）；`duration_ms` / `usage` 是这一轮
    /// 的花销（整段生成花了多久、上行/下行/缓存各多少 tokens）——都只服务显示。
    ///
    /// 生成**回复**走这条：id 在受理那一刻就算好并回了客户端（前端拿它指认"正在生成的那条"），
    /// 落库时必须是同一个。库里只放**完整回复**——所以这次 INSERT 发生在整段拿到之后，
    /// 而不是开跑之前。
    pub fn insert_message_with_id(
        &mut self,
        conversation_id: Uuid,
        id: Uuid,
        role: Role,
        content: &str,
        parent_id: Option<Uuid>,
        reasoning: Option<&str>,
        reasoning_ms: Option<i64>,
        duration_ms: Option<i64>,
        usage: Option<&Usage>,
    ) -> Result<Message> {
        let now = now_ms();
        let msg = Message {
            id,
            conversation_id,
            role,
            content: content.to_owned(),
            reasoning: reasoning.map(str::to_owned),
            reasoning_ms,
            duration_ms,
            usage: usage.cloned(),
            parent_id,
            summary_id: None,
            created_at: now,
        };

        let tx = self.conn.transaction()?;
        let touched = tx.execute(
            "UPDATE conversations SET updated_at = ?1 WHERE id = ?2",
            params![now, conversation_id.to_string()],
        )?;
        if touched == 0 {
            return Err(Error::NotFound);
        }
        tx.execute(
            "INSERT INTO messages
               (id, conversation_id, role, content, parent_id, created_at, reasoning, reasoning_ms, duration_ms, usage)
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10)",
            params![
                msg.id.to_string(),
                msg.conversation_id.to_string(),
                msg.role.as_str(),
                msg.content,
                msg.parent_id.map(|id| id.to_string()),
                msg.created_at,
                msg.reasoning.as_deref(),
                msg.reasoning_ms,
                msg.duration_ms,
                msg.usage
                    .as_ref()
                    .and_then(|usage| serde_json::to_string(usage).ok()),
            ],
        )?;
        // 新条就是新的尾巴：当前路径到此为止
        tx.execute(
            "UPDATE conversations SET current_leaf = ?1 WHERE id = ?2",
            params![msg.id.to_string(), conversation_id.to_string()],
        )?;
        tx.commit()?;
        Ok(msg)
    }

    pub fn get_conversation(&self, conversation_id: Uuid) -> Result<Option<Conversation>> {
        self.conn
            .query_row(
                "SELECT id, title, system_prompt, provider, model, agent_id, current_leaf,
                        created_at, updated_at
                 FROM conversations WHERE id = ?1",
                params![conversation_id.to_string()],
                row_to_conversation,
            )
            .optional()
            .map_err(Into::into)
    }

    pub fn update_title(&mut self, conversation_id: Uuid, title: &str) -> Result<()> {
        let touched = self.conn.execute(
            "UPDATE conversations SET title = ?1 WHERE id = ?2",
            params![title, conversation_id.to_string()],
        )?;
        (touched > 0).then_some(()).ok_or(Error::NotFound)
    }

    pub fn delete_conversation(&mut self, conversation_id: Uuid) -> Result<()> {
        let touched = self.conn.execute(
            "DELETE FROM conversations WHERE id = ?1",
            params![conversation_id.to_string()],
        )?;
        (touched > 0).then_some(()).ok_or(Error::NotFound)
    }

    /// 会话列表，最近活跃在前（侧栏排序口径）。
    pub fn list_conversations(&self) -> Result<Vec<Conversation>> {
        let mut stmt = self.conn.prepare(
            "SELECT id, title, system_prompt, provider, model, agent_id, current_leaf,
                    created_at, updated_at
             FROM conversations ORDER BY updated_at DESC, id DESC",
        )?;
        let rows = stmt.query_map([], row_to_conversation)?;
        Ok(rows.filter_map(std::result::Result::ok).collect())
    }

    /// 按插入序返回消息（分支上线前的活跃路径就是全表）。
    /// 用 `rowid` 而不是 `id`：uuidv7 在同一毫秒内的顺序由随机位决定，不足以表达
    /// "谁先发的"；SQLite 的 rowid 是严格递增的插入序，毫秒内也精确。
    /// 会话不存在 → [`Error::NotFound`]，避免「空列表」把未知会话伪装成空会话。
    /// 会话的**当前路径**：从 `current_leaf` 沿 `parent_id` 回溯到根，再正过来。
    ///
    /// 顺序的来源就是这棵树（不再是 rowid 的插入序）——换分支 = 换一条路径。
    pub fn list_messages(&self, conversation_id: Uuid) -> Result<Vec<Message>> {
        let conversation = self
            .get_conversation(conversation_id)?
            .ok_or(Error::NotFound)?;
        let all = self.list_all_messages(conversation_id)?;
        Ok(path_from(&all, conversation.current_leaf))
    }

    /// 该会话**全部**消息（含各条分支），按 rowid = 生成先后排。
    pub fn list_all_messages(&self, conversation_id: Uuid) -> Result<Vec<Message>> {
        if self.get_conversation(conversation_id)?.is_none() {
            return Err(Error::NotFound);
        }
        let mut stmt = self.conn.prepare(
            "SELECT id, conversation_id, role, content, parent_id, created_at, reasoning, reasoning_ms, duration_ms, usage, summary_id
             FROM messages WHERE conversation_id = ?1 ORDER BY rowid",
        )?;
        let rows = stmt.query_map(params![conversation_id.to_string()], row_to_message)?;
        Ok(rows.filter_map(std::result::Result::ok).collect())
    }

    /// 插一条摘要行（压缩的**写动作之一**；单独用一般只为测试，正式路径走 [`Self::record_summary`]）。
    pub fn insert_summary(&mut self, summary: &Summary) -> Result<()> {
        insert_summary_in(&self.conn, summary)
    }

    /// 压缩的落库动作：**同一个事务**里「插摘要行 + 把成员消息指向它」。
    ///
    /// 顺序是硬条件（§5）：反过来一旦总结失败就白丢数据。**不做剪枝** —— 那是另一个动作，
    /// 而且它必须先导出（§23.G）。成员必须属于这条会话，否则整批拒绝 —— 与"编辑消息不能跨会话"
    /// 同一条规矩，绝不跨会话改。
    pub fn record_summary(&mut self, summary: &Summary, message_ids: &[Uuid]) -> Result<()> {
        let tx = self.conn.transaction()?;
        insert_summary_in(&tx, summary)?;
        for message_id in message_ids {
            let touched = tx.execute(
                "UPDATE messages SET summary_id = ?1 WHERE id = ?2 AND conversation_id = ?3",
                params![
                    summary.id.to_string(),
                    message_id.to_string(),
                    summary.conversation_id.to_string()
                ],
            )?;
            if touched == 0 {
                return Err(Error::Invalid(format!(
                    "消息 {message_id} 不属于会话 {}",
                    summary.conversation_id
                )));
            }
        }
        tx.commit()?;
        Ok(())
    }

    /// 这条会话的全部摘要，按生成先后（id 是 UUIDv7 ⇒ 时间序）。
    pub fn list_summaries(&self, conversation_id: Uuid) -> Result<Vec<Summary>> {
        let mut stmt = self.conn.prepare(
            "SELECT id, conversation_id, parent_summary_id, source_kind, text, blocks, tokens,
                    source_ids, provider, model, prompt_version, usage, dirty, created_at
             FROM summaries WHERE conversation_id = ?1 ORDER BY id",
        )?;
        let rows = stmt.query_map(params![conversation_id.to_string()], row_to_summary)?;
        Ok(rows.filter_map(std::result::Result::ok).collect())
    }

    /// 每条消息"在同龄兄弟里排第几、一共几条"，给界面上「< 2/3 >」用。
    pub fn branch_info(&self, conversation_id: Uuid) -> Result<BTreeMap<Uuid, BranchInfo>> {
        let all = self.list_all_messages(conversation_id)?;
        let mut groups: BTreeMap<Option<Uuid>, Vec<Uuid>> = BTreeMap::new();
        for message in &all {
            groups.entry(message.parent_id).or_default().push(message.id);
        }
        let mut info = BTreeMap::new();
        for siblings in groups.values() {
            let total = siblings.len();
            for (index, id) in siblings.iter().enumerate() {
                info.insert(
                    *id,
                    BranchInfo {
                        index: index + 1,
                        total,
                        siblings: siblings.clone(),
                    },
                );
            }
        }
        Ok(info)
    }

    /// 切分支：**只允许切到当前尾巴的兄弟**（同一上文下的另一条候选）——也就是"这几条回复
    /// 我选哪一条"，路径长度不会缩水。
    ///
    /// 别放行"切到任意旧消息"：那是把整条对话倒回去（后面的话都从视野里消失），
    /// 而变量表是沿当前路径现演的 ⇒ **世界状态也跟着倒退**。那种事必须是一个明确的
    /// "从这里重新开始"，不该藏在一个分支切换的小箭头里。
    pub fn switch_leaf_to_sibling(&mut self, conversation_id: Uuid, message_id: Uuid) -> Result<()> {
        let all = self.list_all_messages(conversation_id)?;
        let current = self
            .get_conversation(conversation_id)?
            .and_then(|conversation| conversation.current_leaf);
        let parent_of = |id: Uuid| {
            all.iter()
                .find(|message| message.id == id)
                .and_then(|message| message.parent_id)
        };
        let Some(current) = current else {
            return Err(Error::Invalid("这条会话还没有对话，谈不上切分支".to_owned()));
        };
        let target = all
            .iter()
            .find(|message| message.id == message_id)
            .ok_or(Error::NotFound)?;
        if target.parent_id != parent_of(current) {
            return Err(Error::Invalid(
                "只能在最新那句上切换分支（切到它的兄弟）".to_owned(),
            ));
        }
        self.set_current_leaf_deep(conversation_id, message_id)
    }

    /// 把"当前尾巴"切到某条消息**所在分支的末端**：先落到它，再顺着最新的孩子一路往下。
    ///
    /// 界面上的「‹ 2/3 ›」要的就是这个——点到某一条回复，就该看到那条分支接下来的全部，
    /// 而不是停在半路。
    pub fn set_current_leaf_deep(&mut self, conversation_id: Uuid, message_id: Uuid) -> Result<()> {
        let all = self.list_all_messages(conversation_id)?;
        let mut cursor = message_id;
        loop {
            let next = all
                .iter()
                .filter(|message| message.parent_id == Some(cursor))
                .next_back()
                .map(|message| message.id);
            match next {
                Some(id) if id != message_id => cursor = id,
                _ => break,
            }
        }
        self.set_current_leaf(conversation_id, cursor)
    }

    /// 把"当前尾巴"切到某条消息上（换分支），**就停在那儿**。那条必须属于这个会话。
    pub fn set_current_leaf(&mut self, conversation_id: Uuid, message_id: Uuid) -> Result<()> {
        let belongs = self
            .list_all_messages(conversation_id)?
            .iter()
            .any(|message| message.id == message_id);
        if !belongs {
            return Err(Error::NotFound);
        }
        self.conn.execute(
            "UPDATE conversations SET current_leaf = ?1 WHERE id = ?2",
            params![message_id.to_string(), conversation_id.to_string()],
        )?;
        Ok(())
    }

    /// 编辑一条消息的正文，返回更新后的消息。
    ///
    /// 变量表不落库（它由正文现演出来），所以这里**不需要**重算什么：
    /// 那句里的 `<state>` 块改了，下次 [`crate::vars::VariableView::from_messages`] 自然不一样。
    pub fn update_message(
        &mut self,
        conversation_id: Uuid,
        message_id: Uuid,
        content: &str,
    ) -> Result<Message> {
        let now = now_ms();
        let conversation = conversation_id.to_string();
        let message = message_id.to_string();
        let tx = self.conn.transaction()?;

        let touched = tx.execute(
            "UPDATE messages SET content = ?1 WHERE id = ?2 AND conversation_id = ?3",
            params![content, message, conversation],
        )?;
        if touched == 0 {
            return Err(Error::NotFound);
        }
        let updated = tx.query_row(
            "SELECT id, conversation_id, role, content, parent_id, created_at, reasoning, reasoning_ms, duration_ms, usage, summary_id FROM messages WHERE id = ?1",
            params![message],
            row_to_message,
        )?;
        tx.execute(
            "UPDATE conversations SET updated_at = ?1 WHERE id = ?2",
            params![now, conversation],
        )?;
        tx.commit()?;
        Ok(updated)
    }

    /// 删掉一条消息**连它整棵子树**（调试期的唯一删除语义：树是级联的）。
    ///
    /// 返回删了几条。`current_leaf` 若在被删的子树里，就退到被删那条的**父亲**上
    /// （父亲一定还在——它不在子树里）。
    pub fn delete_message(&mut self, conversation_id: Uuid, message_id: Uuid) -> Result<usize> {
        let all = self.list_all_messages(conversation_id)?;
        let parent = all
            .iter()
            .find(|message| message.id == message_id)
            .ok_or(Error::NotFound)?
            .parent_id;
        self.delete_subtrees(conversation_id, &[message_id], parent)
    }

    /// 删掉某条消息的**所有兄弟**（连同各自的子树）——"这一组我全不要了"。
    ///
    /// 通常是站在某条回复上点「删除全部」：几个候选回复一次清光，只留下那句问话，
    /// 接着就能重新生成。删完 leaf 自然退到它们的父亲（= 那句用户消息）上。
    pub fn delete_siblings(&mut self, conversation_id: Uuid, message_id: Uuid) -> Result<usize> {
        let all = self.list_all_messages(conversation_id)?;
        let parent = all
            .iter()
            .find(|message| message.id == message_id)
            .ok_or(Error::NotFound)?
            .parent_id;
        let siblings: Vec<Uuid> = all
            .iter()
            .filter(|message| message.parent_id == parent)
            .map(|message| message.id)
            .collect();
        self.delete_subtrees(conversation_id, &siblings, parent)
    }

    /// 删掉一批消息**连各自整棵子树**（树上的删除只允许这种），并按规则修 `current_leaf`。
    /// 返回一共删了几条。`parent` 是这批量共同的上文，用来决定 leaf 退到哪儿。
    fn delete_subtrees(
        &mut self,
        conversation_id: Uuid,
        roots: &[Uuid],
        parent: Option<Uuid>,
    ) -> Result<usize> {
        let all = self.list_all_messages(conversation_id)?;
        if roots.is_empty() {
            return Ok(0);
        }

        // 子树 = 根们 + 所有后代。`all` 按 rowid（父亲一定排在孩子前面）走一遍就够。
        let mut doomed = std::collections::BTreeSet::new();
        for root in roots {
            if !all.iter().any(|message| message.id == *root) {
                return Err(Error::NotFound);
            }
            doomed.insert(*root);
        }
        for message in &all {
            if message.parent_id.is_some_and(|parent| doomed.contains(&parent)) {
                doomed.insert(message.id);
            }
        }
        let count = doomed.len();

        let leaf = self
            .get_conversation(conversation_id)?
            .and_then(|conversation| conversation.current_leaf);
        let now = now_ms();
        let tx = self.conn.transaction()?;
        for id in &doomed {
            tx.execute(
                "DELETE FROM messages WHERE id = ?1 AND conversation_id = ?2",
                params![id.to_string(), conversation_id.to_string()],
            )?;
        }
        if leaf.is_some_and(|leaf| doomed.contains(&leaf)) {
            // 退到哪里：优先"上文下**还活着的最新一个孩子**"——删单条时就是它的上一条兄弟。
            // 只退到父亲是不够的：那样界面上会看到"整条分支都没了"（其实兄弟都在树上），
            // 看起来就像一次删除把好几个分支一起删了。实在没有孩子可退，才退到上文本身。
            let fallback = all
                .iter()
                .filter(|message| message.parent_id == parent && !doomed.contains(&message.id))
                .next_back()
                .map(|message| message.id)
                .or(parent);
            tx.execute(
                "UPDATE conversations SET current_leaf = ?1 WHERE id = ?2",
                params![fallback.map(|id| id.to_string()), conversation_id.to_string()],
            )?;
        }
        tx.execute(
            "UPDATE conversations SET updated_at = ?1 WHERE id = ?2",
            params![now, conversation_id.to_string()],
        )?;
        tx.commit()?;
        Ok(count)
    }

    /// 把引用某个 agent 的会话改指到另一个 id（**agent 重命名**时用）。
    ///
    /// `conversations.agent_id` 是软引用（没有外键）：agent 住在 `agents.json` 里，
    /// 库里管不着。所以重命名必须由 API 一处负责把引用搬过去，否则会话会指向一个
    /// 不存在的 agent——那不会报错，只会**静默**丢掉提示词与变量底子。
    /// 返回搬了多少条会话。
    pub fn rename_agent_references(&mut self, old: &str, new: &str) -> Result<usize> {
        let changed = self.conn.execute(
            "UPDATE conversations SET agent_id = ?1 WHERE agent_id = ?2",
            params![new, old],
        )?;
        Ok(changed)
    }

    pub fn set_conversation_agent(&mut self, conversation_id: Uuid, agent_id: &str) -> Result<()> {
        let touched = self.conn.execute(
            "UPDATE conversations SET agent_id = ?1 WHERE id = ?2",
            params![agent_id, conversation_id.to_string()],
        )?;
        (touched > 0).then_some(()).ok_or(Error::NotFound)
    }

    /// 换模型：provider 与 model 一起写（模型是 provider 下的 id）。
    pub fn set_conversation_model(
        &mut self,
        conversation_id: Uuid,
        provider: &str,
        model: &str,
    ) -> Result<()> {
        let touched = self.conn.execute(
            "UPDATE conversations SET provider = ?1, model = ?2 WHERE id = ?3",
            params![provider, model, conversation_id.to_string()],
        )?;
        (touched > 0).then_some(()).ok_or(Error::NotFound)
    }

    /// 用一次发现结果**同步**该 provider 的模型表：
    /// - 见到的：upsert（发现列更新；用户列 `display_name` / `params` / `tokenizer` 永不被动）；
    /// - 没见到的：**删除**——列表始终等于上游当前给的那一份。
    ///
    /// 全程单事务：失败不会留下半份列表。刷新时间在同一 provider 上严格递增，
    /// 两次刷新落进同一毫秒也能分辨（`last_seen_at` 因此保持可读）。
    pub fn apply_discovery(&mut self, provider: &str, discovered: &[DiscoveredModel]) -> Result<()> {
        let previous = self.last_refresh_at(provider)?;
        let now = now_ms().max(previous.map_or(0, |t| t + 1));
        let tx = self.conn.transaction()?;

        // 用临时表记住"这次见到了谁"：避免 `NOT IN (...)` 撞上 SQLite 的参数个数上限。
        tx.execute_batch(
            "CREATE TEMP TABLE IF NOT EXISTS seen_models (upstream_id TEXT PRIMARY KEY);
             DELETE FROM seen_models;",
        )?;

        for model in discovered {
            tx.execute(
                "INSERT OR IGNORE INTO seen_models (upstream_id) VALUES (?1)",
                params![model.upstream_id],
            )?;
            tx.execute(
                "INSERT INTO models
                 (provider, upstream_id, upstream_name, owned_by, context_length, max_output,
                  upstream_params, first_seen_at, last_seen_at)
                 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?8)
                 ON CONFLICT (provider, upstream_id) DO UPDATE SET
                   upstream_name  = COALESCE(excluded.upstream_name, models.upstream_name),
                   owned_by       = COALESCE(excluded.owned_by, models.owned_by),
                   context_length = COALESCE(excluded.context_length, models.context_length),
                   max_output     = COALESCE(excluded.max_output, models.max_output),
                   -- 上游这次没给参数情报时保留旧的，别把已知信息抹掉
                   upstream_params = CASE
                     WHEN excluded.upstream_params = '{}' THEN models.upstream_params
                     ELSE excluded.upstream_params END,
                   last_seen_at   = excluded.last_seen_at",
                params![
                    provider,
                    model.upstream_id,
                    model.upstream_name,
                    model.owned_by,
                    model.context_length,
                    model.max_output,
                    upstream_params_json(
                        &model.supported_parameters,
                        model.default_parameters.as_ref()
                    ),
                    now
                ],
            )?;
        }

        // 上游这次没给的模型一律删除：列表与上游保持一致。
        tx.execute(
            "DELETE FROM models
             WHERE provider = ?1 AND upstream_id NOT IN (SELECT upstream_id FROM seen_models)",
            params![provider],
        )?;

        tx.execute(
            "INSERT INTO provider_state (provider, last_refresh_at) VALUES (?1, ?2)
             ON CONFLICT (provider) DO UPDATE SET last_refresh_at = excluded.last_refresh_at",
            params![provider, now],
        )?;
        tx.commit()?;
        Ok(())
    }

    pub fn list_models(&self, provider: &str) -> Result<Vec<ModelEntry>> {
        let mut stmt = self.conn.prepare(
            "SELECT provider, upstream_id, upstream_name, owned_by, context_length, max_output,
                    display_name, params, tokenizer, upstream_params, first_seen_at, last_seen_at,
                    context_override
             FROM models WHERE provider = ?1 ORDER BY upstream_id",
        )?;
        let rows = stmt.query_map(params![provider], row_to_model_entry)?;
        Ok(rows.filter_map(std::result::Result::ok).collect())
    }

    pub fn get_model(&self, provider: &str, upstream_id: &str) -> Result<Option<ModelEntry>> {
        self.conn
            .query_row(
                "SELECT provider, upstream_id, upstream_name, owned_by, context_length, max_output,
                        display_name, params, tokenizer, upstream_params, first_seen_at, last_seen_at,
                        context_override
                 FROM models WHERE provider = ?1 AND upstream_id = ?2",
                params![provider, upstream_id],
                row_to_model_entry,
            )
            .optional()
            .map_err(Into::into)
    }

    /// 复制一条会话（Fork）：**一个事务**里复制「当前路径 + 尾巴那一层的兄弟（含各自子树）」
    /// 与摘要行，并把指针按 `旧 id → 新 id` 改写。
    ///
    /// - 消息：`content` / `reasoning` / `usage` 一字不改，**`created_at` 保留原值**；
    /// - 摘要：行照抄、指针改写 —— 副本**不用重新总结**（纯指针模型的红利）；
    /// - **变量不复制**：它从正文现演，副本的世界状态与原件逐键相同（有测试守着）。
    ///
    /// 尾巴那一层兄弟**连子树一起**复制：那是"重摇/退货"的现场，不带走的话副本就少了一条路。
    pub fn fork_conversation(&mut self, source_id: Uuid) -> Result<Conversation> {
        let source = self
            .get_conversation(source_id)?
            .ok_or(Error::NotFound)?;
        let all = self.list_all_messages(source_id)?;
        let path = path_from(&all, source.current_leaf);

        let mut wanted: BTreeSet<Uuid> = path.iter().map(|message| message.id).collect();
        if let Some(tail) = path.last() {
            for sibling in all
                .iter()
                .filter(|message| message.parent_id == tail.parent_id)
            {
                wanted.extend(subtree_ids(&all, sibling.id));
            }
        }

        let summaries = self.list_summaries(source_id)?;
        let now = now_ms();
        let new_id = Uuid::now_v7();
        let tx = self.conn.transaction()?;

        tx.execute(
            "INSERT INTO conversations
               (id, title, system_prompt, provider, model, agent_id, current_leaf, created_at, updated_at)
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, NULL, ?7, ?7)",
            params![
                new_id.to_string(),
                format!("{}（副本）", source.title),
                source.system_prompt,
                source.provider,
                source.model,
                source.agent_id,
                now
            ],
        )?;

        // 旧 id → 新 id（只给要复制的那些）
        let mut message_map: BTreeMap<Uuid, Uuid> = BTreeMap::new();
        for message in &all {
            if wanted.contains(&message.id) {
                message_map.insert(message.id, Uuid::now_v7());
            }
        }
        let mut summary_map: BTreeMap<Uuid, Uuid> = BTreeMap::new();
        for summary in &summaries {
            summary_map.insert(summary.id, Uuid::now_v7());
        }

        for message in &all {
            let Some(&fresh) = message_map.get(&message.id) else {
                continue;
            };
            tx.execute(
                "INSERT INTO messages
                   (id, conversation_id, role, content, parent_id, created_at,
                    reasoning, reasoning_ms, duration_ms, usage, summary_id)
                 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11)",
                params![
                    fresh.to_string(),
                    new_id.to_string(),
                    message.role.as_str(),
                    message.content,
                    message
                        .parent_id
                        .and_then(|id| message_map.get(&id))
                        .map(|id| id.to_string()),
                    message.created_at,
                    message.reasoning,
                    message.reasoning_ms,
                    message.duration_ms,
                    message
                        .usage
                        .as_ref()
                        .and_then(|usage| serde_json::to_string(usage).ok()),
                    message
                        .summary_id
                        .and_then(|id| summary_map.get(&id))
                        .map(|id| id.to_string()),
                ],
            )?;
        }

        for summary in &summaries {
            let copy = Summary {
                id: summary_map[&summary.id],
                conversation_id: new_id,
                parent_summary_id: summary
                    .parent_summary_id
                    .and_then(|id| summary_map.get(&id).copied()),
                source_kind: summary.source_kind,
                text: summary.text.clone(),
                blocks: summary.blocks,
                tokens: summary.tokens,
                source_ids: summary
                    .source_ids
                    .iter()
                    .filter_map(|id| message_map.get(id).copied())
                    .collect(),
                provider: summary.provider.clone(),
                model: summary.model.clone(),
                prompt_version: summary.prompt_version,
                usage: summary.usage.clone(),
                dirty: summary.dirty,
                created_at: summary.created_at,
            };
            insert_summary_in(&tx, &copy)?;
        }

        let leaf = source
            .current_leaf
            .and_then(|id| message_map.get(&id))
            .map(|id| id.to_string());
        tx.execute(
            "UPDATE conversations SET current_leaf = ?1 WHERE id = ?2",
            params![leaf, new_id.to_string()],
        )?;
        tx.commit()?;

        self.get_conversation(new_id)?.ok_or(Error::NotFound)
    }

    /// 忘掉一个 provider 派生出来的全部数据：已发现模型（连用户改的显示名/参数一起）
    /// 与刷新时间戳。历史会话里仍留着它的名字——那由 `chat::Backend::select` 兜底成
    /// fallback 话术，不是错误。返回清掉的模型条数。
    pub fn forget_provider(&mut self, provider: &str) -> Result<usize> {
        let tx = self.conn.transaction()?;
        let removed = tx.execute("DELETE FROM models WHERE provider = ?1", params![provider])?;
        tx.execute("DELETE FROM provider_state WHERE provider = ?1", params![provider])?;
        tx.commit()?;
        Ok(removed)
    }

    pub fn set_model_display_name(
        &mut self,
        provider: &str,
        upstream_id: &str,
        name: Option<&str>,
    ) -> Result<()> {
        let touched = self.conn.execute(
            "UPDATE models SET display_name = ?1 WHERE provider = ?2 AND upstream_id = ?3",
            params![name, provider, upstream_id],
        )?;
        (touched > 0).then_some(()).ok_or(Error::NotFound)
    }

    /// 设置/清除**用户覆盖的"模型上下文"**（`None` = 清掉，回落到发现值与全局兜底）。
    pub fn set_model_context_override(
        &mut self,
        provider: &str,
        upstream_id: &str,
        context: Option<i64>,
    ) -> Result<()> {
        if let Some(value) = context {
            if value <= 0 {
                return Err(Error::Invalid("模型上下文要正数".to_owned()));
            }
        }
        let touched = self.conn.execute(
            "UPDATE models SET context_override = ?1 WHERE provider = ?2 AND upstream_id = ?3",
            params![context, provider, upstream_id],
        )?;
        (touched > 0).then_some(()).ok_or(Error::NotFound)
    }

    pub fn set_model_params(
        &mut self,
        provider: &str,
        upstream_id: &str,
        params: &str,
    ) -> Result<()> {
        serde_json::from_str::<serde_json::Value>(params)
            .map_err(|e| Error::Invalid(format!("params 不是合法 JSON: {e}")))?;
        let touched = self.conn.execute(
            "UPDATE models SET params = ?1 WHERE provider = ?2 AND upstream_id = ?3",
            params![params, provider, upstream_id],
        )?;
        (touched > 0).then_some(()).ok_or(Error::NotFound)
    }

    pub fn last_refresh_at(&self, provider: &str) -> Result<Option<i64>> {
        self.conn
            .query_row(
                "SELECT last_refresh_at FROM provider_state WHERE provider = ?1",
                params![provider],
                |row| row.get(0),
            )
            .optional()
            .map_err(Into::into)
    }

    pub fn stats(&self) -> Result<StoreStats> {
        let count = |sql: &str| -> Result<i64> {
            Ok(self.conn.query_row(sql, [], |row| row.get(0))?)
        };
        Ok(StoreStats {
            conversations: count("SELECT COUNT(*) FROM conversations")?,
            messages: count("SELECT COUNT(*) FROM messages")?,
            models: count("SELECT COUNT(*) FROM models")?,
        })
    }
}

fn migrate(conn: &mut Connection) -> Result<()> {
    let mut version: i64 = conn.query_row("PRAGMA user_version", [], |r| r.get(0))?;
    while (version as usize) < MIGRATIONS.len() {
        let tx = conn.transaction()?;
        tx.execute_batch(MIGRATIONS[version as usize])?;
        version += 1;
        tx.pragma_update(None, "user_version", version)?;
        tx.commit()?;
    }
    Ok(())
}

fn now_ms() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis() as i64)
        .unwrap_or(0)
}

/// 从 `root` 起，沿 `parent_id` 收出整棵子树的 id（含 `root` 自己）。
///
/// 树上每个节点只有一个父亲 ⇒ 一趟扫描按层推进即可，不用递归。
fn subtree_ids(all: &[Message], root: Uuid) -> Vec<Uuid> {
    let mut wanted = vec![root];
    let mut frontier = vec![root];
    while let Some(current) = frontier.pop() {
        for message in all.iter().filter(|m| m.parent_id == Some(current)) {
            wanted.push(message.id);
            frontier.push(message.id);
        }
    }
    wanted
}

/// 从叶子沿 `parent_id` 回溯出的路径（正序）。
/// 父亲缺失就停在断口；万一库被手改成环，也会在 `all.len()` 步内停下。
fn path_from(all: &[Message], leaf: Option<Uuid>) -> Vec<Message> {
    let by_id: BTreeMap<Uuid, &Message> = all.iter().map(|message| (message.id, message)).collect();
    let mut path = Vec::new();
    let mut cursor = leaf;
    while let Some(id) = cursor {
        let Some(message) = by_id.get(&id) else {
            break;
        };
        path.push((*message).clone());
        cursor = message.parent_id;
        if path.len() > all.len() {
            break;
        }
    }
    path.reverse();
    path
}

/// 一条消息在同龄兄弟里的位置（给「< 2/3 >」用）。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BranchInfo {
    /// 第几条（1 起）。
    pub index: usize,
    pub total: usize,
    /// 同龄兄弟，按生成先后。切分支就是把 `current_leaf` 指到其中一个。
    pub siblings: Vec<Uuid>,
}

fn parse_uuid(raw: String) -> rusqlite::Result<Uuid> {
    Uuid::parse_str(&raw).map_err(|e| {
        rusqlite::Error::FromSqlConversionFailure(0, Type::Text, Box::new(e))
    })
}

fn row_to_conversation(row: &Row<'_>) -> rusqlite::Result<Conversation> {
    Ok(Conversation {
        id: parse_uuid(row.get(0)?)?,
        title: row.get(1)?,
        system_prompt: row.get(2)?,
        provider: row.get(3)?,
        model: row.get(4)?,
        agent_id: row.get(5)?,
        current_leaf: row
            .get::<_, Option<String>>(6)?
            .map(parse_uuid)
            .transpose()?,
        created_at: row.get(7)?,
        updated_at: row.get(8)?,
    })
}

fn row_to_message(row: &Row<'_>) -> rusqlite::Result<Message> {
    let role_raw: String = row.get(2)?;
    let role = Role::parse(&role_raw).ok_or_else(|| {
        rusqlite::Error::FromSqlConversionFailure(2, Type::Text, "unknown role".into())
    })?;
    Ok(Message {
        id: parse_uuid(row.get(0)?)?,
        conversation_id: parse_uuid(row.get(1)?)?,
        role,
        content: row.get(3)?,
        reasoning: row.get(6)?,
        reasoning_ms: row.get(7)?,
        duration_ms: row.get(8)?,
        usage: row
            .get::<_, Option<String>>(9)?
            .and_then(|text| serde_json::from_str(&text).ok()),
        parent_id: row
            .get::<_, Option<String>>(4)?
            .map(parse_uuid)
            .transpose()?,
        summary_id: row
            .get::<_, Option<String>>(10)?
            .map(parse_uuid)
            .transpose()?,
        created_at: row.get(5)?,
    })
}

/// 摘要行的 INSERT（`record_summary` 要在事务里复用，所以按 `&Connection` 收口）。
fn insert_summary_in(conn: &Connection, summary: &Summary) -> Result<()> {
    conn.execute(
        "INSERT INTO summaries
           (id, conversation_id, parent_summary_id, source_kind, text, blocks, tokens,
            source_ids, provider, model, prompt_version, usage, dirty, created_at)
         VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14)",
        params![
            summary.id.to_string(),
            summary.conversation_id.to_string(),
            summary.parent_summary_id.map(|id| id.to_string()),
            summary.source_kind.as_str(),
            summary.text,
            summary.blocks,
            summary.tokens,
            serde_json::to_string(
                &summary.source_ids.iter().map(Uuid::to_string).collect::<Vec<_>>()
            )
            .unwrap_or_else(|_| "[]".to_owned()),
            summary.provider,
            summary.model,
            summary.prompt_version,
            summary
                .usage
                .as_ref()
                .and_then(|usage| serde_json::to_string(usage).ok()),
            summary.dirty as i64,
            summary.created_at,
        ],
    )?;
    Ok(())
}

fn row_to_summary(row: &Row<'_>) -> rusqlite::Result<Summary> {
    let kind_raw: String = row.get(3)?;
    let source_kind = SummarySourceKind::parse(&kind_raw).ok_or_else(|| {
        rusqlite::Error::FromSqlConversionFailure(3, Type::Text, "unknown summary kind".into())
    })?;
    Ok(Summary {
        id: parse_uuid(row.get(0)?)?,
        conversation_id: parse_uuid(row.get(1)?)?,
        parent_summary_id: row
            .get::<_, Option<String>>(2)?
            .map(parse_uuid)
            .transpose()?,
        source_kind,
        text: row.get(4)?,
        blocks: row.get(5)?,
        tokens: row.get(6)?,
        source_ids: row
            .get::<_, Option<String>>(7)?
            .and_then(|text| serde_json::from_str::<Vec<String>>(&text).ok())
            .map(|ids| {
                ids.into_iter()
                    .filter_map(|raw| Uuid::parse_str(&raw).ok())
                    .collect()
            })
            .unwrap_or_default(),
        provider: row.get(8)?,
        model: row.get(9)?,
        prompt_version: row.get(10)?,
        usage: row
            .get::<_, Option<String>>(11)?
            .and_then(|text| serde_json::from_str(&text).ok()),
        dirty: row.get(12)?,
        created_at: row.get(13)?,
    })
}

fn row_to_model_entry(row: &Row<'_>) -> rusqlite::Result<ModelEntry> {
    Ok(ModelEntry {
        provider: row.get(0)?,
        upstream_id: row.get(1)?,
        upstream_name: row.get(2)?,
        owned_by: row.get(3)?,
        context_length: row.get(4)?,
        max_output: row.get(5)?,
        display_name: row.get(6)?,
        params: row.get(7)?,
        tokenizer: row.get(8)?,
        upstream_params: row.get(9)?,
        first_seen_at: row.get(10)?,
        last_seen_at: row.get(11)?,
        context_override: row.get(12)?,
    })
}

/// `{supported, default}` 的 JSON 文本；两者都空时给 `{}`，触发"保留旧值"。
fn upstream_params_json(supported: &[String], defaults: Option<&serde_json::Value>) -> String {
    let mut map = serde_json::Map::new();
    if !supported.is_empty() {
        map.insert("supported".to_owned(), serde_json::json!(supported));
    }
    if let Some(defaults) = defaults {
        map.insert("default".to_owned(), defaults.clone());
    }
    serde_json::Value::Object(map).to_string()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::Role;

    fn store() -> Store {
        Store::open_in_memory().unwrap()
    }

    #[test]
    fn roundtrip_keeps_conversation_fields_and_message_order() {
        let mut s = store();
        let conv = s
            .create_conversation("openrouter", "deepseek/deepseek-v4-flash", "你是助手")
            .unwrap();
        let m1 = s.insert_message(conv.id, Role::User, "你好", None).unwrap();
        let m2 = s
            .insert_message(conv.id, Role::Assistant, "你好！", Some(m1.id))
            .unwrap();

        let msgs = s.list_messages(conv.id).unwrap();
        assert_eq!(msgs.iter().map(|m| m.id).collect::<Vec<_>>(), vec![m1.id, m2.id]);
        assert_eq!(msgs[1].content, "你好！");

        let convs = s.list_conversations().unwrap();
        assert_eq!(convs.len(), 1);
        assert_eq!(convs[0].provider, "openrouter");
        assert_eq!(convs[0].model, "deepseek/deepseek-v4-flash");
        assert_eq!(convs[0].system_prompt, "你是助手");
    }

    fn summary_stub(conversation_id: Uuid, source_ids: Vec<Uuid>) -> Summary {
        Summary {
            id: Uuid::now_v7(),
            conversation_id,
            parent_summary_id: None,
            source_kind: SummarySourceKind::Message,
            text: "前两句的梗概".to_owned(),
            blocks: 1,
            tokens: 8,
            source_ids,
            provider: "dummy".to_owned(),
            model: "dummy".to_owned(),
            prompt_version: 1,
            usage: None,
            dirty: false,
            created_at: now_ms(),
        }
    }

    /// 「压缩只插不改」（§19.E 第一条保险）：插摘要 + 指成员之后，
    /// **messages 的每一格都不许变** —— 这是整套设计的地基。
    #[test]
    fn compaction_only_inserts_and_never_touches_messages() {
        let mut s = store();
        let conv = s.create_conversation("dummy", "dummy", "").unwrap();
        let mut ids = Vec::new();
        let mut parent = None;
        for i in 0..4 {
            let role = if i % 2 == 0 {
                Role::User
            } else {
                Role::Assistant
            };
            let message = s
                .insert_message(conv.id, role, &format!("第 {i} 句"), parent)
                .unwrap();
            parent = Some(message.id);
            ids.push(message.id);
        }
        let snapshot = |s: &Store| -> Vec<(String, String, i64)> {
            s.list_all_messages(conv.id)
                .unwrap()
                .into_iter()
                .map(|m| (m.id.to_string(), m.content, m.created_at))
                .collect()
        };
        let before = snapshot(&s);

        let summary = summary_stub(conv.id, ids[..2].to_vec());
        s.record_summary(&summary, &ids[..2]).unwrap();

        assert_eq!(snapshot(&s), before, "压缩只许插摘要，不许碰 messages");

        let messages = s.list_all_messages(conv.id).unwrap();
        assert_eq!(messages[0].summary_id, Some(summary.id));
        assert_eq!(messages[1].summary_id, Some(summary.id));
        assert_eq!(messages[2].summary_id, None, "没被覆盖的仍是 NULL");

        let summaries = s.list_summaries(conv.id).unwrap();
        assert_eq!(summaries.len(), 1);
        assert_eq!(summaries[0].text, "前两句的梗概");
        assert_eq!(summaries[0].source_ids, ids[..2]);
        assert_eq!(summaries[0].blocks, 1);
        assert!(!summaries[0].dirty);
    }

    /// 成员跨会话 ⇒ 整批拒绝、一行不落（与"编辑消息不能跨会话"同一条规矩）。
    #[test]
    fn record_summary_rejects_messages_from_another_conversation() {
        let mut s = store();
        let mine = s.create_conversation("dummy", "dummy", "").unwrap();
        let other = s.create_conversation("dummy", "dummy", "").unwrap();
        let foreign = s
            .insert_message(other.id, Role::User, "别处的话", None)
            .unwrap();

        let summary = summary_stub(mine.id, vec![foreign.id]);
        let err = s.record_summary(&summary, &[foreign.id]).unwrap_err();
        assert!(matches!(err, Error::Invalid(_)));
        assert!(
            s.list_summaries(mine.id).unwrap().is_empty(),
            "整批拒绝：摘要行也不许留下"
        );
        let untouched = s.list_all_messages(other.id).unwrap();
        assert_eq!(untouched[0].summary_id, None, "别处那条一个字不许动");
    }

    /// 删摘要 ⇒ 成员指针自动断开（ON DELETE SET NULL），正文照旧。
    #[test]
    fn deleting_summary_only_clears_the_pointer() {
        let mut s = store();
        let conv = s.create_conversation("dummy", "dummy", "").unwrap();
        let message = s.insert_message(conv.id, Role::User, "留下的话", None).unwrap();
        let summary = summary_stub(conv.id, vec![message.id]);
        s.record_summary(&summary, &[message.id]).unwrap();

        s.conn
            .execute(
                "DELETE FROM summaries WHERE id = ?1",
                params![summary.id.to_string()],
            )
            .unwrap();

        let messages = s.list_all_messages(conv.id).unwrap();
        assert_eq!(messages[0].summary_id, None, "指针应被 SET NULL");
        assert_eq!(messages[0].content, "留下的话", "正文不受影响");
    }

    #[test]
    fn delete_conversation_cascades_messages() {
        let mut s = store();
        let conv = s.create_conversation("x", "y", "").unwrap();
        s.insert_message(conv.id, Role::User, "a", None).unwrap();
        s.delete_conversation(conv.id).unwrap();

        let remaining: i64 = s
            .conn
            .query_row("SELECT COUNT(*) FROM messages", [], |r| r.get(0))
            .unwrap();
        assert_eq!(remaining, 0, "ON DELETE CASCADE 未生效");
        assert!(s.list_conversations().unwrap().is_empty());
    }

    #[test]
    fn insert_message_bumps_conversation_activity() {
        let mut s = store();
        let conv = s.create_conversation("x", "y", "").unwrap();
        s.conn
            .execute(
                "UPDATE conversations SET updated_at = 1 WHERE id = ?1",
                params![conv.id.to_string()],
            )
            .unwrap();

        s.insert_message(conv.id, Role::User, "hi", None).unwrap();

        let updated_at: i64 = s
            .conn
            .query_row(
                "SELECT updated_at FROM conversations WHERE id = ?1",
                params![conv.id.to_string()],
                |r| r.get(0),
            )
            .unwrap();
        assert!(updated_at > 1, "insert_message 的事务没有推活跃时间");
    }

    #[test]
    fn insert_message_rejects_unknown_conversation() {
        let mut s = store();
        let err = s
            .insert_message(Uuid::now_v7(), Role::User, "hi", None)
            .unwrap_err();
        assert!(matches!(err, Error::NotFound));
    }

    #[test]
    fn data_survives_reopen() {
        let path = std::env::temp_dir().join(format!("microchat-store-{}.db", Uuid::now_v7()));
        let conv_id = {
            let mut s = Store::open(&path).unwrap();
            let conv = s.create_conversation("x", "y", "").unwrap();
            s.insert_message(conv.id, Role::User, "持久化", None).unwrap();
            conv.id
        };

        let s = Store::open(&path).unwrap();
        let msgs = s.list_messages(conv_id).unwrap();
        assert_eq!(msgs.len(), 1);
        assert_eq!(msgs[0].content, "持久化");

        for suffix in ["", "-wal", "-shm"] {
            let _ = std::fs::remove_file(format!("{}{suffix}", path.display()));
        }
    }

    /// 本会话 `del` 掉的键会被墓碑挡住，不会从全局底下漏回来。
    /// 切分支 = 换一条路径 ⇒ **变量跟着换**（世界状态回到那次选择的样子）。
    #[test]
    fn switching_branch_changes_the_derived_variables() {
        let mut s = store();
        let conv = s.create_conversation("dummy", "dummy", "").unwrap();
        let question = s.insert_message(conv.id, Role::User, "我推开门", None).unwrap();
        let left = s
            .insert_message(
                conv.id,
                Role::Assistant,
                "<state>set HP = 1</state>左边那条路",
                Some(question.id),
            )
            .unwrap();
        let right = s
            .insert_message(
                conv.id,
                Role::Assistant,
                "<state>set HP = 9</state>右边那条路",
                Some(question.id),
            )
            .unwrap();

        let effective = |s: &Store| {
            let path = s.list_messages(conv.id).unwrap();
            crate::vars::VariableView::from_sources(
                conv.id,
                "",
                crate::vars::PromptSource::Agent,
                &path,
            )
            .effective
        };

        // 现状：尾巴在 right 上
        assert_eq!(effective(&s)["HP"], "9");

        // 切到左边那条：变量跟着换
        s.switch_leaf_to_sibling(conv.id, left.id).unwrap();
        assert_eq!(
            s.list_messages(conv.id).unwrap().last().unwrap().id,
            left.id
        );
        assert_eq!(effective(&s)["HP"], "1", "换了分支，世界状态跟着换");

        // 再切回去
        s.switch_leaf_to_sibling(conv.id, right.id).unwrap();
        assert_eq!(effective(&s)["HP"], "9");

        // 切到不相干的旧消息（上文都对不上）→ 拒绝
        assert!(s.switch_leaf_to_sibling(conv.id, question.id).is_err());
    }

    #[test]
    fn session_delete_hides_the_global_value() {
        let mut s = store();
        let conv = s.create_conversation("dummy", "dummy", "").unwrap();
        s.insert_message(conv.id, Role::User, "<state>del 世界</state>我离开了临安", None)
            .unwrap();

        // 底子由"生效的系统提示词"提供（这里模拟 agent 的提示词里写了 世界=临安）
        let messages = s.list_messages(conv.id).unwrap();
        let view = crate::vars::VariableView::from_sources(
            conv.id,
            "<state>set 世界 = 临安</state>",
            crate::vars::PromptSource::Agent,
            &messages,
        );

        assert!(
            view.effective.is_empty(),
            "删掉的键不该从全局漏回来：{:?}",
            view.effective
        );
        assert_eq!(view.global_values.get("世界").map(String::as_str), Some("临安"));
    }

    fn discovered(upstream_id: &str, name: Option<&str>) -> DiscoveredModel {
        DiscoveredModel {
            upstream_id: upstream_id.to_owned(),
            upstream_name: name.map(str::to_owned),
            ..Default::default()
        }
    }

    #[test]
    fn refresh_updates_discovered_columns_but_never_user_columns() {
        let mut s = store();
        s.apply_discovery("open", &[discovered("m1", Some("M One"))])
            .unwrap();
        s.set_model_display_name("open", "m1", Some("我的名字")).unwrap();
        s.set_model_params("open", "m1", r#"{"temperature":0.3}"#)
            .unwrap();
        s.set_model_context_override("open", "m1", Some(1_048_576))
            .unwrap();
        let first_seen = s.get_model("open", "m1").unwrap().unwrap().first_seen_at;

        s.apply_discovery("open", &[discovered("m1", Some("M One Renamed"))])
            .unwrap();

        let model = s.get_model("open", "m1").unwrap().unwrap();
        assert_eq!(model.upstream_name.as_deref(), Some("M One Renamed"), "发现列应更新");
        assert_eq!(model.display_name.as_deref(), Some("我的名字"), "用户列不该被刷新覆盖");
        assert_eq!(model.params_json()["temperature"], 0.3);
        assert_eq!(
            model.context_override,
            Some(1_048_576),
            "用户填的模型上下文也不该被刷新覆盖"
        );
        assert_eq!(model.first_seen_at, first_seen, "first_seen 只在首次写入");
        assert!(model.last_seen_at >= first_seen);
    }

    #[test]
    fn refresh_prunes_models_absent_from_upstream_but_keeps_overrides_of_survivors() {
        let mut s = store();
        s.apply_discovery(
            "open",
            &[discovered("keep", Some("Keep")), discovered("drop", Some("Drop"))],
        )
        .unwrap();
        s.set_model_display_name("open", "keep", Some("我的名字")).unwrap();

        s.apply_discovery("open", &[discovered("keep", Some("Keep Renamed"))])
            .unwrap();

        let models = s.list_models("open").unwrap();
        assert_eq!(models.len(), 1, "上游没给的模型应被删除");
        assert_eq!(models[0].upstream_id, "keep");
        assert_eq!(
            models[0].display_name.as_deref(),
            Some("我的名字"),
            "留下来的模型要保住用户覆盖"
        );
        assert_eq!(models[0].upstream_name.as_deref(), Some("Keep Renamed"));
    }

    #[test]
    fn upstream_params_are_stored_and_survive_a_silent_refresh() {
        let mut s = store();
        s.apply_discovery(
            "open",
            &[DiscoveredModel {
                upstream_id: "m1".to_owned(),
                supported_parameters: vec!["temperature".to_owned(), "top_p".to_owned()],
                default_parameters: Some(serde_json::json!({ "temperature": 0.7 })),
                ..Default::default()
            }],
        )
        .unwrap();

        let model = s.get_model("open", "m1").unwrap().unwrap();
        let params = model.upstream_params_json();
        assert_eq!(params["supported"][0], "temperature");
        assert_eq!(params["default"]["temperature"], 0.7);
        assert_eq!(model.params, "{}", "用户覆盖列不受影响");

        // 下一次刷新上游没给参数情报 → 保留旧的
        s.apply_discovery("open", &[discovered("m1", None)]).unwrap();
        let after = s.get_model("open", "m1").unwrap().unwrap();
        assert_eq!(
            after.upstream_params_json()["supported"][0], "temperature",
            "已知情报不该被静默抹掉"
        );
    }

    #[test]
    fn refresh_time_is_recorded_and_invalid_params_rejected() {
        let mut s = store();
        assert_eq!(s.last_refresh_at("open").unwrap(), None);
        s.apply_discovery("open", &[discovered("m1", None)]).unwrap();
        assert!(s.last_refresh_at("open").unwrap().is_some());

        let err = s.set_model_params("open", "m1", "{ not json").unwrap_err();
        assert!(matches!(err, Error::Invalid(_)));
        assert_eq!(s.get_model("open", "m1").unwrap().unwrap().params, "{}");
    }

    #[test]
    fn stats_reflect_written_rows() {
        let mut s = store();
        let empty = s.stats().unwrap();
        assert_eq!((empty.conversations, empty.messages, empty.models), (0, 0, 0));

        let conv = s.create_conversation("p", "m", "").unwrap();
        s.insert_message(conv.id, Role::User, "hi", None).unwrap();
        s.apply_discovery("p", &[discovered("m1", None)]).unwrap();

        let stats = s.stats().unwrap();
        assert_eq!((stats.conversations, stats.messages, stats.models), (1, 1, 1));
    }

    #[test]
    fn conversation_records_and_switches_agent() {
        let mut s = store();
        let conv = s.create_conversation("p", "m", "").unwrap();
        assert_eq!(conv.agent_id, DEFAULT_AGENT_ID, "新建会话带缺省 agent");

        s.set_conversation_agent(conv.id, "跑团").unwrap();
        assert_eq!(s.get_conversation(conv.id).unwrap().unwrap().agent_id, "跑团");

        let err = s.set_conversation_agent(Uuid::now_v7(), "x").unwrap_err();
        assert!(matches!(err, Error::NotFound));
    }
}
