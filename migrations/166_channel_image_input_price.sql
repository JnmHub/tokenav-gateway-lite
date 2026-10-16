-- 渠道/模型图片输入 token 单价。NULL 表示继承系统模型目录中的
-- input_cost_per_image_token；显式 0 表示该渠道对图片输入免费。
ALTER TABLE channel_model_pricing
    ADD COLUMN IF NOT EXISTS image_input_price NUMERIC(20,12);
