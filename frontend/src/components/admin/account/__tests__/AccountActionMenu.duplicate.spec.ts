import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import AccountActionMenu from '../AccountActionMenu.vue'
import type { Account } from '@/types'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

const account = (type: Account['type']): Account => ({
  id: 42,
  name: 'source',
  platform: 'openai',
  type,
  credentials: {},
  extra: {},
  concurrency: 1,
  priority: 1,
  rate_multiplier: 1,
  status: 'active',
  schedulable: true,
  auto_pause_on_expired: true,
  created_at: '2026-07-16T00:00:00Z',
  updated_at: '2026-07-16T00:00:00Z'
} as Account)

describe('AccountActionMenu duplicate action', () => {
  it('静态凭据账号显示复制入口并发出源账号，不包含额外凭据表单', async () => {
    const source = account('apikey')
    const wrapper = mount(AccountActionMenu, {
      props: { show: true, account: source, position: { top: 10, left: 10 } },
      global: { stubs: { Teleport: true, Icon: true } }
    })

    const button = wrapper.findAll('button').find(item => item.text().includes('admin.accounts.duplicateAccount'))
    expect(button).toBeDefined()
    await button!.trigger('click')
    expect(wrapper.emitted('duplicate')?.[0]).toEqual([source])
  })

  it('OAuth 轮换凭据账号不显示复制入口', () => {
    const wrapper = mount(AccountActionMenu, {
      props: { show: true, account: account('oauth'), position: { top: 10, left: 10 } },
      global: { stubs: { Teleport: true, Icon: true } }
    })

    expect(wrapper.text()).not.toContain('admin.accounts.duplicateAccount')
  })
})
