import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Account } from '@/types'
import CNProviderBalanceCell from '../CNProviderBalanceCell.vue'
import CNProviderQuotaCell from '../CNProviderQuotaCell.vue'

const { queryBalance, queryQuota } = vi.hoisted(() => ({
  queryBalance: vi.fn(),
  queryQuota: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: { cnProviders: { queryBalance, queryQuota } }
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

const baseAccount = {
  id: 7,
  type: 'apikey',
  extra: {}
} as Account

describe('国产供应商用量单元格', () => {
  beforeEach(() => {
    queryBalance.mockReset()
    queryQuota.mockReset()
  })

  it('Coding 套餐点击后展示 5 小时和每周额度', async () => {
    queryQuota.mockResolvedValue({
      provider: 'zhipu',
      success: true,
      credential_valid: true,
      tiers: [
        { window: '5h', used_percent: 20 },
        { window: 'weekly', used_percent: 45 }
      ],
      fetched_at: 1,
      persisted: true
    })
    const wrapper = mount(CNProviderQuotaCell, {
      props: {
        account: {
          ...baseAccount,
          platform: 'zhipu',
          credentials: { account_mode: 'coding' }
        } as Account
      }
    })

    await wrapper.get('button').trigger('click')
    await flushPromises()

    expect(queryQuota).toHaveBeenCalledWith(7)
    expect(wrapper.findAll('[data-testid="cn-provider-quota-tier"]')).toHaveLength(2)
    expect(wrapper.text()).toContain('20%')
    expect(wrapper.text()).toContain('45%')
  })

  it('按量账号点击后展示供应商返回的多币种余额', async () => {
    queryBalance.mockResolvedValue({
      provider: 'deepseek',
      success: true,
      balance: 12.5,
      currency: 'CNY',
      balances: [
        { currency: 'CNY', balance: 12.5 },
        { currency: 'USD', balance: 1.25 }
      ],
      available: true,
      fetched_at: 1,
      persisted: true
    })
    const wrapper = mount(CNProviderBalanceCell, {
      props: {
        account: {
          ...baseAccount,
          platform: 'deepseek',
          credentials: { account_mode: 'payg' }
        } as Account
      }
    })

    await wrapper.get('button').trigger('click')
    await flushPromises()

    expect(queryBalance).toHaveBeenCalledWith(7)
    expect(wrapper.text()).toContain('CNY 12.50')
    expect(wrapper.text()).toContain('USD 1.25')
  })
})
