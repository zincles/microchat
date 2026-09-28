-- 用户为"这个模型能吃多少"填的覆盖值（token）。**用户列**：刷新只写发现列，永不碰它。
-- 优先顺序：context_override > context_length（发现） > config.json 的 chat.model_context_tokens（兜底）。
ALTER TABLE models ADD COLUMN context_override INTEGER;
