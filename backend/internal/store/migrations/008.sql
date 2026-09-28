-- 每条回复的"这一轮花了多久"与用量（上行/下行/缓存/思考）。用量是各家字段归一化之后
-- 的 JSON（只服务显示，不参与任何计算）；老消息与 dummy 都是 NULL。
ALTER TABLE messages ADD COLUMN duration_ms INTEGER;
ALTER TABLE messages ADD COLUMN usage TEXT;
