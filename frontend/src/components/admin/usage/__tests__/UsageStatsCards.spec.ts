import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import UsageStatsCards from '../UsageStatsCards.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

const baseStats = {
  total_requests: 12,
  total_input_tokens: 100,
  total_output_tokens: 50,
  total_cache_tokens: 70,
  total_cache_creation_tokens: 30,
  total_cache_read_tokens: 40,
  total_tokens: 220,
  total_cost: 1,
  total_actual_cost: 0.8,
  total_account_cost: 0.6,
  average_duration_ms: 1200
}

describe('UsageStatsCards', () => {
  it('separates cache creation and cache read token cards', () => {
    const wrapper = mount(UsageStatsCards, {
      props: { stats: baseStats },
      global: { stubs: { Icon: true } }
    })

    expect(wrapper.text()).toContain('usage.cacheWriteTokens')
    expect(wrapper.text()).toContain('30')
    expect(wrapper.text()).toContain('usage.cacheReadTokens')
    expect(wrapper.text()).toContain('40')
  })

  it('falls back to legacy total_cache_tokens for cache reads', () => {
    const wrapper = mount(UsageStatsCards, {
      props: {
        stats: {
          ...baseStats,
          total_cache_read_tokens: undefined
        } as any
      },
      global: { stubs: { Icon: true } }
    })

    expect(wrapper.text()).toContain('70')
  })

  it('uses the accessible light and dark summary palette', () => {
    const wrapper = mount(UsageStatsCards, {
      props: { stats: baseStats },
      global: { stubs: { Icon: true } }
    })

    const paragraphs = wrapper.findAll('p')
    const totalCost = paragraphs.find((item) => item.text() === '$0.8000')
    const costBreakdown = paragraphs.find((item) => item.text().includes('usage.accountCost'))
    const selectedRange = paragraphs.find((item) => item.text() === 'usage.inSelectedRange')

    expect(totalCost?.classes()).toEqual(expect.arrayContaining(['text-green-700', 'dark:text-green-300']))
    expect(costBreakdown?.classes()).toEqual(expect.arrayContaining(['text-gray-600', 'dark:text-gray-300']))
    expect(costBreakdown?.find('span').classes()).toEqual(
      expect.arrayContaining(['text-orange-700', 'dark:text-orange-300'])
    )
    expect(selectedRange?.classes()).toEqual(expect.arrayContaining(['text-gray-600', 'dark:text-gray-300']))
  })
})
