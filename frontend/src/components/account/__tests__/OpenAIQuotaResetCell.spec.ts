import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import OpenAIQuotaResetCell from '../OpenAIQuotaResetCell.vue'
import type { Account } from '@/types'

const { refreshOpenAIQuota, resetOpenAIQuota } = vi.hoisted(() => ({
  refreshOpenAIQuota: vi.fn(),
  resetOpenAIQuota: vi.fn(),
}))

vi.mock('@/api/admin/accounts', () => ({
  refreshOpenAIQuota,
  resetOpenAIQuota,
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

const account = {
  id: 9,
  name: 'openai',
  platform: 'openai',
  type: 'oauth',
  status: 'active',
  schedulable: true,
  concurrency: 1,
  priority: 1,
  created_at: '2026-07-10T00:00:00Z',
  updated_at: '2026-07-10T00:00:00Z',
} as Account

describe('OpenAIQuotaResetCell', () => {
  beforeEach(() => {
    refreshOpenAIQuota.mockReset()
    resetOpenAIQuota.mockReset()
    refreshOpenAIQuota.mockResolvedValue({
      rate_limit_reset_credits: { available_count: 2 },
      cache_persisted: false,
    })
    resetOpenAIQuota.mockResolvedValue({
      windows_reset: 2,
      cache_refreshed: true,
      account_state_recovered: true,
      quota: { rate_limit_reset_credits: { available_count: 1 }, fetched_at: 1 },
    })
  })

  it('点击重置时先要求确认，确认后才请求后端', async () => {
    const wrapper = mount(OpenAIQuotaResetCell, {
      props: { account },
      global: {
        stubs: {
          ConfirmDialog: {
            props: ['show'],
            emits: ['confirm', 'cancel'],
            template: '<button v-if="show" data-testid="confirm-reset" @click="$emit(\'confirm\')">confirm</button>',
          },
        },
      },
    })

    const actionButtons = wrapper.findAll('button')
    await actionButtons[0].trigger('click')
    await flushPromises()
    await actionButtons[1].trigger('click')

    expect(resetOpenAIQuota).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="confirm-reset"]').exists()).toBe(true)

    await wrapper.find('[data-testid="confirm-reset"]').trigger('click')
    await flushPromises()

    expect(resetOpenAIQuota).toHaveBeenCalledWith(9)
  })
})
