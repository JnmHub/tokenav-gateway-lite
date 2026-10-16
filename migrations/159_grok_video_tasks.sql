-- Grok 视频生成是异步任务。任务状态和结算上下文属于财务数据，必须持久化到
-- PostgreSQL，不能只依赖可能被淘汰或清空的 Redis 缓存。
CREATE TABLE IF NOT EXISTS grok_video_tasks (
    request_id TEXT PRIMARY KEY,
    state VARCHAR(20) NOT NULL,
    task_data JSONB NOT NULL,
    next_poll_at TIMESTAMPTZ,
    poll_locked_until TIMESTAMPTZ,
    settlement_locked_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    billed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_grok_video_tasks_due
    ON grok_video_tasks (next_poll_at, created_at)
    WHERE state IN ('pending', 'running', 'succeeded');

CREATE INDEX IF NOT EXISTS idx_grok_video_tasks_finalized
    ON grok_video_tasks (completed_at)
    WHERE state IN ('failed', 'cancelled', 'billed');
