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
