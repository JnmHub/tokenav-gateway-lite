import { createServer } from 'node:http'
import { once } from 'node:events'
import { readFile } from 'node:fs/promises'
import { before, after, beforeEach, afterEach, test } from 'node:test'
import assert from 'node:assert/strict'
import { parse, compileScript, compileTemplate } from 'vue/compiler-sfc'
import { createRenderer, h, reactive, nextTick } from 'vue'
import { message } from 'ant-design-vue'
import { setToken } from './index.js'
import { resetRequestSession } from './session.js'
import { AI_FIELDS, aiErrorLabel, aiCallLabel, aiPhaseLabel, presentAIDecision, presentAICoverage, presentAITest, presentAIReadiness, isCurrentAITestResult, applyDeepSeekPreset, aiEndpoint, buildRiskConfigPatch, testRiskAI, readRiskLog, legacyKeyErrorLabel, legacyKeyStatusLabel } from './riskControl.js'
import { isAILog, riskLogResult, riskLogCategories, riskLogInputText, riskLogReviewText } from './riskLogPresentation.js'

// Synthetic local HTTP only: no real secrets, billing service or browser.
const storage = new Map()
globalThis.localStorage = { getItem: (key) => storage.get(key) ?? null, setItem: (key, value) => storage.set(key, String(value)), removeItem: (key) => storage.delete(key) }
globalThis.window = { location: { origin: '' }, dispatchEvent() {} }
const nativeFetch = globalThis.fetch
for (const name of ['success', 'error', 'info']) message[name] = () => {}
const renderer = createRenderer({ createElement: () => ({ children: [] }), createText: () => ({}), createComment: () => ({}), setText() {}, setElementText() {}, parentNode: () => null, nextSibling: () => null, insert() {}, remove() {}, patchProp() {} })
let server, mounts = [], responseGate, received, replyValue, writes = [], riskFixture, riskMetricsFixture
const deferred = () => { let resolve; const promise = new Promise((r) => { resolve = r }); return { promise, resolve } }
const setupCache = new Map()
async function setupFor(relative) {
  const url = new URL(relative, import.meta.url)
  if (setupCache.has(url.href)) return setupCache.get(url.href)
  const source = await readFile(url, 'utf8'), { descriptor, errors } = parse(source, { filename: url.pathname })
  assert.equal(errors.length, 0)
  const compiled = compileScript(descriptor, { id: url.href }).content
  const imports = [...compiled.matchAll(/^import[\s\S]*?from ['"]([^'"]+)['"];?\s*$/gm)]
  const statements = [], modules = []
  for (const [statement, specifier] of imports) {
    const module = specifier.endsWith('.vue') ? { default: {} } : await import(specifier.startsWith('.') ? new URL(specifier, url).href : specifier)
    const clause = statement.slice('import'.length, statement.lastIndexOf('from')).trim(), index = modules.push(module) - 1
    statements.push(clause.startsWith('{') ? `const ${clause.replace(/\bas\b/g, ':')} = modules[${index}]` : `const ${clause} = modules[${index}].default`)
  }
  const body = compiled.replace(/^import[\s\S]*?from ['"]([^'"]+)['"];?\s*$/gm, '').replace('export default', 'return')
  const component = new Function('modules', statements.join('\n') + '\n' + body)(modules)
  setupCache.set(url.href, component.setup)
  return component.setup
}
async function mountPanel(relative, initialProps) {
  const setup = await setupFor(relative), props = reactive(initialProps), events = [], exposed = {}
  let state
  const app = renderer.createApp({ setup() { state = setup(props, { expose(value) { Object.assign(exposed, value) }, emit(...args) { events.push(args) } }); return () => h('div') } })
  app.mount({ children: [] }); mounts.push(app)
  return { state, props, events, exposed }
}
const savedConfig = (fingerprint = 'saved-fingerprint-a') => ({ enabled: false, keyword_blocking_mode: 'keyword_only', mode: 'off', ai: { config_fingerprint: fingerprint, adapter: 'deepseek_chat', model: 'deepseek-flash', base_url: 'https://api.deepseek.com', model_revision: 'unverified', external_data_consent: false, policy_version: 'policy-v1', policy: '合成审查策略', penalties_enabled: false, image_review_enabled: false } })
const resultFor = (fingerprint = 'saved-fingerprint-a') => ({ config_fingerprint: fingerprint, policy_version: 'policy-v1', verdict: 'allow', provider_called: true, phase: 'completed', provider_http_status: 200, latency_ms: 15, provider_latency_ms: 10, reason: '合成安全内容', categories: [], coverage: { complete: true }, test: { status: 'passed', config_fingerprint: fingerprint, verdict: 'allow', coverage_complete: true } })
before(async () => {
  server = createServer(async (req, res) => {
    let raw = ''; for await (const chunk of req) raw += chunk
    const body = raw ? JSON.parse(raw) : undefined
    writes.push({ path: req.url, method: req.method, body })
    let snapshot = structuredClone(replyValue)
    if (req.url.endsWith('/risk-control/config')) {
      if (req.method === 'PUT') {
        riskFixture = { ...riskFixture, ...body, ai: { ...riskFixture.ai, ...body.ai, config_fingerprint: 'saved-after-write' } }
        delete riskFixture.ai.api_key
      }
      snapshot = structuredClone(riskFixture)
    } else if (req.url.endsWith('/groups/all')) snapshot = []
    else if (req.url.endsWith('/risk-control/status')) snapshot = { enabled: false, risk_control_enabled: false, mode: 'off' }
    else if (req.url.endsWith('/ai/metrics')) snapshot = { ...structuredClone(riskMetricsFixture), config_fingerprint: riskFixture.ai.config_fingerprint }
    else if (req.url.includes('/risk-control/logs/')) snapshot = { ...snapshot, id: Number(req.url.split('/').at(-1)) }
    else if (req.url.includes('/risk-control/logs?')) snapshot = { items: [], total: 0, page: 1, page_size: 20 }
    else if (req.url.endsWith('/keyword-catalog/status')) snapshot = {}
    received.resolve()
    if (responseGate) await responseGate.promise
    res.writeHead(200, { 'Content-Type': 'application/json' }); res.end(JSON.stringify({ code: 0, data: snapshot }))
  })
  server.listen(0, '127.0.0.1'); await once(server, 'listening')
  window.location.origin = `http://127.0.0.1:${server.address().port}`
})
beforeEach(() => {
  resetRequestSession('synthetic-test'); storage.clear(); setToken('synthetic-admin'); mounts = []; writes = []
  replyValue = resultFor(); riskFixture = savedConfig(); riskMetricsFixture = { production: { ready: false, checks: [] } }; responseGate = null; received = deferred(); globalThis.fetch = nativeFetch
})
afterEach(() => { for (const app of mounts) app.unmount(); responseGate?.resolve() })
after(async () => { globalThis.fetch = nativeFetch; server.closeAllConnections(); await new Promise((resolve) => server.close(resolve)) })

