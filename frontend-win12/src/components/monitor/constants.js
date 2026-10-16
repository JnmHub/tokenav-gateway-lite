/* ============================================================
 * 渠道监控共享常量与格式化（Win12 重构版）
 * 契约来源（全部经旧代码 + 真实后端核实，禁止臆造）：
 *  - frontend/src/constants/channelMonitor.ts（provider/api_mode/status/Grok 默认值）
 *  - frontend/src/api/admin/channelMonitor.ts（类型定义）
 *  - frontend/src/components/admin/monitor/ChannelMonitorV2Panel.vue（V2 枚举与格式化）
 *  - 后端 internal/service/channel_monitor_const.go（interval 上下限 15-3600、状态枚举）
 *  - 后端 internal/service/channel_monitor_v2.go（V2 错误分类全量枚举、range 列表）
 * ============================================================ */

/* provider 枚举（ent enum 实际值，新建默认 anthropic —— 与旧表单一一致） */
export const PROVIDERS = ['anthropic', 'openai', 'gemini', 'grok']

export const PROVIDER_LABELS = {
  anthropic: 'Anthropic',
  openai: 'OpenAI',
  gemini: 'Gemini',
  grok: 'Grok',
}

/* provider 徽标低饱和配色（语义沿用旧 providerBadgeClass：绿/橙/蓝/深灰） */
export const PROVIDER_META = {
  openai: { color: '#047857', bg: 'rgba(16,185,129,.14)' },
  anthropic: { color: '#c2410c', bg: 'rgba(249,115,22,.14)' },
  gemini: { color: '#0369a1', bg: 'rgba(14,165,233,.14)' },
  grok: { color: '#3f3f3f', bg: 'rgba(120,120,120,.22)' },
}

/* api_mode：responses 仅 openai 支持（后端 validateAPIMode），其余 provider 强制 chat_completions */
export const API_MODE_CHAT_COMPLETIONS = 'chat_completions'
export const API_MODE_RESPONSES = 'responses'

export const API_MODE_OPTIONS = [
  { value: API_MODE_CHAT_COMPLETIONS, label: 'OpenAI Compatible', hint: '使用 /v1/chat/completions，发送 messages；适合大多数兼容站。' },
  { value: API_MODE_RESPONSES, label: 'Responses API', hint: '使用 /v1/responses，默认带 instructions + input；适合本站自检/Codex。' },
]

/* 检测状态四态（后端 ent enum；'' = 尚无检测记录） */
export const STATUS_META = {
  operational: { label: '正常', color: 'green' },
  degraded: { label: '降级', color: 'orange' },
  failed: { label: '失败', color: 'red' },
  error: { label: '错误', color: 'red' },
}
export const statusLabel = (s) => STATUS_META[s]?.label || '-'
export const statusColor = (s) => STATUS_META[s]?.color || 'default'

/* 检测间隔上下限与默认值（后端 monitorMinIntervalSeconds=15 / monitorMaxIntervalSeconds=3600；
 * DEFAULT_INTERVAL_SECONDS=60 与旧常量一致；运行时优先取 /settings/public 的系统默认） */
export const MONITOR_MIN_INTERVAL = 15
export const MONITOR_MAX_INTERVAL = 3600
export const DEFAULT_INTERVAL_SECONDS = 60

/* Grok 默认端点/模型（旧 constants + 后端 MonitorDefaultGrokModel='grok-4.5'）
 * 注意端点只允许 origin（后端 validateEndpoint 拒绝带 path 的地址）。 */
export const DEFAULT_GROK_ENDPOINT = 'https://api.x.ai'
export const DEFAULT_GROK_MODEL = 'grok-4.5'

/* V2 统计范围（后端 ParseFilter 支持 90m/24h/7d/30d） */
export const V2_RANGES = [
  { value: '90m', label: '90 分钟' },
  { value: '24h', label: '24 小时' },
  { value: '7d', label: '7 天' },
  { value: '30d', label: '30 天' },
]

