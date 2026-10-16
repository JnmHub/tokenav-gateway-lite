import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copied: false, copyToClipboard: vi.fn() })
}))

import OAuthAuthorizationFlow from '../OAuthAuthorizationFlow.vue'

function mountOpenAIFlow() {
  return mount(OAuthAuthorizationFlow, {
    props: {
      addMethod: 'oauth',
      platform: 'openai',
      showCookieOption: false,
      showRefreshTokenOption: true,
      showMobileRefreshTokenOption: true,
      showCodexSessionImportOption: true,
      showAgentIdentityImportOption: true,
      showPersonalAccessTokenOption: true
    },
    global: {
      stubs: {
        Icon: { template: '<span />' }
      }
    }
  })
}

describe('OAuthAuthorizationFlow OpenAI credentials', () => {
  it('shows the six real OpenAI authorization methods', () => {
    const wrapper = mountOpenAIFlow()
    const values = wrapper.findAll('input[type="radio"]').map((item) => item.attributes('value'))

    expect(values).toEqual([
      'manual',
      'refresh_token',
      'mobile_refresh_token',
      'codex_session',
      'agent_identity',
      'personal_access_token'
    ])
  })

  it('emits the complete Agent Identity document without exposing private sub-fields', async () => {
    const wrapper = mountOpenAIFlow()
    await wrapper.get('[data-testid="openai-agent-identity-method"]').setValue(true)
    const document = '{"auth_mode":"agentIdentity","agent_identity":{"agent_private_key":"secret"}}'
    await wrapper.get('[data-testid="openai-agent-identity-input"]').setValue(document)
    await wrapper.get('[data-testid="openai-agent-identity-import"]').trigger('click')

    expect(wrapper.emitted('import-agent-identity')).toEqual([[document]])
    expect(wrapper.find('input[name="agent_private_key"]').exists()).toBe(false)
  })

  it('keeps PAT hidden by default and supports show, clear, and import actions', async () => {
    const wrapper = mountOpenAIFlow()
    await wrapper.get('[data-testid="openai-personal-access-token-method"]').setValue(true)
    const input = wrapper.get('[data-testid="openai-personal-access-token-input"]')

    expect(input.attributes('type')).toBe('password')
    await input.setValue('at-secret-token')
    await wrapper.get('[data-testid="openai-personal-access-token-visibility"]').trigger('click')
    expect(wrapper.get('[data-testid="openai-personal-access-token-input"]').attributes('type')).toBe('text')

    await wrapper.get('[data-testid="openai-personal-access-token-import"]').trigger('click')
    expect(wrapper.emitted('import-personal-access-token')).toEqual([['at-secret-token']])

    await wrapper.get('[data-testid="openai-personal-access-token-clear"]').trigger('click')
    expect((wrapper.get('[data-testid="openai-personal-access-token-input"]').element as HTMLInputElement).value).toBe('')
  })

  it('loads a dropped Agent Identity file and keeps a long filename visible', async () => {
    const wrapper = mountOpenAIFlow()
    await wrapper.get('[data-testid="openai-agent-identity-method"]').setValue(true)
    const document = JSON.stringify({
      auth_mode: 'agentIdentity',
      agent_identity: { agent_private_key: 'secret' }
    })
    const longName = `${'agent-identity-'.repeat(12)}auth.json`

    await wrapper.get('[data-testid="openai-agent-identity-drop"]').trigger('drop', {
      dataTransfer: {
        files: [{ name: longName, text: async () => document }]
      }
    })
    await flushPromises()

    expect(wrapper.text()).toContain(longName)
    expect((wrapper.get('[data-testid="openai-agent-identity-input"]').element as HTMLTextAreaElement).value).toBe(document)
  })

  it('shows file and server errors, and disables duplicate submissions while loading', async () => {
    const wrapper = mount(OAuthAuthorizationFlow, {
      props: {
        addMethod: 'oauth',
        platform: 'openai',
        showCookieOption: false,
        showRefreshTokenOption: true,
        showAgentIdentityImportOption: true,
        showPersonalAccessTokenOption: true,
        error: 'server rejected credential',
        loading: true
      },
      global: { stubs: { Icon: { template: '<span />' } } }
    })
    await wrapper.get('[data-testid="openai-agent-identity-method"]').setValue(true)
    await wrapper.get('[data-testid="openai-agent-identity-drop"]').trigger('drop', {
      dataTransfer: {
        files: [{ name: 'bad.json', text: async () => '{bad json' }]
      }
    })
    await flushPromises()

    expect(wrapper.text()).toContain('admin.accounts.oauth.openai.agentIdentityInvalidJSON')
    expect(wrapper.get('[data-testid="openai-agent-identity-import"]').attributes()).toHaveProperty('disabled')

    await wrapper.get('[data-testid="openai-personal-access-token-method"]').setValue(true)
    await wrapper.get('[data-testid="openai-personal-access-token-input"]').setValue('at-test-token')
    expect(wrapper.text()).toContain('server rejected credential')
    expect(wrapper.get('[data-testid="openai-personal-access-token-import"]').attributes()).toHaveProperty('disabled')
  })

  it('keeps keyboard-native controls and responsive dark-mode classes', async () => {
    const wrapper = mountOpenAIFlow()
    await wrapper.get('[data-testid="openai-personal-access-token-method"]').setValue(true)

    expect(wrapper.get('[data-testid="openai-personal-access-token-input"]').classes()).toContain('min-w-0')
    expect(wrapper.get('[data-testid="openai-personal-access-token-visibility"]').attributes('type')).toBe('button')
    expect(wrapper.get('[data-testid="openai-personal-access-token-clear"]').attributes('type')).toBe('button')
    expect(wrapper.html()).toContain('dark:border-blue-600')
  })
})
