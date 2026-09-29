-- 001：最初的形状（**开发阶段：删了重建** —— 旧的 11 个迁移已合并成这一份）。
--
-- 命名按 `DEFINE.md`：会话 = `sessions`，消息的归属 = `session_id`。
-- 改形状的办法就是改**这一个文件**（然后把 data/microchat.db 删掉重来），不再补 002、003……

CREATE TABLE sessions (
  id            TEXT PRIMARY KEY,
  title         TEXT NOT NULL DEFAULT '',
  system_prompt TEXT NOT NULL DEFAULT '',
  provider      TEXT NOT NULL,
  model         TEXT NOT NULL,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
, agent_id TEXT NOT NULL DEFAULT 'default', current_leaf TEXT);
CREATE TABLE messages (
  id              TEXT PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  role            TEXT NOT NULL CHECK (role IN ('user','assistant')),
  content         TEXT NOT NULL,
  created_at      INTEGER NOT NULL
, parent_id TEXT REFERENCES messages(id) ON DELETE CASCADE, reasoning TEXT, duration_ms INTEGER, usage TEXT, reasoning_ms INTEGER, summary_id TEXT REFERENCES summaries(id) ON DELETE SET NULL);
CREATE INDEX messages_by_session ON messages(session_id, id);
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
  last_seen_at   INTEGER NOT NULL, upstream_params TEXT NOT NULL DEFAULT '{}', context_override INTEGER,
  PRIMARY KEY (provider, upstream_id)
);
CREATE TABLE provider_state (
  provider        TEXT PRIMARY KEY,
  last_refresh_at INTEGER NOT NULL
);
CREATE INDEX messages_by_parent ON messages(parent_id);
CREATE TABLE summaries (
  id                TEXT PRIMARY KEY,
  session_id   TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
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
CREATE INDEX summaries_by_conv ON summaries(session_id, id);
CREATE INDEX summaries_by_parent ON summaries(parent_summary_id);
CREATE INDEX messages_by_summary ON messages(summary_id);
