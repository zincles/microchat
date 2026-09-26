//! SQLite 存储：会话与消息的唯一权威来源。
//!
//! 口径（与建模讨论一致）：
//! - id 一律 uuidv7，字符串落库，`ORDER BY id` 即时间序；
//! - 消息只追加；分支（`parent_id`）留待 Phase 3 加列 + 按主键顺序回填，历史无损；
//! - `insert_message` 在**单事务**内完成「更新会话活跃时间 + 插消息」，杜绝两步不一致；
//! - 迁移由 `PRAGMA user_version` 驱动，格式升级不丢数据。

use std::path::Path;

use rusqlite::{params, types::Type, Connection, OptionalExtension, Row};
use serde::{Deserialize, Serialize};
use uuid::Uuid;

use crate::model::{Conversation, DiscoveredModel, Message, ModelEntry, Role, DEFAULT_AGENT_ID};
use crate::vars::{OpKind, Scope, VarOp, VarOpRow};

const MIGRATIONS: [&str; 4] = [r#"
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
-- 词汇与 providers.jsonc 对齐：endpoint → provider。
ALTER TABLE conversations RENAME COLUMN endpoint TO provider;
-- agent 来源标记。
ALTER TABLE conversations ADD COLUMN agent_id TEXT NOT NULL DEFAULT 'default';

-- 发现所得与用户覆盖同表分列：刷新只写发现列，用户列永不被动。
-- provider 用 providers.jsonc 里的 handle，无外键（配置在文件里，库不做约束）。
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

-- provider 的刷新簿记。放库里而不是 providers.jsonc：那个文件应用只读不写。
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

    /// 追加一条消息，并把会话的活跃时间推到当前时刻——两步在同一事务内。
    /// 会话不存在则整体回滚并返回 [`Error::NotFound`]。
    pub fn insert_message(
        &mut self,
        conversation_id: Uuid,
        role: Role,
        content: &str,
    ) -> Result<Message> {
        let now = now_ms();
        let msg = Message {
            id: Uuid::now_v7(),
            conversation_id,
            role,
            content: content.to_owned(),
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
            "INSERT INTO messages (id, conversation_id, role, content, created_at)
             VALUES (?1, ?2, ?3, ?4, ?5)",
            params![
                msg.id.to_string(),
                msg.conversation_id.to_string(),
                msg.role.as_str(),
                msg.content,
                msg.created_at
            ],
        )?;
        tx.commit()?;
        Ok(msg)
    }

    pub fn get_conversation(&self, conversation_id: Uuid) -> Result<Option<Conversation>> {
        self.conn
            .query_row(
                "SELECT id, title, system_prompt, provider, model, agent_id, created_at, updated_at
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
            "SELECT id, title, system_prompt, provider, model, agent_id, created_at, updated_at
             FROM conversations ORDER BY updated_at DESC, id DESC",
        )?;
        let rows = stmt.query_map([], row_to_conversation)?;
        Ok(rows.filter_map(std::result::Result::ok).collect())
    }

    /// 按插入序返回消息（分支上线前的活跃路径就是全表）。
    /// 用 `rowid` 而不是 `id`：uuidv7 在同一毫秒内的顺序由随机位决定，不足以表达
    /// "谁先发的"；SQLite 的 rowid 是严格递增的插入序，毫秒内也精确。
    /// 会话不存在 → [`Error::NotFound`]，避免「空列表」把未知会话伪装成空会话。
    pub fn list_messages(&self, conversation_id: Uuid) -> Result<Vec<Message>> {
        if self.get_conversation(conversation_id)?.is_none() {
            return Err(Error::NotFound);
        }
        let mut stmt = self.conn.prepare(
            "SELECT id, conversation_id, role, content, created_at
             FROM messages WHERE conversation_id = ?1 ORDER BY rowid",
        )?;
        let rows = stmt.query_map(params![conversation_id.to_string()], row_to_message)?;
        Ok(rows.filter_map(std::result::Result::ok).collect())
    }

    /// 追加一批变量操作（**只插不改**；回放靠 `seq` 顺序，不做反操作）。
    ///
    /// `scope=Global` 的操作不绑会话（`conversation_id` 写 NULL）；`session` 必绑，
    /// 外键保证会话不存在时整体失败——不会留下悬空的操作。
    pub fn insert_variable_ops(
        &mut self,
        conversation_id: Uuid,
        message_id: Uuid,
        ops: &[VarOp],
    ) -> Result<usize> {
        if ops.is_empty() {
            return Ok(0);
        }
        let now = now_ms();
        let tx = self.conn.transaction()?;
        write_variable_ops(
            &tx,
            &conversation_id.to_string(),
            &message_id.to_string(),
            now,
            ops,
        )?;
        tx.commit()?;
        Ok(ops.len())
    }

    /// 编辑一条消息的正文，并**重算**它产生的变量操作，返回更新后的消息。
    ///
    /// 编辑等于改存档：那条消息当初写下的状态未必还是它现在说的，所以它的操作整批删掉、
    /// 按新正文重新落（新操作拿到新的 `seq`——在时间线上它们属于"现在"）。
    /// 单事务：改正文与换操作要么都成，要么都不成。
    pub fn update_message(
        &mut self,
        conversation_id: Uuid,
        message_id: Uuid,
        content: &str,
        ops: &[VarOp],
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
        tx.execute(
            "DELETE FROM variable_ops WHERE message_id = ?1",
            params![message],
        )?;
        write_variable_ops(&tx, &conversation, &message, now, ops)?;
        let updated = tx.query_row(
            "SELECT id, conversation_id, role, content, created_at FROM messages WHERE id = ?1",
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

    /// 删掉一条消息：连它当初写下的变量操作一起删（单事务）。
    ///
    /// 顺序靠 `rowid`（插入顺序）而不是 `id`，所以删中间一条**不会**动到别人的顺序，
    /// 也没有"位置空缺"要补——剩下的行照样按 rowid 排。
    /// 真正要清的是操作日志：它按 `message_id` 挂着（没有外键），不跟着删的话，
    /// 那句已经不存在的话写下的 `set HP = 12` 会继续影响生效值。
    pub fn delete_message(&mut self, conversation_id: Uuid, message_id: Uuid) -> Result<()> {
        let now = now_ms();
        let conversation = conversation_id.to_string();
        let message = message_id.to_string();
        let tx = self.conn.transaction()?;

        let touched = tx.execute(
            "DELETE FROM messages WHERE id = ?1 AND conversation_id = ?2",
            params![message, conversation],
        )?;
        if touched == 0 {
            return Err(Error::NotFound);
        }
        tx.execute(
            "DELETE FROM variable_ops WHERE message_id = ?1",
            params![message],
        )?;
        tx.execute(
            "UPDATE conversations SET updated_at = ?1 WHERE id = ?2",
            params![now, conversation],
        )?;
        tx.commit()?;
        Ok(())
    }

    /// 某会话要用的全部操作：全局 base + 本会话，按 `seq`（= id）升序。
    pub fn list_variable_ops(&self, conversation_id: Uuid) -> Result<Vec<VarOpRow>> {
        if self.get_conversation(conversation_id)?.is_none() {
            return Err(Error::NotFound);
        }
        let mut stmt = self.conn.prepare(
            "SELECT id, scope, op, key, value, message_id, conversation_id, created_at
             FROM variable_ops
             WHERE scope = 'global' OR conversation_id = ?1
             ORDER BY id",
        )?;
        let rows = stmt.query_map(params![conversation_id.to_string()], row_to_var_op)?;
        // 注意：不像 list_messages 那样吞掉行错误——枚举列认不出来说明库被动过，必须报。
        Ok(rows.collect::<rusqlite::Result<Vec<_>>>()?)
    }

    /// 只要全局的（"全局变量"面板用，不依赖任何会话）。
    pub fn list_global_variable_ops(&self) -> Result<Vec<VarOpRow>> {
        let mut stmt = self.conn.prepare(
            "SELECT id, scope, op, key, value, message_id, conversation_id, created_at
             FROM variable_ops WHERE scope = 'global' ORDER BY id",
        )?;
        let rows = stmt.query_map([], row_to_var_op)?;
        Ok(rows.collect::<rusqlite::Result<Vec<_>>>()?)
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
                    display_name, params, tokenizer, upstream_params, first_seen_at, last_seen_at
             FROM models WHERE provider = ?1 ORDER BY upstream_id",
        )?;
        let rows = stmt.query_map(params![provider], row_to_model_entry)?;
        Ok(rows.filter_map(std::result::Result::ok).collect())
    }

    pub fn get_model(&self, provider: &str, upstream_id: &str) -> Result<Option<ModelEntry>> {
        self.conn
            .query_row(
                "SELECT provider, upstream_id, upstream_name, owned_by, context_length, max_output,
                        display_name, params, tokenizer, upstream_params, first_seen_at, last_seen_at
                 FROM models WHERE provider = ?1 AND upstream_id = ?2",
                params![provider, upstream_id],
                row_to_model_entry,
            )
            .optional()
            .map_err(Into::into)
    }

    /// 忘掉一个 provider 派生出来的全部数据：已发现模型（连用户改的显示名/参数一起）
    /// 与刷新时间戳。历史会话里仍留着它的名字——那由 `chat::Backend::select` 兜底成
    /// fallback 话术，不是错误。
    pub fn forget_provider(&mut self, provider: &str) -> Result<()> {
        let tx = self.conn.transaction()?;
        tx.execute("DELETE FROM models WHERE provider = ?1", params![provider])?;
        tx.execute("DELETE FROM provider_state WHERE provider = ?1", params![provider])?;
        tx.commit()?;
        Ok(())
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
        created_at: row.get(6)?,
        updated_at: row.get(7)?,
    })
}

