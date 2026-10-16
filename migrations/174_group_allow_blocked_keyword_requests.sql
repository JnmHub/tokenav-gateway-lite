ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS allow_blocked_keyword_requests BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN groups.allow_blocked_keyword_requests IS
    '是否允许处理命中强拦截关键词的请求；开启表示允许全部关键词';

ALTER TABLE content_moderation_logs
    ADD COLUMN IF NOT EXISTS original_group_id BIGINT,
    ADD COLUMN IF NOT EXISTS original_group_name VARCHAR(255) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS effective_group_id BIGINT,
    ADD COLUMN IF NOT EXISTS effective_group_name VARCHAR(255) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS content_source VARCHAR(16) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS detected_languages JSONB NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN IF NOT EXISTS keyword_policy_rerouted BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS keyword_policy_reason VARCHAR(64) NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_content_moderation_logs_original_group
    ON content_moderation_logs(original_group_id, created_at DESC)
    WHERE original_group_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_content_moderation_logs_effective_group
    ON content_moderation_logs(effective_group_id, created_at DESC)
    WHERE effective_group_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_content_moderation_logs_policy_source
    ON content_moderation_logs(content_source, created_at DESC)
    WHERE content_source <> '';
