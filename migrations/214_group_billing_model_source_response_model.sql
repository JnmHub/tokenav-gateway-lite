-- 允许分组显式按上游成功响应声明的模型计费；列本身已由 193 创建。
ALTER TABLE groups DROP CONSTRAINT IF EXISTS groups_billing_model_source_check;
ALTER TABLE groups
    ADD CONSTRAINT groups_billing_model_source_check
    CHECK (billing_model_source IS NULL OR billing_model_source IN ('requested', 'channel_mapped', 'upstream', 'response_model'));

COMMENT ON COLUMN groups.billing_model_source IS
    '分组计费模型来源：requested/channel_mapped/upstream/response_model；分组设置优先于渠道设置';
