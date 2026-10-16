-- Recovery evidence is independent from finished usage events: a reserve or
-- release can commit before any UsageEvent exists. Never store an invocation token.
CREATE TABLE IF NOT EXISTS gateway_lite_oauth_invocation_recovery (
    gateway_code TEXT NOT NULL,
    request_id TEXT NOT NULL,
    snapshot_payload TEXT NOT NULL,
    snapshot_sha256 TEXT NOT NULL,
    generation BIGINT NOT NULL DEFAULT 1 CHECK (generation > 0),
    owner_token TEXT NOT NULL DEFAULT '',
    owner_until_millis BIGINT NOT NULL DEFAULT 0,
    claim_token TEXT NOT NULL DEFAULT '',
    claim_until_millis BIGINT NOT NULL DEFAULT 0,
    may_dispatch BOOLEAN NOT NULL DEFAULT FALSE,
    finished BOOLEAN NOT NULL DEFAULT FALSE,
    state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'attention', 'completed')),
    phase TEXT NOT NULL DEFAULT '',
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_retry_millis BIGINT NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    created_millis BIGINT NOT NULL,
    updated_millis BIGINT NOT NULL,
    PRIMARY KEY (gateway_code, request_id)
);

CREATE INDEX IF NOT EXISTS idx_gateway_lite_oauth_invocation_recovery_due
    ON gateway_lite_oauth_invocation_recovery (state, next_retry_millis, owner_until_millis, claim_until_millis);
