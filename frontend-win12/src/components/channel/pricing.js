/* ============================================================
 * 渠道模型定价共享工具：单位换算 / 区间转换 / 冲突与区间校验
 * 契约来源：旧 frontend/src/components/admin/channel/types.ts
 * 后端结构：internal/handler/admin/channel_handler.go
 *   channelModelPricingRequest / pricingIntervalRequest
 * 价格单位约定：后端存 per-token，前端显示 $/MTok（$/百万 token）
 * ============================================================ */

const MTOK = 1_000_000

/** 空串/null/非数字 → null，其余转数字 */
export function toNullableNumber(val) {
  if (val === null || val === undefined || val === '') return null
  const num = Number(val)
  return Number.isNaN(num) ? null : num
}

/** 倍率合法性：空视为未配置（合法），配置了则必须 > 0 */
export function isValidPositiveMultiplier(val) {
  if (val === null || val === undefined || val === '') return true
  const multiplier = Number(val)
  return Number.isFinite(multiplier) && multiplier > 0
}

/** 前端显示值（$/MTok）→ 后端存储值（per-token） */
export function mTokToPerToken(val) {
  const num = toNullableNumber(val)
  return num === null ? null : parseFloat((num / MTOK).toPrecision(10))
}

/** 后端存储值（per-token）→ 前端显示值（$/MTok）
 *  toPrecision(10) 消除 IEEE 754 浮点乘法误差（如 5e-8 * 1e6 = 0.04999...96 → 0.05） */
export function perTokenToMTok(val) {
  if (val === null || val === undefined) return null
  return parseFloat((val * MTOK).toPrecision(10))
}

/** 空白区间条目工厂（token / per_request / image 共用结构，对照旧 IntervalFormEntry） */
export function emptyInterval(sortOrder = 0) {
  return {
    min_tokens: 0,
    max_tokens: null,
    tier_label: '',
    input_price: null,
    output_price: null,
    cache_write_price: null,
    cache_write_1h_price: null,
    cache_read_price: null,
    input_multiplier: null,
    output_multiplier: null,
    cache_write_multiplier: null,
    cache_read_multiplier: null,
    per_request_price: null,
    sort_order: sortOrder,
  }
}

/** 空白定价条目工厂（对照旧 addPricingEntry） */
export function emptyPricingEntry() {
  return {
    models: [],
    billing_mode: 'token',
    input_price: null,
    output_price: null,
    cache_write_price: null,
    cache_write_1h_price: null,
    cache_read_price: null,
    fast_multiplier: null,
    flex_multiplier: null,
    max_reasoning_effort_multiplier: null,
    image_input_price: null,
    image_output_price: null,
    per_request_price: null,
    intervals: [],
    time_pricing: createDefaultTimePricingForm(),
  }
}

/** 后端 intervals → 表单 intervals（价格 per-token → $/MTok） */
export function apiIntervalsToForm(intervals) {
  return (intervals || []).map((iv) => ({
    min_tokens: iv.min_tokens,
    max_tokens: iv.max_tokens,
    tier_label: iv.tier_label || '',
    input_price: perTokenToMTok(iv.input_price),
    output_price: perTokenToMTok(iv.output_price),
    cache_write_price: perTokenToMTok(iv.cache_write_price),
    cache_write_1h_price: perTokenToMTok(iv.cache_write_1h_price),
    cache_read_price: perTokenToMTok(iv.cache_read_price),
    input_multiplier: iv.input_multiplier,
    output_multiplier: iv.output_multiplier,
    cache_write_multiplier: iv.cache_write_multiplier,
    cache_read_multiplier: iv.cache_read_multiplier,
    per_request_price: iv.per_request_price,
    sort_order: iv.sort_order,
  }))
}

/** 表单 intervals → 后端 intervals（价格 $/MTok → per-token） */
export function formIntervalsToAPI(intervals) {
  return (intervals || []).map((iv) => ({
    min_tokens: iv.min_tokens,
    max_tokens: iv.max_tokens,
    tier_label: iv.tier_label,
    input_price: mTokToPerToken(iv.input_price),
    output_price: mTokToPerToken(iv.output_price),
    cache_write_price: mTokToPerToken(iv.cache_write_price),
    cache_write_1h_price: mTokToPerToken(iv.cache_write_1h_price),
    cache_read_price: mTokToPerToken(iv.cache_read_price),
    input_multiplier: toNullableNumber(iv.input_multiplier),
    output_multiplier: toNullableNumber(iv.output_multiplier),
    cache_write_multiplier: toNullableNumber(iv.cache_write_multiplier),
    cache_read_multiplier: toNullableNumber(iv.cache_read_multiplier),
    per_request_price: toNullableNumber(iv.per_request_price),
    sort_order: iv.sort_order,
  }))
}

