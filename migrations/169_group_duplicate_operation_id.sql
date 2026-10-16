-- 分组复制使用内部 operation identity（操作标识）恢复已经提交但响应不确定的结果。
-- 部分唯一索引允许软删除后重用，同时保证同一管理员幂等操作只生成一个有效副本。
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS duplicate_operation_id VARCHAR(64);

CREATE UNIQUE INDEX IF NOT EXISTS idx_groups_duplicate_operation_id_active
    ON groups (duplicate_operation_id)
    WHERE duplicate_operation_id IS NOT NULL AND deleted_at IS NULL;