fn row_to_var_op(row: &Row<'_>) -> rusqlite::Result<VarOpRow> {
    Ok(VarOpRow {
        seq: row.get(0)?,
        scope: parse_enum(row.get::<_, String>(1)?, "scope", Scope::parse)?,
        kind: parse_enum(row.get::<_, String>(2)?, "op", OpKind::parse)?,
        key: row.get(3)?,
        value: row.get(4)?,
        message_id: row.get::<_, Option<String>>(5)?.map(parse_uuid).transpose()?,
        conversation_id: row.get::<_, Option<String>>(6)?.map(parse_uuid).transpose()?,
        created_at: row.get(7)?,
    })
}

/// 把一批操作写进 `variable_ops`。调用方负责事务与"先删旧的"。
fn write_variable_ops(
    conn: &Connection,
    conversation_id: &str,
    message_id: &str,
    created_at: i64,
    ops: &[VarOp],
) -> rusqlite::Result<()> {
    for op in ops {
        conn.execute(
            "INSERT INTO variable_ops
               (scope, conversation_id, message_id, op, key, value, created_at)
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)",
            params![
                op.scope.as_str(),
                match op.scope {
                    Scope::Session => Some(conversation_id),
                    Scope::Global => None,
                },
                message_id,
                op.kind.as_str(),
                op.key.as_str(),
                op.value.as_deref(),
                created_at,
            ],
        )?;
    }
    Ok(())
}