test('AI 只提交简化字段，历史限制草稿不参与保存或脏状态，密钥单独显式处理', () => {
  assert.deepEqual(AI_FIELDS, ['adapter', 'base_url', 'model', 'model_revision', 'external_data_consent', 'policy_version', 'policy', 'penalties_enabled', 'image_review_enabled'])
  const saved = savedConfig(), draft = structuredClone(saved)
  const removed = ['daily_budget_usd_micros', 'tenant_daily_budget_usd_micros', 'daily_token_budget', 'tenant_daily_token_budget', 'input_price_per_million_usd_micros', 'output_price_per_million_usd_micros', 'total_timeout_ms', 'max_concurrency', 'tenant_max_concurrency', 'queue_size', 'rpm', 'tenant_rpm', 'max_input_bytes', 'batch_input_runes', 'fragment_runes', 'fragment_overlap_runes', 'max_output_tokens', 'max_images_per_request', 'max_image_bytes', 'retry_count', 'cache_ttl_seconds', 'circuit_failure_threshold', 'circuit_open_seconds']
  for (const key of removed) draft.ai[key] = 99
  assert.deepEqual(buildRiskConfigPatch(draft, saved), {})
  draft.ai.model = 'custom-model'; draft.ai.image_review_enabled = true
  assert.deepEqual(buildRiskConfigPatch(draft, saved, { apiKey: ' synthetic-key ' }), { ai: { model: 'custom-model', image_review_enabled: true, api_key: 'synthetic-key' } })
  assert.deepEqual(buildRiskConfigPatch(saved, saved, { clearAPIKey: true }), { ai: { clear_api_key: true } })
})

