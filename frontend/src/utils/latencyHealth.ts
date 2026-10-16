/**
 * 请求延迟健康度分档。首 Token 更直接影响用户感知，因此阈值比总耗时严格；
 * 总耗时需兼容长流式响应，使用 1 分钟、3 分钟和 5 分钟边界。
 */
export type LatencySeverity = 'good' | 'warn' | 'slow' | 'critical'

export const FIRST_TOKEN_THRESHOLDS_MS = {
  warn: 10_000,
  slow: 30_000,
  critical: 60_000
} as const

export const DURATION_THRESHOLDS_MS = {
  warn: 60_000,
  slow: 180_000,
  critical: 300_000
} as const

interface Thresholds {
  warn: number
  slow: number
  critical: number
}

const classify = (ms: number, thresholds: Thresholds): LatencySeverity => {
  if (ms >= thresholds.critical) return 'critical'
  if (ms >= thresholds.slow) return 'slow'
  if (ms >= thresholds.warn) return 'warn'
  return 'good'
}

export const firstTokenSeverity = (ms: number): LatencySeverity =>
  classify(ms, FIRST_TOKEN_THRESHOLDS_MS)

export const durationSeverity = (ms: number): LatencySeverity =>
  classify(ms, DURATION_THRESHOLDS_MS)

export const LATENCY_TEXT_CLASSES: Record<LatencySeverity, string> = {
  good: 'text-emerald-700 dark:text-emerald-400',
  warn: 'text-amber-700 dark:text-amber-400',
  slow: 'text-orange-700 dark:text-orange-400',
  critical: 'text-red-700 dark:text-red-400'
}

export const LATENCY_BADGE_CLASSES: Record<LatencySeverity, string> = {
  good: 'bg-emerald-50 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300',
  warn: 'bg-amber-50 text-amber-700 dark:bg-amber-500/15 dark:text-amber-300',
  slow: 'bg-orange-50 text-orange-700 dark:bg-orange-500/15 dark:text-orange-300',
  critical: 'bg-red-50 text-red-700 dark:bg-red-500/15 dark:text-red-300'
}

export const LATENCY_BAR_CLASSES: Record<LatencySeverity, string> = {
  good: 'bg-emerald-600 dark:bg-emerald-400',
  warn: 'bg-amber-600 dark:bg-amber-400',
  slow: 'bg-orange-600 dark:bg-orange-400',
  critical: 'bg-red-600 dark:bg-red-400'
}

export const LATENCY_BAR_FROM_CLASSES: Record<LatencySeverity, string> = {
  good: 'from-emerald-500',
  warn: 'from-amber-400',
  slow: 'from-orange-500',
  critical: 'from-red-500'
}

export const LATENCY_BAR_TO_CLASSES: Record<LatencySeverity, string> = {
  good: 'to-emerald-500',
  warn: 'to-amber-400',
  slow: 'to-orange-500',
  critical: 'to-red-500'
}
