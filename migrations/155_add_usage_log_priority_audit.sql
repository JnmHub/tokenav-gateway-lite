ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS requested_service_tier VARCHAR(16),
    ADD COLUMN IF NOT EXISTS priority_policy_mode VARCHAR(24);
