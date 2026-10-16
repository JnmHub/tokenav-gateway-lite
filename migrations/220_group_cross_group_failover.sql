-- 普通文本上游故障的一次性跨分组兜底策略。
-- 与 fallback_group_id（客户端限制/无效请求兜底）保持独立，避免两个语义互相覆盖。
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS failover_group_id BIGINT REFERENCES groups(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS failover_strategy VARCHAR(32) NOT NULL DEFAULT 'original_model_first';

CREATE INDEX IF NOT EXISTS idx_groups_failover_group_id
    ON groups(failover_group_id)
    WHERE deleted_at IS NULL AND failover_group_id IS NOT NULL;

COMMENT ON COLUMN groups.failover_group_id IS '普通文本上游可恢复故障时一次性转移到的兜底分组 ID';
COMMENT ON COLUMN groups.failover_strategy IS '跨分组故障转移顺序：original_model_first 或 current_group_first';

UPDATE groups
SET failover_strategy = 'original_model_first'
WHERE failover_strategy IS NULL OR failover_strategy = '';
