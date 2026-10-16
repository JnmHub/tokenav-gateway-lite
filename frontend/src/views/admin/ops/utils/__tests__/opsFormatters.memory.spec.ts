import { describe, expect, it } from 'vitest'

import { formatMemorySizeMB } from '../opsFormatters'

describe('formatMemorySizeMB', () => {
  it('保留小于 1 GiB 的 MB 显示', () => {
    expect(formatMemorySizeMB(61)).toBe('61 MB')
    expect(formatMemorySizeMB(1023)).toBe('1023 MB')
  })

  it('将大容量自动换算为 GB', () => {
    expect(formatMemorySizeMB(1024)).toBe('1 GB')
    expect(formatMemorySizeMB(1536)).toBe('1.5 GB')
    expect(formatMemorySizeMB(32768)).toBe('32 GB')
  })

  it('拒绝无效容量', () => {
    expect(formatMemorySizeMB(0)).toBe('0 MB')
    expect(formatMemorySizeMB(-1)).toBe('-')
    expect(formatMemorySizeMB(Number.NaN)).toBe('-')
    expect(formatMemorySizeMB(Number.POSITIVE_INFINITY)).toBe('-')
    expect(formatMemorySizeMB(null)).toBe('-')
    expect(formatMemorySizeMB(undefined)).toBe('-')
  })
})