/* ── 模型模式冲突检测（支持尾部 * 通配符，对照旧 types.ts 101-136） ── */

function toModelPattern(model) {
  const lower = String(model).toLowerCase()
  const wildcard = lower.endsWith('*')
  return { pattern: model, prefix: wildcard ? lower.slice(0, -1) : lower, wildcard }
}

function patternsConflict(a, b) {
  if (!a.wildcard && !b.wildcard) return a.prefix === b.prefix
  if (a.wildcard && !b.wildcard) return b.prefix.startsWith(a.prefix)
  if (!a.wildcard && b.wildcard) return a.prefix.startsWith(b.prefix)
  // 双通配符：任一前缀是另一前缀的前缀即冲突
  return a.prefix.startsWith(b.prefix) || b.prefix.startsWith(a.prefix)
}

/** 检测模型模式列表中的冲突，返回冲突的两个模式名；无冲突返回 null */
export function findModelConflict(models) {
  const patterns = models.map(toModelPattern)
  for (let i = 0; i < patterns.length; i++) {
    for (let j = i + 1; j < patterns.length; j++) {
      if (patternsConflict(patterns[i], patterns[j])) {
        return [patterns[i].pattern, patterns[j].pattern]
      }
    }
  }
  return null
}

/* ── 区间校验（对照旧 types.ts 138-228） ──
 * mode 决定区间语义：
 * - token：上下文 token 数分段 (min, max]，不能重叠，无上限段必须放最后
 * - per_request / image：按 tier_label 分层匹配，跳过重叠 / 无上限校验 */

export function validateIntervals(intervals, mode = 'token') {
  if (!intervals || intervals.length === 0) return null
  // 按 min_tokens 排序（不修改原数组）
  const sorted = [...intervals].sort((a, b) => a.min_tokens - b.min_tokens)
  for (let i = 0; i < sorted.length; i++) {
    const err = validateSingleInterval(sorted[i], i)
    if (err) return err
  }
  if (mode !== 'token') return null
  return checkIntervalOverlap(sorted)
}

function validateSingleInterval(iv, idx) {
  if (iv.min_tokens < 0) {
    return `区间 #${idx + 1}: 最小 token 数 (${iv.min_tokens}) 不能为负数`
  }
  if (iv.max_tokens != null) {
    if (iv.max_tokens <= 0) {
      return `区间 #${idx + 1}: 最大 token 数 (${iv.max_tokens}) 必须大于 0`
    }
    if (iv.max_tokens <= iv.min_tokens) {
      return `区间 #${idx + 1}: 最大 token 数 (${iv.max_tokens}) 必须大于最小 token 数 (${iv.min_tokens})`
    }
  }
  return validateIntervalPrices(iv, idx)
}

function validateIntervalPrices(iv, idx) {
  const prices = [
    ['输入价格', iv.input_price],
    ['输出价格', iv.output_price],
    ['缓存写入价格', iv.cache_write_price],
    ['缓存写入 1h 价格', iv.cache_write_1h_price],
    ['缓存读取价格', iv.cache_read_price],
    ['单次价格', iv.per_request_price],
  ]
  const multipliers = [
    ['输入倍率', iv.input_multiplier],
    ['输出倍率', iv.output_multiplier],
    ['缓存写入倍率', iv.cache_write_multiplier],
    ['缓存读取倍率', iv.cache_read_multiplier],
  ]
  const hasPrice = prices.some(([, v]) => v != null && v !== '')
  const hasMultiplier = multipliers.some(([, v]) => v != null && v !== '')
  if (!hasPrice && !hasMultiplier) {
    return `区间 #${idx + 1}: 至少填写一个价格`
  }
  for (const [name, val] of prices) {
    if (val != null && val !== '' && Number(val) < 0) {
      return `区间 #${idx + 1}: ${name}不能为负数`
    }
  }
  for (const [name, val] of multipliers) {
    if (!isValidPositiveMultiplier(val)) {
      return `区间 #${idx + 1}: ${name}必须大于 0`
    }
  }
  return null
}

