ALTER TABLE content_moderation_logs
    ADD COLUMN IF NOT EXISTS matched_keyword VARCHAR(200) NOT NULL DEFAULT '';

UPDATE content_moderation_logs
SET matched_keyword = ''
WHERE matched_keyword IS NULL;

ALTER TABLE content_moderation_logs
    ALTER COLUMN matched_keyword SET DEFAULT '',
    ALTER COLUMN matched_keyword SET NOT NULL;

COMMENT ON COLUMN content_moderation_logs.matched_keyword IS
    '本次本地关键词命中项；仅供管理员复核误判，不写入用户响应头';
