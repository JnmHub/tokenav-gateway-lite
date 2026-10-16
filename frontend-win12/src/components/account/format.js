/* ============================================================
 * 账号模块共用格式化工具（三期）
 * 对照旧 frontend/src/utils/format.ts 中账号视图用到的子集。
 * ============================================================ */
import dayjs from 'dayjs'

export const fmtDateTime = (v) => {
  if (!v) return '-'
  const d = dayjs(typeof v === 'number' && v < 1e12 ? v * 1000 : v)
  return d.isValid() ? d.format('YYYY-MM-DD HH:mm') : '-'
}

// 相对时间（旧 formatRelativeTime 简化等价）
export const fmtRelative = (v) => {
  if (!v) return '-'
  const t = dayjs(v).valueOf()
  if (!Number.isFinite(t)) return '-'
  const diff = Date.now() - t
  const abs = Math.abs(diff)
  const suffix = diff >= 0 ? '前' : '后'
  if (abs < 60_000) return '刚刚'
  if (abs < 3_600_000) return `${Math.floor(abs / 60_000)} 分钟${suffix}`
  if (abs < 86_400_000) return `${Math.floor(abs / 3_600_000)} 小时${suffix}`
  if (abs < 30 * 86_400_000) return `${Math.floor(abs / 86_400_000)} 天${suffix}`
  return dayjs(t).format('YYYY-MM-DD')
}

// 倒计时（到未来时间点的剩余，旧 formatCountdown）
export const fmtCountdown = (v) => {
  if (!v) return ''
  const ms = dayjs(v).valueOf() - Date.now()
  if (!Number.isFinite(ms) || ms <= 0) return ''
  const totalSec = Math.floor(ms / 1000)
  const h = Math.floor(totalSec / 3600)
  const m = Math.floor((totalSec % 3600) / 60)
  const s = totalSec % 60
  if (h > 0) return `${h}小时${m}分`
  if (m > 0) return `${m}分${s}秒`
  return `${s}秒`
}

export const fmtNum = (n) => new Intl.NumberFormat('zh-CN').format(Math.round(Number(n) || 0))

export const fmtCost = (n) => `$${(Number(n) || 0).toFixed(4).replace(/\.?0+$/, '')}`

// 表格金额：固定两位，三种计费口径并排时对齐
export const fmtMoney = (n) => `$${(Number(n) || 0).toFixed(2)}`

// 预估总额：过千不加小数，避免 $1116.1428 这种噪声
export const fmtMoneyCompact = (n) => {
  const v = Number(n) || 0
  if (Math.abs(v) >= 1000) return `$${Math.round(v).toLocaleString('en-US')}`
  return `$${v.toFixed(2)}`
}

export const fmtTokens = (n) => {
  const v = Number(n) || 0
  if (v >= 1e9) return `${(v / 1e9).toFixed(2)}B`
  if (v >= 1e6) return `${(v / 1e6).toFixed(2)}M`
  if (v >= 1e3) return `${(v / 1e3).toFixed(1)}K`
  return String(Math.round(v))
}
