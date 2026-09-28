-- 上游声明的参数信息（supported/default），与用户覆盖的 params 分列。
ALTER TABLE models ADD COLUMN upstream_params TEXT NOT NULL DEFAULT '{}';