test('服务预设保留 deepseek-flash，不默认开启外部授权，自定义协议路径一致', () => {
  const ai = { external_data_consent: false, api_key_configured: false }
  applyDeepSeekPreset(ai, 'deepseek_responses')
  assert.equal(ai.model, 'deepseek-flash'); assert.equal(ai.external_data_consent, false)
  assert.equal(Object.keys(ai).some((key) => /price|budget|timeout|cache|retry/.test(key)), false)
  assert.equal(aiEndpoint(ai), 'https://api.deepseek.com/responses')
  ai.base_url = 'http://192.168.1.10:8000/v1'; ai.adapter = 'deepseek_chat'
  assert.equal(aiEndpoint(ai), 'http://192.168.1.10:8000/v1/chat/completions')
  for (const base_url of ['ftp://review.internal', 'https://user:secret@review.internal', 'https://review.internal?token=secret', 'https://review.internal#secret']) assert.equal(aiEndpoint({ ...ai, base_url }), '')
})

test('服务端错误分类中文且不回显供应商原文或秘密，404 不冒充密钥失效', () => {
  assert.match(aiErrorLabel('ai_provider_invalid_credential', 401), /密钥.*HTTP 401/)
  assert.match(aiErrorLabel('ai_provider_not_found', 404), /路径.*不能证明密钥无效.*HTTP 404/)
  assert.match(aiErrorLabel('ai_response_read'), /读取失败/)
  assert.match(aiErrorLabel('ai_result_schema'), /四字段.*策略版本/)
  assert.match(aiErrorLabel('ai_penalty_store_unavailable'), /处罚.*核实/)
  const secret = 'private-provider-body synthetic-secret'
  assert.doesNotMatch(aiErrorLabel(secret), /private-provider-body|synthetic-secret/)
  assert.doesNotMatch(presentAIDecision({ verdict: 'block', message: secret, coverage: { complete: true } }).reason, /synthetic-secret/)
  assert.equal(presentAIDecision({ verdict: 'block', coverage: { complete: false } }).label, '未判定')
  assert.equal(legacyKeyStatusLabel('frozen'), '暂时冷却（本进程）')
  assert.match(legacyKeyErrorLabel({ last_http_status: 404, last_error: secret }), /旧 \/v1\/moderations.*不能/)
  assert.doesNotMatch(legacyKeyErrorLabel({ last_error: secret }), /synthetic-secret/)
})

test('测试与生产条件只采用匹配的服务端配置指纹，修订/协议/密钥不冒充验证', () => {
  const saved = savedConfig(), fingerprint = saved.ai.config_fingerprint
  assert.equal(presentAITest(null, fingerprint).tone, 'warn')
  assert.equal(presentAITest({ status: 'passed', config_fingerprint: 'old' }, fingerprint).tone, 'warn')
  assert.equal(presentAITest({ status: 'stale', config_fingerprint: fingerprint }, fingerprint).tone, 'warn')
  assert.equal(presentAITest({ status: 'expected_rejection', config_fingerprint: fingerprint }, fingerprint).label, '测试状态未知')
  const missing = [{ code: 'external_data_consent', ready: false, message: '尚未授权' }, { code: 'penalty_store', ready: false, message: '处罚存储不可用' }]
  const metrics = { config_fingerprint: fingerprint, test: resultFor().test, production: { ready: false, checks: missing }, production_checked: 0 }
  const display = presentAIReadiness(metrics, saved)
  assert.equal(display.known, true); assert.equal(display.tone, 'warn'); assert.deepEqual(display.missing, missing)
  assert.equal(presentAIReadiness({ ...metrics, config_fingerprint: 'old' }, saved).known, false)
  assert.equal(presentAIReadiness({ configured: true, model_revision: 'verified' }, saved).known, false)
  assert.equal(isCurrentAITestResult(resultFor(), fingerprint), true)
  assert.equal(isCurrentAITestResult({ ...resultFor(), test: { ...resultFor().test, status: 'stale' } }, fingerprint), false)
})

