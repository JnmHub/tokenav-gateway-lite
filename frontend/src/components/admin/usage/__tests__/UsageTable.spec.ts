import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'

import UsageTable from '../UsageTable.vue'

const messages: Record<string, string> = {
  'admin.usage.userDeletedBadge': 'Deleted',
  'admin.usage.grokTemporarilyPaused': 'Temporarily paused',
  'admin.usage.grokRequestQuotaShort': 'Requests',
  'admin.usage.grokTokenQuotaShort': 'Tokens',
  'admin.usage.grokPauseReasonUnknown': 'Pause reason was not recorded',
  'admin.usage.grokPauseTooltip': 'Pause reason: {reason}; resumes at: {until}',
  'usage.costDetails': 'Cost Breakdown',
  'admin.usage.inputCost': 'Input Cost',
  'admin.usage.outputCost': 'Output Cost',
  'admin.usage.cacheCreationCost': 'Cache Creation Cost',
  'admin.usage.cacheReadCost': 'Cache Read Cost',
  'usage.inputTokenPrice': 'Input price',
  'usage.imageInputTokens': 'Image Input Tokens',
  'usage.imageInputTokenPrice': 'Image Input Price',
  'usage.imageInputCost': 'Image Input Cost',
  'usage.imageInputPriceSource': 'Image Input Price Source',
  'usage.imageInputPriceSourceChannel': 'Channel/model override',
  'usage.imageInputPriceSourceModelCatalog': 'System model catalog',
  'usage.imageInputPriceSourceStaticModel': 'Built-in model price',
  'usage.imageInputPriceSourceTextFallback': 'Inherited text input price',
  'usage.imageInputPriceSourceUnknown': 'Historical or unrecorded',
  'usage.outputTokenPrice': 'Output price',
  'usage.perMillionTokens': '/ 1M tokens',
  'usage.serviceTier': 'Service tier',
  'usage.reasoningEffort': 'Actual Reasoning Effort',
  'usage.reasoningEffortUnspecified': '— (Unspecified)',
  'usage.reasoningEffortLevels.none': 'None',
  'usage.reasoningEffortLevels.minimal': 'Minimal',
  'usage.reasoningEffortLevels.high': 'High',
  'usage.serviceTierPriority': 'Priority',
  'usage.serviceTierFlex': 'Flex',
  'usage.serviceTierStandard': 'Standard processing',
  'usage.requestedServiceTier': 'Client requested mode',
  'usage.sentServiceTier': 'Mode sent upstream',
  'usage.responseServiceTier': 'Upstream response mode',
  'usage.priorityPolicyMode': 'Gateway policy',
  'usage.priorityPolicyFollowRequest': 'Follow request',
  'usage.priorityPolicyForcePriority': 'Force Priority',
  'usage.priorityPolicyForceStandard': 'Force standard',
  'usage.priorityDetectionSource': 'Final decision source',
  'usage.priorityDetectionRequest': 'Determined from the upstream request',
  'usage.priorityDetectionResponse': 'Determined from the upstream response',
  'usage.priorityDetectionRequestAfterInvalidResponse': 'Invalid upstream response; fell back to request',
  'usage.priorityDetectionStandardDefault': 'No valid tier evidence; billed as standard',
  'usage.priorityDetectionStandardAfterInvalidResponse': 'Invalid upstream response; billed as standard',
  'usage.priorityDetectionHistorical': 'Historical row without decision evidence',
  'usage.priorityPolicyAction': 'Policy action',
  'usage.priorityPolicyActionPass': 'Pass through',
  'usage.priorityPolicyActionFilter': 'Filter tier field',
  'usage.priorityPolicyActionBlock': 'Block request',
  'usage.priorityPolicyActionForcePriority': 'Force Priority',
  'usage.priorityPolicyActionFilterInvalid': 'Filter invalid tier field',
  'usage.priorityPolicyRuleId': 'Matched rule ID',
  'usage.priorityPolicyNoRule': 'No rule matched',
  'usage.priorityPolicyFallback': 'Used rule fallback action',
  'usage.priorityPolicyFallbackYes': 'Yes',
  'usage.priorityPolicyFallbackNo': 'No',
  'usage.rate': 'Rate',
  'usage.accountMultiplier': 'Account rate',
  'usage.original': 'Original',
  'usage.userBilled': 'User billed',
  'usage.accountBilled': 'Account billed',
  'usage.imageUnit': ' images',
  'usage.imageCount': 'Image count',
  'usage.imageBillingSize': 'Billing size',
  'usage.imageInputSize': 'Input size',
  'usage.imageOutputSize': 'Output size',
  'usage.imageSizeSource': 'Size source',
  'usage.imageSizeBreakdown': 'Size breakdown',
  'usage.imageSizeSourceOutput': 'Upstream output',
  'usage.imageSizeSourceInput': 'Request input',
  'usage.imageSizeSourceDefault': 'Default billing tier',
  'usage.imageSizeSourceLegacy': 'Legacy record',
  'usage.imageSizeSourceMissing': 'Not recorded',
  'usage.imageSizeNotRecorded': 'not recorded',
  'usage.imageSizeLegacyUnstandardized': 'legacy unstandardized',
  'usage.imageSizeUnknown': 'unknown',
  'usage.imageUnitPrice': 'Per-image price',
  'usage.imageTotalPrice': 'Image total price',
  'usage.videoCount': 'Video count',
  'usage.videoResolution': 'Video resolution',
  'usage.videoDuration': 'Video duration',
  'usage.videoUnitPrice': 'Per-second price',
  'usage.videoTotalPrice': 'Video total price',
  'usage.videoUnit': ' videos',
  'usage.secondUnit': 's',
  'usage.latencyFirstToken': 'First Token',
  'usage.latencyDuration': 'Total',
  'usage.latencyHealth.good': 'Normal',
  'usage.latencyHealth.warn': 'Elevated',
  'usage.latencyHealth.slow': 'Slow',
  'usage.latencyHealth.critical': 'Critical',
  'admin.usage.billingModeToken': 'Token',
  'admin.usage.billingModePerRequest': 'Per request',
  'admin.usage.billingModeImage': 'Image',
  'admin.usage.billingModeVideo': 'Video',
}

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => messages[key] ?? key,
    }),
  }
})

