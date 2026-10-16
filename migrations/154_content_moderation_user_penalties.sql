-- 内容审计用户处置状态：记录强拦截扣款累计和永久封禁状态。

CREATE TABLE IF NOT EXISTS content_moderation_user_penalties (
    user_id                 BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    blocked_count           INT NOT NULL DEFAULT 0,
    deducted_balance        DECIMAL(20, 8) NOT NULL DEFAULT 0,
    permanently_banned      BOOLEAN NOT NULL DEFAULT FALSE,
    permanent_ban_reason    TEXT NOT NULL DEFAULT '',
    last_blocked_at         TIMESTAMPTZ,
    permanently_banned_at   TIMESTAMPTZ,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_content_moderation_user_penalties_permanent
    ON content_moderation_user_penalties(permanently_banned, updated_at DESC);
