-- 渠道监控复制只保存哈希后的 operation identity（操作身份），用于并发幂等和响应不确定时恢复。
-- API Key 仍只存在 api_key_encrypted 加密列，duplicate_operation_id 不进入任何 API。
ALTER TABLE channel_monitors
    ADD COLUMN IF NOT EXISTS duplicate_operation_id VARCHAR(64);

CREATE UNIQUE INDEX IF NOT EXISTS idx_channel_monitors_duplicate_operation_id
    ON channel_monitors (duplicate_operation_id)
    WHERE duplicate_operation_id IS NOT NULL;
