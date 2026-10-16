import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import AccountNameCell from '../AccountNameCell.vue'
import type { Account } from '@/types'

const account = (overrides: Partial<Account> = {}): Account => ({
  id: 1,
  name: 'Upstream account',
  platform: 'openai',
  type: 'apikey',
  credentials: { base_url: 'https://user:pass@example.com:8443/v1?api_key=secret#fragment' },
  proxy_id: null,
  concurrency: 1,
  priority: 1,
  status: 'active',
  error_message: null,
  last_used_at: null,
  expires_at: null,
  auto_pause_on_expired: false,
  created_at: '2026-07-18T00:00:00Z',
  updated_at: '2026-07-18T00:00:00Z',
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
})

describe('AccountNameCell', () => {
  it('links API-key accounts to a credential-free origin', () => {
    const wrapper = mount(AccountNameCell, { props: { account: account() } })
    const link = wrapper.get('a')
    expect(link.attributes('href')).toBe('https://example.com:8443')
    expect(link.attributes('target')).toBe('_blank')
    expect(link.attributes('rel')).toBe('noopener noreferrer')
		expect(link.classes()).toContain('truncate')
		expect(link.classes()).toContain('dark:text-white')
    expect(wrapper.html()).not.toContain('user:pass')
    expect(wrapper.html()).not.toContain('api_key=secret')
  })

  it.each([
    ['oauth account', account({ type: 'oauth' })],
    ['missing URL', account({ credentials: {} })],
    ['relative URL', account({ credentials: { base_url: '/v1' } })],
    ['non-HTTP URL', account({ credentials: { base_url: 'javascript:alert(1)' } })],
  ])('keeps %s as plain text', (_name, value) => {
    const wrapper = mount(AccountNameCell, { props: { account: value } })
    expect(wrapper.find('a').exists()).toBe(false)
    expect(wrapper.get('span').text()).toBe('Upstream account')
  })

  it('supports IPv6 origins and keeps the existing email subline', () => {
    const wrapper = mount(AccountNameCell, {
      props: {
        account: account({
          credentials: { base_url: 'http://[2001:db8::1]:8080/v1' },
          extra: { email_address: 'owner@example.com' },
        }),
      },
    })
    expect(wrapper.get('a').attributes('href')).toBe('http://[2001:db8::1]:8080')
    expect(wrapper.text()).toContain('owner@example.com')
  })
})
