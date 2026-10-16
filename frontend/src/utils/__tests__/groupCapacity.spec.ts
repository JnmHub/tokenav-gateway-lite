import { describe, expect, it } from 'vitest'
import { normalizeGroupIngressRequests } from '../groupCapacity'

describe('normalizeGroupIngressRequests', () => {
  it('保留包含零在内的有效实时请求数', () => {
    expect(normalizeGroupIngressRequests(0)).toBe(0)
    expect(normalizeGroupIngressRequests(27)).toBe(27)
  })

  it('将缺失或异常的观测字段标记为未知', () => {
    expect(normalizeGroupIngressRequests(undefined)).toBeNull()
    expect(normalizeGroupIngressRequests(null)).toBeNull()
    expect(normalizeGroupIngressRequests('27')).toBeNull()
    expect(normalizeGroupIngressRequests(-1)).toBeNull()
    expect(normalizeGroupIngressRequests(1.5)).toBeNull()
  })
})