function checkIntervalOverlap(sorted) {
  for (let i = 0; i < sorted.length; i++) {
    // 无上限区间（max 为空）只能是最后一个
    if (sorted[i].max_tokens == null && i < sorted.length - 1) {
      return `区间 #${i + 1}: 无上限区间（最大 token 数为空）只能是最后一个`
    }
    if (i === 0) continue
    const prev = sorted[i - 1]
    // (min, max] 语义：前一个区间上界 > 当前区间下界则重叠
    if (prev.max_tokens == null || prev.max_tokens > sorted[i].min_tokens) {
      const prevMax = prev.max_tokens == null ? '∞' : String(prev.max_tokens)
      return `区间 #${i} 和 #${i + 1} 重叠：前一个区间上界 (${prevMax}) 大于当前区间下界 (${sorted[i].min_tokens})`
    }
  }
  return null
}

/* ── 平台目录与配色 ──
 * 平台清单对照旧 constants/platforms.ts CONCRETE_PLATFORM_OPTIONS；
 * 配色延续旧 getPlatformTagClass 色系，改为低饱和半透明底（Win12 状态标签规范） */

export const PLATFORM_META = {
  anthropic: { label: 'Anthropic', color: '#a05a12', bg: 'rgba(194,116,31,.14)' },
  openai: { label: 'OpenAI', color: '#1d7a4f', bg: 'rgba(29,122,79,.14)' },
  gemini: { label: 'Gemini', color: '#1f5e96', bg: 'rgba(59,145,216,.14)' },
  antigravity: { label: 'Antigravity', color: '#7a3fa8', bg: 'rgba(122,63,168,.14)' },
  grok: { label: 'Grok', color: '#4a5568', bg: 'rgba(74,85,104,.16)' },
  kimi: { label: 'Kimi', color: '#0e6e6e', bg: 'rgba(14,110,110,.14)' },
  zhipu: { label: '智谱 GLM', color: '#2b5fd9', bg: 'rgba(43,95,217,.14)' },
  deepseek: { label: 'DeepSeek', color: '#3b5bd9', bg: 'rgba(59,91,217,.14)' },
}

export const PLATFORM_ORDER = Object.keys(PLATFORM_META)

export function platformLabel(p) {
  return PLATFORM_META[p]?.label || p
}

/** 平台 tag 内联样式（ModelTagInput / 折叠摘要共用） */
export function platformTagStyle(platform) {
  const meta = PLATFORM_META[platform]
  if (!meta) return {}
  return { color: meta.color, background: meta.bg }
}

/** 计费模式目录（对照旧 PricingEntryCard billingModeOptions） */
export const BILLING_MODES = [
  { value: 'token', label: 'Token' },
  { value: 'per_request', label: '按次' },
  { value: 'image', label: '图片（按次）' },
  { value: 'video', label: '视频（按秒）' },
]

export function billingModeLabel(mode) {
  return BILLING_MODES.find((o) => o.value === mode)?.label || mode
}

/* ── 分时计价（v0.2.5，对照官方 channel/types.ts）──
 * 后端契约：service.ChannelTimePricing { timezone, weekdays_only, periods[{start_time,end_time,multiplier}] }
 * 时区格式：IANA 名称（Asia/Shanghai 等），后端 loadChannelTimePricingLocation 用 time.LoadLocation 解析 */

export const DEFAULT_TIME_PRICING_TIMEZONE = 'Asia/Shanghai'

