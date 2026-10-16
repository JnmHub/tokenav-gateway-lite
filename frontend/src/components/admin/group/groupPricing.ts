import type {
  AccountStatsPricingRule,
  BillingMode,
  ChannelModelPricing,
} from '@/api/admin/channels'
import type {
  GroupPricingConfig,
  UpdateGroupPricingRequest,
} from '@/api/admin/groups'
import type { GroupPlatform } from '@/types'
import { CONCRETE_PLATFORM_OPTIONS } from '@/constants/platforms'
import {
  apiIntervalsToForm,
  findModelConflict,
  formIntervalsToAPI,
  mTokToPerToken,
  perTokenToMTok,
  toNullableNumber,
  validateIntervals,
  type PricingFormEntry,
} from '@/components/admin/channel/types'

export interface GroupAccountPricingRuleForm {
  name: string
  scope: 'whole_group' | 'selected_accounts'
  account_ids: number[]
  pricing: PricingFormEntry[]
}

export interface GroupPricingFormState {
  enabled: boolean
  restrict_models: boolean
  features_config: Record<string, unknown>
  web_search_emulation: boolean
  codex_image_generation_bridge: boolean
  bedrock_cc_compat: boolean
  model_pricing: PricingFormEntry[]
  apply_pricing_to_account_stats: boolean
  account_stats_pricing_rules: GroupAccountPricingRuleForm[]
}

const concretePricingPlatforms = new Set<GroupPlatform>(
  CONCRETE_PLATFORM_OPTIONS.map(option => option.value),
)

export function createPricingEntry(
  billingMode: BillingMode = 'token',
  platform?: string,
): PricingFormEntry {
  return {
    platform,
    models: [],
    billing_mode: billingMode,
    input_price: null,
    output_price: null,
    cache_write_price: null,
    cache_read_price: null,
    fast_multiplier: null,
    flex_multiplier: null,
    image_input_price: null,
    image_output_price: null,
    per_request_price: null,
    intervals: [],
  }
}

export function createGroupPricingFormState(): GroupPricingFormState {
  return {
    enabled: false,
    restrict_models: false,
    features_config: {},
    web_search_emulation: false,
    codex_image_generation_bridge: false,
    bedrock_cc_compat: false,
    model_pricing: [],
    apply_pricing_to_account_stats: false,
    account_stats_pricing_rules: [],
  }
}

function featureEnabled(
  config: Record<string, unknown>,
  key: string,
  platform: GroupPlatform,
): boolean {
  const value = config[key]
  if (typeof value === 'boolean') return value
  if (value && typeof value === 'object') {
    return (value as Record<string, unknown>)[platform] === true
  }
  return false
}

function pricingToForm(entry: ChannelModelPricing, groupPlatform: GroupPlatform): PricingFormEntry {
  return {
    platform: groupPlatform === 'composite' ? entry.platform : groupPlatform,
    models: [...(entry.models || [])],
    billing_mode: entry.billing_mode || 'token',
    input_price: perTokenToMTok(entry.input_price),
    output_price: perTokenToMTok(entry.output_price),
    cache_write_price: perTokenToMTok(entry.cache_write_price),
    cache_read_price: perTokenToMTok(entry.cache_read_price),
    fast_multiplier: entry.fast_multiplier,
    flex_multiplier: entry.flex_multiplier,
    image_input_price: perTokenToMTok(entry.image_input_price),
    image_output_price: perTokenToMTok(entry.image_output_price),
    per_request_price: entry.per_request_price,
    intervals: apiIntervalsToForm(entry.intervals || []),
  }
}

export function groupPricingConfigToForm(
  config: GroupPricingConfig,
  platform: GroupPlatform,
): GroupPricingFormState {
  const features = { ...(config.features_config || {}) }
  return {
    enabled: config.enabled,
    restrict_models: config.restrict_models,
    features_config: features,
    web_search_emulation: featureEnabled(features, 'web_search_emulation', platform),
    codex_image_generation_bridge: featureEnabled(features, 'codex_image_generation_bridge', platform),
    bedrock_cc_compat: featureEnabled(features, 'bedrock_cc_compat', platform),
    model_pricing: (config.model_pricing || []).map(entry => pricingToForm(entry, platform)),
    apply_pricing_to_account_stats: config.apply_pricing_to_account_stats,
    account_stats_pricing_rules: (config.account_stats_pricing_rules || []).map(rule => ({
      name: rule.name || '',
      scope: rule.account_ids?.length ? 'selected_accounts' : 'whole_group',
      account_ids: [...(rule.account_ids || [])],
      pricing: (rule.pricing || []).map(entry => pricingToForm(entry, platform)),
    })),
  }
}

