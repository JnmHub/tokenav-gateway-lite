import { describe, expect, it } from 'vitest'
import {
  durationSeverity,
  firstTokenSeverity,
  LATENCY_BAR_CLASSES,
  LATENCY_TEXT_CLASSES,
} from '../latencyHealth'

describe('latencyHealth', () => {
  it('classifies first-token latency at 10s, 30s and 60s boundaries', () => {
    expect(firstTokenSeverity(0)).toBe('good')
    expect(firstTokenSeverity(9_999)).toBe('good')
    expect(firstTokenSeverity(10_000)).toBe('warn')
    expect(firstTokenSeverity(29_999)).toBe('warn')
    expect(firstTokenSeverity(30_000)).toBe('slow')
    expect(firstTokenSeverity(59_999)).toBe('slow')
    expect(firstTokenSeverity(60_000)).toBe('critical')
  })

  it('classifies total duration at 1min, 3min and 5min boundaries', () => {
    expect(durationSeverity(0)).toBe('good')
    expect(durationSeverity(59_999)).toBe('good')
    expect(durationSeverity(60_000)).toBe('warn')
    expect(durationSeverity(179_999)).toBe('warn')
    expect(durationSeverity(180_000)).toBe('slow')
    expect(durationSeverity(299_999)).toBe('slow')
    expect(durationSeverity(300_000)).toBe('critical')
  })

  it('uses darker light-theme text and bar colors while retaining dark-theme variants', () => {
    expect(LATENCY_TEXT_CLASSES).toEqual({
      good: 'text-emerald-700 dark:text-emerald-400',
      warn: 'text-amber-700 dark:text-amber-400',
      slow: 'text-orange-700 dark:text-orange-400',
      critical: 'text-red-700 dark:text-red-400',
    })
    expect(LATENCY_BAR_CLASSES).toEqual({
      good: 'bg-emerald-600 dark:bg-emerald-400',
      warn: 'bg-amber-600 dark:bg-amber-400',
      slow: 'bg-orange-600 dark:bg-orange-400',
      critical: 'bg-red-600 dark:bg-red-400',
    })
  })
})
