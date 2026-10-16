ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS scheduling_mode VARCHAR(20) NOT NULL DEFAULT 'default';

ALTER TABLE groups
    DROP CONSTRAINT IF EXISTS groups_scheduling_mode_check;

ALTER TABLE groups
    ADD CONSTRAINT groups_scheduling_mode_check
    CHECK (scheduling_mode IN ('default', 'random'));
