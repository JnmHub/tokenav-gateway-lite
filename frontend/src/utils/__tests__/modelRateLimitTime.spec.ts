import { describe, expect, it } from 'vitest'
import { formatModelRateLimitCountdown } from '../modelRateLimitTime'

describe('formatModelRateLimitCountdown', () => {
  const now = Date.parse('2026-07-23T00:00:00Z')

  it.each([
    ['2026-07-23T23:59:00Z', '23h59m'],
    ['2026-07-24T00:00:00Z', '24h0m'],
    ['2026-07-24T00:00:01Z', '2d'],
    ['2026-07-25T01:00:00Z', '3d'],
    ['2027-07-24T00:00:00Z', '366d'],
  ])('按 24 小时边界格式化 %s', (resetAt, expected) => {
    expect(formatModelRateLimitCountdown(resetAt, now)).toBe(expected)
  })

  it.each(['invalid', '2026-07-22T23:59:00Z'])('无效或已过期时间不显示：%s', resetAt => {
    expect(formatModelRateLimitCountdown(resetAt, now)).toBe('')
  })
})
