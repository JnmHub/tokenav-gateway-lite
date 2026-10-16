import { describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { mount } from '@vue/test-utils'
import type { Account } from '@/types'
import UpstreamBillingRateCell from '../UpstreamBillingRateCell.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

const HelpTooltipStub = defineComponent({
  template: '<div><slot name="trigger" /><slot /></div>'
})

const IconStub = defineComponent({
  template: '<i />'
})

function account(overrides: Partial<Account> = {}): Account {
  return {
    id: 8,
    name: 'upstream',
    platform: 'openai',
    type: 'apikey',
    proxy_id: null,
    concurrency: 1,
    priority: 1,
    rate_multiplier: 1.25,
    status: 'active',
    error_message: null,
    last_used_at: null,
    expires_at: null,
    auto_pause_on_expired: false,
    created_at: '2026-08-01T00:00:00Z',
    updated_at: '2026-08-01T00:00:00Z',
    schedulable: true,
    rate_limited_at: null,
    rate_limit_reset_at: null,
    overload_until: null,
    temp_unschedulable_until: null,
    temp_unschedulable_reason: null,
    session_window_start: null,
    session_window_end: null,
    session_window_status: null,
    ...overrides
  }
}

function render(value: Account) {
  return mount(UpstreamBillingRateCell, {
    props: { account: value },
    global: {
      stubs: {
        HelpTooltip: HelpTooltipStub,
        Icon: IconStub
      }
    }
  })
}

describe('UpstreamBillingRateCell', () => {
  it('同时展示手工成本倍率与上游探测倍率', () => {
    const wrapper = render(account({
      extra: {
        upstream_provider: 'sub2api',
        upstream_billing_probe: {
          status: 'ok',
          received_at: '2026-08-01T00:00:00Z',
          fresh_until: '2099-08-01T00:00:00Z',
          last_attempt_at: '2026-08-01T00:00:00Z',
          data: {
            object: 'sub2api.key_billing',
            schema_version: 1,
            billing_scope: 'token',
            group_rate_multiplier: 0.8,
            resolved_rate_multiplier: 0.75,
            peak_rate_enabled: false,
            effective_rate_multiplier: 0.75,
            observed_at: '2026-08-01T00:00:00Z'
          }
        }
      }
    }))

    expect(wrapper.text()).toContain('1.25x')
    expect(wrapper.text()).toContain('0.75x')
  })

  it('点击刷新只发出探测事件', async () => {
    const wrapper = render(account({ extra: { upstream_provider: 'sub2api' } }))
    await wrapper.get('button[aria-label="admin.accounts.upstreamBilling.manualProbe"]').trigger('click')
    expect(wrapper.emitted('probe')).toHaveLength(1)
  })

  it('非 OpenAI API Key 账号不显示探测按钮', () => {
    const wrapper = render(account({ type: 'oauth' }))
    expect(wrapper.text()).toContain('1.25x')
    expect(wrapper.find('button').exists()).toBe(false)
  })

  it('未标记为 Sub2API 的普通 API Key 不显示无效探测按钮', () => {
    const wrapper = render(account())
    expect(wrapper.text()).toContain('1.25x')
    expect(wrapper.find('button').exists()).toBe(false)
  })
})
