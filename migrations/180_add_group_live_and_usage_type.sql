-- ChatGPT Live 默认关闭；只有节点总开关和分组开关同时开启才会进入请求链路。
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS allow_live BOOLEAN NOT NULL DEFAULT FALSE;

-- 4=cyber，5=live。重建约束同时修正旧约束仍只允许 0..3 的历史缺口。
ALTER TABLE usage_logs
    DROP CONSTRAINT IF EXISTS usage_logs_request_type_check;

ALTER TABLE usage_logs
    ADD CONSTRAINT usage_logs_request_type_check
    CHECK (request_type IN (0, 1, 2, 3, 4, 5));