/* V2 聚合刷新频率（后端强校验只接受 60 / 300，其余值 400） */
export const V2_REFRESH_OPTIONS = [
  { value: 60, label: '每 1 分钟' },
  { value: 300, label: '每 5 分钟' },
]

/* V2 错误分类中文标签：
 * 前 9 类逐一对照旧 ChannelMonitorV2Panel.vue errorLabel()；后 8 类是后端
 * ChannelMonitorV2ErrorCategories 已扩充而旧前端未覆盖的分类，标签按后端
 * 分类语义补充；未知分类回退展示原始值（与旧实现一致）。 */
export const V2_ERROR_LABELS = {
  authentication: '认证失败',
  context_limit: '上下文超限',
  content_policy: '内容策略',
  rate_or_capacity: '限流或容量',
  timeout: '超时',
  transport_or_stream: '传输或流错误',
  upstream_5xx: '上游 5xx',
  internal: '内部错误',
  other: '其他',
  // ---- 后端已扩充、旧前端未覆盖的分类 ----
  invalid_request: '无效请求',
  model_unsupported: '模型不支持',
  group_access: '分组无权限',
  quota_or_balance: '配额或余额不足',
  account_pool_unavailable: '账号池不可用',
  upstream_forbidden: '上游禁止访问',
  not_found: '资源不存在',
  client_cancelled: '客户端取消',
}
export const errorLabel = (c) => V2_ERROR_LABELS[c] || c

/* ---------- 数值格式化（对照旧 panel 的 number/decimal/percent/milliseconds） ---------- */
const nf = (max) => new Intl.NumberFormat('zh-CN', { maximumFractionDigits: max })

export const fmtNum = (v) => nf(1).format(v || 0)
export const fmtDec = (v) => nf(2).format(v || 0)

/* 百分比：入参为 0-1 小数；极小正值显示 <0.1%（与旧实现一致） */
export const fmtPercent = (v) => {
  const p = Math.max(0, v || 0) * 100
  return `${p < 0.1 && p > 0 ? '<0.1' : p.toFixed(1)}%`
}

export const fmtMs = (v) => (v == null ? '—' : `${fmtNum(v)} ms`)

/* 时间戳 → MM-DD HH:mm（趋势图横轴与「数据更新至」共用） */
export const fmtTime = (iso) => {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '-'
  const p = (n) => String(n).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

/* 可用率：无检测记录时展示 '-'（对照旧 formatAvailability 的语义） */
export const fmtAvailability = (row) => {
  if (!row || !row.primary_status) return '-'
  const v = Number(row.availability_7d)
  return Number.isNaN(v) ? '-' : `${v.toFixed(2)}%`
}

/* ---------- 高级请求配置（body_override / extra_headers） ----------
 * 枚举与校验规则镜像后端 channel_monitor_template_service.go：
 *  - body_override_mode oneof=off merge replace；validateBodyModeParams 要求
 *    merge/replace 必须携带非空 body_override（空体保存会被 400 拒绝）。
 *  - header 名需匹配 headerNameRegex（RFC 7230 token），且不在
 *    forbiddenHeaderNames（hop-by-hop + 客户端自管头）黑名单内。 */
export const BODY_OVERRIDE_MODES = [
  { value: 'off', label: '默认', hint: '使用默认生成的健康检查请求体。' },
  { value: 'merge', label: '合并', hint: '与默认请求体浅合并，用户字段优先；model / messages / contents 受保护不允许覆盖。' },
  { value: 'replace', label: '覆盖', hint: '完全用下方 JSON 作为请求体；跳过默认 challenge 校验，HTTP 2xx + 响应非空即视为可用。' },
]
export const bodyOverrideModeMeta = (mode) =>
  BODY_OVERRIDE_MODES.find((m) => m.value === mode) || BODY_OVERRIDE_MODES[0]

export const HEADER_NAME_RE = /^[A-Za-z0-9!#$%&'*+\-.^_`|~]+$/
export const FORBIDDEN_HEADERS = ['host', 'content-length', 'content-encoding', 'transfer-encoding', 'connection']
