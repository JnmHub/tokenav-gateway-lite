import { describe, expect, it } from 'vitest'

import {
  dateInputBoundaryToISOString,
  formatDefaultTimeZoneDateInput,
  formatDefaultTimeZoneDateTimeInput,
  parseDefaultTimeZoneDateTimeInput
} from '@/utils/timezone'

describe('gateway-lite 默认中国上海时区', () => {
  it('UTC 16 点之后显示为上海次日', () => {
    const instant = '2026-08-01T16:30:00.000Z'
    expect(formatDefaultTimeZoneDateInput(instant)).toBe('2026-08-02')
    expect(formatDefaultTimeZoneDateTimeInput(instant)).toBe('2026-08-02T00:30')
  })

  it('把 datetime-local 明确按上海时间解析', () => {
    expect(parseDefaultTimeZoneDateTimeInput('2026-08-02T00:30')?.toISOString()).toBe('2026-08-01T16:30:00.000Z')
    expect(dateInputBoundaryToISOString('2026-08-02')).toBe('2026-08-01T16:00:00.000Z')
  })
})
