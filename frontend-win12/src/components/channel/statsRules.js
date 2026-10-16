/* ============================================================
 * 账号统计定价规则（account_stats_pricing_rules）纯函数工具
 * 契约来源：internal/handler/admin/channel_handler.go
 *   accountStatsPricingRuleRequest/Response + channelModelPricingRequest
 * 规则结构（后端 1:1，无平台字段；platform 在每个 pricing 条目上）：
 *   { name, group_ids[], account_ids[], pricing[] }
 * 匹配语义（internal/service/account_stats_pricing.go）：先命中为准，
 *   accountID ∈ rule.account_ids 或 groupID ∈ rule.group_ids；
 *   规则始终优先于「定价计入账号统计」开关，不依赖开关开启。
 * 后端校验（channel_handler.go Create/Update）：
 *   - 每条规则 ≥1 分组或 ≥1 账号（PRICING_RULE_EMPTY_SCOPE）
 *   - 每条规则 ≥1 定价条目（PRICING_RULE_EMPTY_PRICING）
 *   - 规则定价不入倍率（accountStatsPricingRuleRequestToService
 *     走 pricingRequestToService(r.Pricing, false)，Fast/Flex 与区间倍率被丢弃）
 * 价格单位约定与 pricing.js 一致：后端存 per-token，前端显示 $/MTok
 * ============================================================ */
import {
  apiIntervalsToForm, findModelConflict, formIntervalsToAPI,
  mTokToPerToken, perTokenToMTok, emptyPricingEntry, validateIntervals,
} from './pricing.js'

let uidSeq = 0

/** 空白规则工厂（_uid 仅前端 key/搜索状态用，提交时剔除） */
export function emptyStatsRule() {
  return { _uid: `sr-${++uidSeq}`, name: '', group_ids: [], account_ids: [], pricing: [] }
}

/** 规则定价条目工厂：与主定价同构，但 platform 显式必填（后端规则不设默认平台，
 *  空平台 = 匹配任意平台，显式化避免歧义） */
export function emptyStatsPricingEntry(platform) {
  return { ...emptyPricingEntry(), platform: platform || 'anthropic' }
}

/** 后端规则数组 → 表单规则数组（价格 per-token → $/MTok） */
export function apiStatsRulesToForm(rules) {
  return (rules || []).map((rule) => ({
    ...emptyStatsRule(),
    name: rule.name || '',
    group_ids: [...(rule.group_ids || [])],
    account_ids: [...(rule.account_ids || [])],
    pricing: (rule.pricing || []).map((p) => ({
      ...emptyStatsPricingEntry(p.platform || 'anthropic'),
      models: [...(p.models || [])],
      billing_mode: p.billing_mode || 'token',
      input_price: perTokenToMTok(p.input_price),
      output_price: perTokenToMTok(p.output_price),
      cache_write_price: perTokenToMTok(p.cache_write_price),
      cache_read_price: perTokenToMTok(p.cache_read_price),
      // 规则定价不支持倍率：展示/提交恒为 null（后端 allowMultipliers=false）
      fast_multiplier: null,
      flex_multiplier: null,
      image_input_price: perTokenToMTok(p.image_input_price),
      image_output_price: perTokenToMTok(p.image_output_price),
      per_request_price: p.per_request_price ?? null,
      intervals: apiIntervalsToForm(p.intervals || []),
    })),
  }))
}

/** 表单规则数组 → 提交数组（$/MTok → per-token；剔除 _uid；倍率恒 null） */
export function statsRulesFormToAPI(rules) {
  return (rules || []).map((rule) => ({
    name: (rule.name || '').trim(),
    group_ids: [...(rule.group_ids || [])],
    account_ids: [...(rule.account_ids || [])],
    pricing: (rule.pricing || [])
      .filter((e) => e.models.length > 0)
      .map((e) => ({
        platform: e.platform || 'anthropic',
        models: [...e.models],
        billing_mode: e.billing_mode,
        input_price: mTokToPerToken(e.input_price),
        output_price: mTokToPerToken(e.output_price),
        cache_write_price: mTokToPerToken(e.cache_write_price),
        cache_read_price: mTokToPerToken(e.cache_read_price),
        fast_multiplier: null,
        flex_multiplier: null,
        image_input_price: mTokToPerToken(e.image_input_price),
        image_output_price: mTokToPerToken(e.image_output_price),
        per_request_price: e.per_request_price != null && e.per_request_price !== '' ? Number(e.per_request_price) : null,
        intervals: formIntervalsToAPI(e.intervals || []),
      })),
  }))
}

/** 规则数组校验（对照后端 Create/Update 校验链 + validatePricingEntries），
 *  返回错误文案或 null */
export function validateStatsRules(rules) {
  const list = rules || []
  for (let i = 0; i < list.length; i++) {
    const rule = list[i]
    const label = rule.name?.trim() ? `「${rule.name.trim()}」` : `#${i + 1}`
    // PRICING_RULE_EMPTY_SCOPE：至少一个分组或一个账号
    if (rule.group_ids.length === 0 && rule.account_ids.length === 0) {
      return `统计定价规则 ${label}：至少选择一个分组或一个账号`
    }
    // PRICING_RULE_EMPTY_PRICING：至少一个定价条目
    if (rule.pricing.length === 0) {
      return `统计定价规则 ${label}：至少添加一个定价条目`
    }
    const allModels = []
    for (const entry of rule.pricing) {
      if (entry.models.length === 0) {
        return `统计定价规则 ${label}：有定价条目未添加模型，请补齐或删除该条目`
      }
      allModels.push(...entry.models)
      // 按次/图片/视频必须设置默认价格或至少一个层级（后端 checkBillingModeRequirements）
      if (
        entry.billing_mode !== 'token' &&
        (entry.per_request_price == null || entry.per_request_price === '') &&
        (!entry.intervals || entry.intervals.length === 0)
      ) {
        return `统计定价规则 ${label}：按次/图片/视频条目必须设置默认价格或至少一个计费层级`
      }
      // 区间合法性（负数 / 顺序 / 重叠 / 无上限置尾 / 至少一价）
      if (entry.intervals && entry.intervals.length > 0) {
        const ivErr = validateIntervals(entry.intervals, entry.billing_mode)
        if (ivErr) return `统计定价规则 ${label}：${ivErr}`
      }
    }
    // 规则内跨条目模型模式冲突
    const pc = findModelConflict(allModels)
    if (pc) {
      return `统计定价规则 ${label}：模型模式 '${pc[0]}' 与 '${pc[1]}' 冲突，匹配范围重叠`
    }
  }
  return null
}
