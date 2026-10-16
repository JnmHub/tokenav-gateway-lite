-- 严格优先级是新增的分组调度枚举。必须在发布二进制前放开数据库约束，
-- 否则后台会显示选项但保存时被 PostgreSQL 拒绝。
ALTER TABLE groups
    DROP CONSTRAINT IF EXISTS groups_scheduling_mode_check;

ALTER TABLE groups
    ADD CONSTRAINT groups_scheduling_mode_check
    CHECK (scheduling_mode IN ('default', 'random', 'strict_priority'));
