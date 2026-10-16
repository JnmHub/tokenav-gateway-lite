-- [tokenav 定制] 上游原语句为 `ADD COLUMN allow_live`（无 IF NOT EXISTS）。
-- 本仓库自有的 180_add_group_live_and_usage_type.sql 已按字典序先建出同一列，
-- 导致全新空库按文件名顺序迁移时在此处报 `column "allow_live" already exists`，
-- 安装向导与 AUTO_SETUP 全部失败（本地闭环无法复现）。改为幂等语句后：
--   * 全新库：180 建列，此处为无操作；
--   * 只迁移到 180 的历史节点：此处同样为无操作，不再失败；
--   * 已迁移过的库：走 migrations_runner.go 的 checksum 兼容规则放行。
ALTER TABLE groups ADD COLUMN IF NOT EXISTS allow_live BOOLEAN NOT NULL DEFAULT false;
