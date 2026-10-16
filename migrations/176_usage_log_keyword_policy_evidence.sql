-- 关键词临时切组的本地用量证据。group_id 仍是实际计费分组；这里补充
-- 原始分组与非敏感决策摘要，便于在主站上报异常时独立核账。
ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS original_group_id BIGINT,
    ADD COLUMN IF NOT EXISTS keyword_policy_matched BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS keyword_policy_rerouted BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS keyword_policy_source VARCHAR(16) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS keyword_policy_reason VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS detected_languages JSONB NOT NULL DEFAULT '[]'::jsonb;

CREATE INDEX IF NOT EXISTS idx_usage_logs_original_group_created_at
    ON usage_logs(original_group_id, created_at DESC)
    WHERE original_group_id IS NOT NULL;
