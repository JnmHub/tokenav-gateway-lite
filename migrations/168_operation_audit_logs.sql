-- 通用后台操作审计。禁止保存请求体、凭据、完整 User-Agent 或带 userinfo/query secret 的 URL。
CREATE TABLE IF NOT EXISTS operation_audit_logs (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    actor_user_id BIGINT,
    actor_email VARCHAR(255) NOT NULL DEFAULT '',
    actor_role VARCHAR(32) NOT NULL DEFAULT '',
    auth_method VARCHAR(32) NOT NULL DEFAULT '',
    action VARCHAR(128) NOT NULL,
    resource_type VARCHAR(64) NOT NULL DEFAULT '',
    resource_id VARCHAR(128) NOT NULL DEFAULT '',
    result VARCHAR(24) NOT NULL,
    method VARCHAR(12) NOT NULL DEFAULT '',
    path VARCHAR(512) NOT NULL DEFAULT '',
    request_id VARCHAR(128) NOT NULL DEFAULT '',
    client_ip VARCHAR(64) NOT NULL DEFAULT '',
    ua_hash VARCHAR(64) NOT NULL DEFAULT '',
    status_code INTEGER NOT NULL DEFAULT 0,
    latency_ms BIGINT NOT NULL DEFAULT 0,
    summary JSONB NOT NULL DEFAULT '{}'::jsonb,
    extra JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS idx_operation_audit_logs_id_desc
    ON operation_audit_logs (id DESC);
CREATE INDEX IF NOT EXISTS idx_operation_audit_logs_created_id
    ON operation_audit_logs (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_operation_audit_logs_actor_created
    ON operation_audit_logs (actor_user_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_operation_audit_logs_action_created
    ON operation_audit_logs (action, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_operation_audit_logs_resource_created
    ON operation_audit_logs (resource_type, resource_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_operation_audit_logs_result_created
    ON operation_audit_logs (result, created_at DESC, id DESC);
