import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import type { DOMWrapper, VueWrapper } from '@vue/test-utils'

import RiskControlView from '../RiskControlView.vue'
import zh from '@/i18n/locales/zh'
import type {
  ContentModerationAction,
  ContentModerationConfig,
  ContentModerationLog,
  UpdateContentModerationConfig,
} from '@/api/admin/riskControl'

const {
  getConfig,
  updateConfig,
  getStatus,
  listLogs,
  getGroups,
  getKeywordCatalogStatus,
  syncKeywordCatalog,
  showError,
  showSuccess,
} = vi.hoisted(() => ({
  getConfig: vi.fn(),
  updateConfig: vi.fn(),
  getStatus: vi.fn(),
  listLogs: vi.fn(),
  getGroups: vi.fn(),
  getKeywordCatalogStatus: vi.fn(),
  syncKeywordCatalog: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    riskControl: {
      getConfig,
      updateConfig,
      getStatus,
      listLogs,
      testAPIKeys: vi.fn(),
      deleteFlaggedHash: vi.fn(),
      clearFlaggedHashes: vi.fn(),
      unbanUser: vi.fn(),
      getKeywordCatalogStatus,
      syncKeywordCatalog,
    },
    groups: {
      getAll: getGroups,
    },
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError,
    showSuccess,
  }),
}))

vi.mock('@/utils/apiError', () => ({
  extractApiErrorMessage: (_err: unknown, fallback: string) => fallback,
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, string | number>) => {
        if (key === 'admin.riskControl.preBlockAPIKeyLoadSummary') {
          return `同步并发 ${params?.active} / 可用 Key ${params?.available}，累计 ${params?.total} 次，worker：${params?.workerActive} / ${params?.workerTotal}`
        }
        return key.replace(/\{(\w+)\}/g, (_, token) => String(params?.[token] ?? `{${token}}`))
      },
    }),
  }
})

const baseConfig = (): ContentModerationConfig => ({
  enabled: true,
  mode: 'pre_block',
  base_url: 'https://api.openai.com',
  model: 'omni-moderation-latest',
  api_key_configured: false,
  api_key_masked: '',
  api_key_count: 0,
  api_key_masks: [],
  api_key_statuses: [],
  timeout_ms: 3000,
  sample_rate: 100,
  all_groups: true,
  group_ids: [],
  record_non_hits: false,
  worker_count: 4,
  queue_size: 32768,
  block_status: 403,
  block_message: '内容审计命中风险规则，请调整输入后重试',
  email_on_hit: true,
  auto_ban_enabled: true,
  ban_threshold: 10,
  deduct_balance_on_block_enabled: false,
  deduct_balance_on_block_amount: 0,
  permanent_ban_enabled: false,
  permanent_ban_threshold: 20,
  violation_window_hours: 720,
  retry_count: 2,
  hit_retention_days: 180,
  non_hit_retention_days: 3,
  pre_hash_check_enabled: false,
  blocked_keywords: [],
  keyword_blocking_mode: 'keyword_and_api',
  keyword_rule_packs: [
    {
      id: 'credential_theft',
      name: '账号与密钥盗取',
      description: '拦截索要密钥的请求。',
      action: 'keyword_block',
      enabled: true,
      keywords: ['API Key', 'Cookie'],
      built_in: true,
    },
  ],
  thresholds: {
    harassment: 0.98,
    sexual: 0.65,
  },
  model_filter: {
    type: 'all',
    models: [],
  },
  local_keyword_only: true,
  allowed_languages: ['zh', 'en'],
  scan_latest_user_input: true,
  scan_top_level_instructions: true,
  scan_new_skill_content: true,
  scan_new_tool_output: true,
  keyword_group_routing_enabled: true,
  cyber_policy_exclude_from_ban_count: false,
})

