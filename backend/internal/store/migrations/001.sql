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
