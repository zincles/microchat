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
