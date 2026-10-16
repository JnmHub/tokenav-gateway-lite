-- 181/182 为上游 prompt audit（提示词审计）保留编号，本项目明确不引入该功能。
-- 语种拦截仅保存可复核统计和按用户/API Key 隔离、带进程随机盐的 HMAC 指纹，不保存客户原文。

ALTER TABLE content_moderation_logs
    ADD COLUMN IF NOT EXISTS content_fingerprint VARCHAR(80) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS language_evidence JSONB NOT NULL DEFAULT '{}'::jsonb;

-- 旧版本会把语种拦截的客户摘要写入 input_excerpt。新版本承诺该类日志只
-- 保存不可逆指纹和字符统计，因此迁移时同步清空历史值；重复执行结果不变。
UPDATE content_moderation_logs
SET input_excerpt = ''
WHERE action = 'language_block' AND input_excerpt <> '';
