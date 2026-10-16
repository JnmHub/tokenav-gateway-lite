import { findModelConflict } from './modelMapping.js'

/* ============================================================
 * 分组专属定价工具层
 * 契约来源：旧 components/admin/group/groupPricing.ts +
 *   components/admin/channel/types.ts（MTok 换算/冲突检测），
 *   GET/PUT /admin/groups/:id/pricing
 * GroupPricingConfig: { enabled, source, channel_id, shared, restrict_models,
 *   features_config, model_pricing[], apply_pricing_to_account_stats,
 *   account_stats_pricing_rules[] }
 * 价格单位：后端存 per-token，前端展示 $/MTok（百万 token）
 * features_config 按平台嵌套：{ web_search_emulation: { anthropic: true }, ... }
 * 保存时序（对照旧 handleUpdateGroup）：先 PUT pricing 再 PUT 分组主体
 * ============================================================ */

const MTOK = 1_000_000

export const toNullableNumber = (v) => {
  if (v === null || v === undefined || v === '') return null
  const n = Number(v)
  return Number.isFinite(n) ? n : null
}

export const mTokToPerToken = (v) => {
  const n = toNullableNumber(v)
  return n === null ? null : parseFloat((n / MTOK).toPrecision(10))
}

export const perTokenToMTok = (v) => {
  if (v === null || v === undefined) return null
  // toPrecision(10) 消除浮点误差：5e-8 * 1e6 = 0.04999...96 → 0.05
  return parseFloat((v * MTOK).toPrecision(10))
}

export const CONCRETE_PLATFORMS = ['anthropic', 'openai', 'gemini', 'antigravity', 'grok', 'kimi', 'zhipu', 'deepseek']

export const createPricingEntry = (billingMode = 'token', platform) => ({
  platform: platform || undefined,
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
})

export const createPricingFormState = () => ({
  enabled: false,
  restrict_models: false,
  features_config: {},
  web_search_emulation: false,
  codex_image_generation_bridge: false,
  bedrock_cc_compat: false,
  model_pricing: [],
  apply_pricing_to_account_stats: false,
  account_stats_pricing_rules: [],
})

// features_config 里的开关可能按平台嵌套（布尔或 { 平台: 布尔 }）
function featureEnabled(config, key, platform) {
  const v = config?.[key]
  if (typeof v === 'boolean') return v
  if (v && typeof v === 'object') return v[platform] === true
  return false
}

function pricingToForm(entry, groupPlatform) {
  return {
    platform: groupPlatform === 'composite' ? entry.platform : groupPlatform,
    models: [...(entry.models || [])],
    billing_mode: entry.billing_mode || 'token',
    input_price: perTokenToMTok(entry.input_price),
    output_price: perTokenToMTok(entry.output_price),
    cache_write_price: perTokenToMTok(entry.cache_write_price),
    cache_read_price: perTokenToMTok(entry.cache_read_price),
    fast_multiplier: entry.fast_multiplier ?? null,
    flex_multiplier: entry.flex_multiplier ?? null,
    image_input_price: perTokenToMTok(entry.image_input_price),
    image_output_price: perTokenToMTok(entry.image_output_price),
    per_request_price: entry.per_request_price ?? null,
    intervals: apiIntervalsToForm(entry.intervals),
  }
}

export const pricingConfigToForm = (config, platform) => {
  const features = { ...(config?.features_config || {}) }
  return {
    enabled: !!config?.enabled,
    restrict_models: !!config?.restrict_models,
    features_config: features,
    web_search_emulation: featureEnabled(features, 'web_search_emulation', platform),
    codex_image_generation_bridge: featureEnabled(features, 'codex_image_generation_bridge', platform),
    bedrock_cc_compat: featureEnabled(features, 'bedrock_cc_compat', platform),
    model_pricing: (config?.model_pricing || []).map((e) => pricingToForm(e, platform)),
    apply_pricing_to_account_stats: !!config?.apply_pricing_to_account_stats,
    account_stats_pricing_rules: (config?.account_stats_pricing_rules || []).map((rule) => ({
      name: rule.name || '',
      scope: rule.account_ids?.length ? 'selected_accounts' : 'whole_group',
      account_ids: [...(rule.account_ids || [])],
      pricing: (rule.pricing || []).map((e) => pricingToForm(e, platform)),
    })),
  }
}

