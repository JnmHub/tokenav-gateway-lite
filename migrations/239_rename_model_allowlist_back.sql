-- [tokenav 定制] 239: 官方迁移 235/236 将 models_list_config 重命名为 model_allowlist，
-- 但本地代码与 win12 前端全量使用 models_list_config 语义。此处恢复本仓库既定列名。
-- （官方 235 的语义扩展——展示白名单演进——由本仓库 models_list_config 字段继续承载。）
ALTER TABLE groups RENAME COLUMN model_allowlist TO models_list_config;
