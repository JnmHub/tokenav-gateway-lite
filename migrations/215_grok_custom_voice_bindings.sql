-- xAI custom voice IDs are account-scoped provider resources. Persist the
-- exact customer/API-key owner and upstream account so later CRUD operations
-- cannot leak across tenants or drift to a different credential.
CREATE TABLE IF NOT EXISTS grok_custom_voice_bindings (
    voice_id TEXT PRIMARY KEY,
    user_id BIGINT NOT NULL,
    api_key_id BIGINT,
    gateway_key_id TEXT,
    group_id BIGINT,
    account_id BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT grok_custom_voice_owner_key_chk CHECK (
        (api_key_id IS NOT NULL AND api_key_id > 0 AND gateway_key_id IS NULL)
        OR (api_key_id IS NULL AND gateway_key_id IS NOT NULL AND gateway_key_id <> '')
    )
);

CREATE INDEX IF NOT EXISTS idx_grok_custom_voice_bindings_local_owner
    ON grok_custom_voice_bindings (user_id, api_key_id, created_at)
    WHERE api_key_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_grok_custom_voice_bindings_remote_owner
    ON grok_custom_voice_bindings (user_id, gateway_key_id, created_at)
    WHERE gateway_key_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_grok_custom_voice_bindings_account
    ON grok_custom_voice_bindings (account_id);