const DataTableStub = {
  props: ['data'],
  template: `
    <div>
      <div v-for="row in data" :key="row.request_id">
        <slot name="cell-account" :row="row" />
        <slot name="cell-model" :row="row" :value="row.model" />
        <slot name="cell-reasoning_effort" :row="row" />
        <slot name="cell-billing_mode" :row="row" />
        <slot name="cell-tokens" :row="row" />
        <slot name="cell-cost" :row="row" />
        <slot name="cell-latency" :row="row" />
      </div>
    </div>
  `,
}

const baseImageRow = {
  request_id: 'req-admin-image',
  model: 'gpt-image-2',
  actual_cost: 0.4,
  total_cost: 0.4,
  account_rate_multiplier: 1,
  rate_multiplier: 1,
  service_tier: null,
  input_cost: 0,
  output_cost: 0,
  cache_creation_cost: 0,
  cache_read_cost: 0,
  input_tokens: 0,
  output_tokens: 0,
  cache_creation_tokens: 0,
  cache_read_tokens: 0,
  cache_creation_5m_tokens: 0,
  cache_creation_1h_tokens: 0,
  cache_ttl_overridden: false,
  billing_mode: 'image',
  image_count: 2,
  image_size: '2K',
  image_input_size: null,
  image_output_size: null,
  image_size_source: null,
  image_size_breakdown: null,
  image_input_tokens: 0,
  image_input_cost: 0,
  image_input_price: 0,
  image_input_price_source: null,
  image_output_tokens: 0,
  image_output_cost: 0,
}

