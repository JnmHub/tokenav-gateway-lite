-- 智能分流映射：公开模型 -> 带权重的目标模型列表（JSONB）
-- 与 model_mapping 单目标映射互斥；空对象表示未启用，行为与升级前完全一致。
ALTER TABLE groups ADD COLUMN IF NOT EXISTS weighted_model_mapping JSONB NOT NULL DEFAULT '{}'::jsonb;