const CLOCK_TIME = /^(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d$/
const LEGACY_CLOCK_TIME = /^(?:[01]\d|2[0-3]):[0-5]\d$/
const TWO_DECIMAL_MULTIPLIER = /^\d+(?:\.\d{1,2})?$/

export const COMMON_TIMEZONES = [
  'UTC', 'Asia/Shanghai', 'Asia/Tokyo', 'Asia/Seoul', 'Asia/Singapore', 'Asia/Kolkata',
  'Australia/Sydney', 'Europe/London', 'Europe/Paris', 'Europe/Berlin',
  'America/New_York', 'America/Chicago', 'America/Denver', 'America/Los_Angeles',
  'America/Toronto', 'America/Sao_Paulo', 'Pacific/Auckland', 'Pacific/Honolulu',
]

/** 分时倍率合法性：两位小数且 > 0（与官方 isValidTimePricingMultiplier 一致） */
export function isValidTimePricingMultiplier(value) {
  const s = String(value)
  const num = Number(s)
  return TWO_DECIMAL_MULTIPLIER.test(s) && Number.isFinite(num) && num > 0
}

/** 时区当前 UTC 偏移标签（如 UTC+08:00），用于下拉展示 */
export function formatTimezoneOffset(timezone, at = new Date()) {
  try {
    const part = new Intl.DateTimeFormat('en-US', { timeZone: timezone, timeZoneName: 'shortOffset' })
      .formatToParts(at)
      .find((item) => item.type === 'timeZoneName')?.value
    if (!part || part === 'GMT') return 'UTC+00:00'
    const match = /^GMT([+-])(\d{1,2})(?::(\d{2}))?$/.exec(part)
    if (!match) return ''
    return `UTC${match[1]}${match[2].padStart(2, '0')}:${match[3] || '00'}`
  } catch {
    return ''
  }
}

export function createDefaultTimePricingForm() {
  return { timezone: DEFAULT_TIME_PRICING_TIMEZONE, weekdays_only: false, periods: [] }
}

/** 后端 time_pricing → 表单：HH:mm 补秒、倍率统一两位小数 */
export function apiTimePricingToForm(value) {
  if (!value) return createDefaultTimePricingForm()
  return {
    timezone: value.timezone || DEFAULT_TIME_PRICING_TIMEZONE,
    weekdays_only: value.weekdays_only === true,
    periods: (value.periods || []).map((period) => ({
      start_time: LEGACY_CLOCK_TIME.test(period.start_time) ? `${period.start_time}:00` : period.start_time,
      end_time: LEGACY_CLOCK_TIME.test(period.end_time) ? `${period.end_time}:00` : period.end_time,
      multiplier: Number(period.multiplier).toFixed(2),
    })),
  }
}

/** 表单 → 后端：无时段返回 null（未启用分时） */
export function formTimePricingToAPI(value) {
  if (!value?.periods?.length) return null
  const timezone = typeof value.timezone === 'string' ? value.timezone.trim() : ''
  return {
    timezone,
    weekdays_only: value.weekdays_only === true,
    periods: value.periods.map((period) => ({
      start_time: period.start_time,
      end_time: period.end_time,
      multiplier: Number(period.multiplier),
    })),
  }
}

function timeToSeconds(time, isEnd) {
  // 24h 收口：结束 00:00:00 视为当天 24:00（与官方 timeToSeconds 一致）
  if (isEnd && time === '00:00:00') return 24 * 60 * 60
  const [h, m, s] = time.split(':').map(Number)
  return h * 60 * 60 + m * 60 + s
}

/** 分时计价校验：返回错误消息，通过返回 null（规则对照官方 validateTimePricing） */
export function validateTimePricing(value) {
  if (!value?.periods?.length) return null
  const timezone = typeof value.timezone === 'string' ? value.timezone.trim() : ''
  if (!timezone) return '分时计价：必须选择时区'
  try {
    new Intl.DateTimeFormat('en-US', { timeZone: timezone })
  } catch {
    return `分时计价：时区 '${timezone}' 无效`
  }
  const periods = []
  for (const period of value.periods) {
    if (!CLOCK_TIME.test(period.start_time) || !CLOCK_TIME.test(period.end_time)) {
      return '分时计价：时段格式必须为 HH:mm:ss'
    }
    const start = timeToSeconds(period.start_time, false)
    const end = timeToSeconds(period.end_time, true)
    if (start >= end) return `分时计价：时段 ${period.start_time} - ${period.end_time} 起止时间无效`
    if (!isValidTimePricingMultiplier(period.multiplier)) {
      return '分时计价：倍率必须是大于 0 的数值（最多两位小数）'
    }
    periods.push({ start, end })
  }
  const sorted = [...periods].sort((a, b) => a.start - b.start)
  for (let i = 1; i < sorted.length; i++) {
    if (sorted[i].start < sorted[i - 1].end) return '分时计价：时段之间存在重叠'
  }
  return null
}
