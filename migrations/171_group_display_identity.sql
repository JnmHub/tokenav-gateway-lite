-- 分组展示身份与调度平台解耦：Kimi、GLM 等 OpenAI 兼容分组仍使用 openai 调度，
-- 但可以在用户界面显示自己的品牌类别和内置图标。
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS display_brand VARCHAR(40) NOT NULL DEFAULT '';

ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS display_icon VARCHAR(32) NOT NULL DEFAULT '';
