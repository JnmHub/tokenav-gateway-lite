-- session_id 只用于把同一客户端会话的 usage_logs 关联起来。
-- 可空且无默认值，在 PostgreSQL 11+ 不会重写历史大表。
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS session_id VARCHAR(255);