test('就绪摘要独立显示全部测试状态，缺真实条件仍警告，旧指纹不得冒充当前状态', async () => {
  const saved = savedConfig(), fingerprint = saved.ai.config_fingerprint
  const metrics = { config_saved: true, config_fingerprint: fingerprint, production: { ready: true, checks: [] } }
  const { state, props } = await mountPanel('../components/risk/AIReadinessSummary.vue', { saved, metrics, dirty: false })
  for (const status of ['not_tested', 'running', 'passed', 'failed', 'stale']) {
    props.metrics = { ...metrics, test: { status, config_fingerprint: status === 'stale' ? 'older-fingerprint' : fingerprint } }
    assert.equal(state.readiness.value.known, true, status)
    assert.equal(state.readiness.value.tone, 'good', status)
    assert.deepEqual(state.readiness.value.missing, [])
    assert.deepEqual(state.test.value, presentAITest(props.metrics.test, fingerprint))
    if (status === 'stale') assert.match(state.test.value.label, /旧测试已过期.*可选重测/)
    if (status === 'failed') assert.equal(state.test.value.tone, 'warn')
  }
  const missing = [{ code: 'external_data_consent', ready: false, message: '尚未授权' }]
  props.metrics = { ...metrics, test: resultFor().test, production: { ready: false, checks: missing } }
  assert.equal(state.test.value.tone, 'good')
  assert.equal(state.readiness.value.tone, 'warn')
  assert.deepEqual(state.readiness.value.missing, missing)
  props.metrics = { ...metrics, config_fingerprint: 'older-fingerprint', test: resultFor().test }
  assert.equal(state.readiness.value.known, false)
  assert.equal(state.test.value.label, '当前配置测试状态未知')
})

test('测试封包只传合成案例和已保存指纹，不传地址、模型或密钥', async () => {
  await testRiskAI('allow', { configFingerprint: 'saved-fingerprint-a' })
  assert.deepEqual(writes[0], { path: '/api/v1/admin/risk-control/ai/test', method: 'POST', body: { case_id: 'allow', config_fingerprint: 'saved-fingerprint-a' } })
  const saved = savedConfig(), draft = structuredClone(saved)
  draft.ai.config_fingerprint = 'forged'; draft.ai.test = resultFor().test
  draft.ai.production = { ready: true }; draft.ai.api_key_masked = 'synthetic-mask'
  assert.deepEqual(buildRiskConfigPatch(draft, saved), {})
})

test('实际 AI 配置组件保留自定义服务模型，不再产生金额有效性或限制草稿', async () => {
  const cfg = savedConfig(), { state, events } = await mountPanel('../components/risk/AIConfigPanel.vue', { cfg, saved: structuredClone(cfg), saving: false })
  assert.equal(state.service.value, 'deepseek_chat')
  state.selectService('custom'); assert.equal(state.service.value, 'custom'); assert.equal(cfg.ai.base_url, 'https://api.deepseek.com')
  state.selectModel('deepseek-v4-pro'); assert.equal(cfg.ai.model, 'deepseek-v4-pro')
  state.selectModel('custom'); assert.equal(state.modelChoice.value, 'custom')
  assert.equal(cfg.ai.external_data_consent, false)
  assert.equal(state.editMoney, undefined); assert.equal(state.advancedGroups, undefined)
  assert.equal(events.some(([event]) => event === 'validity'), false)
  assert.deepEqual(Object.keys(cfg.ai).filter((key) => !['config_fingerprint', ...AI_FIELDS].includes(key)), [])
})

test('实际合成测试组件按已保存配置测试，有草稿也不传草稿，配置改变即清除旧成功', async () => {
  const { state, props, events } = await mountPanel('../components/risk/AITestPanel.vue', { saved: savedConfig(), dirty: true, saving: false })
  await state.run()
  assert.equal(state.test.value.tone, 'good')
  assert.deepEqual(writes[0].body, { case_id: 'allow', config_fingerprint: 'saved-fingerprint-a' })
  assert.equal(events.filter(([event]) => event === 'tested').length, 1)
  props.saved = savedConfig('saved-fingerprint-b'); await nextTick()
  assert.equal(state.result.value, null); assert.equal(state.stale.value, true)
  assert.equal(state.test.value.tone, 'warn')
})

