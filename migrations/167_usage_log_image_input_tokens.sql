-- 图片输入 token、费用与实际价格来源证据。input_tokens 继续保存总的非缓存输入，
-- image_input_tokens 是其子集；total_cost / actual_cost 口径不变。
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS image_input_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS image_input_cost DECIMAL(20,10) NOT NULL DEFAULT 0;
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS image_input_price DECIMAL(20,12) NOT NULL DEFAULT 0;
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS image_input_price_source VARCHAR(32);
