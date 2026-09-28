-- 思考用时（受理 → 第一段正文）。界面上显示成「思考（2.1s）」。
ALTER TABLE messages ADD COLUMN reasoning_ms INTEGER;