test('合成测试读取覆盖字段层级，外链未覆盖与无输入跳过不冒充已审', async () => {
  const { state } = await mountPanel('../components/risk/AITestPanel.vue', { saved: savedConfig(), saving: false })
  const uncovered = ['input[0].content[1]:image_url']
  replyValue = { ...resultFor(), coverage: { complete: true, uncovered_fields: uncovered } }
  state.caseId.value = 'unsupported_media'
  await state.run()
  assert.deepEqual(state.uncoveredFields.value, uncovered)
  assert.equal(state.decision.value.label, '通过')
  assert.equal(state.coverage.value.label, '已选内容已完成审查')
  replyValue = { ...resultFor(), provider_called: false, phase: 'skipped', coverage: { complete: false, uncovered_fields: uncovered }, test: { ...resultFor().test, status: 'failed' } }
  await state.run()
  assert.equal(state.decision.value.label, '跳过审查 · 放行')
  assert.equal(state.coverage.value.label, '未送审')
  assert.deepEqual(state.uncoveredFields.value, uncovered)
})

test('实际合成测试组件拒绝配置变化后迟到的成功响应', async () => {
  const { state, props } = await mountPanel('../components/risk/AITestPanel.vue', { saved: savedConfig(), saving: false })
  responseGate = deferred()
  const pending = state.run(); await received.promise
  props.saved = savedConfig('saved-fingerprint-b'); await nextTick()
  responseGate.resolve(); await pending
  assert.equal(state.result.value, null); assert.equal(state.stale.value, true)
  assert.equal(state.test.value.tone, 'warn'); assert.equal(state.running.value, false)
})

test('实际合成测试组件拒绝后台 stale 状态和指纹缺失，网络失败不展示原文', async () => {
  const { state } = await mountPanel('../components/risk/AITestPanel.vue', { saved: savedConfig(), saving: false })
  replyValue = { ...resultFor(), test: { ...resultFor().test, status: 'stale' } }
  await state.run(); assert.equal(state.stale.value, true); assert.equal(state.result.value, null)
  replyValue = { verdict: 'allow', coverage: { complete: true } }
  await state.run(); assert.equal(state.result.value, null)
  globalThis.fetch = async () => { throw new Error('private-provider-body synthetic-secret') }
  await state.run(); assert.doesNotMatch(state.error.value, /synthetic-secret/); assert.match(state.error.value, /失败/)
})

test('实际页面保存后才调用合成测试，携带新指纹且不依赖已删除金额字段', async () => {
  const { state } = await mountPanel('../apps/RiskControl.vue', {})
  // Ensure the page onMounted read has settled before editing its real state.
  await state.load()
  state.cfg.value.ai.model = 'deepseek-v4-pro'
  let tested = 0
  state.aiTestPanel.value = { async run() {
    tested++
    assert.equal(state.saved.value.ai.model, 'deepseek-v4-pro')
    await testRiskAI('allow', { configFingerprint: state.saved.value.ai.config_fingerprint })
  } }
  await state.saveConfig(true)
  assert.equal(tested, 1)
  const put = writes.findIndex((item) => item.method === 'PUT')
  const post = writes.findIndex((item) => item.path.endsWith('/ai/test'))
  assert.equal(put >= 0 && post > put, true)
  assert.deepEqual(writes[post].body, { case_id: 'allow', config_fingerprint: 'saved-after-write' })
  assert.equal(state.activeTab.value, 'aitest')
  assert.equal(state.moneyValid, undefined)
  state.cfg.value.ai.daily_budget_usd_micros = 0
  state.cfg.value.ai.retry_count = 99
  assert.equal(state.dirty.value, false)
  state.cfg.value.ai.image_review_enabled = true
  await state.saveConfig()
  const lastPut = writes.filter((item) => item.method === 'PUT').at(-1)
  assert.deepEqual(lastPut.body, { ai: { image_review_enabled: true } })
})

