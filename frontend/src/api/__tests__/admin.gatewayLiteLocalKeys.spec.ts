import { describe, expect, it } from 'vitest'
import {
  normalizeGatewayLiteLocalKey,
  normalizeGatewayLiteLocalKeyList,
} from '@/api/admin/gatewayLiteLocalKeys'

const baseKey = {
  id: 1,
  name: 'local',
  key_masked: 'gw_local_...test',
  group_id: 2,
  group_name: 'OpenAI',
  platform: 'openai',
  status: 'active' as const,
  created_at: '2026-07-15T00:00:00Z',
  updated_at: '2026-07-15T00:00:00Z',
}

describe('gateway-lite local key response normalization', () => {
  it.each([
    ['null', null],
    ['missing', undefined],
    ['non-array', '127.0.0.1'],
  ])('normalizes %s IP rules to empty arrays', (_, value) => {
    const key = normalizeGatewayLiteLocalKey({
      ...baseKey,
      ip_whitelist: value,
      ip_blacklist: value,
    })

    expect(key.ip_whitelist).toEqual([])
    expect(key.ip_blacklist).toEqual([])
  })

  it('preserves valid rule arrays and removes non-string entries', () => {
    const key = normalizeGatewayLiteLocalKey({
      ...baseKey,
      ip_whitelist: ['127.0.0.1', 123, null],
      ip_blacklist: ['10.0.0.0/8'],
    })

    expect(key.ip_whitelist).toEqual(['127.0.0.1'])
    expect(key.ip_blacklist).toEqual(['10.0.0.0/8'])
  })

  it('normalizes null or malformed list responses to an empty list', () => {
    expect(normalizeGatewayLiteLocalKeyList(null)).toEqual([])
    expect(normalizeGatewayLiteLocalKeyList({ items: [] })).toEqual([])
  })

  it('normalizes every item in a list and skips invalid entries', () => {
    const keys = normalizeGatewayLiteLocalKeyList([
      { ...baseKey, ip_whitelist: null, ip_blacklist: [] },
      null,
      'invalid',
    ])

    expect(keys).toHaveLength(1)
    expect(keys[0]?.ip_whitelist).toEqual([])
  })
})