const runtimeStatus = () => ({
  enabled: true,
  risk_control_enabled: true,
  mode: 'pre_block',
  worker_count: 4,
  max_workers: 32,
  active_workers: 0,
  idle_workers: 4,
  queue_size: 32768,
  queue_length: 0,
  queue_usage_percent: 0,
  enqueued: 0,
  dropped: 0,
  processed: 0,
  errors: 0,
  pre_block_active: 0,
  pre_block_checked: 0,
  pre_block_allowed: 0,
  pre_block_blocked: 0,
  pre_block_errors: 0,
  pre_block_avg_latency_ms: 0,
  pre_block_api_key_active: 0,
  pre_block_api_key_available_count: 0,
  pre_block_api_key_total_calls: 0,
  pre_block_api_key_loads: [],
  api_key_statuses: [],
  flagged_hash_count: 0,
  last_cleanup_deleted_hit: 0,
  last_cleanup_deleted_non_hit: 0,
})

const AppLayoutStub = { template: '<div><slot /></div>' }
const BaseDialogStub = defineComponent({
  props: {
    show: {
      type: Boolean,
      default: false,
    },
  },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
})
const ModelWhitelistSelectorStub = defineComponent({
  props: {
    modelValue: {
      type: Array,
      default: () => [],
    },
  },
  emits: ['update:modelValue'],
  setup(props, { emit }) {
    const onInput = (event: Event) => {
      const value = (event.target as HTMLInputElement).value
      emit(
        'update:modelValue',
        value
          .split(/[,\n]/)
          .map((item) => item.trim())
          .filter(Boolean)
      )
    }
    return () =>
      h('input', {
        'data-test': 'model-filter-input',
        value: (props.modelValue as string[]).join('\n'),
        onInput,
      })
  },
})

const SelectStub = defineComponent({
  props: {
    modelValue: {
      type: [String, Number],
      default: '',
    },
    options: {
      type: Array,
      default: () => [],
    },
  },
  emits: ['update:modelValue', 'change'],
  setup(props, { emit }) {
    return () => h(
      'select',
      {
        value: props.modelValue,
        onChange: (event: Event) => {
          const value = (event.target as HTMLSelectElement).value
          emit('update:modelValue', value)
          emit('change', value)
        },
      },
      (props.options as Array<{ value: string | number; label: string }>).map((option) =>
        h('option', { value: option.value }, option.label)
      )
    )
  },
})

function moderationLog(action: ContentModerationAction, id: number): ContentModerationLog {
  return {
    id,
    request_id: `request-${id}`,
    user_id: 1,
    user_email: 'user@example.test',
    api_key_id: 1,
    api_key_name: 'test-key',
    group_id: 1,
    group_name: 'test-group',
    original_group_id: 1,
    original_group_name: 'test-group',
    effective_group_id: 1,
    effective_group_name: 'test-group',
    content_source: 'user',
    detected_languages: ['zh'],
    content_fingerprint: '',
    language_evidence: {
      detector_version: '', content_runes: 0, sampled_runes: 0, total_letter_runes: 0,
      script_counts: {}, language_counts: {}, language_ratio_bps: {}, latin_evidence_counts: {},
      min_language_runes: 0, min_language_ratio_bps: 0,
    },
    keyword_policy_rerouted: action === 'keyword_reroute',
    keyword_policy_reason: '',
    endpoint: '/v1/responses',
    provider: 'openai',
    model: 'gpt-5.6-sol',
    mode: 'pre_block',
    action,
    // 三种关键词命中放行历史数据可能是 flagged=false；标签必须仍以 action 为准。
    flagged: ['block', 'hash_block', 'keyword_block', 'language_block', 'cyber_policy'].includes(action),
    highest_category: 'keyword',
    highest_score: 1,
    category_scores: { keyword: 1 },
    threshold_snapshot: {},
    input_excerpt: 'test input',
    upstream_latency_ms: 10,
    error: action === 'error' ? 'moderation failed' : '',
    violation_count: 0,
    auto_banned: false,
    email_sent: false,
    user_status: 'active',
    queue_delay_ms: 0,
    created_at: '2026-07-29T00:00:00Z',
  }
}

function findButtonByText(wrapper: VueWrapper, text: string): DOMWrapper<HTMLButtonElement> {
  const button = wrapper.findAll<HTMLButtonElement>('button').find((item) => item.text().includes(text))
  if (!button) {
    throw new Error(`button not found: ${text}`)
  }
  return button
}

