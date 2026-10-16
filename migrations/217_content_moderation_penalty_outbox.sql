CREATE TABLE IF NOT EXISTS gateway_lite_content_moderation_penalty_outbox (
    request_id TEXT PRIMARY KEY,
    event_payload JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    attempts INT NOT NULL DEFAULT 0,
    next_retry_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_error TEXT NOT NULL DEFAULT '',
    claim_token TEXT NOT NULL DEFAULT '',
    claimed_at TIMESTAMPTZ,
    dead_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT gateway_lite_content_moderation_penalty_attempts_nonnegative CHECK (attempts >= 0),
    CONSTRAINT gateway_lite_content_moderation_penalty_status_valid CHECK (status IN ('pending', 'dead'))
);

ALTER TABLE gateway_lite_content_moderation_penalty_outbox
    ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'pending';

ALTER TABLE gateway_lite_content_moderation_penalty_outbox
    ADD COLUMN IF NOT EXISTS dead_at TIMESTAMPTZ;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'gateway_lite_content_moderation_penalty_status_valid'
          AND conrelid = 'gateway_lite_content_moderation_penalty_outbox'::regclass
    ) THEN
        ALTER TABLE gateway_lite_content_moderation_penalty_outbox
            ADD CONSTRAINT gateway_lite_content_moderation_penalty_status_valid
            CHECK (status IN ('pending', 'dead'));
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_gateway_lite_content_moderation_penalty_pending_due
    ON gateway_lite_content_moderation_penalty_outbox(next_retry_at, claimed_at, created_at)
    WHERE status = 'pending';