/// 文本列 → 枚举。认不出来的值**报错**而不是跳过：那说明库被别的东西写过。
fn parse_enum<T>(raw: String, column: &str, parse: fn(&str) -> Option<T>) -> rusqlite::Result<T> {
    parse(&raw).ok_or_else(|| {
        rusqlite::Error::FromSqlConversionFailure(
            0,
            Type::Text,
            format!("{column} 列的值不认识: {raw}").into(),
        )
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
        created_at: row.get(4)?,
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
        let m1 = s.insert_message(conv.id, Role::User, "你好").unwrap();
        let m2 = s.insert_message(conv.id, Role::Assistant, "你好！").unwrap();

        let msgs = s.list_messages(conv.id).unwrap();
        assert_eq!(msgs.iter().map(|m| m.id).collect::<Vec<_>>(), vec![m1.id, m2.id]);
        assert_eq!(msgs[1].content, "你好！");

        let convs = s.list_conversations().unwrap();
        assert_eq!(convs.len(), 1);
        assert_eq!(convs[0].provider, "openrouter");
        assert_eq!(convs[0].model, "deepseek/deepseek-v4-flash");
        assert_eq!(convs[0].system_prompt, "你是助手");
    }

    #[test]
    fn delete_conversation_cascades_messages() {
        let mut s = store();
        let conv = s.create_conversation("x", "y", "").unwrap();
        s.insert_message(conv.id, Role::User, "a").unwrap();
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

        s.insert_message(conv.id, Role::User, "hi").unwrap();

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
            .insert_message(Uuid::now_v7(), Role::User, "hi")
            .unwrap_err();
        assert!(matches!(err, Error::NotFound));
    }

    #[test]
    fn data_survives_reopen() {
        let path = std::env::temp_dir().join(format!("microchat-store-{}.db", Uuid::now_v7()));
        let conv_id = {
            let mut s = Store::open(&path).unwrap();
            let conv = s.create_conversation("x", "y", "").unwrap();
            s.insert_message(conv.id, Role::User, "持久化").unwrap();
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

    #[test]
    fn variable_ops_append_and_fold_in_order() {
        let mut s = store();
        let conv = s.create_conversation("dummy", "dummy", "你是助手").unwrap();
        let msg = s.insert_message(conv.id, Role::User, "来了").unwrap();
        let ops = vec![
            VarOp {
                scope: Scope::Session,
                kind: OpKind::Set,
                key: "HP".to_owned(),
                value: Some("12".to_owned()),
            },
            VarOp {
                scope: Scope::Global,
                kind: OpKind::Set,
                key: "季节".to_owned(),
                value: Some("初冬".to_owned()),
            },
        ];
        assert_eq!(s.insert_variable_ops(conv.id, msg.id, &ops).unwrap(), 2);

        let view = crate::vars::VariableView::build(&s.list_variable_ops(conv.id).unwrap());
        assert_eq!(view.effective.get("HP").map(String::as_str), Some("12"));
        assert_eq!(view.effective.get("季节").map(String::as_str), Some("初冬"));
        assert_eq!(view.session.len(), 1);
        assert_eq!(view.global.len(), 1);
        assert_eq!(s.list_global_variable_ops().unwrap().len(), 1);

        // 全局对所有会话可见；别人的 session 不可见。
        let other = s.create_conversation("dummy", "dummy", "").unwrap();
        let view = crate::vars::VariableView::build(&s.list_variable_ops(other.id).unwrap());
        assert_eq!(view.effective.get("季节").map(String::as_str), Some("初冬"));
        assert!(!view.effective.contains_key("HP"));
    }

    #[test]
    fn session_delete_hides_the_global_value() {
        let mut s = store();
        let conv = s.create_conversation("dummy", "dummy", "").unwrap();
        let msg = s.insert_message(conv.id, Role::User, "x").unwrap();
        let global = VarOp {
            scope: Scope::Global,
            kind: OpKind::Set,
            key: "世界".to_owned(),
            value: Some("临安".to_owned()),
        };
        let delete = VarOp {
            scope: Scope::Session,
            kind: OpKind::Delete,
            key: "世界".to_owned(),
            value: None,
        };
        s.insert_variable_ops(conv.id, msg.id, &[global]).unwrap();
        s.insert_variable_ops(conv.id, msg.id, &[delete]).unwrap();

        let view = crate::vars::VariableView::build(&s.list_variable_ops(conv.id).unwrap());
        assert!(view.effective.is_empty(), "{:?}", view.effective);
        assert_eq!(view.global_values.get("世界").map(String::as_str), Some("临安"));
    }

    #[test]
    fn variable_ops_reject_unknown_conversation() {
        let mut s = store();
        let message = Uuid::now_v7();
        let op = VarOp {
            scope: Scope::Session,
            kind: OpKind::Set,
            key: "HP".to_owned(),
            value: Some("1".to_owned()),
        };
        // 外键在事务里失败 → 一条都不落
        assert!(matches!(
            s.insert_variable_ops(Uuid::now_v7(), message, &[op]),
            Err(Error::Db(_))
        ));
        assert!(matches!(
            s.list_variable_ops(Uuid::now_v7()),
            Err(Error::NotFound)
        ));
    }

    #[test]
    fn editing_a_message_recomputes_its_variable_ops() {
        let mut s = store();
        let conv = s.create_conversation("dummy", "dummy", "").unwrap();
        let msg = s
            .insert_message(conv.id, Role::Assistant, "好的\n<state>\nset HP = 12\n</state>")
            .unwrap();
        crate::vars::parse(&msg.content);
        let ops = crate::vars::parse(&msg.content).ops;
        s.insert_variable_ops(conv.id, msg.id, &ops).unwrap();
        assert_eq!(
            crate::vars::VariableView::build(&s.list_variable_ops(conv.id).unwrap())
                .effective
                .get("HP")
                .map(String::as_str),
            Some("12")
        );

        // 编辑成另一套状态：旧操作必须整批消失，不能与新正文并存
        let edited = "算了\n<state>\nset HP = 3\ndel 房号\n</state>";
        let ops = crate::vars::parse(edited).ops;
        let updated = s.update_message(conv.id, msg.id, edited, &ops).unwrap();
        assert_eq!(updated.content, edited);

        let view = crate::vars::VariableView::build(&s.list_variable_ops(conv.id).unwrap());
        assert_eq!(view.effective.get("HP").map(String::as_str), Some("3"));
        assert_eq!(view.session.len(), 2, "旧的那条 set 必须被删掉");

        // 别人的消息 / 不存在的消息都不能改
        let other = s.create_conversation("dummy", "dummy", "").unwrap();
        assert!(matches!(
            s.update_message(other.id, msg.id, "x", &[]),
            Err(Error::NotFound)
        ));
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
        let first_seen = s.get_model("open", "m1").unwrap().unwrap().first_seen_at;

        s.apply_discovery("open", &[discovered("m1", Some("M One Renamed"))])
            .unwrap();

        let model = s.get_model("open", "m1").unwrap().unwrap();
        assert_eq!(model.upstream_name.as_deref(), Some("M One Renamed"), "发现列应更新");
        assert_eq!(model.display_name.as_deref(), Some("我的名字"), "用户列不该被刷新覆盖");
        assert_eq!(model.params_json()["temperature"], 0.3);
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
        s.insert_message(conv.id, Role::User, "hi").unwrap();
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
