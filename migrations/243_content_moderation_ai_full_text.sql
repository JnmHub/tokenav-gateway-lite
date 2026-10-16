-- 原始提取文本与实际送审文本独立保存；input_excerpt 继续作为列表预览。
-- 不从历史摘要回填全文，避免把旧截断内容误标成完整输入。
ALTER TABLE content_moderation_logs
    ADD COLUMN IF NOT EXISTS full_input_text TEXT NOT NULL DEFAULT '';

COMMENT ON COLUMN content_moderation_logs.full_input_text IS
    '完整提取的输入文本；仅管理员日志详情返回，历史记录可能为空';
COMMENT ON COLUMN content_moderation_logs.review_text_excerpt IS
    '完整实际送审文本（沿用旧字段名）；仅管理员日志详情返回，历史记录可能是截断摘要';
