/* ============================================================
 * 用量模块共享常量与格式化（Win12 重构版）
 * 从 Usage.vue 平移的通用格式化函数 + 模块级常量；
 * 明细表 / 统计卡 / 图表 / 错误面板 / 清理弹窗 / 展开详情组件共用，避免多处复制。
 * ============================================================ */

/* 浏览器 IANA 时区（如 Asia/Shanghai）：后端 /admin/usage、/admin/usage/stats、
 * /admin/dashboard/* 与清理任务创建都按 timezone 参数解析 start_date/end_date
 * （半开区间 [start, end+1d)），不传则按服务器时区，跨时区会看错「今天」。 */
export const BROWSER_TZ = Intl.DateTimeFormat().resolvedOptions().timeZone || ''

/* 数量：千分位；null/undefined 显示 '-' */
export const fmt = (n) => (n === null || n === undefined ? '-' : Number(n).toLocaleString('zh-CN'))

/* 金额：最多 4 位小数（统计卡/明细成本列口径） */
export const fmt$ = (n) => (n === null || n === undefined ? '-' : `$${parseFloat(Number(n).toFixed(4))}`)

/* ISO 时间 → 'YYYY-MM-DD HH:mm:ss'（后端返回 UTC ISO，直接截断展示与旧版一致） */
export const fmtTime = (t) => (t ? String(t).replace('T', ' ').slice(0, 19) : '-')

/* 令牌缩写：1.2K / 3.4M / 5.6B（统计卡与图表共用） */
export const fmtTokens = (n) => {
  const v = Number(n)
  if (n === null || n === undefined || !Number.isFinite(v)) return '-'
  if (Math.abs(v) >= 1e9) return (v / 1e9).toFixed(2) + 'B'
  if (Math.abs(v) >= 1e6) return (v / 1e6).toFixed(2) + 'M'
  if (Math.abs(v) >= 1e3) return (v / 1e3).toFixed(1) + 'K'
  return String(Math.round(v))
}

/* 耗时：1000ms 以上换算秒（延迟列与统计卡共用） */
export const fmtDur = (ms) => {
  const n = Number(ms)
  if (ms === null || ms === undefined || !Number.isFinite(n)) return '-'
  return n >= 1000 ? `${(n / 1000).toFixed(2)}s` : `${Math.round(n)}ms`
}

/* 成本缩写：排行条形/图表 tooltip 口径（大数 K、小数保留精度） */
export const fmtCost = (v) => {
  const n = Number(v)
  if (v === null || v === undefined || !Number.isFinite(n)) return '0.00'
  if (Math.abs(n) >= 1000) return (n / 1000).toFixed(2) + 'K'
  if (Math.abs(n) >= 1) return n.toFixed(2)
  if (Math.abs(n) >= 0.01) return n.toFixed(3)
  return n.toFixed(4)
}

/* 请求类型选项（后端 ParseUsageRequestType 实测接受；主筛选栏与清理弹窗共用） */
export const REQ_TYPE_OPTS = [
  { value: 'ws_v2', label: 'WS 连接' },
  { value: 'stream', label: '流式' },
  { value: 'sync', label: '同步' },
  { value: 'cyber', label: 'Cyber 拦截' },
]
