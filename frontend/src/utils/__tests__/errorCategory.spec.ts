import { describe, expect, it } from 'vitest'
import { mapErrorCategory } from '../errorCategory'

describe('mapErrorCategory', () => {
  it.each([
    ['auth', '', 'auth'],
    ['request', 'rate_limit_error', 'rate_limit'],
    ['request', 'billing_error', 'quota'],
    ['request', 'invalid_request_error', 'invalid_request'],
    ['routing', '', 'service_unavailable'],
    ['network', '', 'upstream'],
    ['internal', '', 'internal'],
    ['request', 'cyber_policy', 'cyber']
  ])('maps %s/%s to %s', (phase, errorType, expected) => {
    expect(mapErrorCategory(phase, errorType)).toBe(expected)
  })
})