test('普通保存关闭图片并重读不调用合成测试，证据过期与生产就绪可同时显示', async () => {
  Object.assign(riskFixture, { enabled: true, keyword_blocking_mode: 'ai_only', mode: 'pre_block' })
  Object.assign(riskFixture.ai, { image_review_enabled: true, external_data_consent: true, api_key_configured: true })
  riskMetricsFixture = { config_saved: true, test: resultFor().test, production: { ready: true, checks: [] } }
  const { state } = await mountPanel('../apps/RiskControl.vue', {})
  await state.load()
  const testPanel = await mountPanel('../components/risk/AITestPanel.vue', { saved: state.saved.value, saving: false })
  state.aiTestPanel.value = testPanel.exposed
  const summary = await mountPanel('../components/risk/AIReadinessSummary.vue', { saved: state.saved.value, metrics: state.metrics.value, dirty: false })
  assert.equal(summary.state.test.value.tone, 'good')
  state.cfg.value.ai.image_review_enabled = false
  // 模拟后端独立证据过期，不由页面保存动作发起任何新合成验证。
  riskMetricsFixture.test = { ...riskMetricsFixture.test, status: 'stale' }
  await state.saveConfig()
  assert.deepEqual(writes.filter((item) => item.method === 'PUT').map((item) => item.body), [{ ai: { image_review_enabled: false } }])
  assert.equal(state.saved.value.ai.image_review_enabled, false)
  assert.equal(state.saved.value.ai.config_fingerprint, 'saved-after-write')
  assert.equal(state.dirty.value, false)
  const afterSave = writes.length
  await state.load()
  assert.equal(writes.slice(afterSave).some((item) => item.method === 'GET' && item.path.endsWith('/risk-control/config')), true)
  assert.equal(writes.slice(afterSave).some((item) => item.method === 'GET' && item.path.endsWith('/ai/metrics')), true)
  assert.equal(state.cfg.value.ai.image_review_enabled, false)
  summary.props.saved = state.saved.value; summary.props.metrics = state.metrics.value
  assert.equal(summary.props.metrics.config_saved, true)
  assert.equal(summary.state.readiness.value.known, true)
  assert.equal(summary.state.readiness.value.tone, 'good')
  assert.deepEqual(summary.state.readiness.value.missing, [])
  assert.match(summary.state.test.value.label, /旧测试已过期.*可选重测/)
  assert.equal(writes.some((item) => item.path.endsWith('/ai/test')), false)
  assert.equal(testPanel.state.result.value, null)
})

test('整体判定区分真实调用、失败与无输入跳过，不将覆盖缺失标为通过', () => {
  assert.equal(presentAIDecision(resultFor()).label, '通过')
  assert.equal(presentAIDecision({ ...resultFor(), verdict: 'block', categories: ['violence'] }).label, '违规')
  const failed = { ...resultFor(), verdict: 'error', phase: 'provider', provider_called: true, error_code: 'ai_transport', coverage: { complete: false } }
  assert.equal(presentAIDecision(failed).label, '未判定')
  assert.equal(aiCallLabel(failed.provider_called), '已调用模型')
  assert.equal(aiPhaseLabel(failed.phase), '调用模型服务')
  assert.equal(aiCallLabel(false), '未调用模型'); assert.equal(aiCallLabel(undefined), '调用状态未知')
  const skipped = { verdict: 'allow', provider_called: false, phase: 'skipped', coverage: { complete: false } }
  assert.equal(presentAIDecision(skipped).label, '跳过审查 · 放行')
  assert.match(presentAIDecision(skipped).reason, /未调用模型/)
  assert.equal(presentAIDecision({ ...resultFor(), provider_called: false }).label, '未判定')
  assert.equal(presentAIDecision({ ...resultFor(), provider_called: undefined }).label, '未判定')
  assert.equal(presentAIDecision({ ...resultFor(), error_code: 'ai_result_schema' }).label, '未判定')
  const partialMedia = { ...resultFor(), coverage: { complete: true, uncovered_fields: ['messages[0].image_url'] } }
  assert.equal(presentAIDecision(partialMedia).label, '通过')
  assert.equal(presentAIDecision({ ...resultFor(), phase: 'skipped' }).label, '未判定')
  assert.equal(presentAIDecision({ ...resultFor(), phase: undefined }).label, '未判定')
  assert.equal(presentAICoverage({ ...resultFor(), phase: 'skipped' }).label, '未送审')
  assert.equal(presentAICoverage({ ...resultFor(), provider_called: false }).label, '未送审')
  assert.equal(presentAICoverage({ ...resultFor(), verdict: 'error' }).label, '未完成 / 未知')
  assert.equal(presentAICoverage({ ...resultFor(), error_code: 'ai_result_schema' }).label, '未完成 / 未知')
})

