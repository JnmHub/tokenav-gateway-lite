import { describe, expect, it } from 'vitest'

import { formatDateTime, formatHistoryLabel } from '../opsFormatters'

describe('opsFormatters 上海时区', () => {
  it('在美国浏览器中仍按上海时间展示绝对时间', () => {
    const originalTimeZone = process.env.TZ
    process.env.TZ = 'America/Los_Angeles'
    try {
      expect(formatDateTime('2026-08-03T16:30:45.000Z')).toBe('08-04 00:30:45')
      expect(formatHistoryLabel('2026-08-03T16:30:45.000Z', '24h')).toBe('08-04 00:30')
    } finally {
      if (originalTimeZone === undefined) delete process.env.TZ
      else process.env.TZ = originalTimeZone
    }
  })
})
