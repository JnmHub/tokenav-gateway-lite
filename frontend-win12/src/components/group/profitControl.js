/* ============================================================
 * 分组利润控制表单助手（对照官方 v0.2.5 views/admin/groupsProfitControl.ts）
 * 后端按小数存 decimal（0.30 = 30%），界面按百分比输入展示；
 * 固定换算精度，避免 0.3 * 100 = 30.000000000000004 之类浮点尾数回显。
 * ============================================================ */

// 百分比 → 小数（0.30），非法/非正值归 0；*100 后 round 到 4 位小数
export const profitPercentToDecimal = (value) => {
  const parsed = Number(value)
  if (!Number.isFinite(parsed) || parsed <= 0) return 0
  return Math.round(parsed * 100) / 10000
}

// 小数 → 百分比（30），非法/非正值归 0
export const profitDecimalToPercent = (value) => {
  const parsed = Number(value)
  if (!Number.isFinite(parsed) || parsed <= 0) return 0
  return Math.round(parsed * 1e6) / 1e4
}

// 利润控制仅对这五个平台的 token 调度生效（与官方 isProfitControlPlatform 一致）
export const isProfitControlPlatform = (platform) =>
  ['openai', 'anthropic', 'gemini', 'grok', 'antigravity'].includes(platform)

/**
 * 保存前校验（照官方 validateProfitControlFormState 语义）：
 * 开关开启时 margin/buffer 各自 ∈ [0,1)，且相加 < 1（否则阈值 <= 0，
 * 所有可核价账号都会被排除）。返回 null 表示通过，否则返回错误 key。
 * 上界必须按换算后的小数判断（99.999% 会被进位成 1.0，按百分比判会放行）。
 */
export const validateProfitControl = (form) => {
  if (!isProfitControlPlatform(form.platform) || !form.profit_control_enabled) return null
  const margin = Number(form.profit_min_margin) || 0
  const buffer = Number(form.profit_safety_buffer) || 0
  if (!Number.isFinite(margin) || margin < 0 || margin >= 1) return 'marginRangeError'
  if (!Number.isFinite(buffer) || buffer < 0 || buffer >= 1) return 'bufferRangeError'
  if (margin + buffer >= 1) return 'sumTooHigh'
  return null
}

// 校验错误 key → 中文提示（对应官方 i18n admin.groups.profitControl.*）
export const PROFIT_CONTROL_ERROR_MESSAGES = {
  marginRangeError: '最低毛利率应在 0 到 99.99 之间',
  bufferRangeError: '安全缓冲应在 0 到 99.99 之间',
  sumTooHigh: '最低毛利率与安全缓冲之和必须小于 100%，否则将排除全部账号',
}