function formPricingToAPI(entry, platform) {
  const pricingPlatform = platform === 'composite'
    ? String(entry.platform || '').trim().toLowerCase()
    : platform
  return {
    platform: pricingPlatform,
    models: entry.models.map((m) => m.trim()).filter(Boolean),
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
    intervals: formIntervalsToAPI(entry.intervals),
  }
}

function setPlatformFeature(config, key, platform, enabled) {
  const current = config?.[key]
  const values = current && typeof current === 'object' && !Array.isArray(current)
    ? { ...current }
    : {}
  values[platform] = enabled
  config[key] = values
}

// 表单 → PUT /admin/groups/:id/pricing 请求体
export const pricingFormToRequest = (state, platform, groupId) => {
  if (!state.enabled) {
    // 关闭即整体清空（后端按提交内容拆分/停用渠道）
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
    setPlatformFeature(features, 'codex_image_generation_bridge', platform, state.codex_image_generation_bridge)
  }
  const accountRules = state.apply_pricing_to_account_stats
    ? state.account_stats_pricing_rules.map((rule) => ({
        name: rule.name.trim(),
        // 规则匹配语义是账号或分组二选一；指定账号时不能再带 group_id，
        // 否则 OR 条件会让规则意外命中整个分组（对照旧 groupPricing.ts）
        group_ids: rule.scope === 'whole_group' ? [groupId] : [],
        account_ids: rule.scope === 'selected_accounts' ? [...rule.account_ids] : [],
        pricing: rule.pricing.map((e) => formPricingToAPI(e, platform)),
      }))
    : []
  return {
    enabled: true,
    restrict_models: state.restrict_models,
    features_config: features,
    model_pricing: state.model_pricing.map((e) => formPricingToAPI(e, platform)),
    apply_pricing_to_account_stats: state.apply_pricing_to_account_stats,
    account_stats_pricing_rules: accountRules,
  }
}

// 阶梯价格同时出现在客户计价和账号成本规则里，转换必须对称，避免保存主体时丢旧配置。
export const createPricingInterval = (sortOrder = 0, label = '') => ({
  min_tokens: 0, max_tokens: null, tier_label: label,
  input_price: null, output_price: null, cache_write_price: null, cache_read_price: null,
  input_multiplier: null, output_multiplier: null, cache_write_multiplier: null, cache_read_multiplier: null,
  per_request_price: null, sort_order: sortOrder,
})
const intervalPriceFields = ['input_price', 'output_price', 'cache_write_price', 'cache_read_price']
const intervalMultiplierFields = ['input_multiplier', 'output_multiplier', 'cache_write_multiplier', 'cache_read_multiplier']
const configured = value => value != null && value !== ''
// null 表示未配阶梯，读取/提交/校验均归一为空数组，不能在表单边界抛异常。
const intervalList = intervals => intervals ?? []
export const apiIntervalsToForm = (intervals = []) => intervalList(intervals).map(interval => ({
  ...createPricingInterval(interval.sort_order ?? 0, interval.tier_label || ''),
  min_tokens: interval.min_tokens ?? 0, max_tokens: interval.max_tokens ?? null,
  ...Object.fromEntries(intervalPriceFields.map(field => [field, perTokenToMTok(interval[field])])),
  ...Object.fromEntries(intervalMultiplierFields.map(field => [field, interval[field] ?? null])),
  per_request_price: interval.per_request_price ?? null,
}))
export const formIntervalsToAPI = (intervals = []) => intervalList(intervals).map(interval => ({
  min_tokens: Number(interval.min_tokens) || 0, max_tokens: toNullableNumber(interval.max_tokens),
  tier_label: String(interval.tier_label || '').trim(), sort_order: interval.sort_order ?? 0,
  ...Object.fromEntries(intervalPriceFields.map(field => [field, mTokToPerToken(interval[field])])),
  ...Object.fromEntries(intervalMultiplierFields.map(field => [field, toNullableNumber(interval[field])])),
  per_request_price: toNullableNumber(interval.per_request_price),
}))

