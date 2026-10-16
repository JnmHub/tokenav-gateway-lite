ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS priority_detection_source VARCHAR(48),
    ADD COLUMN IF NOT EXISTS priority_policy_action VARCHAR(32),
    ADD COLUMN IF NOT EXISTS priority_policy_rule_id VARCHAR(64),
    ADD COLUMN IF NOT EXISTS priority_policy_fallback BOOLEAN NOT NULL DEFAULT FALSE;