function formPricingToAPI(entry: PricingFormEntry, platform: GroupPlatform): ChannelModelPricing {
  const pricingPlatform = platform === 'composite'
    ? String(entry.platform || '').trim().toLowerCase()
    : platform
  return {
    platform: pricingPlatform,
    models: entry.models.map(model => model.trim()).filter(Boolean),
    billing_mode: entry.billing_mode,
    input_price: mTokToPerToken(entry.input_price),
    output_price: mTokToPerToken(entry.output_price),
    cache_write_price: mTokToPerToken(entry.cache_write_price),
    cache_read_price: mTokToPerToken(entry.cache_read_price),
    fast_multiplier: toNullableNumber(entry.fast_multiplier),
    flex_multiplier: toNullableNumber(entry.flex_multiplier),
    image_input_price: mTokToPerToken(entry.image_input_price),
    image_output_price: mTokToPerToken(entry.image_output_price),
    per_request_price: toNullableNumber(entry.per_request_price),
    intervals: formIntervalsToAPI(entry.intervals || []),
  }
}

function setPlatformFeature(
  config: Record<string, unknown>,
  key: string,
  platform: GroupPlatform,
  enabled: boolean,
) {
  const current = config[key]
  const values = current && typeof current === 'object' && !Array.isArray(current)
    ? { ...(current as Record<string, unknown>) }
    : {}
  values[platform] = enabled
  config[key] = values
}

export function groupPricingFormToRequest(
  state: GroupPricingFormState,
  platform: GroupPlatform,
  groupID: number,
): UpdateGroupPricingRequest {
  if (!state.enabled) {
    return {
      enabled: false,
      restrict_models: false,
      features_config: {},
      model_pricing: [],
      apply_pricing_to_account_stats: false,
      account_stats_pricing_rules: [],
    }
  }

  const features = { ...state.features_config }
  if (platform === 'anthropic') {
    setPlatformFeature(features, 'web_search_emulation', platform, state.web_search_emulation)
    setPlatformFeature(features, 'bedrock_cc_compat', platform, state.bedrock_cc_compat)
  }
  if (platform === 'openai') {
    setPlatformFeature(
      features,
      'codex_image_generation_bridge',
      platform,
      state.codex_image_generation_bridge,
    )
  }

  const accountRules: AccountStatsPricingRule[] = state.apply_pricing_to_account_stats
    ? state.account_stats_pricing_rules.map(rule => ({
        name: rule.name.trim(),
        // 规则匹配语义是账号或分组二选一；指定账号时不能再带 group_id，
        // 否则 OR 条件会让规则意外命中整个分组。
        group_ids: rule.scope === 'whole_group' ? [groupID] : [],
        account_ids: rule.scope === 'selected_accounts' ? [...rule.account_ids] : [],
        pricing: rule.pricing.map(entry => formPricingToAPI(entry, platform)),
      }))
    : []

  return {
    enabled: true,
    restrict_models: state.restrict_models,
    features_config: features,
    model_pricing: state.model_pricing.map(entry => formPricingToAPI(entry, platform)),
    apply_pricing_to_account_stats: state.apply_pricing_to_account_stats,
    account_stats_pricing_rules: accountRules,
  }
}

function validatePricingEntries(
  entries: PricingFormEntry[],
  groupPlatform: GroupPlatform,
): string | null {
  const patternsByPlatform = new Map<string, string[]>()
  for (const entry of entries) {
    if (entry.models.every(model => !model.trim())) return 'models_required'
    const pricingPlatform = groupPlatform === 'composite'
      ? String(entry.platform || '').trim().toLowerCase()
      : groupPlatform
    if (!concretePricingPlatforms.has(pricingPlatform as GroupPlatform)) {
      return 'platform_required'
    }
    const patterns = patternsByPlatform.get(pricingPlatform) || []
    patterns.push(...entry.models.map(model => model.trim()).filter(Boolean))
    patternsByPlatform.set(pricingPlatform, patterns)
    if (
      (entry.billing_mode === 'per_request' ||
        entry.billing_mode === 'image' ||
        entry.billing_mode === 'video') &&
      toNullableNumber(entry.per_request_price) === null &&
      !entry.intervals.some(interval => toNullableNumber(interval.per_request_price) !== null)
    ) {
      return 'per_request_price_required'
    }
    const intervalsError = validateIntervals(entry.intervals || [], entry.billing_mode)
    if (intervalsError) return `interval:${intervalsError}`
  }
  for (const patterns of patternsByPlatform.values()) {
    const conflict = findModelConflict(patterns)
    if (conflict) return `conflict:${conflict[0]}:${conflict[1]}`
  }
  return null
}

export function validateGroupPricingForm(
  state: GroupPricingFormState,
  platform: GroupPlatform,
): string | null {
  if (!state.enabled) return null
  if (state.restrict_models && state.model_pricing.length === 0) return 'restricted_without_prices'
  const mainError = validatePricingEntries(state.model_pricing, platform)
  if (mainError) return mainError

  if (!state.apply_pricing_to_account_stats) return null
  for (const rule of state.account_stats_pricing_rules) {
    if (!rule.name.trim()) return 'rule_name_required'
    if (rule.scope === 'selected_accounts' && rule.account_ids.length === 0) {
      return 'rule_accounts_required'
    }
    if (rule.pricing.length === 0) return 'rule_pricing_required'
    const ruleError = validatePricingEntries(rule.pricing, platform)
    if (ruleError) return ruleError
  }
  return null
}
