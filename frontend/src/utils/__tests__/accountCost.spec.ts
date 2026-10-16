import { describe, expect, it } from 'vitest'

import { resolveAccountBilledCost } from '../accountCost'

describe('resolveAccountBilledCost', () => {
  it('uses the dedicated account cost, including an intentional zero', () => {
    expect(resolveAccountBilledCost({
      total_cost: 25,
      account_stats_cost: 0,
      account_rate_multiplier: 2,
      upstream_model: 'gpt-5.6-luna',
    })).toBe(0)
  })

  it('keeps the total-cost fallback for legacy rows without an upstream model', () => {
    expect(resolveAccountBilledCost({
      total_cost: 3,
      account_stats_cost: null,
      account_rate_multiplier: 1.5,
      upstream_model: null,
    })).toBe(4.5)
  })

  it('does not expose customer cost when an upstream model has no resolved price', () => {
    expect(resolveAccountBilledCost({
      total_cost: 25,
      account_stats_cost: null,
      account_rate_multiplier: 1,
      upstream_model: 'gpt-5.6-luna',
    })).toBe(0)
  })
})
