import { formatDateTime } from '@/utils/format'

const SECOND_MS = 1000
const MINUTE_MS = 60 * SECOND_MS
const HOUR_MS = 60 * MINUTE_MS
const DAY_MS = 24 * HOUR_MS

/**
 * 模型限流的短标签：24 小时以内保留精确小时/分钟，超过 24 小时改为
 * 向上取整天数，避免在账号列表里出现 49h、8761h 这类难读文本。
 */
export function formatModelRateLimitCountdown(resetAt: string, nowMillis = Date.now()): string {
  const resetMillis = Date.parse(resetAt)
  if (!Number.isFinite(resetMillis)) return ''
  const diffMillis = resetMillis - nowMillis
  if (diffMillis <= 0) return ''
  if (diffMillis > DAY_MS) return `${Math.ceil(diffMillis / DAY_MS)}d`

  const totalSeconds = Math.floor(diffMillis / SECOND_MS)
  const hours = Math.floor(totalSeconds / 3600)
  const minutes = Math.floor((totalSeconds % 3600) / 60)
  const seconds = totalSeconds % 60
  if (hours > 0) return `${hours}h${minutes}m`
  if (minutes > 0) return `${minutes}m${seconds}s`
  return `${seconds}s`
}

/** 完整恢复时间使用浏览器本地时区，悬浮时能直接确认具体日期。 */
export function formatModelRateLimitResetAt(resetAt: string): string {
  if (!Number.isFinite(Date.parse(resetAt))) return ''
  return formatDateTime(resetAt)
}
