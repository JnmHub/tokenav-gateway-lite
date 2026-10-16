-- Codex Tail-Burst 历史会话。账号 extra 只保留当前/上次摘要，
-- 本表保存每次会话，便于管理员长期统计；不建立账号外键，账号软删后仍保留审计记录。
CREATE TABLE IF NOT EXISTS codex_tail_burst_sessions (
    id BIGSERIAL PRIMARY KEY,
    session_id TEXT NOT NULL UNIQUE,
    account_id BIGINT NOT NULL,
    account_name TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NOT NULL,
    ended_at TIMESTAMPTZ,
    quota_window VARCHAR(8) NOT NULL DEFAULT '',
    reason VARCHAR(32) NOT NULL DEFAULT '',
    entry_percent NUMERIC(8,3) NOT NULL DEFAULT 0,
    delta_percent NUMERIC(8,3) NOT NULL DEFAULT 0,
    requests BIGINT NOT NULL DEFAULT 0,
    tokens BIGINT NOT NULL DEFAULT 0,
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT codex_tail_burst_sessions_status_check CHECK (status IN ('active', 'settled'))
);

CREATE INDEX IF NOT EXISTS codex_tail_burst_sessions_started_at_idx
    ON codex_tail_burst_sessions (started_at DESC);
CREATE INDEX IF NOT EXISTS codex_tail_burst_sessions_account_started_idx
    ON codex_tail_burst_sessions (account_id, started_at DESC);
CREATE INDEX IF NOT EXISTS codex_tail_burst_sessions_status_started_idx
    ON codex_tail_burst_sessions (status, started_at DESC);
