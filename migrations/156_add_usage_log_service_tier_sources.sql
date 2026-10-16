ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS sent_service_tier VARCHAR(16),
    ADD COLUMN IF NOT EXISTS response_service_tier VARCHAR(16);
