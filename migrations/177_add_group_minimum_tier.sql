-- 分组只能抬高主站节点所需等级；NULL 表示继承节点等级。
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS minimum_tier INTEGER;

ALTER TABLE groups
    DROP CONSTRAINT IF EXISTS groups_minimum_tier_check;

ALTER TABLE groups
    ADD CONSTRAINT groups_minimum_tier_check
    CHECK (minimum_tier IS NULL OR minimum_tier BETWEEN 0 AND 2);
