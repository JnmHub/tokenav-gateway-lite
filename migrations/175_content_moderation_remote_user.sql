-- gateway-lite（轻量网关）收到的主站用户 ID 不一定存在于本地 users 表。
-- 风控日志必须保留远程 user_id 作为审计证据，但不能用本地用户外键阻止写入。
ALTER TABLE content_moderation_logs
    DROP CONSTRAINT IF EXISTS content_moderation_logs_user_id_fkey;

CREATE INDEX IF NOT EXISTS idx_content_moderation_logs_user_created_at
    ON content_moderation_logs(user_id, created_at DESC);
