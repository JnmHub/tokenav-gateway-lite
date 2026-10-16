CREATE TABLE IF NOT EXISTS gateway_lite_usage_retry_outbox (
    request_id TEXT NOT NULL,
    operation_kind TEXT NOT NULL,
    event_payload JSONB NOT NULL,
    generation BIGINT NOT NULL DEFAULT 1,
    attempts INTEGER NOT NULL DEFAULT 0,
    next_retry_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_error TEXT NOT NULL DEFAULT '',
    claim_token TEXT NOT NULL DEFAULT '',
    claimed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (request_id, operation_kind),
    CONSTRAINT gateway_lite_usage_retry_outbox_operation_kind_valid
        CHECK (operation_kind IN ('finalize', 'correction', 'report')),
    CONSTRAINT gateway_lite_usage_retry_outbox_attempts_nonnegative CHECK (attempts >= 0)
);

CREATE INDEX IF NOT EXISTS idx_gateway_lite_usage_retry_outbox_due
    ON gateway_lite_usage_retry_outbox (next_retry_at, claimed_at, created_at);
