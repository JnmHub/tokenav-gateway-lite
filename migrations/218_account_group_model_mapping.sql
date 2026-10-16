-- 每个账号—分组绑定保存独立模型映射，避免一个账号被多个分组复用时全局映射互相覆盖。
ALTER TABLE account_groups
    ADD COLUMN IF NOT EXISTS model_mapping JSONB NOT NULL DEFAULT '{}'::jsonb;