describe('admin UsageTable tooltip', () => {
  beforeEach(() => {
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({
      x: 0,
      y: 0,
      top: 20,
      left: 20,
      right: 120,
      bottom: 40,
      width: 100,
      height: 20,
      toJSON: () => ({}),
    } as DOMRect)
  })

  it('shows only the effort actually sent upstream without falling back to the requested effort', () => {
    const wrapper = mount(UsageTable, {
      props: {
        data: [
          {
            ...baseImageRow,
            request_id: 'req-actual-none',
            reasoning_effort: 'none',
            requested_reasoning_effort: 'high',
          },
          {
            ...baseImageRow,
            request_id: 'req-actual-minimal',
            reasoning_effort: 'minimal',
            requested_reasoning_effort: 'max',
          },
          {
            ...baseImageRow,
            request_id: 'req-actual-unspecified',
            reasoning_effort: null,
            requested_reasoning_effort: 'high',
          },
        ],
        loading: false,
        columns: [],
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    const text = wrapper.text()
    expect(text).toContain('None')
    expect(text).toContain('Minimal')
    expect(text).toContain('— (Unspecified)')
    expect(text).not.toContain('High')
    expect(text).not.toContain('Max')
  })

  it('keeps latency bar proportions, colors and tooltip aligned with displayed values', () => {
    const wrapper = mount(UsageTable, {
      props: {
        data: [{
          ...baseImageRow,
          request_id: 'req-latency-bar',
          first_token_ms: 40_000,
          duration_ms: 160_000,
        }],
        loading: false,
        columns: [],
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    const bar = wrapper.get('[data-testid="latency-bar"]')
    expect(bar.attributes('title')).toBe('First Token: 40.00s (25.0%); Total: 2m 40s')
    expect(bar.attributes('aria-label')).toBe(bar.attributes('title'))

    const firstToken = bar.get('[data-latency-segment="first-token"]')
    const remaining = bar.get('[data-latency-segment="remaining"]')
    expect(firstToken.attributes('style')).toContain('height: 25%')
    expect(firstToken.classes()).toContain('bg-orange-600')
    expect(remaining.attributes('style')).toContain('height: 75%')
    expect(remaining.classes()).toContain('bg-amber-600')
  })

  it('uses accessible, distinct light-theme colors for usage metrics', () => {
    const wrapper = mount(UsageTable, {
      props: {
        data: [{
          ...baseImageRow,
          request_id: 'req-metric-colors',
          billing_mode: 'token',
          image_count: 0,
          input_tokens: 1200,
          output_tokens: 80,
          cache_read_tokens: 64,
          cache_creation_tokens: 32,
          image_input_tokens: 24,
          image_output_tokens: 16,
          first_token_ms: 900,
          duration_ms: 1200,
        }],
        loading: false,
        columns: [],
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    const html = wrapper.html()
    expect(html).toContain('text-emerald-700')
    expect(html).toContain('text-violet-700')
    expect(html).toContain('text-sky-700')
    expect(html).toContain('text-amber-700')
    expect(html).toContain('text-fuchsia-700')
    expect(html).toContain('text-pink-700')
    expect(html).toContain('text-green-700')
    expect(html).toContain('text-orange-700')
  })

  it('shows image input token, cost, exact price and source without mixing output evidence', async () => {
    const wrapper = mount(UsageTable, {
      props: {
        data: [{
          ...baseImageRow,
          request_id: 'req-image-input-evidence',
          billing_mode: 'token',
          image_count: 0,
          input_tokens: 100,
          image_input_tokens: 20,
          input_cost: 0.00024,
          image_input_cost: 0.00016,
          image_input_price: 0.000008,
          image_input_price_source: 'channel',
          output_tokens: 0,
          image_output_tokens: 0,
          image_output_cost: 0,
        }],
        loading: false,
        columns: [],
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    const tooltipTriggers = wrapper.findAll('.group.relative')
    await tooltipTriggers[0].trigger('mouseenter')
    await nextTick()
    expect(wrapper.text()).toContain('Image Input Tokens')
    expect(wrapper.text()).toContain('20')

    await tooltipTriggers[tooltipTriggers.length - 1].trigger('mouseenter')
    await nextTick()
    const text = wrapper.text()
    expect(text).toContain('Image Input Cost')
    expect(text).toContain('$0.000160')
    expect(text).toContain('Image Input Price')
    expect(text).toContain('$8.0000')
    expect(text).toContain('Channel/model override')
    expect(text).not.toContain('Image Output Cost')
  })

  it('shows Grok quota and temporary pause diagnostics in the account cell', () => {
    const wrapper = mount(UsageTable, {
      props: {
        data: [{
          ...baseImageRow,
          request_id: 'req-grok-account-state',
          account: {
            id: 9,
            name: 'Grok Primary',
            platform: 'grok',
            temp_unschedulable_until: new Date(Date.now() + 60_000).toISOString(),
            temp_unschedulable_reason: 'grok rate limited',
            grok_quota_snapshot: {
              requests: { limit: 100, remaining: 7 },
              tokens: { limit: 1000, remaining: 250 },
              entitlement_status: 'active',
            },
          },
        }],
        loading: false,
        columns: [],
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    expect(wrapper.text()).toContain('Grok Primary')
    expect(wrapper.text()).toContain('Temporarily paused')
    expect(wrapper.text()).toContain('Requests 7/100')
    expect(wrapper.text()).toContain('Tokens 250/1000')
    expect(wrapper.text()).toContain('active')
  })

  it('shows service tier and billing breakdown in cost tooltip', async () => {
    const row = {
      request_id: 'req-admin-1',
      actual_cost: 0.092883,
      total_cost: 0.092883,
      account_rate_multiplier: 1,
      rate_multiplier: 1,
      service_tier: 'priority',
      reasoning_effort: 'high',
      requested_service_tier: 'priority',
      sent_service_tier: 'priority',
      response_service_tier: 'default',
      priority_detection_source: 'response',
      priority_policy_action: 'force_priority',
      priority_policy_rule_id: 'priority-vip',
      priority_policy_fallback: false,
      input_cost: 0.020285,
      output_cost: 0.00303,
      cache_creation_cost: 0,
      cache_read_cost: 0.069568,
      input_tokens: 4057,
      output_tokens: 101,
    }

    const wrapper = mount(UsageTable, {
      props: {
        data: [row],
        loading: false,
        columns: [],
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    const tooltipTriggers = wrapper.findAll('.group.relative')
    await tooltipTriggers[tooltipTriggers.length - 1].trigger('mouseenter')
    await nextTick()

    expect(wrapper.text()).toContain('Actual Reasoning Effort')
    expect(wrapper.text()).toContain('High')

    const text = wrapper.text()
    expect(text).toContain('Service tier')
    expect(text).toContain('Priority')
    expect(text).toContain('Client requested mode')
    expect(text).toContain('Mode sent upstream')
    expect(text).toContain('Upstream response mode')
    expect(text).toContain('Final decision source')
    expect(text).toContain('Determined from the upstream response')
    expect(text).toContain('Policy action')
    expect(text).toContain('Force Priority')
    expect(text).toContain('priority-vip')
    expect(text).toContain('Used rule fallback action')
    expect(text).toContain('Rate')
    expect(text).toContain('1.00x')
    expect(text).toContain('Account rate')
    expect(text).toContain('User billed')
    expect(text).toContain('Account billed')
    expect(text).toContain('$0.092883')
    expect(text).toContain('$5.0000 / 1M tokens')
    expect(text).toContain('$30.0000 / 1M tokens')
    expect(text).toContain('$0.069568')
  })

  it('shows requested and upstream models separately for admin rows', () => {
    const row = {
      request_id: 'req-admin-model-1',
      model: 'claude-sonnet-4',
      upstream_model: 'claude-sonnet-4-20250514',
      actual_cost: 0,
      total_cost: 0,
      account_rate_multiplier: 1,
      rate_multiplier: 1,
      input_cost: 0,
      output_cost: 0,
      cache_creation_cost: 0,
      cache_read_cost: 0,
      input_tokens: 0,
      output_tokens: 0,
    }

    const wrapper = mount(UsageTable, {
      props: {
        data: [row],
        loading: false,
        columns: [],
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    const text = wrapper.text()
    expect(text).toContain('claude-sonnet-4')
    expect(text).toContain('claude-sonnet-4-20250514')
  })

  it('does not fall back to customer cost when an upstream model has no resolved account price', () => {
    const wrapper = mount(UsageTable, {
      props: {
        data: [{
          ...baseImageRow,
          request_id: 'req-admin-unresolved-account-cost',
          model: 'gpt-5.6-sol',
          upstream_model: 'gpt-5.6-luna',
          total_cost: 25,
          account_stats_cost: null,
          account_rate_multiplier: 1,
        }],
        loading: false,
        columns: [],
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    const accountCostBadge = wrapper.findAll('div').find((node) => node.text().trim() === 'A $0.000000')
    expect(accountCostBadge).toBeDefined()
  })

  it('keeps the upstream model when resolving account cost in the cost tooltip', async () => {
    const wrapper = mount(UsageTable, {
      props: {
        data: [{
          ...baseImageRow,
          request_id: 'req-admin-tooltip-unresolved-account-cost',
          model: 'gpt-5.6-sol',
          upstream_model: 'gpt-5.6-luna',
          total_cost: 25,
          account_stats_cost: null,
          account_rate_multiplier: 1,
        }],
        loading: false,
        columns: [],
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    const tooltipTriggers = wrapper.findAll('.group.relative')
    await tooltipTriggers[tooltipTriggers.length - 1].trigger('mouseenter')
    await nextTick()

    const accountBilledLabel = wrapper.findAll('span').find((node) => node.text() === 'Account billed')
    expect(accountBilledLabel).toBeDefined()
    const accountBilledRow = accountBilledLabel?.element.parentElement?.textContent ?? ''
    expect(accountBilledRow).toContain('$0.000000')
    expect(accountBilledRow).not.toContain('$25.000000')
  })

  it.each([
    {
      name: 'defaulted row',
      row: {
        ...baseImageRow,
        request_id: 'req-admin-default-image',
        image_size: '2K',
        image_input_size: 'auto',
        image_output_size: null,
        image_size_source: 'default',
      },
      expected: ['2K', 'Default billing tier', 'auto', 'unknown'],
    },
    {
      name: 'output-sourced row',
      row: {
        ...baseImageRow,
        request_id: 'req-admin-output-image',
        image_size: '4K',
        image_input_size: '1024x1024',
        image_output_size: '3840x2160',
        image_size_source: 'output',
        image_size_breakdown: { '4K': 1 },
      },
      expected: ['4K', 'Upstream output', '1024x1024', '3840x2160', '4K x 1'],
    },
    {
      name: 'input-sourced row',
      row: {
        ...baseImageRow,
        request_id: 'req-admin-input-image',
        image_size: '1K',
        image_input_size: '1024x1024',
        image_output_size: null,
        image_size_source: 'input',
      },
      expected: ['1K', 'Request input', '1024x1024', 'unknown'],
    },
    {
      name: 'legacy unstandardized row',
      row: {
        ...baseImageRow,
        request_id: 'req-admin-legacy-unstandardized-image',
        image_size: '512x512',
        image_input_size: null,
        image_output_size: null,
        image_size_source: null,
      },
      expected: ['legacy unstandardized: 512x512', 'Legacy record', 'unknown'],
    },
  ])('shows image usage metadata for $name', async ({ row, expected }) => {
    const wrapper = mount(UsageTable, {
      props: {
        data: [row],
        loading: false,
        columns: [],
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    await wrapper.find('.group.relative').trigger('mouseenter')
    await nextTick()

    const text = wrapper.text()
    expect(text).toContain('Image count')
    expect(text).toContain('Billing size')
    expect(text).toContain('Size source')
    expect(text).toContain('Input size')
    expect(text).toContain('Output size')
    expect(text).toContain('Per-image price')
    expect(text).toContain('Image total price')
    for (const value of expected) {
      expect(text).toContain(value)
    }
  })

  it('displays historical image rows with missing billing_mode as image usage without a 2K fallback', async () => {
    const wrapper = mount(UsageTable, {
      props: {
        data: [
          {
            ...baseImageRow,
            request_id: 'req-admin-legacy-missing-image',
            billing_mode: null,
            image_size: null,
            image_input_size: null,
            image_output_size: null,
            image_size_source: null,
            image_size_breakdown: null,
          },
        ],
        loading: false,
        columns: [],
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    await wrapper.find('.group.relative').trigger('mouseenter')
    await nextTick()

    const text = wrapper.text()
    expect(text).toContain('Image')
    expect(text).toContain('Image count')
    expect(text).toContain('Per-image price')
    expect(text).toContain('not recorded')
    expect(text).not.toContain('(2K)')
  })

  it('shows Grok video count, resolution, duration and per-second price', async () => {
    const wrapper = mount(UsageTable, {
      props: {
        data: [{
          request_id: 'req-admin-grok-video',
          model: 'grok-imagine-video-1.5',
          billing_mode: 'video',
          video_count: 2,
          video_resolution: '1080p',
          video_duration_seconds: 8,
          image_count: 0,
          actual_cost: 4,
          total_cost: 4,
          account_rate_multiplier: 1,
          rate_multiplier: 1,
          service_tier: null,
          input_cost: 0,
          output_cost: 0,
          cache_creation_cost: 0,
          cache_read_cost: 0,
          input_tokens: 0,
          output_tokens: 0,
        }],
        loading: false,
        columns: [],
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    await wrapper.find('.group.relative').trigger('mouseenter')
    await nextTick()

    const text = wrapper.text()
    expect(text).toContain('Video')
    expect(text).toContain('2 videos')
    expect(text).toContain('1080p')
    expect(text).toContain('8s')
    expect(text).toContain('Per-second price')
    expect(text).toContain('$0.250000')
    expect(text).toContain('Video total price')
  })
})

// A DataTable stub that also renders cell-user, so the deleted badge can be asserted.
const DataTableStubWithUser = {
  props: ['data'],
  template: `
    <div>
      <div v-for="row in data" :key="row.request_id">
        <slot name="cell-user" :row="row" />
        <slot name="cell-model" :row="row" :value="row.model" />
        <slot name="cell-billing_mode" :row="row" />
        <slot name="cell-tokens" :row="row" />
        <slot name="cell-cost" :row="row" />
      </div>
    </div>
  `,
}

describe('admin UsageTable deleted-user badge', () => {
  it('renders deleted badge for a soft-deleted user row', () => {
    const row = {
      request_id: 'req-deleted-user-1',
      model: 'claude-3',
      user_id: 2,
      user: { id: 2, email: 'd@test.com', deleted_at: '2026-05-28T00:00:00Z' },
      actual_cost: 0,
      total_cost: 0,
      input_cost: 0,
      output_cost: 0,
      rate_multiplier: 1,
      input_tokens: 1,
      output_tokens: 1,
    }

    const wrapper = mount(UsageTable, {
      props: {
        data: [row],
        loading: false,
        columns: [{ key: 'user', label: 'User' }],
      },
      global: {
        stubs: {
          DataTable: DataTableStubWithUser,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    expect(wrapper.text()).toContain('Deleted')
    expect(wrapper.text()).toContain('d@test.com')
  })

  it('does NOT render deleted badge for an active user row', () => {
    const row = {
      request_id: 'req-active-user-1',
      model: 'claude-3',
      user_id: 3,
      user: { id: 3, email: 'active@test.com', deleted_at: null },
      actual_cost: 0,
      total_cost: 0,
      input_cost: 0,
      output_cost: 0,
      rate_multiplier: 1,
      input_tokens: 1,
      output_tokens: 1,
    }

    const wrapper = mount(UsageTable, {
      props: {
        data: [row],
        loading: false,
        columns: [{ key: 'user', label: 'User' }],
      },
      global: {
        stubs: {
          DataTable: DataTableStubWithUser,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    expect(wrapper.text()).not.toContain('Deleted')
    expect(wrapper.text()).toContain('active@test.com')
  })
})
