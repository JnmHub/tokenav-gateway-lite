import { describe, expect, it } from 'vitest'
import { formatCompactNumber, formatTokenCount } from '../format'

describe('formatCompactNumber', () => {
  it('formats boundary values with K/M/B', () => {
    expect(formatCompactNumber(0)).toBe('0')
    expect(formatCompactNumber(999)).toBe('999')
    expect(formatCompactNumber(1000)).toBe('1.0K')
    expect(formatCompactNumber(999999)).toBe('1000.0K')
    expect(formatCompactNumber(1000000)).toBe('1.0M')
    expect(formatCompactNumber(1000000000)).toBe('1.0B')
  })

  it('supports disabling billion unit (requests style)', () => {
    expect(formatCompactNumber(1000000000, { allowBillions: false })).toBe('1000.0M')
  })

  it('returns 0 for nullish input', () => {
    expect(formatCompactNumber(null)).toBe('0')
    expect(formatCompactNumber(undefined)).toBe('0')
  })
})

describe('formatTokenCount', () => {
  it('uses yi for Chinese values at or above one hundred million', () => {
    expect(formatTokenCount(100_000_000, 'zh-CN')).toBe('1亿')
    expect(formatTokenCount(379_840_000, 'zh-CN')).toBe('3.8亿')
    expect(formatTokenCount(1_213_383_000, 'zh-CN')).toBe('12.13亿')
  })

  it('keeps K M B units outside Chinese locales', () => {
    expect(formatTokenCount(379_840_000, 'en')).toBe('379.84M')
    expect(formatTokenCount(1_213_383_000, 'en')).toBe('1.21B')
  })
})
