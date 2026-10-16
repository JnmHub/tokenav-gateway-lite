// 默认字段与提交清空语义独立存放；保存时序由 useGroupEditor 负责。
import { rowsToMappingRecord, rowsToWeightedRecord } from './modelMapping.js'
import { emptyGrokVideoMatrix, compactGrokVideoMatrix } from './pricing.js'
import { createDefaultMessagesDispatchConfigState, messagesDispatchFormStateToConfig } from './messagesDispatch.js'
import { supportsReasoningEffort, reasoningEffortRowsToApi } from './reasoningEffort.js'
import { isProfitControlPlatform } from './profitControl.js'

const normLimit = (v) => {
  if (v === null || v === undefined || v === '') return null
  const n = Number(v)
  return Number.isFinite(n) && n > 0 ? n : null
}
// 图片/视频倍率：空/非法回退 1，负数回退 1（对照旧 normalizeImageRateMultiplier）
const normRateMultiplier = (v) => {
  if (v === null || v === undefined || v === '') return 1
  const n = Number(v)
  return Number.isFinite(n) && n >= 0 ? n : 1
}
// optionalPriceField：'' → null（清除），数字原样
const emptyToNull = (v) => (v === '' ? null : v)
// 白名单模型规范化：trim / 去空 / 去重（对照后端 normalizeGroupModelsListConfig）
export const normModelsList = (list) => {
  const seen = new Set()
  const out = []
  for (const raw of list ?? []) {
    const model = String(raw ?? '').trim()
    if (!model || seen.has(model)) continue
    seen.add(model)
    out.push(model)
  }
  return out
}

export const defaultForm = () => ({
  id: 0, copy_accounts_from_group_ids: [],
  // —— 一期字段（保持不变） ——
  name: '', description: '', display_brand: '', display_icon: '', rate_multiplier: 1, local_only: false,
  platform: 'anthropic', status: 'active', subscription_type: 'standard',
  daily_limit_usd: null, weekly_limit_usd: null, monthly_limit_usd: null,
  is_exclusive: false, minimum_tier: null,
  scheduling_mode: 'default', per_user_concurrency: 0, rpm_limit: 0,
  peak_rate_enabled: false, peak_start: '18:00', peak_end: '22:00',
  peak_rate_multiplier: 1, peak_timezone: 'Asia/Shanghai',
  billing_model_source: 'requested',
  allow_blocked_keyword_requests: false, require_oauth_only: false, require_privacy_set: false,
  allow_openai_long_context_billing: false, allow_live: false,
  fallback_model: '', failover_group_id: null, failover_strategy: 'original_model_first',
  claude_code_only: false, mcp_xml_inject: true,
  supported_model_scopes: ['claude', 'gemini_text', 'gemini_image'],
  allow_image_generation: false,
  // —— 二期 A 新增：媒体与按次计价 ——
  image_rate_independent: false, image_rate_multiplier: 1,
  image_price_1k: null, image_price_2k: null, image_price_4k: null,
  video_rate_independent: false, video_rate_multiplier: 1,
  video_price_480p: null, video_price_720p: null, video_price_1080p: null,
  video_model_prices: emptyGrokVideoMatrix(),
  web_search_price_per_call: null, search_price_per_1k: null,
  audio_realtime_price_per_min: null, audio_tts_price_per_million_chars: null, audio_stt_price_per_hour: null,
  // —— 二期 A 新增：映射 / 路由 / 兜底 / 响应声明 ——
  model_mapping_rows: [],
  fallback_group_id: null,
  fallback_group_id_on_invalid_request: null,
  response_model_mode: 'request',
  response_model_custom: '',
  // —— 二期 B：OpenAI Messages 调度 / 推理力度 / 模型白名单 ——
  allow_messages_dispatch: false,
  default_mapped_model: '',
  // 家族映射预填后端内置默认，管理员可改可清空
  ...createDefaultMessagesDispatchConfigState(),
  max_reasoning_effort: '',
  reasoning_effort_rows: [],
  models_list_enabled: false,
  models_list_models: [],
  // —— v0.2.5：利润控制（decimals 存储，0.30 = 30%） ——
  profit_control_enabled: false,
  profit_min_margin: 0,
  profit_safety_buffer: 0,
})

  // UI → API：无有效规则返回 null（后端 nil 不修改，与旧行为一致）
  const routingRulesToApi = (rules) => {
    const result = {}
    let hasValid = false
    for (const rule of rules) {
      const pattern = rule.pattern.trim()
      if (!pattern) continue
      const ids = rule.accounts.map((a) => a.id).filter((id) => id > 0)
      if (ids.length > 0) {
        result[pattern] = ids
        hasValid = true
      }
    }
    return hasValid ? result : null
  }

  // ===== 构建主 PUT/POST body =====
