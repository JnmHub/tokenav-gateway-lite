-- 账号复制在幂等协调器不可用或 observe-only（仅观测）时仍必须由数据库兜底去重。
-- operation id 是“管理员 + 源账号 + Idempotency-Key”的 SHA-256，非空活动账号全局唯一。
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_accounts_duplicate_operation_id_unique
    ON accounts ((extra ->> 'duplicate_operation_id'))
    WHERE deleted_at IS NULL
      AND NULLIF(btrim(extra ->> 'duplicate_operation_id'), '') IS NOT NULL;
