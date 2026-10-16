import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import OpsIngressProtectionCard from '../OpsIngressProtectionCard.vue'

const mocks = vi.hoisted(() => ({
  getPanelRateLimitSettings: vi.fn(),
  updatePanelRateLimitSettings: vi.fn(),
  resetPanelRateLimitSettings: vi.fn(),
  getIngressRejections: vi.fn()
}))

vi.mock('@/api/admin/ops', () => ({
  opsAPI: mocks
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params ? `${key}:${JSON.stringify(params)}` : key
    })
  }
})

const defaultConfig = {
  enabled: true,
  preset: 'default' as const,
  user_rpm: 240,
  heavy_rpm: 60,
  window_seconds: 60
}

const runtimeResponse = (settings = defaultConfig) => ({
  settings: { ...settings },
  runtime_stats: {
    checked_total: 120,
    rejected_total: 3,
    disabled_bypass_total: 0,
    active_buckets: 2
  }
})

const rejectionSnapshot = {
  node: 'us-1',
  window_minutes: 60,
  window_start: '2026-08-01T09:00:00Z',
  window_end: '2026-08-01T10:00:00Z',
  request_count: 1000,
  rejection_count: 10,
  rejection_rate_percent: 1,
  top_reasons: [
    { reason: 'admin_rate_limited' as const, count: 7 },
    { reason: 'body_too_large' as const, count: 3 }
  ],
  trend: [
    { bucket_start: '2026-08-01T09:58:00Z', total: 3, counts: { body_too_large: 3 } },
    { bucket_start: '2026-08-01T09:59:00Z', total: 7, counts: { admin_rate_limited: 7 } }
  ]
}

async function mountCard() {
  const wrapper = mount(OpsIngressProtectionCard, {
    props: { refreshToken: 0 }
  })
  await flushPromises()
  return wrapper
}

describe('OpsIngressProtectionCard', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.getPanelRateLimitSettings.mockResolvedValue(runtimeResponse())
    mocks.updatePanelRateLimitSettings.mockImplementation(async (value) => runtimeResponse(value))
    mocks.resetPanelRateLimitSettings.mockResolvedValue(runtimeResponse())
    mocks.getIngressRejections.mockResolvedValue(rejectionSnapshot)
  })

  it('加载当前限流配置和最近 60 分钟入口拒绝摘要', async () => {
    const wrapper = await mountCard()

    expect(mocks.getPanelRateLimitSettings).toHaveBeenCalledTimes(1)
    expect(mocks.getIngressRejections).toHaveBeenCalledWith(60)
    expect(wrapper.text()).toContain('1.00%')
    expect(wrapper.find('[data-testid="ingress-rejection-trend"]').exists()).toBe(true)
    expect(wrapper.findAll('[data-testid="ingress-rejection-trend"] > div')).toHaveLength(2)
  })

  it('通过快捷按钮切换严格预设，不要求输入表达式', async () => {
    const wrapper = await mountCard()

    await wrapper.get('[data-testid="preset-strict"]').trigger('click')
    await flushPromises()

    expect(mocks.updatePanelRateLimitSettings).toHaveBeenCalledWith({
      ...defaultConfig,
      preset: 'strict'
    })
  })

  it('支持关闭保护和一键恢复默认', async () => {
    const wrapper = await mountCard()

    await wrapper.get('[data-testid="panel-rate-limit-toggle"]').trigger('click')
    await flushPromises()
    expect(mocks.updatePanelRateLimitSettings).toHaveBeenCalledWith({
      ...defaultConfig,
      enabled: false
    })

    await wrapper.get('[data-testid="reset-panel-rate-limit"]').trigger('click')
    await flushPromises()
    expect(mocks.resetPanelRateLimitSettings).toHaveBeenCalledTimes(1)
  })

  it('支持自定义限流并用快捷窗口选项保存', async () => {
    const wrapper = await mountCard()

    await wrapper.get('[data-testid="preset-custom"]').trigger('click')
    await flushPromises()
    expect(mocks.updatePanelRateLimitSettings).toHaveBeenLastCalledWith({
      ...defaultConfig,
      preset: 'custom'
    })

    await wrapper.get('[data-testid="custom-user-rpm"]').setValue('360')
    await wrapper.get('[data-testid="custom-heavy-rpm"]').setValue('90')
    await wrapper.get('[data-testid="custom-window-seconds"]').setValue('30')
    await wrapper.get('[data-testid="save-custom-rate-limit"]').trigger('click')
    await flushPromises()

    expect(mocks.updatePanelRateLimitSettings).toHaveBeenLastCalledWith({
      enabled: true,
      preset: 'custom',
      user_rpm: 360,
      heavy_rpm: 90,
      window_seconds: 30
    })
  })

  it('仪表盘统一刷新时只刷新拒绝摘要，不重复拉取运行配置', async () => {
    const wrapper = await mountCard()

    await wrapper.setProps({ refreshToken: 1 })
    await flushPromises()

    expect(mocks.getIngressRejections).toHaveBeenCalledTimes(2)
    expect(mocks.getPanelRateLimitSettings).toHaveBeenCalledTimes(1)
  })
})
