-- 图片生成可持续数分钟；任务和金额预留必须跨进程重启持久化。请求正文只保存
-- AES-GCM（对称加密）密文，API Key、OAuth Token 和上游密钥禁止进入本表。
CREATE TABLE IF NOT EXISTS image_async_tasks (
    id UUID PRIMARY KEY,
    owner_key TEXT NOT NULL,
    user_id BIGINT NOT NULL,
    api_key_id BIGINT NOT NULL DEFAULT 0,
    gateway_key_id TEXT NOT NULL DEFAULT '',
    idempotency_key TEXT NOT NULL DEFAULT '',
    payload_hash CHAR(64) NOT NULL,
    state VARCHAR(20) NOT NULL,
    settlement_state VARCHAR(20) NOT NULL DEFAULT 'pending',
    encrypted_request TEXT NOT NULL,
    task_data JSONB NOT NULL,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    claim_owner TEXT NOT NULL DEFAULT '',
    claim_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT image_async_tasks_state_check CHECK (
        state IN ('queued', 'running', 'succeeded', 'failed', 'cancelled', 'expired')
    ),
    CONSTRAINT image_async_tasks_settlement_state_check CHECK (
        settlement_state IN ('pending', 'settled', 'released')
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_image_async_tasks_idempotency
    ON image_async_tasks (owner_key, idempotency_key)
    WHERE idempotency_key <> '';

CREATE INDEX IF NOT EXISTS idx_image_async_tasks_due
    ON image_async_tasks (next_attempt_at, created_at)
    WHERE settlement_state = 'pending'
       OR state IN ('queued', 'running');

CREATE INDEX IF NOT EXISTS idx_image_async_tasks_owner
    ON image_async_tasks (owner_key, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_image_async_tasks_cleanup
    ON image_async_tasks (expires_at)
    WHERE state IN ('succeeded', 'failed', 'cancelled', 'expired')
      AND settlement_state IN ('settled', 'released');
