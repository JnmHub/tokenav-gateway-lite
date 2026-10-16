import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import BillingCacheRuntimeCard from '../BillingCacheRuntimeCard.vue'

const api = vi.hoisted(() => ({
  get: vi.fn(),
  update: vi.fn(),
}))

vi.mock('@/api/admin/runtimeSummary', () => ({
  getBillingCacheRuntimeConfig: api.get,
  updateBillingCacheRuntimeConfig: api.update,
}))

const fixture = {
  revision: 7,
  preset: 'standard',
  available_presets: ['low', 'standard', 'high'],
  critical_worker_count: 8,
  critical_buffer_size: 2048,
  critical_queue_length: 3,
  critical_queue_capacity: 2048,
  best_effort_worker_count: 2,
  best_effort_buffer_size: 512,
  best_effort_queue_length: 5,
  best_effort_queue_capacity: 512,
  timeout_milliseconds: 2000,
  critical_active_workers: 8,
  best_effort_active_workers: 2,
  best_effort_deferred_length: 1,
  best_effort_deferred_capacity: 512,
} as const

function mountCard() {
  const i18n = createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': {} } })
  return mount(BillingCacheRuntimeCard, {
    global: {
      plugins: [i18n],
      stubs: { Icon: { template: '<span aria-hidden="true" />' } },
    },
  })
}

describe('BillingCacheRuntimeCard', () => {
  beforeEach(() => {
    api.get.mockReset().mockResolvedValue({ ...fixture, available_presets: [...fixture.available_presets] })
    api.update.mockReset().mockImplementation(async (input) => ({
      ...fixture,
      preset: input.preset,
      revision: fixture.revision + 1,
      available_presets: [...fixture.available_presets],
    }))
  })

  it('只提供三档点击选择并展示当前有效工作池和队列', async () => {
    const wrapper = mountCard()
    await flushPromises()

    expect(wrapper.text()).toContain('低配')
    expect(wrapper.text()).toContain('标准')
    expect(wrapper.text()).toContain('高并发')
    expect(wrapper.text()).toContain('关键有效线程')
    expect(wrapper.text()).toContain('3 / 2,048')
    expect(wrapper.find('input[type="number"]').exists()).toBe(false)
  })

  it('用当前修订号切换预设且不提交数值覆盖', async () => {
    const wrapper = mountCard()
    await flushPromises()
    const high = wrapper.findAll('button').find((button) => button.text().includes('高并发'))
    await high!.trigger('click')
    const save = wrapper.findAll('button').find((button) => button.text().includes('保存并热更新'))
    await save!.trigger('click')
    await flushPromises()

    expect(api.update).toHaveBeenCalledWith({ expected_revision: 7, preset: 'high' })
    expect(wrapper.text()).toContain('旧队列已安全排空')
    expect(wrapper.text()).toContain('rev 8')
  })

  it('保存失败立即恢复服务端旧档位', async () => {
    api.update.mockRejectedValueOnce(new Error('persist failed'))
    const wrapper = mountCard()
    await flushPromises()
    const high = wrapper.findAll('button').find((button) => button.text().includes('高并发'))
    await high!.trigger('click')
    const save = wrapper.findAll('button').find((button) => button.text().includes('保存并热更新'))
    await save!.trigger('click')
    await flushPromises()

    expect(wrapper.findAll('button').find((button) => button.text().includes('标准'))?.attributes('aria-pressed')).toBe('true')
    expect(wrapper.text()).toContain('已恢复原档位')
  })
})
