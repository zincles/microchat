-- 变量操作日志整张表拆掉：它本来就是从消息正文推出来的东西。
-- 现在**库里只存正文**，变量表在读取时顺着消息重演（world::WorldStateView::from_sources）；
-- 全局变量搬去 system prompt。`config/variables.json` 与 `setglobal` 都已废弃。
DROP TABLE IF EXISTS variable_ops;
