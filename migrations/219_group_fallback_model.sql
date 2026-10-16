-- 同分组模型兜底默认关闭；只有原模型在选择阶段没有任何可用账号时才由后续调度逻辑尝试一次。
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS fallback_model VARCHAR(100) NOT NULL DEFAULT '';

COMMENT ON COLUMN groups.fallback_model IS
    '同分组模型兜底：原模型没有可用账号时才尝试一次；空字符串表示关闭';