export function validateIntervals(intervals = [], mode = 'token') {
  const sorted = [...intervalList(intervals)].sort((a, b) => a.min_tokens - b.min_tokens)
  for (let index = 0; index < sorted.length; index++) {
    const interval = sorted[index], prefix = '区间 #' + (index + 1) + '：'
    if (!Number.isSafeInteger(Number(interval.min_tokens)) || Number(interval.min_tokens) < 0) return prefix + '最小 Token 数必须是非负整数'
    if (configured(interval.max_tokens) && (!Number.isSafeInteger(Number(interval.max_tokens)) || Number(interval.max_tokens) <= Number(interval.min_tokens))) return prefix + '最大 Token 数必须大于最小值'
    const prices = [...intervalPriceFields, 'per_request_price']
    if (![...prices, ...intervalMultiplierFields].some(field => configured(interval[field]))) return prefix + '至少填写一个价格或倍率'
    if (prices.some(field => configured(interval[field]) && (!Number.isFinite(Number(interval[field])) || Number(interval[field]) < 0))) return prefix + '价格必须是非负数'
    if (intervalMultiplierFields.some(field => configured(interval[field]) && (!Number.isFinite(Number(interval[field])) || Number(interval[field]) <= 0))) return prefix + '倍率必须大于零'
    // 按次、图片和视频按层级名匹配，不能应用 Token 区间重叠规则。
    if (mode === 'token' && index < sorted.length - 1 && !configured(interval.max_tokens)) return prefix + '无上限区间只能放在最后'
    if (mode === 'token' && index > 0 && Number(sorted[index - 1].max_tokens) > Number(interval.min_tokens)) return prefix + '上下文区间与前一段重叠'
  }
  return null
}

function validatePricingEntries(entries, groupPlatform) {
  const patternsByPlatform = new Map()
  for (const entry of entries) {
    if (entry.models.every((m) => !m.trim())) return 'models_required'
    const pricingPlatform = groupPlatform === 'composite'
      ? String(entry.platform || '').trim().toLowerCase()
      : groupPlatform
    if (!CONCRETE_PLATFORMS.includes(pricingPlatform)) return 'platform_required'
    const patterns = patternsByPlatform.get(pricingPlatform) || []
    patterns.push(...entry.models.map((m) => m.trim()).filter(Boolean))
    patternsByPlatform.set(pricingPlatform, patterns)
    const priceFields = [...intervalPriceFields, 'image_input_price', 'image_output_price', 'per_request_price']
    if (priceFields.some(field => configured(entry[field]) && (!Number.isFinite(Number(entry[field])) || Number(entry[field]) < 0))) return 'invalid_price'
    if (['fast_multiplier', 'flex_multiplier'].some(field => configured(entry[field]) && (!Number.isFinite(Number(entry[field])) || Number(entry[field]) <= 0))) return 'invalid_multiplier'
    if (
      ['per_request', 'image', 'video'].includes(entry.billing_mode) &&
      toNullableNumber(entry.per_request_price) === null &&
      !(entry.intervals || []).some(interval => toNullableNumber(interval.per_request_price) !== null)
    ) {
      return 'per_request_price_required'
    }
    const intervalError = validateIntervals(entry.intervals, entry.billing_mode)
    if (intervalError) return 'interval:' + intervalError
  }
  for (const patterns of patternsByPlatform.values()) {
    const conflict = findModelConflict(patterns)
    if (conflict) return `conflict:${conflict[0]}:${conflict[1]}`
  }
  return null
}

// 校验返回 null 或「错误码[:细节]」（对照旧 validateGroupPricingForm）
export const validatePricingForm = (state, platform) => {
  if (!state.enabled) return null
  if (state.restrict_models && state.model_pricing.length === 0) return 'restricted_without_prices'
  const mainError = validatePricingEntries(state.model_pricing, platform)
  if (mainError) return mainError
  if (!state.apply_pricing_to_account_stats) return null
  for (const rule of state.account_stats_pricing_rules) {
    if (!rule.name.trim()) return 'rule_name_required'
    if (rule.scope === 'selected_accounts' && rule.account_ids.length === 0) return 'rule_accounts_required'
    if (rule.pricing.length === 0) return 'rule_pricing_required'
    const ruleError = validatePricingEntries(rule.pricing, platform)
    if (ruleError) return ruleError
  }
  return null
}

