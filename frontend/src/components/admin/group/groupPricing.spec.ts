import { describe, expect, it } from 'vitest'
import {
  createGroupPricingFormState,
  createPricingEntry,
  groupPricingFormToRequest,
  validateGroupPricingForm,
} from './groupPricing'

describe('group pricing form', () => {
  it('uses only group scope for a whole-group account cost rule', () => {
    const state = createGroupPricingFormState()
    state.enabled = true
    state.apply_pricing_to_account_stats = true
    state.account_stats_pricing_rules = [{
      name: '整组成本',
      scope: 'whole_group',
      account_ids: [99],
      pricing: [{ ...createPricingEntry(), models: ['gpt-*'] }],
    }]

    const request = groupPricingFormToRequest(state, 'openai', 7)
    expect(request.account_stats_pricing_rules[0].group_ids).toEqual([7])
    expect(request.account_stats_pricing_rules[0].account_ids).toEqual([])
  })

  it('uses only account scope for selected accounts', () => {
    const state = createGroupPricingFormState()
    state.enabled = true
    state.apply_pricing_to_account_stats = true
    state.account_stats_pricing_rules = [{
      name: '指定账号成本',
      scope: 'selected_accounts',
      account_ids: [11, 12],
      pricing: [{ ...createPricingEntry(), models: ['gpt-*'] }],
    }]

    const request = groupPricingFormToRequest(state, 'openai', 7)
    expect(request.account_stats_pricing_rules[0].group_ids).toEqual([])
    expect(request.account_stats_pricing_rules[0].account_ids).toEqual([11, 12])
  })

  it('rejects selected-account scope without an account', () => {
    const state = createGroupPricingFormState()
    state.enabled = true
    state.apply_pricing_to_account_stats = true
    state.account_stats_pricing_rules = [{
      name: '空规则',
      scope: 'selected_accounts',
      account_ids: [],
      pricing: [{ ...createPricingEntry(), models: ['gpt-*'] }],
    }]

    expect(validateGroupPricingForm(state, 'openai')).toBe('rule_accounts_required')
  })

  it('requires a concrete platform for composite pricing', () => {
    const state = createGroupPricingFormState()
    state.enabled = true
    state.model_pricing = [{
      ...createPricingEntry('video'),
      models: ['grok-imagine-video'],
      per_request_price: 0.05,
    }]

    expect(validateGroupPricingForm(state, 'composite')).toBe('platform_required')
    state.model_pricing[0].platform = 'grok'
    expect(validateGroupPricingForm(state, 'composite')).toBeNull()
    expect(groupPricingFormToRequest(state, 'composite', 7).model_pricing[0].platform).toBe('grok')
  })

  it('preserves video per-second resolution tiers', () => {
    const state = createGroupPricingFormState()
    state.enabled = true
    state.model_pricing = [{
      ...createPricingEntry('video', 'grok'),
      models: ['grok-imagine-video-1.5'],
      intervals: [
        {
          min_tokens: 0,
          max_tokens: null,
          tier_label: '480p',
          input_price: null,
          output_price: null,
          cache_write_price: null,
          cache_read_price: null,
          input_multiplier: null,
          output_multiplier: null,
          cache_write_multiplier: null,
          cache_read_multiplier: null,
          per_request_price: 0.08,
          sort_order: 0,
        },
        {
          min_tokens: 0,
          max_tokens: null,
          tier_label: '1080p',
          input_price: null,
          output_price: null,
          cache_write_price: null,
          cache_read_price: null,
          input_multiplier: null,
          output_multiplier: null,
          cache_write_multiplier: null,
          cache_read_multiplier: null,
          per_request_price: 0.25,
          sort_order: 2,
        },
      ],
    }]

    const pricing = groupPricingFormToRequest(state, 'composite', 7).model_pricing[0]
    expect(pricing.billing_mode).toBe('video')
    expect(pricing.intervals.map(interval => [interval.tier_label, interval.per_request_price])).toEqual([
      ['480p', 0.08],
      ['1080p', 0.25],
    ])
  })

  it('keeps service tier and context interval multipliers', () => {
    const state = createGroupPricingFormState()
    state.enabled = true
    state.model_pricing = [{
      ...createPricingEntry('token', 'openai'),
      models: ['gpt-5.5'],
      fast_multiplier: 2.5,
      flex_multiplier: 0.4,
      intervals: [{
        min_tokens: 272000,
        max_tokens: null,
        tier_label: '',
        input_price: null,
        output_price: null,
        cache_write_price: null,
        cache_read_price: null,
        input_multiplier: 2,
        output_multiplier: 1.5,
        cache_write_multiplier: 2,
        cache_read_multiplier: 2,
        per_request_price: null,
        sort_order: 0,
      }],
    }]

    const pricing = groupPricingFormToRequest(state, 'openai', 7).model_pricing[0]
    expect(pricing.fast_multiplier).toBe(2.5)
    expect(pricing.flex_multiplier).toBe(0.4)
    expect(pricing.intervals[0].input_multiplier).toBe(2)
  })
})