test('完整违规附带处罚存储异常仍显示违规，失败结果不能沿用完整覆盖', async () => {
  const ai = { ...resultFor(), verdict: 'block', categories: ['violence'], error_code: 'ai_penalty_store_unavailable' }
  assert.equal(presentAIDecision(ai).label, '违规')
  assert.match(presentAIDecision(ai).reason, /处罚结果需核实/)
  assert.equal(riskLogResult({ action: 'block', ai_review_detail: ai }).label, 'AI 违规 · 已拦截')
  const { state, props } = await mountPanel('../components/risk/RiskLogDetail.vue', { row: { id: 1, action: 'block', ai_review_detail: ai } })
  assert.equal(state.decision.value.label, '违规')
  assert.equal(state.coverage.value.label, '已选内容已完成审查')
  props.row = { id: 2, ai_review_detail: { ...ai, verdict: 'error', phase: 'provider' } }
  assert.equal(state.decision.value.label, '未判定')
  assert.equal(state.coverage.value.label, '未完成 / 未知')
  props.row = { id: 3, ai_review_detail: { ...ai, verdict: 'allow', error_code: '', phase: 'skipped', provider_called: false } }
  assert.equal(state.decision.value.label, '跳过审查 · 放行')
  assert.equal(state.coverage.value.label, '未送审')
})

test('AI 日志分类只呈现整体标签，错误与历史缺失不伪造概率或通过', () => {
  const row = { action: 'block', flagged: true, highest_category: 'violence', highest_score: 1, category_scores: { violence: 1 }, ai_review_detail: { ...resultFor(), verdict: 'block', categories: ['violence', 'illicit'] } }
  assert.equal(isAILog(row), true)
  assert.deepEqual(riskLogCategories(row), ['violence', 'illicit'])
  assert.equal(riskLogResult(row).label, 'AI 违规 · 已拦截')
  assert.equal(riskLogResult({ ...row, action: 'ai_shadow_block' }).label, 'AI 观察 · 违规但未处罚')
  row.ai_review_detail = { verdict: 'error', provider_called: false, phase: 'preparation', error_code: 'ai_invalid_config', categories: [] }
  assert.deepEqual(riskLogCategories(row), [])
  assert.equal(riskLogResult(row).label, 'AI 未判定 · 请求拒绝')
  assert.equal(riskLogResult({ action: 'ai_shadow_allow' }).label, 'AI 未判定 · 历史信息不足')
  assert.equal(riskLogResult({ ai_review_detail: { verdict: 'allow', provider_called: false, phase: 'skipped', coverage: { complete: false } } }).label, 'AI 跳过审查 · 放行')
  assert.equal(riskLogResult({ action: 'allow', flagged: true }).label, '已命中 · 观察放行')
})

test('日志完整提取与实际送审文本不截断，未送审与错误分开', () => {
  const full = '合成完整输入\n'.repeat(10000), review = `整体送审\n${full}`
  const row = { full_input_text: full, review_text_excerpt: review, input_excerpt: '短预览', error: 'ai_transport', ai_review_detail: { provider_called: true } }
  assert.equal(riskLogInputText(row), full); assert.equal(riskLogReviewText(row), review)
  assert.doesNotMatch(riskLogInputText({ input_excerpt: '预览', error: '错误不能成为原文' }), /错误不能成为原文|预览/)
  assert.doesNotMatch(riskLogReviewText({ error: '错误不能成为送审文本' }), /错误不能成为送审文本/)
  assert.equal(riskLogReviewText({ ...row, ai_review_detail: { provider_called: false } }), '未调用模型，无实际送审文本')
})

test('日志详情通过管理员 GET 按条读取并只添加一次接口前缀，搜索保留 AI 与错误文本', async () => {
  replyValue = { full_input_text: '完整输入', review_text_excerpt: '整体送审', ai_review_detail: resultFor() }
  const row = await readRiskLog(42)
  assert.equal(row.id, 42); assert.equal(row.full_input_text, '完整输入')
  assert.deepEqual(writes[0], { path: '/api/v1/admin/risk-control/logs/42', method: 'GET', body: undefined })
  const { state } = await mountPanel('../components/risk/RiskLogsPanel.vue', {})
  state.filters.value.search = 'AI 文本 ai_transport'
  await state.reload()
  const query = writes.filter((item) => item.path.includes('/risk-control/logs?')).at(-1)
  assert.equal(new URL(query.path, window.location.origin).searchParams.get('search'), 'AI 文本 ai_transport')
})

