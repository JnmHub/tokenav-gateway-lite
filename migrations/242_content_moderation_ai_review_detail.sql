-- 内容审查可见性：AI 送审详情与图片审查支持
-- 1) ai_review_detail 保存 AI 审查的完整判定证据（每个片段的 verdict/categories、
--    未覆盖字段清单、图片数量），供管理员复核误判；不含图片字节与密钥。
-- 2) review_text_excerpt 保存实际送审文本的拼接摘录（截断），便于直接核对拦截原因。

ALTER TABLE content_moderation_logs ADD COLUMN IF NOT EXISTS ai_review_detail JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE content_moderation_logs ADD COLUMN IF NOT EXISTS review_text_excerpt TEXT NOT NULL DEFAULT '';
