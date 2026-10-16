import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import GrokQuotaProbeCell from '../GrokQuotaProbeCell.vue'
import type { Account } from '@/types'

const { queryQuota } = vi.hoisted(() => ({
  queryQuota: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    grok: {
      queryQuota
    }
  }
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params?.percent == null ? key : `${key}:${params.percent}`
    })
  }
})

const account = {
  id: 501,
  name: 'Grok OAuth',
  platform: 'grok',
  type: 'oauth'
} as Account

describe('GrokQuotaProbeCell', () => {
  it('shows media eligibility evidence for Grok API Key without a quota probe action', () => {
    const apiKeyAccount = {
      ...account,
      type: 'apikey',
      grok_media_eligibility: {
        state: 'ineligible',
        eligible: false,
        effective_eligible: false,
        source: 'override',
        reason: 'override_disabled',
        override: false,
        stale: true,
        weekly_status_code: 403,
        monthly_status_code: 200,
        probed_at: '2026-07-18T00:00:00Z'
      }
    } as Account

    const wrapper = mount(GrokQuotaProbeCell, { props: { account: apiKeyAccount } })

    expect(wrapper.get('[data-testid="grok-media-eligibility"]').text())
      .toContain('admin.accounts.usageWindow.grokMediaEligibilityIneligible')
    expect(wrapper.text()).toContain('admin.accounts.usageWindow.grokMediaEligibilityManual')
    expect(wrapper.text()).toContain('admin.accounts.usageWindow.grokMediaEligibilityStale')
    expect(wrapper.find('button').exists()).toBe(false)
  })

  it('shows billing and active-probe evidence from a hybrid result', async () => {
    queryQuota.mockResolvedValue({
      source: 'hybrid_probe',
      model: 'grok-4.5',
      billing: {
        period_type: 'weekly',
        usage_percent: 23.8,
        used_percent: 9.6,
        plan: 'SuperGrok',
        partial: true
      },
      snapshot: {
        requests: { limit: 20, remaining: 18 },
        updated_at: '2026-07-15T00:00:00Z'
      },
      headers_observed: true,
      reset_supported: false,
      fetched_at: 1,
      persisted: true
    })

    const wrapper = mount(GrokQuotaProbeCell, { props: { account } })
    await wrapper.find('button').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('SuperGrok')
    expect(wrapper.text()).toContain('admin.accounts.usageWindow.grokBillingWeeklySummary:24')
    expect(wrapper.text()).toContain('admin.accounts.usageWindow.grokBillingMonthlySummary:10')
    expect(wrapper.text()).toContain('admin.accounts.usageWindow.grokBillingPartialSummary')
    expect(wrapper.text()).toContain('18/20')
  })
})