// 错误码 → 中文（对照旧 i18n admin.groups.pricing.errors.*）
export const PRICING_ERROR_MESSAGES = {
  restricted_without_prices: '开启模型限制前，请至少添加一个模型价格。',
  models_required: '每条价格规则都必须选择或填写模型。',
  platform_required: '聚合分组的每条价格必须选择真实目标平台。',
  per_request_price_required: '按次、图片或视频计费必须填写默认价格或至少一个层级价格。',
  invalid_price: '价格必须是非负数，留空表示继承。',
  invalid_multiplier: '服务档位倍率必须大于零，留空表示沿用官方价格。',
  rule_name_required: '请填写账号成本规则名称。',
  rule_accounts_required: '选择“指定账号”时，请至少选择一个账号。',
  rule_pricing_required: '账号成本规则至少需要一条价格。',
  conflict: '模型匹配规则存在冲突，请检查重复或通配符。',
}

/* ---- Grok 视频计价矩阵（对照旧 GroupsView.vue grokVideoPriceFamilies 等） ----
 * video_model_prices: { 模型族: { 分辨率: 每秒价格 } }，留空使用目录价。
 * 专属定价中的 intervals 与此分组级矩阵独立保存，两者都需完整保留。 */
export const GROK_VIDEO_FAMILIES = ['grok-imagine-video', 'grok-imagine-video-1.5']
export const GROK_VIDEO_TIERS = ['480p', '720p', '1080p']

export const emptyGrokVideoMatrix = () =>
  Object.fromEntries(GROK_VIDEO_FAMILIES.map((f) => [f, Object.fromEntries(GROK_VIDEO_TIERS.map((t) => [t, null]))]))

// 后端矩阵键可能大小写不一致（历史数据/人工写入），统一按小写族名与小写档位读取，
// 与后端 NormalizeVideoModelPrices 的归一化口径保持一致。
const normalizedVideoMatrixSource = (value) => {
  const out = {}
  for (const [family, tiers] of Object.entries(value || {})) {
    const key = String(family).trim().toLowerCase()
    if (!key || !tiers || typeof tiers !== 'object') continue
    const normalizedTiers = {}
    for (const [tier, price] of Object.entries(tiers)) {
      const tierKey = String(tier).trim().toLowerCase()
      if (tierKey) normalizedTiers[tierKey] = price
    }
    out[key] = normalizedTiers
  }
  return out
}

// 矩阵展示行：规范族 + 后端已存在的其他族（按字典序），对照官方
// groupsVideoModelPricing.ts 的 videoModelPriceFamilyRows，避免未来新增模型族不可见。
export const grokVideoPriceFamilyRows = (value) => {
  const extra = Object.keys(normalizedVideoMatrixSource(value))
    .filter((family) => !GROK_VIDEO_FAMILIES.includes(family))
  return [...GROK_VIDEO_FAMILIES, ...extra.sort()]
}

// 回填：后端矩阵可能缺键。规范族一定出现（保证两个官方族始终可编辑），
// 同时保留后端已有的其他模型族，不能静默丢弃运营已经配过的价。
export const normalizeGrokVideoMatrix = (value) => {
  const source = normalizedVideoMatrixSource(value)
  const matrix = emptyGrokVideoMatrix()
  for (const family of grokVideoPriceFamilyRows(source)) {
    matrix[family] ||= Object.fromEntries(GROK_VIDEO_TIERS.map((t) => [t, null]))
    for (const tier of GROK_VIDEO_TIERS) {
      matrix[family][tier] = source[family]?.[tier] ?? null
    }
  }
  return matrix
}

// 提交：剔除空值/非法值，输出 { 族: { 档: 价格 } }；只输出至少有一个有效档位的族，
// 但遍历范围是展示行（含未知族），与回填逻辑对称，不会丢失既有配置。
export const compactGrokVideoMatrix = (matrix) => {
  const source = normalizedVideoMatrixSource(matrix)
  const out = {}
  for (const f of grokVideoPriceFamilyRows(source)) {
    for (const t of GROK_VIDEO_TIERS) {
      const raw = source[f]?.[t]
      if (raw === null || raw === '' || raw === undefined) continue
      const price = Number(raw)
      if (!Number.isFinite(price) || price < 0) continue
      ;(out[f] ||= {})[t] = price
    }
  }
  return out
}
