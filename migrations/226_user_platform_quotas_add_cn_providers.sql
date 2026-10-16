-- 国产供应商进入默认平台额度列表后，数据库约束也必须同步放开。
-- 默认额度采用单条多行 INSERT，遗漏任一平台会让整批额度写入失败。
ALTER TABLE user_platform_quotas
    DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check;

ALTER TABLE user_platform_quotas
    ADD CONSTRAINT user_platform_quotas_platform_check
    CHECK (platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok',
                        'kimi', 'zhipu', 'deepseek')) NOT VALID;

ALTER TABLE user_platform_quotas
    VALIDATE CONSTRAINT user_platform_quotas_platform_check;
