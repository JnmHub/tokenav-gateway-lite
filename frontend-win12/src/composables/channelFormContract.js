import { apiStatsRulesToForm, statsRulesFormToAPI, validateStatsRules } from '../components/channel/statsRules.js'
import { PLATFORM_ORDER, platformLabel, apiIntervalsToForm, formIntervalsToAPI, apiTimePricingToForm, formTimePricingToAPI, validateTimePricing, mTokToPerToken, perTokenToMTok, findModelConflict, validateIntervals } from '../components/channel/pricing.js'

/* ===== 回填：渠道行 → 表单（对照旧 apiToForm） ===== */
export function channelToSections(channel, groups) {
  // gid → 平台映射（依赖 allGroups 已加载）
  const groupPlatform = new Map(groups.map((g) => [g.id, g.platform]))
  // 活跃平台 = 分组平台 ∪ 定价平台 ∪ 映射平台
  const active = new Set()
  for (const gid of channel.group_ids || []) {
    const p = groupPlatform.get(gid)
    if (p) active.add(p)
  }
  for (const p of channel.model_pricing || []) if (p.platform) active.add(p.platform)
  for (const p of Object.keys(channel.model_mapping || {})) {
    if (PLATFORM_ORDER.includes(p)) active.add(p)
  }

  const fc = channel.features_config || {}
  const sections = []
  for (const platform of PLATFORM_ORDER) {
    if (!active.has(platform)) continue
    const pricing = (channel.model_pricing || [])
      .filter((p) => (p.platform || 'anthropic') === platform)
      .map((p) => ({
        models: p.models || [],
        billing_mode: p.billing_mode || 'token',
        // per-token 存储 → $/MTok 显示换算（对照旧 apiToForm 的 perTokenToMTok）
        input_price: perTokenToMTok(p.input_price),
        output_price: perTokenToMTok(p.output_price),
        cache_write_price: perTokenToMTok(p.cache_write_price),
        // v0.2.5：1h 缓存写价（per-token → $/MTok）
        cache_write_1h_price: perTokenToMTok(p.cache_write_1h_price),
        cache_read_price: perTokenToMTok(p.cache_read_price),
        fast_multiplier: p.fast_multiplier ?? null,
        flex_multiplier: p.flex_multiplier ?? null,
        max_reasoning_effort_multiplier: p.max_reasoning_effort_multiplier ?? null,
        image_input_price: perTokenToMTok(p.image_input_price),
        image_output_price: perTokenToMTok(p.image_output_price),
        per_request_price: p.per_request_price ?? null,
        intervals: apiIntervalsToForm(p.intervals || []),
        // v0.2.5：分时计价（HH:mm 补秒、倍率两位小数；空配置给默认结构）
        time_pricing: apiTimePricingToForm(p.time_pricing),
      }))
    sections.push({
      platform,
      enabled: true,
      group_ids: (channel.group_ids || []).filter((gid) => groupPlatform.get(gid) === platform),
      model_mapping: { ...(channel.model_mapping || {})[platform] },
      model_pricing: pricing,
      // web_search_emulation / codex_image_generation_bridge 后端读平台 map
      web_search_emulation: fc?.web_search_emulation?.[platform] === true,
      codex_image_generation_bridge: fc?.codex_image_generation_bridge?.[platform] === true,
      // bedrock_cc_compat 后端读布尔；兼容旧前端误写的 {anthropic:true} map
      bedrock_cc_compat: fc?.bedrock_cc_compat === true || fc?.bedrock_cc_compat?.[platform] === true,
    })
  }
  return sections
}

/* ===== 提交校验（对照旧 handleSubmit 校验链）：失败 message 提示并展开对应平台 ===== */
export function validateChannelForm(f) {
  if (!f.name.trim()) return { msg: '名称为必填项' }
  for (const sec of f.platforms.filter((section) => section.enabled)) {
    const label = platformLabel(sec.platform)
    if (sec.group_ids.length === 0) {
      return { platform: sec.platform, msg: `${label} 平台未选择分组，请至少选择一个分组或关闭该平台` }
    }
    for (const entry of sec.model_pricing) {
      if (entry.models.length === 0) {
        return { platform: sec.platform, msg: `${label} 平台有定价条目未添加模型，请补齐或删除该条目` }
      }
    }
    // 定价模型跨条目冲突（精确/通配符重叠）
    const allModels = sec.model_pricing.flatMap((e) => e.models)
    const pc = findModelConflict(allModels)
    if (pc) return { platform: sec.platform, msg: `${label} 平台模型模式 '${pc[0]}' 与 '${pc[1]}' 冲突：匹配范围重叠` }
    // 映射源冲突
    const mappingKeys = Object.keys(sec.model_mapping)
    const mc = mappingKeys.length > 0 ? findModelConflict(mappingKeys) : null
    if (mc) return { platform: sec.platform, msg: `${label} 平台映射源 '${mc[0]}' 与 '${mc[1]}' 冲突：匹配范围重叠` }
    // per_request / image 必须有默认价或至少一个层级
    for (const entry of sec.model_pricing) {
      if (entry.models.length === 0) continue
      if (
        (entry.billing_mode === 'per_request' || entry.billing_mode === 'image') &&
        (entry.per_request_price == null || entry.per_request_price === '') &&
        (!entry.intervals || entry.intervals.length === 0)
      ) {
        return { platform: sec.platform, msg: `${label} 平台按次/图片计费条目必须设置默认价格或至少一个计费层级` }
      }
    }
    // 区间合法性（负数 / 顺序 / 重叠 / 无上限置尾 / 至少一价）
    for (const entry of sec.model_pricing) {
      if (!entry.intervals || entry.intervals.length === 0) continue
      const ivErr = validateIntervals(entry.intervals, entry.billing_mode)
      if (ivErr) {
        const modelLabel = entry.models.join(', ') || '未命名条目'
        return { platform: sec.platform, msg: `${label} - ${modelLabel}: ${ivErr}` }
      }
      // v0.2.5：分时计价（时区 / 时段格式 / 起止 / 重叠 / 倍率）
      const tpErr = validateTimePricing(entry.time_pricing)
      if (tpErr) {
        const modelLabel = entry.models.join(', ') || '未命名条目'
        return { platform: sec.platform, msg: `${label} - ${modelLabel}: ${tpErr}` }
      }
    }
  }
  // 统计定价规则：对照后端 PRICING_RULE_EMPTY_SCOPE / EMPTY_PRICING + validatePricingEntries
  const ruleErr = validateStatsRules(f.stats_rules)
  if (ruleErr) return { msg: ruleErr }
  return null
}

