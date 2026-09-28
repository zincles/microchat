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
