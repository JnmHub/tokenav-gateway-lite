ALTER TABLE content_moderation_logs
    ADD COLUMN IF NOT EXISTS organization_id BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS billing_subject_type VARCHAR(32) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS billing_subject_id BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS control_plane_key_id VARCHAR(128) NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_content_moderation_logs_enterprise_member
    ON content_moderation_logs(organization_id, user_id, control_plane_key_id, created_at DESC)
    WHERE organization_id > 0;
