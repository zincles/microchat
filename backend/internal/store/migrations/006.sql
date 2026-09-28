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
