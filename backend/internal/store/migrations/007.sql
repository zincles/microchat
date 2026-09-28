-- 推理型模型的"思考"（reasoning / reasoning_content）：**只留档**。
-- 它不进历史（不回喂给模型）、不扫 <state>、不可编辑；界面上默认折叠。
ALTER TABLE messages ADD COLUMN reasoning TEXT;
