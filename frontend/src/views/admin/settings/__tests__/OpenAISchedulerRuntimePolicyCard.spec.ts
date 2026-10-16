import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import OpenAISchedulerRuntimePolicyCard from '../OpenAISchedulerRuntimePolicyCard.vue'

const api = vi.hoisted(() => ({
  get: vi.fn(),
  update: vi.fn(),
  reset: vi.fn(),
}))

vi.mock('@/api/admin/runtimeSummary', () => ({
  getOpenAISchedulerRuntimePolicy: api.get,
  updateOpenAISchedulerRuntimePolicy: api.update,
  resetOpenAISchedulerRuntimePolicy: api.reset,
}))

const fixture = {
  revision: 3,
  preset: 'safe_default',
  advanced_enabled: false,
  sticky_soft_scoring_enabled: false,
  top_k: 7,
  prefer_subscription_pool: false,
  priority_weight: 1,
  load_weight: 1,
  queue_weight: 0.7,
  error_rate_weight: 0.8,
  ttft_weight: 0.5,
  prefer_soonest_reset_weight: 0,
  quota_headroom_weight: 0,
  upstream_cost_enabled: false,
  upstream_cost_weight: 0,
  session_sticky_weight: 0,
  previous_response_mode: 'hard',
  sales_billing_isolated: true,
  updated_at: '2026-08-02T08:00:00Z',
  source: 'database_setting_and_startup_config',
} as const

function mountCard() {
  const i18n = createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': {} } })
  return mount(OpenAISchedulerRuntimePolicyCard, {
    global: {
      plugins: [i18n],
      stubs: {
        Icon: { template: '<span />' },
        Toggle: {
          props: ['modelValue'],
          emits: ['update:modelValue'],
          template: '<button class="toggle-stub" type="button" @click="$emit(\'update:modelValue\', !modelValue)">{{ modelValue }}</button>',
        },
      },
    },
  })
}

describe('OpenAISchedulerRuntimePolicyCard', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    api.get.mockReset().mockResolvedValue({ ...fixture })
    api.update.mockReset().mockImplementation(async (input) => ({ ...fixture, ...input, revision: 4 }))
    api.reset.mockReset().mockResolvedValue({ ...fixture, revision: 4 })
  })

  it('展示不可修改的硬绑定与销售计费隔离说明', async () => {
    const wrapper = mountCard()
    await flushPromises()

    expect(wrapper.text()).toContain('previous_response_id')
    expect(wrapper.text()).toContain('固定硬绑定')
    expect(wrapper.text()).toContain('销售计费隔离')
    expect(wrapper.text()).toContain('rev 3')
  })

  it('点击均衡预设后以当前修订号原子保存且不会自动开启粘性实验', async () => {
    const wrapper = mountCard()
    await flushPromises()
    const balanced = wrapper.findAll('button').find((button) => button.text() === '均衡')
    expect(balanced).toBeTruthy()
    await balanced!.trigger('click')
    const save = wrapper.findAll('button').find((button) => button.text().includes('保存并热更新'))
    await save!.trigger('click')
    await flushPromises()

    expect(api.update).toHaveBeenCalledWith(expect.objectContaining({
      expected_revision: 3,
      preset: 'balanced',
      advanced_enabled: true,
      sticky_soft_scoring_enabled: false,
      top_k: 10,
    }))
    expect(wrapper.text()).toContain('只影响之后开始的请求')
  })

  it('恢复默认需要确认并携带当前修订号', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const wrapper = mountCard()
    await flushPromises()
    const reset = wrapper.findAll('button').find((button) => button.text().includes('恢复安全默认'))
    await reset!.trigger('click')
    await flushPromises()

    expect(api.reset).toHaveBeenCalledWith(3)
  })
})
