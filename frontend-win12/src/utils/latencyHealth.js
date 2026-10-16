/**
 * 请求延迟健康度分档（对照旧 utils/latencyHealth.ts，Win12 色彩语义）。
 * 首 Token 直接影响用户感知，阈值比总耗时严格；总耗时兼容长流式响应，
 * 用 1 / 3 / 5 分钟边界。分档：good 正常 / warn 注意 / slow 缓慢 / critical 严重。
 */
export const FIRST_TOKEN_THRESHOLDS_MS = { warn: 10_000, slow: 30_000, critical: 60_000 }
export const DURATION_THRESHOLDS_MS = { warn: 60_000, slow: 180_000, critical: 300_000 }

const classify = (ms, t) => {
  if (ms >= t.critical) return 'critical'
  if (ms >= t.slow) return 'slow'
  if (ms >= t.warn) return 'warn'
  return 'good'
}

export const firstTokenSeverity = (ms) => classify(ms, FIRST_TOKEN_THRESHOLDS_MS)
export const durationSeverity = (ms) => classify(ms, DURATION_THRESHOLDS_MS)

// 各分档颜色（竖条 / 数值文字 / 徽章底色）——低饱和 Win12 语义色
export const LATENCY_COLORS = {
  good: { bar: '#2e7d32', text: '#1f7a33', badge: 'rgba(46,125,50,0.12)', label: '正常' },
  warn: { bar: '#b45309', text: '#9a5b00', badge: 'rgba(217,119,6,0.14)', label: '偏慢' },
  slow: { bar: '#c2410c', text: '#bb3e0a', badge: 'rgba(234,88,12,0.14)', label: '缓慢' },
  critical: { bar: '#c42b1c', text: '#c42b1c', badge: 'rgba(196,43,28,0.14)', label: '严重' },
}

// 首 Token 段占竖条高度百分比（总耗时不足时取首 Token 值）
export const firstTokenPercent = (firstTokenMs, durationMs) => {
  if (firstTokenMs == null) return 0
  const total = Math.max(durationMs ?? 0, firstTokenMs)
  if (total <= 0) return 0
  return Math.min(100, Math.max(0, (firstTokenMs / total) * 100))
}

export const formatDuration = (ms) => {
  if (ms == null) return '-'
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(2)}s`
}
