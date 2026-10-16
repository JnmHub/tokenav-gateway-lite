-- 分组级计费模型来源，直接在分组编辑页选择；默认按映射后模型计费。
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS billing_model_source VARCHAR(20);

ALTER TABLE groups DROP CONSTRAINT IF EXISTS groups_billing_model_source_check;
ALTER TABLE groups
    ADD CONSTRAINT groups_billing_model_source_check
    CHECK (billing_model_source IS NULL OR billing_model_source IN ('requested', 'channel_mapped', 'upstream'));

COMMENT ON COLUMN groups.billing_model_source IS
    '分组计费模型来源：requested/channel_mapped/upstream；分组设置优先于渠道设置';
