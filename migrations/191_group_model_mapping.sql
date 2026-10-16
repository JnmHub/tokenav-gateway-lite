-- 普通分组模型映射在账号调度前生效；默认空对象保持现有分组行为不变。
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS model_mapping JSONB NOT NULL DEFAULT '{}'::jsonb;

COMMENT ON COLUMN groups.model_mapping IS
    '客户请求模型到分组目标模型的映射；支持精确匹配与末尾 * 前缀匹配';
