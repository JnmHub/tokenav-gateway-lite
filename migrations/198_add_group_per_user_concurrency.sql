ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS per_user_concurrency INTEGER NOT NULL DEFAULT 0;

ALTER TABLE groups
    DROP CONSTRAINT IF EXISTS groups_per_user_concurrency_nonnegative;

ALTER TABLE groups
    ADD CONSTRAINT groups_per_user_concurrency_nonnegative
    CHECK (per_user_concurrency >= 0);
