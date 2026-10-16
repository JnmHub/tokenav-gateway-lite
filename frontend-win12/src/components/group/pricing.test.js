import test from 'node:test'
import assert from 'node:assert/strict'
import { createPricingEntry, createPricingFormState, createPricingInterval, pricingConfigToForm, pricingFormToRequest, validatePricingForm } from './pricing.js'
import { defaultForm, buildGroupBody } from './groupEditorPayload.js'

const stateWith = (entry, values = {}) => ({ ...createPricingFormState(), enabled: true, model_pricing: [entry], ...values })

test('定价回填与保存保留上下文阶梯、零价和服务档位倍率', () => {
  const config = { enabled: true, model_pricing: [{ models: ['gpt-5.5'], billing_mode: 'token', input_price: 5e-8, output_price: 0,
    fast_multiplier: 2.5, flex_multiplier: 0.4, intervals: [{ min_tokens: 272000, max_tokens: null, input_price: 3e-6,
      output_price: 0, input_multiplier: 2, output_multiplier: 1.5, sort_order: 3 }] }] }
  const form = pricingConfigToForm(config, 'openai')
  assert.equal(form.model_pricing[0].input_price, 0.05)
  assert.equal(form.model_pricing[0].intervals[0].input_price, 3)
  const request = pricingFormToRequest(form, 'openai', 7).model_pricing[0]
  assert.equal(request.input_price, 5e-8)
  assert.equal(request.output_price, 0)
  assert.equal(request.intervals[0].input_price, 3e-6)
  assert.equal(request.intervals[0].output_price, 0)
  assert.equal(request.intervals[0].input_multiplier, 2)
  assert.equal(request.intervals[0].max_tokens, null)
  assert.equal(request.intervals[0].sort_order, 3)
  assert.equal(request.fast_multiplier, 2.5)
  assert.equal(validatePricingForm(form, 'openai'), null)
})

test('视频分辨率允许只配层级价格；按次层级不使用上下文重叠判断', () => {
  const form = stateWith({ ...createPricingEntry('video', 'grok'), models: ['grok-imagine-video'], intervals: [
    { ...createPricingInterval(0, '480p'), per_request_price: 0 },
    { ...createPricingInterval(2, '1080p'), per_request_price: 0.25 },
  ] })
  assert.equal(validatePricingForm(form, 'composite'), null)
  assert.deepEqual(pricingFormToRequest(form, 'composite', 7).model_pricing[0].intervals.map(interval => [interval.tier_label, interval.per_request_price]), [['480p', 0], ['1080p', 0.25]])
})

test('模型冲突仅在同平台发生并覆盖精确名称和通配符', () => {
  const entry = { ...createPricingEntry('token', 'openai'), models: ['GPT-*', 'gpt-5.5'] }
  assert.match(validatePricingForm(stateWith(entry), 'openai'), /^conflict:/)
  const form = stateWith({ ...entry, models: ['same-model'] })
  form.model_pricing.push({ ...createPricingEntry('token', 'anthropic'), models: ['same-model'] })
  assert.equal(validatePricingForm(form, 'composite'), null)
  form.model_pricing[1].platform = 'openai'
  assert.match(validatePricingForm(form, 'composite'), /^conflict:/)
})

test('上下文重叠、非末尾无限段、负价和非正倍率阻止保存', () => {
  const entry = { ...createPricingEntry('token'), models: ['gpt-test'], intervals: [
    { ...createPricingInterval(), min_tokens: 0, max_tokens: 200, input_price: 1 },
    { ...createPricingInterval(1), min_tokens: 100, max_tokens: null, input_price: 2 },
  ] }
  assert.match(validatePricingForm(stateWith(entry), 'openai'), /重叠/)
  entry.intervals[0].max_tokens = null
  assert.match(validatePricingForm(stateWith(entry), 'openai'), /只能放在最后/)
  entry.intervals = []; entry.input_price = -1
  assert.equal(validatePricingForm(stateWith(entry), 'openai'), 'invalid_price')
  entry.input_price = 0; entry.fast_multiplier = 0
  assert.equal(validatePricingForm(stateWith(entry), 'openai'), 'invalid_multiplier')
})

test('账号成本规则按整组或指定账号二选一，阶梯转换同客户价', () => {
  const form = stateWith({ ...createPricingEntry('token'), models: ['gpt-test'] }, { apply_pricing_to_account_stats: true })
  const price = { ...createPricingEntry('token'), models: ['gpt-test'], intervals: [{ ...createPricingInterval(), input_price: 1 }] }
  form.account_stats_pricing_rules = [{ name: '成本', scope: 'whole_group', account_ids: [99], pricing: [price] }]
  let rule = pricingFormToRequest(form, 'openai', 7).account_stats_pricing_rules[0]
  assert.deepEqual(rule.group_ids, [7]); assert.deepEqual(rule.account_ids, [])
  assert.equal(rule.pricing[0].intervals[0].input_price, 1e-6)
  form.account_stats_pricing_rules[0].scope = 'selected_accounts'
  rule = pricingFormToRequest(form, 'openai', 7).account_stats_pricing_rules[0]
  assert.deepEqual(rule.group_ids, []); assert.deepEqual(rule.account_ids, [99])
  form.account_stats_pricing_rules[0].account_ids = []
  assert.equal(validatePricingForm(form, 'openai'), 'rule_accounts_required')
})

test('继承全局显式清空全部自定义规则，启用时保留其他平台的特性', () => {
  const form = stateWith({ ...createPricingEntry('token'), models: ['gpt-test'] }, { features_config: { custom_feature: true, web_search_emulation: { gemini: true } } })
  const request = pricingFormToRequest(form, 'anthropic', 7)
  assert.equal(request.features_config.custom_feature, true)
  assert.deepEqual(request.features_config.web_search_emulation, { gemini: true, anthropic: false })
  form.enabled = false
  assert.deepEqual(pricingFormToRequest(form, 'anthropic', 7), { enabled: false, restrict_models: false, features_config: {}, model_pricing: [], apply_pricing_to_account_stats: false, account_stats_pricing_rules: [] })
})

test('分组更新保留零价及T0，空兜底用零清除，映射和白名单可清空', () => {
  const form = { ...defaultForm(), id: 7, name: ' 测试 ', minimum_tier: 0, image_price_1k: 0, image_price_2k: '',
    image_rate_multiplier: 0, models_list_models: [' gpt-test ', '', 'gpt-test'], copy_accounts_from_group_ids: [3] }
  const body = buildGroupBody(form, [], false)
  assert.equal(body.name, '测试'); assert.equal(body.minimum_tier, 0)
  assert.equal(body.image_price_1k, 0); assert.equal(body.image_price_2k, null); assert.equal(body.image_rate_multiplier, 0)
  assert.equal(body.failover_group_id, 0); assert.equal(body.fallback_group_id, 0)
  assert.deepEqual(body.model_mapping, {}); assert.deepEqual(body.weighted_model_mapping, {})
  assert.deepEqual(body.models_list_config.models, ['gpt-test'])
  assert.deepEqual(body.copy_accounts_from_group_ids, [3])
  const created = buildGroupBody({ ...form, id: 0 }, [], false)
  assert.equal(created.failover_group_id, null); assert.equal(created.fallback_group_id, null)
})
