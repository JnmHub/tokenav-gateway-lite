ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS rate_policy_audit JSONB;

COMMENT ON COLUMN usage_logs.rate_policy_audit IS
    '请求开始时冻结的客户销售倍率证据；不包含账号采购成本倍率';
