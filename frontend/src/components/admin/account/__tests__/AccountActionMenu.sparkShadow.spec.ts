import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import AccountActionMenu from '../AccountActionMenu.vue'
import type { Account } from '@/types'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const position = { top: 24, left: 24 }

function account(overrides: Partial<Account> = {}): Account {
  return {
    id: 1,
    name: 'openai-parent',
    platform: 'openai',
    type: 'oauth',
    proxy_id: null,
    concurrency: 2,
    priority: 50,
    status: 'active',
    error_message: null,
    last_used_at: null,
    expires_at: null,
    auto_pause_on_expired: false,
    created_at: '2026-08-02T00:00:00Z',
    updated_at: '2026-08-02T00:00:00Z',
    schedulable: true,
    rate_limited_at: null,
    rate_limit_reset_at: null,
    overload_until: null,
    temp_unschedulable_until: null,
    temp_unschedulable_reason: null,
    session_window_start: null,
    session_window_end: null,
    session_window_status: null,
    ...overrides,
  }
}

afterEach(() => {
  document.body.innerHTML = ''
})

describe('AccountActionMenu Spark 影子操作', () => {
  it('OpenAI OAuth 母账号只显示创建影子入口', () => {
    const wrapper = mount(AccountActionMenu, {
      props: { show: true, account: account(), position },
      attachTo: document.body,
    })

    expect(document.body.textContent).toContain('admin.accounts.createSparkShadow')
    expect(document.body.textContent).not.toContain('admin.accounts.syncSparkShadow')
    wrapper.unmount()
  })

  it('影子只显示同步入口，不显示重授权、Token 刷新和隐私设置', async () => {
    const shadow = account({ id: 2, name: 'openai-spark', parent_account_id: 1, quota_dimension: 'spark' })
    const wrapper = mount(AccountActionMenu, {
      props: { show: true, account: shadow, position },
      attachTo: document.body,
    })

    const text = document.body.textContent ?? ''
    expect(text).toContain('admin.accounts.syncSparkShadow')
    expect(text).not.toContain('admin.accounts.createSparkShadow')
    expect(text).not.toContain('admin.accounts.reAuthorize')
    expect(text).not.toContain('admin.accounts.refreshToken')
    expect(text).not.toContain('admin.accounts.setPrivacy')

    const button = Array.from(document.body.querySelectorAll('button'))
      .find(item => item.textContent?.includes('admin.accounts.syncSparkShadow'))
    button?.click()
    await wrapper.vm.$nextTick()
    expect(wrapper.emitted('sync-spark-shadow')?.[0]).toEqual([shadow])
    wrapper.unmount()
  })
})
