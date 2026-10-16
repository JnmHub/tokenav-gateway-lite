/* ============================================================
 * 账号表列配置（三期）
 * 旧实现契约：frontend/src/views/admin/AccountsView.vue 的 allColumns /
 * ACCOUNT_SORTABLE_KEYS / DEFAULT_HIDDEN_COLUMNS / HIDDEN_COLUMNS_KEY
 * 列集合与可排序键逐一对照旧版，仅标题改为中文直显（Win12 版不用 i18n）。
 * ============================================================ */

// 全部业务列（不含 select/name/actions——旧版规定这三列不可隐藏）
export const ALL_COLUMNS = [
  { key: 'priority', label: '优先级' },
  { key: 'id', label: 'ID' },
  { key: 'platform_type', label: '平台/类型' },
  { key: 'capacity', label: '并发/负载' },
  { key: 'status', label: '状态' },
  { key: 'schedulable', label: '可调度' },
  { key: 'usage', label: '用量窗口' },
  { key: 'today_stats', label: '今日统计' },
  { key: 'groups', label: '分组' },
  { key: 'proxy', label: '代理' },
  { key: 'rate_multiplier', label: '计费倍率' },
  { key: 'last_used_at', label: '最后使用' },
  { key: 'created_at', label: '创建时间' },
  { key: 'expires_at', label: '过期时间' },
  { key: 'notes', label: '备注' },
]

// 服务端排序白名单（旧 ACCOUNT_SORTABLE_KEYS，参数 sort_by/sort_order）
export const SORTABLE_KEYS = new Set([
  'id', 'name', 'status', 'schedulable', 'priority',
  'rate_multiplier', 'last_used_at', 'created_at', 'expires_at',
])

// 默认只留名称/状态/用量/今日计费；低频列进「显示列」
export const DEFAULT_HIDDEN = [
  'priority', 'id', 'capacity', 'groups', 'proxy', 'rate_multiplier',
  'last_used_at', 'created_at', 'expires_at', 'notes',
]

// v3：把用量、今日统计和周额度预估留在首屏
export const HIDDEN_COLUMNS_KEY = 'w12-account-hidden-columns-v3'
export const SORT_STORAGE_KEY = 'w12-account-sort'
export const AUTO_REFRESH_KEY = 'w12-account-auto-refresh'

// 自动刷新间隔档位（旧 autoRefreshIntervals：5/10/15/30 秒）
export const AUTO_REFRESH_INTERVALS = [5, 10, 15, 30]

// 静默窗口毫秒（编辑等本地操作后暂停自动刷新，旧 AUTO_REFRESH_SILENT_WINDOW_MS）
export const AUTO_REFRESH_SILENT_MS = 15000
