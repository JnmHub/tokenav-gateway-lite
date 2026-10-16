import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const { createAccountMock, refreshOpenAITokenMock } = vi.hoisted(() => ({
  createAccountMock: vi.fn(),
  refreshOpenAITokenMock: vi.fn()
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showWarning: vi.fn(),
    showInfo: vi.fn()
  })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ isSimpleMode: true })
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      create: createAccountMock,
      refreshOpenAIToken: refreshOpenAITokenMock,
      validateGroupModelMappings: vi.fn(),
      checkMixedChannelRisk: vi.fn()
    },
    settings: {
      getWebSearchEmulationConfig: vi.fn().mockResolvedValue({ enabled: false, providers: [] }),
      getSettings: vi.fn().mockResolvedValue({})
    },
    tlsFingerprintProfiles: {
      list: vi.fn().mockResolvedValue([])
    }
  }
}))

vi.mock('@/api/admin/accounts', () => ({
  getAntigravityDefaultModelMapping: vi.fn().mockResolvedValue([])
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

import CreateAccountModal from '../CreateAccountModal.vue'

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: { show: { type: Boolean, default: false } },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

const ModelWhitelistSelectorStub = defineComponent({
  name: 'ModelWhitelistSelector',
  props: {
    modelValue: { type: Array, default: () => [] },
    platform: String,
    syncCredentials: Object
  },
  template: '<div data-testid="model-whitelist-selector" />'
})

const OAuthAuthorizationFlowStub = defineComponent({
  name: 'OAuthAuthorizationFlow',
  emits: ['validate-refresh-token'],
  template: `
    <button
      type="button"
      data-testid="validate-openai-refresh-token"
      @click="$emit('validate-refresh-token', 'rt-test')"
    >
      validate
    </button>
  `
})

function mountModal() {
  return mount(CreateAccountModal, {
    props: { show: true, proxies: [], groups: [] },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        OAuthAuthorizationFlow: OAuthAuthorizationFlowStub,
        ConfirmDialog: true,
        Select: true,
        Icon: true,
        PlatformIcon: true,
        ProxySelector: true,
        ProxyAdBanner: true,
        GroupSelector: true,
        ModelWhitelistSelector: ModelWhitelistSelectorStub,
        QuotaLimitCard: true
      }
    }
  })
}

describe('CreateAccountModal 国产供应商行为', () => {
  beforeEach(() => {
    createAccountMock.mockReset()
    refreshOpenAITokenMock.mockReset()
    createAccountMock.mockResolvedValue({ id: 1 })
    refreshOpenAITokenMock.mockResolvedValue({
      access_token: 'at-test',
      refresh_token: 'rt-test',
      expires_at: 2_000_000_000,
      email: 'oauth@example.com'
    })
  })

  it('模型同步使用自适应模式中实时编辑的 Chat Completions 地址', async () => {
    const wrapper = mountModal()
    const kimiButton = wrapper.findAll('button').find((button) => button.text().includes('Kimi'))
    expect(kimiButton).toBeDefined()
    await kimiButton!.trigger('click')

    await wrapper
      .get<HTMLInputElement>('[data-testid="cn-adaptive-base-url-chat_completions"]')
      .setValue('https://relay.example.com/v1')
    await wrapper.get<HTMLInputElement>('form#create-account-form input[type="password"]')
      .setValue('sk-relay')

    expect(wrapper.getComponent(ModelWhitelistSelectorStub).props('syncCredentials')).toMatchObject({
      platform: 'kimi',
      type: 'apikey',
      base_url: 'https://relay.example.com/v1',
      api_key: 'sk-relay'
    })
  })

  it.each([
    { platform: 'kimi', buttonLabel: 'Kimi' },
    { platform: 'zhipu', buttonLabel: '智谱 GLM' },
    { platform: 'deepseek', buttonLabel: 'DeepSeek' }
  ])('创建 $platform 账号时提交 Sub2API 倍率探测设置', async ({ platform, buttonLabel }) => {
    const wrapper = mountModal()
    const platformButton = wrapper.findAll('button').find((button) => button.text().includes(buttonLabel))
    expect(platformButton).toBeDefined()
    await platformButton!.trigger('click')

    await wrapper.get<HTMLInputElement>('input[data-tour="account-form-name"]')
      .setValue(`${buttonLabel} Sub2API`)
    await wrapper.get<HTMLInputElement>('form#create-account-form input[type="password"]')
      .setValue('sk-sub2api')

    const billingSection = wrapper.findAll('[data-account-editor-section="capacity"]')
      .find((section) => section.text().includes('admin.accounts.upstreamBilling.providerLabel'))
    expect(billingSection).toBeDefined()
    const sub2apiButton = billingSection!.findAll('button')
      .find((button) => button.text().includes('admin.accounts.upstreamBilling.providerSub2API'))
    expect(sub2apiButton).toBeDefined()
    await sub2apiButton!.trigger('click')

    const autoProbeToggle = billingSection!.get('button[aria-pressed="false"]')
    await autoProbeToggle.trigger('click')

    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]).toMatchObject({
      platform,
      extra: {
        upstream_provider: 'sub2api',
        upstream_billing_auto_probe_enabled: true
      }
    })
  })

  it.each([
    { enabled: false, expected: undefined },
    { enabled: true, expected: true }
  ])('创建 OpenAI OAuth 账号时按开关提交 namespace 摊平字段：$enabled', async ({ enabled, expected }) => {
    const wrapper = mountModal()
    const openAIButton = wrapper.findAll('button').find((button) => button.text().includes('OpenAI'))
    expect(openAIButton).toBeDefined()
    await openAIButton!.trigger('click')
    await wrapper.get<HTMLInputElement>('input[data-tour="account-form-name"]').setValue('OpenAI OAuth')

    const toggle = wrapper.get('[data-testid="create-openai-flatten-namespaces-toggle"]')
    expect(toggle.attributes('aria-checked')).toBe('false')
    if (enabled) await toggle.trigger('click')

    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()
    await wrapper.get('[data-testid="validate-openai-refresh-token"]').trigger('click')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    const extra = createAccountMock.mock.calls[0]?.[0]?.extra
    if (expected === true) {
      expect(extra?.openai_responses_flatten_namespaces).toBe(true)
    } else {
      expect(extra).not.toHaveProperty('openai_responses_flatten_namespaces')
    }
  })
})
