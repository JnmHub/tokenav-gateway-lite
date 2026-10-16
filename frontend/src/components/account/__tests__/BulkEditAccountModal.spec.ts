import { describe, expect, it, vi, beforeEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import BulkEditAccountModal from '../BulkEditAccountModal.vue'
import ModelWhitelistSelector from '../ModelWhitelistSelector.vue'
import { adminAPI } from '@/api/admin'

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showInfo: vi.fn()
  })
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      bulkUpdate: vi.fn(),
      checkMixedChannelRisk: vi.fn()
    }
  }
}))

vi.mock('@/api/admin/accounts', () => ({
  getAntigravityDefaultModelMapping: vi.fn()
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

function mountModal(extraProps: Record<string, unknown> = {}) {
  return mount(BulkEditAccountModal, {
    props: {
      show: true,
      accountIds: [1, 2],
      selectedPlatforms: ['antigravity'],
      selectedTypes: ['apikey'],
      proxies: [],
      groups: [],
      ...extraProps
    } as any,
    global: {
      stubs: {
        BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
        ConfirmDialog: true,
        Select: {
          props: ['modelValue', 'options'],
          emits: ['update:modelValue'],
          template: `
            <select
              v-bind="$attrs"
              :value="modelValue"
              @change="$emit('update:modelValue', $event.target.value)"
            >
              <option v-for="option in options" :key="option.value" :value="option.value">
                {{ option.label }}
              </option>
            </select>
          `
        },
        ProxySelector: true,
        GroupSelector: true,
        Icon: true
      }
    }
  })
}

describe('BulkEditAccountModal', () => {
  beforeEach(() => {
    vi.mocked(adminAPI.accounts.bulkUpdate).mockReset()
    vi.mocked(adminAPI.accounts.checkMixedChannelRisk).mockReset()

    vi.mocked(adminAPI.accounts.bulkUpdate).mockResolvedValue({
      success: 2,
      failed: 0,
      results: []
    } as any)
    vi.mocked(adminAPI.accounts.checkMixedChannelRisk).mockResolvedValue({
      has_risk: false
    } as any)
  })

  it('only submits the account keyword policy after the field is enabled', async () => {
    const wrapper = mountModal({
      selectedPlatforms: ['openai'],
      selectedTypes: ['oauth']
    })

    await wrapper.get('#bulk-edit-keyword-policy-enabled').setValue(true)
    await wrapper.get('[data-testid="bulk-edit-account-keyword-policy-protect"]').trigger('click')
    await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
      extra: { keyword_policy_mode: 'protect' }
    })
  })

  it('antigravity 白名单包含 Gemini 图片模型且过滤掉普通 GPT 模型', async () => {
    const wrapper = mountModal()
    const selector = wrapper.findComponent(ModelWhitelistSelector)
    expect(selector.exists()).toBe(true)

    await selector.find('div.cursor-pointer').trigger('click')

    expect(wrapper.text()).toContain('gemini-3.1-flash-image')
    expect(wrapper.text()).toContain('gemini-2.5-flash-image')
    expect(wrapper.text()).not.toContain('gpt-5.3-codex')
  })

  it('antigravity 映射预设包含图片映射并过滤 OpenAI 预设', async () => {
    const wrapper = mountModal()

    const mappingTab = wrapper.findAll('button').find((btn) => btn.text().includes('admin.accounts.modelMapping'))
    expect(mappingTab).toBeTruthy()
    await mappingTab!.trigger('click')

    expect(wrapper.text()).toContain('3.1-Flash-Image透传')
    expect(wrapper.text()).toContain('3-Pro-Image→3.1')
    expect(wrapper.text()).not.toContain('GPT-5.3 Codex Spark')
  })

  it('仅勾选模型限制且白名单留空时，应提交空 model_mapping 以支持所有模型', async () => {
    const wrapper = mountModal({
      selectedPlatforms: ['anthropic'],
      selectedTypes: ['apikey']
    })

    await wrapper.get('#bulk-edit-model-restriction-enabled').setValue(true)
    await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledTimes(1)
    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
      credentials: {
        model_mapping: {}
      }
    })
  })

  it('OpenAI 账号批量编辑可开启自动透传', async () => {
    const wrapper = mountModal({
      selectedPlatforms: ['openai'],
      selectedTypes: ['oauth']
    })

    await wrapper.get('#bulk-edit-openai-passthrough-enabled').setValue(true)
    await wrapper.get('#bulk-edit-openai-passthrough-toggle').trigger('click')
    await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledTimes(1)
    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
      extra: {
        openai_passthrough: true
      }
    })
  })

  it.each([
    { enabled: false, expected: false },
    { enabled: true, expected: true }
  ])('OpenAI OAuth 批量编辑可明确设置 namespace 摊平为 $expected', async ({ enabled, expected }) => {
    const wrapper = mountModal({
      selectedPlatforms: ['openai'],
      selectedTypes: ['oauth']
    })

    await wrapper.get('#bulk-edit-openai-flatten-namespaces-enabled').setValue(true)
    if (enabled) await wrapper.get('#bulk-edit-openai-flatten-namespaces-toggle').trigger('click')
    await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledTimes(1)
    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
      extra: {
        openai_responses_flatten_namespaces: expected
      }
    })
  })

  it('OpenAI OAuth 批量编辑未勾选字段时保持原值且不提交', async () => {
    const wrapper = mountModal({
      selectedPlatforms: ['openai'],
      selectedTypes: ['oauth']
    })

    await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(adminAPI.accounts.bulkUpdate).not.toHaveBeenCalled()
  })

  it('namespace 摊平开关不对 setup-token 等非 OAuth 选择展示', () => {
    const wrapper = mountModal({
      selectedPlatforms: ['openai'],
      selectedTypes: ['oauth', 'setup-token']
    })

    expect(wrapper.find('#bulk-edit-openai-flatten-namespaces-enabled').exists()).toBe(false)
  })

  it('OpenAI 账号批量编辑可统一开启厂商长上下文计费', async () => {
    const wrapper = mountModal({
      selectedPlatforms: ['openai'],
      selectedTypes: ['oauth', 'setup-token', 'apikey']
    })

    await wrapper.get('#bulk-edit-openai-long-context-billing-enabled').setValue(true)
    await wrapper.get('[data-testid="bulk-edit-openai-long-context-billing-toggle"]').trigger('click')
    await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledTimes(1)
    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
      extra: {
        openai_long_context_billing_enabled: true
      }
    })
  })

  it('OpenAI OAuth 批量编辑应提交 OAuth 专属 WS mode 字段', async () => {
    const wrapper = mountModal({
      selectedPlatforms: ['openai'],
      selectedTypes: ['oauth']
    })

    await wrapper.get('#bulk-edit-openai-ws-mode-enabled').setValue(true)
    await wrapper.get('[data-testid="bulk-edit-openai-ws-mode-select"]').setValue('passthrough')
    await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledTimes(1)
    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
      extra: {
        openai_oauth_responses_websockets_v2_mode: 'passthrough',
        openai_oauth_responses_websockets_v2_enabled: true
      }
    })
  })

  it('OpenAI API Key 批量编辑不显示 WS mode 入口', () => {
    const wrapper = mountModal({
      selectedPlatforms: ['openai'],
      selectedTypes: ['apikey']
    })

    expect(wrapper.find('#bulk-edit-openai-ws-mode-enabled').exists()).toBe(false)
  })

  it('Grok OAuth 批量编辑可用区域端点并提交请求头覆写', async () => {
    const wrapper = mountModal({
      selectedPlatforms: ['grok'],
      selectedTypes: ['oauth']
    })

    const presets = wrapper.findAll('[data-testid="grok-base-url-preset"]')
    expect(presets).toHaveLength(5)
    await presets[3].trigger('click')

    await wrapper.get('#bulk-edit-header-override-enabled').setValue(true)
    await wrapper.get('#bulk-edit-header-override-body > button').trigger('click')
    await wrapper.findAll('#bulk-edit-header-override-body button')
      .find((button) => button.text().includes('admin.accounts.headerOverride.addRow'))!
      .trigger('click')

    const nameInput = wrapper.get<HTMLInputElement>(
      'input[placeholder="admin.accounts.headerOverride.namePlaceholder"]'
    )
    const valueInput = wrapper.get<HTMLInputElement>(
      'input[placeholder="admin.accounts.headerOverride.valuePlaceholder"]'
    )
    await nameInput.setValue('x-tenant')
    await valueInput.setValue('tenant-a')
    await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
      credentials: {
        base_url: 'https://us-west-2.api.x.ai/v1',
        header_override_enabled: true,
        header_overrides: {
          'x-tenant': 'tenant-a'
        }
      }
    })
  })

  it('OpenAI OAuth 批量编辑应提交 codex_cli_only 字段', async () => {
    const wrapper = mountModal({
      selectedPlatforms: ['openai'],
      selectedTypes: ['oauth']
    })

    await wrapper.get('#bulk-edit-openai-codex-cli-only-enabled').setValue(true)
    await wrapper.get('#bulk-edit-openai-codex-cli-only-toggle').trigger('click')
    await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledTimes(1)
    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
      extra: {
        codex_cli_only: true
      }
    })
  })

  it('OpenAI OAuth 批量编辑应提交 codex_cli_only_allowed_clients 字段', async () => {
    const wrapper = mountModal({
      selectedPlatforms: ['openai'],
      selectedTypes: ['oauth']
    })

    await wrapper.get('#bulk-edit-openai-codex-allow-claude-code-enabled').setValue(true)
    await wrapper.get('#bulk-edit-openai-codex-allow-claude-code-toggle').trigger('click')
    await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledTimes(1)
    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
      extra: {
        codex_cli_only_allowed_clients: ['claude_code']
      }
    })
  })

  it('OpenAI 账号批量编辑可彻底阻断 Codex 图片工具', async () => {
    const wrapper = mountModal({
      selectedPlatforms: ['openai'],
      selectedTypes: ['oauth', 'setup-token', 'apikey']
    })

    await wrapper.get('#bulk-edit-codex-image-tool-enabled').setValue(true)
    await wrapper.get('[data-testid="bulk-edit-codex-image-tool-block"]').trigger('click')
    await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
      extra: {
        codex_image_generation_bridge: null,
        codex_image_generation_bridge_enabled: null,
        codex_image_generation_explicit_tool_policy: 'strip'
      }
    })
  })

  it('OpenAI 账号批量恢复继承时显式清除旧图片策略', async () => {
    const wrapper = mountModal({
      selectedPlatforms: ['openai'],
      selectedTypes: ['apikey']
    })

    await wrapper.get('#bulk-edit-codex-image-tool-enabled').setValue(true)
    await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
      extra: {
        codex_image_generation_bridge: null,
        codex_image_generation_bridge_enabled: null,
        codex_image_generation_explicit_tool_policy: null
      }
    })
  })

  it('OpenAI API Key 批量编辑应提交 API Key 专属 WS mode 字段', async () => {
    const wrapper = mountModal({
      selectedPlatforms: ['openai'],
      selectedTypes: ['apikey']
    })

    await wrapper.get('#bulk-edit-openai-apikey-ws-mode-enabled').setValue(true)
    await wrapper.get('[data-testid="bulk-edit-openai-apikey-ws-mode-select"]').setValue('ctx_pool')
    await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledTimes(1)
    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
      extra: {
        openai_apikey_responses_websockets_v2_mode: 'ctx_pool',
        openai_apikey_responses_websockets_v2_enabled: true
      }
    })
  })

  it('筛选 OpenAI 账号批量编辑应提交 Compact 模式和专属模型映射', async () => {
    const wrapper = mountModal({
      accountIds: [],
      selectedPlatforms: [],
      selectedTypes: [],
      target: {
        mode: 'filtered',
        filters: { platform: 'openai' },
        previewCount: 12,
        selectedPlatforms: ['openai'],
        selectedTypes: ['oauth', 'apikey']
      }
    })

    await wrapper.get('#bulk-edit-openai-compact-mode-enabled').setValue(true)
    await wrapper.get('[data-testid="bulk-edit-openai-compact-mode-select"]').setValue('force_on')
    await wrapper.get('#bulk-edit-openai-compact-model-mapping-enabled').setValue(true)
    await wrapper.get('[data-testid="bulk-edit-openai-compact-model-mapping-add"]').trigger('click')
    const inputs = wrapper.findAll('[data-testid="bulk-edit-openai-compact-model-mapping-input"]')
    await inputs[0].setValue('gpt-5.4')
    await inputs[1].setValue('gpt-5.4-openai-compact')
    await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledTimes(1)
    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith({
      filters: { platform: 'openai' },
      extra: {
        openai_compact_mode: 'force_on'
      },
      credentials: {
        compact_model_mapping: {
          'gpt-5.4': 'gpt-5.4-openai-compact'
        }
      }
    })
  })

  it('OpenAI 账号批量编辑可关闭自动透传', async () => {
    const wrapper = mountModal({
      selectedPlatforms: ['openai'],
      selectedTypes: ['apikey']
    })

    await wrapper.get('#bulk-edit-openai-passthrough-enabled').setValue(true)
    await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledTimes(1)
    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
      extra: {
        openai_passthrough: false,
        openai_oauth_passthrough: false
      }
    })
  })

  it('开启 OpenAI 自动透传时不再同时提交模型限制', async () => {
    const wrapper = mountModal({
      selectedPlatforms: ['openai'],
      selectedTypes: ['oauth']
    })

    await wrapper.get('#bulk-edit-openai-passthrough-enabled').setValue(true)
    await wrapper.get('#bulk-edit-openai-passthrough-toggle').trigger('click')
    await wrapper.get('#bulk-edit-model-restriction-enabled').setValue(true)
    await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledTimes(1)
    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith([1, 2], {
      extra: {
        openai_passthrough: true
      }
    })
    expect(wrapper.text()).toContain('admin.accounts.openai.modelRestrictionDisabledByPassthrough')
  })

  it('filtered-results 模式下应提交 filters 而不是 account_ids', async () => {
    const wrapper = mountModal({
      accountIds: [],
      target: {
        mode: 'filtered',
        filters: {
          platform: 'openai',
          type: 'oauth',
          status: 'active',
          group: '12',
          search: 'bulk-target',
          privacy_mode: 'training_set_cf_blocked'
        },
        previewCount: 5,
        selectedPlatforms: ['openai'],
        selectedTypes: ['oauth']
      }
    })

    await wrapper.get('#bulk-edit-status-enabled').setValue(true)
    await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledTimes(1)
    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledWith({
      filters: {
        platform: 'openai',
        type: 'oauth',
        status: 'active',
        group: '12',
        search: 'bulk-target',
        privacy_mode: 'training_set_cf_blocked'
      },
      status: 'active'
    })
  })
})