describe('admin RiskControlView', () => {
  beforeEach(() => {
    getConfig.mockReset()
    updateConfig.mockReset()
    getStatus.mockReset()
    listLogs.mockReset()
    getGroups.mockReset()
    getKeywordCatalogStatus.mockReset()
    syncKeywordCatalog.mockReset()
    showError.mockReset()
    showSuccess.mockReset()

    getConfig.mockResolvedValue(baseConfig())
    getStatus.mockResolvedValue(runtimeStatus())
    listLogs.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20, pages: 1 })
    getGroups.mockResolvedValue([])
    getKeywordCatalogStatus.mockResolvedValue({
      enabled: true,
      catalog_version: '2026.08.22',
      hash: '1234567890abcdef',
      source: 'remote',
      last_updated_at: '2026-08-22T08:00:00Z',
      last_error: '',
    })
    syncKeywordCatalog.mockResolvedValue({
      enabled: true,
      catalog_version: '2026.08.23',
      hash: 'abcdef1234567890',
      source: 'remote',
      last_updated_at: '2026-08-23T08:00:00Z',
      last_error: '',
    })
    updateConfig.mockImplementation(async (payload: UpdateContentModerationConfig) => ({
      ...baseConfig(),
      ...payload,
      model_filter: payload.model_filter ?? baseConfig().model_filter,
      api_key_configured: false,
      api_key_masked: '',
      api_key_count: 0,
      api_key_masks: [],
      api_key_statuses: [],
    }))
  })

  it('saves the selected model filter mode and models', async () => {
    const wrapper = mount(RiskControlView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          BaseDialog: BaseDialogStub,
          Icon: true,
          Select: true,
          Toggle: true,
          Pagination: true,
          ModelWhitelistSelector: ModelWhitelistSelectorStub,
        },
      },
    })

    await flushPromises()

    await findButtonByText(wrapper, 'admin.riskControl.openSettings').trigger('click')
    await findButtonByText(wrapper, 'admin.riskControl.tabs.scope').trigger('click')
    await findButtonByText(wrapper, 'admin.riskControl.modelFilterInclude').trigger('click')
    await wrapper.get('[data-test="model-filter-input"]').setValue('gpt-5.5, gpt-5.4')
    await findButtonByText(wrapper, 'admin.riskControl.saveConfig').trigger('click')
    await flushPromises()

    expect(updateConfig).toHaveBeenCalledWith(expect.objectContaining({
      model_filter: {
        type: 'include',
        models: ['gpt-5.5', 'gpt-5.4'],
      },
    }))
    expect(showError).not.toHaveBeenCalled()
  })

  it('keeps a server-disabled instructions scan disabled when saving', async () => {
    getConfig.mockResolvedValue({
      ...baseConfig(),
      scan_top_level_instructions: false,
    })
    const wrapper = mount(RiskControlView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          BaseDialog: BaseDialogStub,
          Icon: true,
          Select: true,
          Toggle: true,
          Pagination: true,
          ModelWhitelistSelector: ModelWhitelistSelectorStub,
        },
      },
    })

    await flushPromises()
    await findButtonByText(wrapper, 'admin.riskControl.openSettings').trigger('click')
    await findButtonByText(wrapper, 'admin.riskControl.saveConfig').trigger('click')
    await flushPromises()

    expect(updateConfig).toHaveBeenCalledWith(expect.objectContaining({
      scan_top_level_instructions: false,
    }))
  })

  it('shows every moderation action with its real outcome label and badge color', async () => {
    const cases: Array<{
      action: ContentModerationAction
      label: string
      color: string
    }> = [
      { action: 'allow', label: 'admin.riskControl.action.allow', color: 'bg-green-100' },
      { action: 'block', label: 'admin.riskControl.action.block', color: 'bg-red-100' },
      { action: 'hash_block', label: 'admin.riskControl.action.hashBlock', color: 'bg-red-100' },
      { action: 'keyword_block', label: 'admin.riskControl.action.keywordBlock', color: 'bg-red-100' },
      { action: 'keyword_audit', label: 'admin.riskControl.action.keywordAudit', color: 'bg-sky-100' },
      { action: 'keyword_allow_original', label: 'admin.riskControl.action.keywordAllowOriginal', color: 'bg-green-100' },
      { action: 'keyword_reroute', label: 'admin.riskControl.action.keywordReroute', color: 'bg-indigo-100' },
      { action: 'language_block', label: 'admin.riskControl.action.languageBlock', color: 'bg-red-100' },
      { action: 'error', label: 'admin.riskControl.action.error', color: 'bg-amber-100' },
      { action: 'cyber_policy', label: 'admin.riskControl.action.cyberPolicy', color: 'bg-red-100' },
    ]
    listLogs.mockResolvedValue({
      items: cases.map(({ action }, index) => moderationLog(action, index + 1)),
      total: cases.length,
      page: 1,
      page_size: 20,
      pages: 1,
    })

    const wrapper = mount(RiskControlView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          BaseDialog: BaseDialogStub,
          Icon: true,
          Select: true,
          Toggle: true,
          Pagination: true,
          ModelWhitelistSelector: ModelWhitelistSelectorStub,
        },
      },
    })

    await flushPromises()

    for (const item of cases) {
      const badge = wrapper.get(`[data-test="moderation-result-badge"][data-action="${item.action}"]`)
      expect(badge.text()).toBe(item.label)
      expect(badge.classes()).toContain(item.color)
      expect(badge.classes()).not.toContain('bg-pink-100')
    }
  })

  it('uses explicit Chinese labels for keyword hit outcomes and true non-hits', () => {
    const labels = zh.admin.riskControl.action
    expect(labels.allow).toBe('未命中')
    expect(labels.keywordAudit).toBe('已命中 · 仅审计')
    expect(labels.keywordAllowOriginal).toBe('已命中 · 原组放行')
    expect(labels.keywordReroute).toBe('已命中 · 已切组放行')
  })

  it('distinguishes a true non-hit from an observed hit that was allowed', async () => {
    const nonHit = moderationLog('allow', 1)
    const observedHit = moderationLog('allow', 2)
    observedHit.flagged = true
    observedHit.matched_keyword = 'exp'
    listLogs.mockResolvedValue({
      items: [nonHit, observedHit],
      total: 2,
      page: 1,
      page_size: 20,
      pages: 1,
    })

    const wrapper = mount(RiskControlView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          BaseDialog: BaseDialogStub,
          Icon: true,
          Select: true,
          Toggle: true,
          Pagination: true,
          ModelWhitelistSelector: ModelWhitelistSelectorStub,
        },
      },
    })
    await flushPromises()

    const badges = wrapper.findAll('[data-test="moderation-result-badge"][data-action="allow"]')
    expect(badges).toHaveLength(2)
    expect(badges[0].text()).toBe('admin.riskControl.action.allow')
    expect(badges[1].text()).toBe('admin.riskControl.action.auditAllow')
    expect(wrapper.text()).toContain('admin.riskControl.matchedKeyword：exp')
  })

  it('shows the privacy notice only for language evidence and a neutral empty state for other logs', async () => {
    const languageLog = moderationLog('language_block', 1)
    languageLog.input_excerpt = ''
    languageLog.language_evidence = {
      ...languageLog.language_evidence,
      detector_version: 'v2',
      language_counts: { ja: 12 },
      language_ratio_bps: { ja: 10000 },
    }
    const ordinaryLog = moderationLog('allow', 2)
    ordinaryLog.input_excerpt = ''
    ordinaryLog.language_evidence.detector_version = ''
    ordinaryLog.language_evidence.language_counts = {}
    ordinaryLog.language_evidence.language_ratio_bps = {}
    listLogs.mockResolvedValue({
      items: [languageLog, ordinaryLog],
      total: 2,
      page: 1,
      page_size: 20,
      pages: 1,
    })

    const wrapper = mount(RiskControlView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          BaseDialog: BaseDialogStub,
          Icon: true,
          Select: true,
          Toggle: true,
          Pagination: true,
          ModelWhitelistSelector: ModelWhitelistSelectorStub,
        },
      },
    })
    await flushPromises()

    await wrapper.get('button[title="ja 12"]').trigger('click')
    expect(wrapper.get('[data-test="language-evidence-privacy"]').text()).toBe('admin.riskControl.languageEvidencePrivacy')
    expect(wrapper.find('[data-test="empty-input-detail"]').exists()).toBe(false)

    await findButtonByText(wrapper, 'common.close').trigger('click')
    await wrapper.get('button[title="-"]').trigger('click')
    expect(wrapper.get('[data-test="empty-input-detail"]').text()).toBe('-')
    expect(wrapper.find('[data-test="language-evidence-privacy"]').exists()).toBe(false)
  })

  it('sends the blocked result filter when the administrator selects it', async () => {
    const wrapper = mount(RiskControlView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          BaseDialog: BaseDialogStub,
          Icon: true,
          Select: SelectStub,
          Toggle: true,
          Pagination: true,
          ModelWhitelistSelector: ModelWhitelistSelectorStub,
        },
      },
    })

    await flushPromises()
    listLogs.mockClear()

    await wrapper.findAll('select')[0].setValue('blocked')
    await flushPromises()

    expect(listLogs).toHaveBeenCalledWith(expect.objectContaining({ result: 'blocked' }))
  })

  it('interprets datetime-local log filters as China Shanghai time', async () => {
    const originalTimeZone = process.env.TZ
    process.env.TZ = 'America/Los_Angeles'
    try {
      const wrapper = mount(RiskControlView, {
        global: {
          stubs: {
            AppLayout: AppLayoutStub,
            BaseDialog: BaseDialogStub,
            Icon: true,
            Select: true,
            Toggle: true,
            Pagination: true,
            ModelWhitelistSelector: ModelWhitelistSelectorStub,
          },
        },
      })
      await flushPromises()
      listLogs.mockClear()

      const inputs = wrapper.findAll<HTMLInputElement>('input[type="datetime-local"]')
      await inputs[0].setValue('2026-08-04T00:30')
      await inputs[1].setValue('2026-08-04T01:30')
      await flushPromises()

      expect(listLogs).toHaveBeenLastCalledWith(expect.objectContaining({
        from: '2026-08-03T16:30:00.000Z',
        to: '2026-08-03T17:30:00.000Z',
      }))
      wrapper.unmount()
    } finally {
      if (originalTimeZone === undefined) delete process.env.TZ
      else process.env.TZ = originalTimeZone
    }
  })

  it('submits edited risk control thresholds when saving moderation config', async () => {
    const wrapper = mount(RiskControlView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          BaseDialog: BaseDialogStub,
          Icon: true,
          Select: true,
          Toggle: true,
          Pagination: true,
          ModelWhitelistSelector: ModelWhitelistSelectorStub,
        },
      },
    })

    await flushPromises()

    await findButtonByText(wrapper, 'admin.riskControl.openSettings').trigger('click')
    await findButtonByText(wrapper, 'admin.riskControl.tabs.riskThresholds').trigger('click')
    await wrapper.get('[data-test="risk-threshold-sexual"]').setValue('72')
    await wrapper.get('[data-test="risk-threshold-harassment"]').setValue('99')
    await findButtonByText(wrapper, 'admin.riskControl.saveConfig').trigger('click')
    await flushPromises()

    expect(updateConfig).toHaveBeenCalledWith(expect.objectContaining({
      thresholds: expect.objectContaining({
        sexual: 0.72,
        harassment: 0.99,
      }),
    }))
    expect(showError).not.toHaveBeenCalled()
  })

  it('lets admins change built-in rule pack action and inspect keywords', async () => {
    const wrapper = mount(RiskControlView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          BaseDialog: BaseDialogStub,
          Icon: true,
          Select: true,
          Toggle: true,
          Pagination: true,
          ModelWhitelistSelector: ModelWhitelistSelectorStub,
        },
      },
    })

    await flushPromises()

    await findButtonByText(wrapper, 'admin.riskControl.openSettings').trigger('click')
    await findButtonByText(wrapper, 'admin.riskControl.tabs.keywords').trigger('click')
    expect(wrapper.text()).toContain('账号与密钥盗取')
    expect(wrapper.text()).toContain('admin.riskControl.keywordRuleActionBlock')

    await findButtonByText(wrapper, 'admin.riskControl.keywordRuleActionAudit').trigger('click')
    await findButtonByText(wrapper, 'admin.riskControl.keywordRuleShowDetails').trigger('click')
    expect(wrapper.text()).toContain('API Key')
    expect(wrapper.text()).toContain('Cookie')

    await findButtonByText(wrapper, 'admin.riskControl.saveConfig').trigger('click')
    await flushPromises()

    expect(updateConfig).toHaveBeenCalledWith(expect.objectContaining({
      keyword_rule_packs: [
        expect.objectContaining({
          id: 'credential_theft',
          action: 'keyword_audit',
          enabled: true,
          keywords: ['API Key', 'Cookie'],
          built_in: true,
        }),
      ],
    }))
    expect(showError).not.toHaveBeenCalled()
  })

  it('shows and synchronizes the remote keyword catalog in keyword settings', async () => {
    const refreshedConfig = baseConfig()
    refreshedConfig.keyword_rule_packs = [
      ...refreshedConfig.keyword_rule_packs,
      {
        id: 'new_remote_pack',
        name: '新增远程规则包',
        description: '同步后立即展示。',
        action: 'keyword_audit',
        enabled: true,
        keywords: ['新增远程审计短语'],
        built_in: true,
      },
    ]
    getConfig.mockResolvedValueOnce(baseConfig()).mockResolvedValueOnce(refreshedConfig)
    getKeywordCatalogStatus.mockResolvedValueOnce({
      enabled: true,
      catalog_version: '2026.08.22',
      hash: '1234567890abcdef',
      source: 'disk',
      last_updated_at: '2026-08-22T08:00:00Z',
      last_error: '',
    })
    const wrapper = mount(RiskControlView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          BaseDialog: BaseDialogStub,
          Icon: true,
          Select: true,
          Toggle: true,
          Pagination: true,
          ModelWhitelistSelector: ModelWhitelistSelectorStub,
        },
      },
    })
    await flushPromises()

    await findButtonByText(wrapper, 'admin.riskControl.openSettings').trigger('click')
    await findButtonByText(wrapper, 'admin.riskControl.tabs.keywords').trigger('click')
    expect(wrapper.get('[data-test="keyword-catalog-status"]').text()).toContain('2026.08.22')
    expect(wrapper.get('[data-test="keyword-catalog-status"]').text()).toContain('admin.riskControl.keywordCatalog.sourceCache')

    await wrapper.get('[data-test="sync-keyword-catalog"]').trigger('click')
    await flushPromises()
    expect(syncKeywordCatalog).toHaveBeenCalledOnce()
    expect(getConfig).toHaveBeenCalledTimes(2)
    expect(wrapper.get('[data-test="keyword-catalog-status"]').text()).toContain('2026.08.23')
    expect(wrapper.text()).toContain('新增远程规则包')
    expect(showSuccess).toHaveBeenCalledWith('admin.riskControl.keywordCatalog.syncSuccess')
  })

  it('describes worker runtime as async audit and pre-block record processing', async () => {
    getStatus.mockResolvedValue({
      ...runtimeStatus(),
      mode: 'observe',
      processed: 12,
      queue_length: 2,
    })

    const wrapper = mount(RiskControlView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          BaseDialog: BaseDialogStub,
          Icon: true,
          Select: true,
          Toggle: true,
          Pagination: true,
          ModelWhitelistSelector: ModelWhitelistSelectorStub,
        },
      },
    })

    await flushPromises()

    expect(wrapper.text()).toContain('admin.riskControl.workerStatusHint')
    expect(wrapper.text()).not.toContain('admin.riskControl.preBlockSyncStatus')
    expect(wrapper.text()).toContain('admin.riskControl.records')
    expect(wrapper.text()).toContain('12')
    expect(wrapper.text()).toContain('2 / 32,768')
  })

  it('shows pre-block synchronous moderation metrics separately from worker queue', async () => {
    getStatus.mockResolvedValue({
      ...runtimeStatus(),
      pre_block_active: 2,
      pre_block_checked: 128,
      pre_block_allowed: 120,
      pre_block_blocked: 8,
      pre_block_errors: 1,
      pre_block_avg_latency_ms: 86,
      pre_block_api_key_active: 2,
      pre_block_api_key_available_count: 2,
      pre_block_api_key_total_calls: 128,
      active_workers: 3,
      worker_count: 7,
      pre_block_api_key_loads: [
        {
          index: 0,
          key_hash: 'hash-one',
          masked: 'sk-...one',
          status: 'ok',
          active: 1,
          total: 72,
          success: 70,
          errors: 2,
          avg_latency_ms: 84,
          last_latency_ms: 80,
          last_http_status: 200,
        },
        {
          index: 1,
          key_hash: 'hash-two',
          masked: 'sk-...two',
          status: 'ok',
          active: 1,
          total: 56,
          success: 56,
          errors: 0,
          avg_latency_ms: 90,
          last_latency_ms: 92,
          last_http_status: 200,
        },
      ],
    })

    const wrapper = mount(RiskControlView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          BaseDialog: BaseDialogStub,
          Icon: true,
          Select: true,
          Toggle: true,
          Pagination: true,
          ModelWhitelistSelector: ModelWhitelistSelectorStub,
        },
      },
    })

    await flushPromises()

    expect(wrapper.text()).toContain('admin.riskControl.preBlockSyncStatus')
    expect(wrapper.text()).toContain('admin.riskControl.preBlockSyncHint')
    expect(wrapper.text()).not.toContain('admin.riskControl.workerStatus')
    expect(wrapper.text()).toContain('admin.riskControl.records')
    expect(wrapper.text()).toContain('128')
    expect(wrapper.text()).toContain('120')
    expect(wrapper.text()).toContain('8')
    expect(wrapper.text()).toContain('86 ms')
    expect(wrapper.text()).toContain('admin.riskControl.preBlockAPIKeyLoad')
    expect(wrapper.text()).toContain('sk-...one')
    expect(wrapper.text()).toContain('sk-...two')
    expect(wrapper.text()).toContain('72')
    expect(wrapper.text()).toContain('56')
    expect(wrapper.text()).toContain('同步并发 2 / 可用 Key 2，累计 128 次，worker：3 / 7')

    const runtimeCards = wrapper.get('[data-test="pre-block-runtime-cards"]')
    const syncCard = wrapper.get('[data-test="pre-block-sync-card"]')
    const apiKeyLoadCard = wrapper.get('[data-test="pre-block-api-key-load-card"]')

    expect(runtimeCards.classes()).toEqual(expect.arrayContaining([
      'grid',
      'grid-cols-1',
      'xl:grid-cols-[minmax(0,520px)_minmax(0,1fr)]',
    ]))
    expect(syncCard.element.parentElement).toBe(runtimeCards.element)
    expect(apiKeyLoadCard.element.parentElement).toBe(runtimeCards.element)
    expect(syncCard.classes()).toContain('card')
    expect(apiKeyLoadCard.classes()).toContain('card')
    expect(syncCard.get('h2').text()).toBe('admin.riskControl.preBlockSyncStatus')
    expect(syncCard.text()).toContain('admin.riskControl.preBlockSyncHint')
    expect(apiKeyLoadCard.get('h2').text()).toBe('admin.riskControl.preBlockAPIKeyLoad')
    expect(apiKeyLoadCard.text()).toContain('admin.riskControl.preBlockAPIKeyLoadHint')
    expect(wrapper.get('[data-test="pre-block-api-key-load-list"]').classes()).toEqual(expect.arrayContaining([
      'max-h-[280px]',
      'overflow-y-auto',
    ]))
  })
})
