-- AI（人工智能）审查运营预算与客户钱包分离；未知消费不因进程退出或 TTL（存活期）而退款。
CREATE TABLE IF NOT EXISTS content_moderation_ai_budget (
 scope_key TEXT NOT NULL, bucket TEXT NOT NULL, cost_micros BIGINT NOT NULL DEFAULT 0 CHECK(cost_micros >= 0),
 tokens BIGINT NOT NULL DEFAULT 0 CHECK(tokens >= 0), requests BIGINT NOT NULL DEFAULT 0,
 PRIMARY KEY(scope_key, bucket)
);
CREATE TABLE IF NOT EXISTS content_moderation_ai_attempts (
 attempt_id TEXT PRIMARY KEY, node_scope TEXT NOT NULL, tenant_scope TEXT NOT NULL, day_bucket TEXT NOT NULL,
 cost_micros BIGINT NOT NULL CHECK(cost_micros >= 0), tokens BIGINT NOT NULL CHECK(tokens >= 0),
 active BOOLEAN NOT NULL DEFAULT TRUE, expires_at TIMESTAMPTZ NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS content_moderation_ai_attempt_node ON content_moderation_ai_attempts(node_scope, expires_at) WHERE active;
CREATE INDEX IF NOT EXISTS content_moderation_ai_attempt_tenant ON content_moderation_ai_attempts(tenant_scope, expires_at) WHERE active;
CREATE TABLE IF NOT EXISTS content_moderation_ai_penalty_outbox (
 request_id TEXT PRIMARY KEY, payload JSONB NOT NULL, status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','delivered','dead')),
 attempts INTEGER NOT NULL DEFAULT 0, next_retry_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 claim_token TEXT NOT NULL DEFAULT '', claimed_at TIMESTAMPTZ, last_error TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS content_moderation_ai_penalty_due ON content_moderation_ai_penalty_outbox(next_retry_at) WHERE status='pending';
