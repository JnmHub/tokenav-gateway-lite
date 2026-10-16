-- 230: usage_logs 加 fake_first_token_ms 列（伪造流式首字功能）
-- 记录网关向客户端发送 SSE 注释行的时刻（距请求开始的毫秒数），
-- 与 first_token_ms（上游真实首字）并列展示。
-- 幂等：ADD COLUMN IF NOT EXISTS。
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS fake_first_token_ms INTEGER;
