-- local_only 将“主站可管理的专属分组”和“仅轻量网关本地使用的分组”分开。
-- 默认 false 保持所有已有分组继续同步主站；开启后仅 glk_ 本地密钥可使用。
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS local_only BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN groups.local_only IS
    '仅允许轻量网关本地 glk_ 密钥使用；不向主站同步';
