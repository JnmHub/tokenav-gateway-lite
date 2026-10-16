-- 新建分组默认按客户原始请求模型计费；已有分组的明确配置和历史空值均不改写。
ALTER TABLE groups
    ALTER COLUMN billing_model_source SET DEFAULT 'requested';

COMMENT ON COLUMN groups.billing_model_source IS
    '分组计费模型来源：requested/channel_mapped/upstream；新建默认 requested，分组设置优先于渠道设置';