export const buildGroupBody = (f, routingRules, routingEnabled) => ({
    // 一期字段（语义不变）
    name: f.name.trim(),
    copy_accounts_from_group_ids: f.copy_accounts_from_group_ids || [],
    description: f.description,
    display_brand: f.display_brand || f.name.trim(),
    display_icon: f.display_icon || '',
    rate_multiplier: Number(f.rate_multiplier) || 1,
    local_only: !!f.local_only,
    platform: f.platform,
    status: f.status,
    subscription_type: f.subscription_type,
    daily_limit_usd: normLimit(f.daily_limit_usd),
    weekly_limit_usd: normLimit(f.weekly_limit_usd),
    monthly_limit_usd: normLimit(f.monthly_limit_usd),
    is_exclusive: !!f.is_exclusive,
    minimum_tier: f.minimum_tier === null ? null : Number(f.minimum_tier),
    scheduling_mode: f.scheduling_mode,
    per_user_concurrency: Math.max(0, Math.round(Number(f.per_user_concurrency) || 0)),
    rpm_limit: Math.max(0, Math.round(Number(f.rpm_limit) || 0)),
    peak_rate_enabled: !!f.peak_rate_enabled,
    peak_start: f.peak_start || '18:00',
    peak_end: f.peak_end || '22:00',
    peak_rate_multiplier: Number(f.peak_rate_multiplier) || 1,
    peak_timezone: f.peak_timezone || 'Asia/Shanghai',
    billing_model_source: f.billing_model_source || 'requested',
    allow_blocked_keyword_requests: !!f.allow_blocked_keyword_requests,
    require_oauth_only: !!f.require_oauth_only,
    require_privacy_set: !!f.require_privacy_set,
    allow_openai_long_context_billing: !!f.allow_openai_long_context_billing,
    allow_live: !!f.allow_live,
    fallback_model: (f.fallback_model || '').trim(),
    failover_strategy: f.failover_strategy || 'original_model_first',
    claude_code_only: !!f.claude_code_only,
    mcp_xml_inject: !!f.mcp_xml_inject,
    supported_model_scopes: f.platform === 'antigravity' ? (f.supported_model_scopes || []) : [],
    allow_image_generation: !!f.allow_image_generation,
    failover_group_id: f.id ? (f.failover_group_id ?? 0) : (f.failover_group_id ?? null),
    // —— 二期 A：媒体与按次计价 ——
    image_rate_independent: !!f.image_rate_independent,
    image_rate_multiplier: normRateMultiplier(f.image_rate_multiplier),
    image_price_1k: emptyToNull(f.image_price_1k),
    image_price_2k: emptyToNull(f.image_price_2k),
    image_price_4k: emptyToNull(f.image_price_4k),
    video_rate_independent: !!f.video_rate_independent,
    video_rate_multiplier: normRateMultiplier(f.video_rate_multiplier),
    video_price_480p: emptyToNull(f.video_price_480p),
    video_price_720p: emptyToNull(f.video_price_720p),
    video_price_1080p: emptyToNull(f.video_price_1080p),
    video_model_prices: compactGrokVideoMatrix(f.video_model_prices),
    web_search_price_per_call: emptyToNull(f.web_search_price_per_call),
    search_price_per_1k: emptyToNull(f.search_price_per_1k),
    audio_realtime_price_per_min: emptyToNull(f.audio_realtime_price_per_min),
    audio_tts_price_per_million_chars: emptyToNull(f.audio_tts_price_per_million_chars),
    audio_stt_price_per_hour: emptyToNull(f.audio_stt_price_per_hour),
    // —— 二期 A：映射 / 路由 / 兜底 / 响应声明 ——
    model_mapping: rowsToMappingRecord(f.model_mapping_rows),       // 空对象 = 清空
    weighted_model_mapping: rowsToWeightedRecord(f.model_mapping_rows), // nil 不修改/空对象清空（后端语义）
    fallback_group_id: f.id ? (f.fallback_group_id ?? 0) : (f.fallback_group_id ?? null),
    fallback_group_id_on_invalid_request: f.id
      ? (f.fallback_group_id_on_invalid_request ?? 0)
      : (f.fallback_group_id_on_invalid_request ?? null),
    model_routing: routingRulesToApi(routingRules),
    model_routing_enabled: routingEnabled,
    models_list_config: {
      // 白名单 UI 已接入（二期 B）：初始值来自编辑快照，用户改动后随表单回传，
      // response_model 两键仍由响应声明字段负责
      enabled: !!f.models_list_enabled,
      models: normModelsList(f.models_list_models),
      response_model_mode: f.response_model_mode || 'request',
      response_model_custom: (f.response_model_custom || '').trim(),
    },
    // —— 二期 B：OpenAI Messages 调度（仅 openai 平台下发配置对象；
    // 其余平台不带该键 → 更新请求指针为 nil 不改动，后端 sanitize 兜底清空） ——
    allow_messages_dispatch: !!f.allow_messages_dispatch,
    default_mapped_model: (f.default_mapped_model || '').trim(),
    ...(f.platform === 'openai'
      ? { messages_dispatch_model_config: messagesDispatchFormStateToConfig(f) }
      : {}),
    // —— 二期 B：推理力度（仅 openai / composite；空串 = 不设上限，
    // 空数组 = 清空映射；其余平台发空值，后端 normalize 返回空值不报错） ——
    max_reasoning_effort: supportsReasoningEffort(f.platform) ? (f.max_reasoning_effort || '') : '',
    reasoning_effort_mappings: supportsReasoningEffort(f.platform)
      ? reasoningEffortRowsToApi(f.reasoning_effort_rows)
      : [],
    // —— v0.2.5：利润控制（始终下发小数；关闭/不支持平台时归 0，与官方 payload 一致） ——
    profit_control_enabled: isProfitControlPlatform(f.platform) ? !!f.profit_control_enabled : false,
    profit_min_margin: isProfitControlPlatform(f.platform) ? (Number(f.profit_min_margin) || 0) : 0,
    profit_safety_buffer: isProfitControlPlatform(f.platform) ? (Number(f.profit_safety_buffer) || 0) : 0,
  })