/* ===== 表单 → 提交 body（对照旧 formToAPI） ===== */
export function channelFormToBody(f, editing) {
  const group_ids = []
  const model_pricing = []
  const model_mapping = {}
  // features_config：保留接口返回的未托管字段，再覆写三个受管开关
  const features_config = { ...(editing?.features_config || {}) }

  for (const sec of f.platforms.filter((section) => section.enabled)) {
    group_ids.push(...sec.group_ids)
    if (Object.keys(sec.model_mapping).length > 0) {
      model_mapping[sec.platform] = { ...sec.model_mapping }
    }
    for (const entry of sec.model_pricing) {
      if (entry.models.length === 0) continue
      model_pricing.push({
        platform: sec.platform,
        models: entry.models,
        billing_mode: entry.billing_mode,
        // 显示值 $/MTok → 存储 per-token
        input_price: mTokToPerToken(entry.input_price),
        output_price: mTokToPerToken(entry.output_price),
        cache_write_price: mTokToPerToken(entry.cache_write_price),
        // v0.2.5：1h 缓存写价（$/MTok → per-token）
        cache_write_1h_price: mTokToPerToken(entry.cache_write_1h_price),
        cache_read_price: mTokToPerToken(entry.cache_read_price),
        fast_multiplier: entry.fast_multiplier != null && entry.fast_multiplier !== '' ? Number(entry.fast_multiplier) : null,
        flex_multiplier: entry.flex_multiplier != null && entry.flex_multiplier !== '' ? Number(entry.flex_multiplier) : null,
        max_reasoning_effort_multiplier:
          entry.max_reasoning_effort_multiplier != null && entry.max_reasoning_effort_multiplier !== ''
            ? Number(entry.max_reasoning_effort_multiplier)
            : null,
        image_input_price: mTokToPerToken(entry.image_input_price),
        image_output_price: mTokToPerToken(entry.image_output_price),
        per_request_price: entry.per_request_price != null && entry.per_request_price !== '' ? Number(entry.per_request_price) : null,
        intervals: formIntervalsToAPI(entry.intervals || []),
        // v0.2.5：分时计价（无时段提交 null = 未启用）
        time_pricing: formTimePricingToAPI(entry.time_pricing),
      })
    }
  }

  // web_search_emulation：仅 anthropic；始终覆写以免禁用后残留旧 true 值
  const wse = {}
  for (const sec of f.platforms.filter((section) => section.enabled)) {
    if (sec.platform === 'anthropic') wse[sec.platform] = !!sec.web_search_emulation
  }
  if (Object.keys(wse).length > 0) features_config.web_search_emulation = wse
  else delete features_config.web_search_emulation

  // codex_image_generation_bridge：仅 openai
  const cigb = {}
  for (const sec of f.platforms.filter((section) => section.enabled)) {
    if (sec.platform === 'openai') cigb[sec.platform] = !!sec.codex_image_generation_bridge
  }
  if (Object.keys(cigb).length > 0) features_config.codex_image_generation_bridge = cigb
  else delete features_config.codex_image_generation_bridge

  // bedrock_cc_compat：渠道级布尔（后端 IsBedrockCCCompatEnabled 只读 bool）
  const anthropicSec = f.platforms.filter((section) => section.enabled).find((s) => s.platform === 'anthropic')
  if (anthropicSec) features_config.bedrock_cc_compat = !!anthropicSec.bedrock_cc_compat
  else delete features_config.bedrock_cc_compat

  const body = {
    name: f.name.trim(),
    description: f.description,
    group_ids,
    model_pricing,
    model_mapping,
    billing_model_source: f.billing_model_source,
    restrict_models: !!f.restrict_models,
    features_config,
    apply_pricing_to_account_stats: !!f.apply_pricing_to_account_stats,
    // 统计定价规则：编辑器全量携带（PUT 语义 = 整组替换，空数组即清空规则）
    account_stats_pricing_rules: statsRulesFormToAPI(f.stats_rules),
  }
  if (editing) {
    body.status = f.status // 仅更新走 status（后端 Create 无该字段）
    body.id = editing.id
  }
  return body
}