test('实际日志详情切换、关闭与会话结束后不接受迟到的完整文本', async () => {
  const { state } = await mountPanel('../components/risk/RiskLogsPanel.vue', {})
  await state.loadLogs()
  const first = deferred(), second = deferred(), third = deferred(), fourth = deferred()
  const gates = new Map([['101', first], ['102', second], ['103', third], ['104', fourth]])
  globalThis.fetch = (url, options) => {
    const gate = gates.get(new URL(url).pathname.split('/').at(-1))
    return gate ? gate.promise : nativeFetch(url, options)
  }
  const response = (id) => new Response(JSON.stringify({ code: 0, data: { id, full_input_text: `完整输入 ${id}`, ai_review_detail: resultFor() } }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  const old = state.openDetail({ id: 101, input_excerpt: '列表预览不能冒充完整输入' })
  const current = state.openDetail({ id: 102 })
  assert.equal(state.detailRow.value, null); assert.equal(state.detailLoading.value, true)
  second.resolve(response(102)); await current
  first.resolve(response(101)); await old
  assert.equal(state.detailRow.value.id, 102)
  const closed = state.openDetail({ id: 103 })
  state.detailOpen.value = false
  third.resolve(response(103)); await closed
  assert.equal(state.detailRow.value, null); assert.equal(state.detailLoading.value, false)
  const ended = state.openDetail({ id: 104 })
  resetRequestSession('synthetic-session-ended')
  fourth.resolve(response(104)); await ended
  assert.equal(state.detailRow.value, null); assert.equal(state.detailOpen.value, false)
})

test('日志详情读取失败支持重试且不把失败响应当作原文', async () => {
  const { state } = await mountPanel('../components/risk/RiskLogsPanel.vue', {})
  await state.loadLogs()
  globalThis.fetch = async () => { throw new Error('private-provider-body synthetic-secret') }
  await state.openDetail({ id: 42, input_excerpt: '列表短预览' })
  assert.equal(state.detailRow.value, null); assert.match(state.detailError.value, /读取失败/)
  assert.doesNotMatch(state.detailError.value, /synthetic-secret|private-provider-body/)
  globalThis.fetch = nativeFetch
  replyValue = { full_input_text: '重试后的完整输入' }
  await state.loadDetail()
  assert.equal(state.detailRow.value.full_input_text, '重试后的完整输入')
})

test('日志详情只将片段和图片作为输入元信息，整体类别与错误分开展示', async () => {
  const { state } = await mountPanel('../components/risk/RiskLogDetail.vue', { row: { id: 1, ai_review_detail: { ...resultFor(), verdict: 'block', categories: ['violence'], fragments: [{ id: 'user:0', parent: 'messages[0]', role: 'user', verdict: 'allow', text: '不应在元信息中展示' }], images: [{ id: 'image:0', media_type: 'image/png', bytes: 128, verdict: 'allow' }] } } })
  assert.equal(state.decision.value.label, '违规')
  assert.deepEqual(state.categories.value, ['violence'])
  const metadata = state.metadata(state.ai.value.fragments[0])
  assert.match(metadata, /messages\[0\].*user/); assert.doesNotMatch(metadata, /allow|不应在元信息中展示/)
})

test('本轮修改的 Vue 单文件组件脚本与模板可编译', async () => {
  for (const relative of ['../apps/RiskControl.vue', '../components/risk/AIConfigPanel.vue', '../components/risk/AIRuntimePanel.vue', '../components/risk/AIReadinessSummary.vue', '../components/risk/AITestPanel.vue', '../components/risk/RiskLogsPanel.vue', '../components/risk/RiskLogDetail.vue']) {
    const url = new URL(relative, import.meta.url), source = await readFile(url, 'utf8')
    const { descriptor, errors } = parse(source, { filename: url.pathname })
    assert.deepEqual(errors, [], relative)
    const script = compileScript(descriptor, { id: relative })
    const template = compileTemplate({ source: descriptor.template.content, filename: url.pathname, id: relative, compilerOptions: { bindingMetadata: script.bindings } })
    assert.deepEqual(template.errors, [], relative)
  }
})

test('实际合成测试组件关窗后不采用迟到结果', async () => {
  const { state } = await mountPanel('../components/risk/AITestPanel.vue', { saved: savedConfig(), saving: false })
  globalThis.fetch = (url, options) => nativeFetch(url, { ...options, signal: undefined })
  responseGate = deferred()
  const pending = state.run(); await received.promise
  mounts[0].unmount(); mounts = []; responseGate.resolve(); await pending
  assert.equal(state.result.value, null)
})
