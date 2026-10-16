import { createServer } from 'node:http'
import { once } from 'node:events'
import { before, after, beforeEach, afterEach, test } from 'node:test'
import { readFile } from 'node:fs/promises'
import { extname } from 'node:path'
import { parse, compileScript } from 'vue/compiler-sfc'
import assert from 'node:assert/strict'
import { createRenderer, h } from 'vue'
import { message, Modal } from 'ant-design-vue'
import { createPinia } from 'pinia'
import { request, requestWithMeta, getToken, setToken, ApiError } from './index.js'
import { getSessionEpoch, resetRequestSession, subscribeSessionReset } from './session.js'
import { createLatestRequest, runLatest } from './latestRequest.js'
import { readSettings, saveSettingsPart } from './settings.js'
import { clearAuditLogs } from './audit.js'
import { buildRiskConfigPatch, testRiskAI, presentAIDecision, readRiskConfig, writeRiskConfig } from './riskControl.js'
import { useSettingsContract } from '../composables/useSettingsContract.js'
import { useAccountsQuery } from '../composables/useAccountsQuery.js'
import { useRiskConfig } from '../composables/useRiskConfig.js'
import { setChannelPlatform } from '../composables/channelPlatforms.js'
import { channelFormToBody, validateChannelForm } from '../composables/channelFormContract.js'
import { enqueueUsage, writeUsageCache, readUsageCache, trackUsageInflight, readUsageInflight } from '../components/account/accountsApi.js'

// 仅合成测试数据。本机真实 HTTP + fetch，不读环境凭据、不访问模型服务和业务数据库。
const storage = new Map()
globalThis.localStorage = { getItem: (key) => storage.get(key) ?? null, setItem: (key, value) => storage.set(key, String(value)), removeItem: (key) => storage.delete(key) }
const eventTarget = new EventTarget()
globalThis.window = { location: { origin: '' }, dispatchEvent: (event) => eventTarget.dispatchEvent(event) }
globalThis.document = { hidden: false }
const nativeFetch = globalThis.fetch
const notifications = []
for (const name of ['success', 'error', 'warning', 'info']) message[name] = (text) => notifications.push({ name, text })
const deferred = () => { let resolve; const promise = new Promise((r) => { resolve = r }); return { promise, resolve } }
const received = new Map(), waiting = new Map(), gates = new Map()
const requests = []
let server, settings, risk, validTickets, ticketNumber, mounts, fixtureGroups, groupsFailure, hashDeleteResult, hashClearResult
const confirmations = []
const originalConfirm = Modal.confirm
Modal.confirm = (options) => { confirmations.push(options); return { destroy() {} } }
const responsePauses = new Map(), completed = new Map()
function pauseResponse(path, method, key) { responsePauses.set(`${method} ${path}`, key); completed.set(key, deferred()) }
async function reply(path, method, record, res, data, headers) {
  const key = responsePauses.get(`${method} ${path}`)
  if (!key) return ok(res, data, headers)
  responsePauses.delete(`${method} ${path}`)
  const snapshot = clone(data), pending = delay(key)
  observe(key, record)
  await pending
  ok(res, snapshot, headers)
  completed.get(key)?.resolve()
}
const settledResponse = (key) => completed.get(key).promise
const clone = (value) => JSON.parse(JSON.stringify(value))
function seen(key) {
  if (received.has(key)) return Promise.resolve(received.get(key))
  if (!waiting.has(key)) waiting.set(key, deferred())
  return waiting.get(key).promise
}
function observe(key, value) { received.set(key, value); waiting.get(key)?.resolve(value) }
function delay(key) { const gate = deferred(); gates.set(key, gate); return gate.promise }
const release = (key) => gates.get(key)?.resolve()
const ok = (res, data, headers = {}) => { res.writeHead(200, { 'Content-Type': 'application/json', ...headers }); res.end(JSON.stringify({ code: 0, data })) }
const fail = (res, status, reason, details = {}) => { res.writeHead(status, { 'Content-Type': 'application/json', 'X-Request-ID': 'synthetic-request' }); res.end(JSON.stringify({ code: status, reason, details, message: reason })) }
const defaultSettings = () => ({ version: '0', site_name: '合成站点', site_logo: '', site_subtitle: '', api_base_url: '', contact_info: '', doc_url: '',
  backend_mode_enabled: true, hide_ccs_import_button: false, table_default_page_size: 20, table_page_size_options: [10,20,50],
  totp_enabled: true, turnstile_enabled: false, turnstile_site_key: '', turnstile_secret_key_configured: false, api_key_acl_trust_forwarded_ip: false, forwarded_client_ip_headers: [],
  channel_monitor_enabled: true, channel_monitor_mode: 'v1', channel_monitor_default_interval_seconds: 60, channel_monitor_hide_throughput: true,
  risk_control_enabled: true, cyber_session_block_enabled: false, cyber_session_block_ttl_seconds: 3600, cyber_policy_user_allowlist: '',
  ...Object.fromEntries(['gateway_custom_error_message','fallback_model_anthropic','fallback_model_openai','fallback_model_gemini','fallback_model_antigravity','identity_patch_prompt','min_claude_code_version','max_claude_code_version','antigravity_user_agent_version','openai_codex_user_agent','openai_codex_client_version','claude_oauth_system_prompt','claude_oauth_system_prompt_blocks'].map(k=>[k,''])),
  ...Object.fromEntries(['enable_model_fallback','enable_identity_patch','openai_codex_version_auto_sync_enabled','allow_ungrouped_key_scheduling','enable_fingerprint_unification','enable_metadata_passthrough','enable_cch_signing','enable_anthropic_cache_ttl_1h_injection','rewrite_message_cache_control','openai_allow_claude_code_codex_plugin','enable_client_dateline_normalization','grok_cross_client_model_map_enabled','upstream_billing_auto_probe_enabled','enable_claude_oauth_system_prompt_injection'].map(k=>[k,false])),
  grok_default_text_model: 'grok-4.5', grok_scheduling_threshold_percent: 100, upstream_billing_probe_interval_minutes: 30, upstream_billing_probe_batch_size: 20, upstream_billing_probe_concurrency: 4,
  openai_fast_policy_settings: { version: 1, priority_detection_by_request_enabled: true, priority_detection_by_response_enabled: true, rules: [] },
  count: 10, ids: [1], password_configured: true })
const defaultRisk = () => ({ enabled: false, mode: 'pre_block', keyword_blocking_mode: 'keyword_only', pre_hash_check_enabled: false,
  blocked_keywords: [], keyword_rule_packs: [], model_filter: { type: 'all', models: [] }, group_ids: [],
  ai: { adapter: 'deepseek_chat', model: 'deepseek-flash', model_revision: 'unverified', base_url: 'https://api.deepseek.com',
    external_data_consent: false, penalties_enabled: false, image_review_enabled: true, api_key_configured: true, api_key_masked: '****synthetic',
    policy_version: 'synthetic-policy-v1', policy: '合成审查策略', config_fingerprint: 'synthetic-fingerprint',
    capabilities: [{ protocol: 'chat_completions', supported: true, scope: 'text_plus_inline_images' }] } })
const renderer = createRenderer({
  createElement: () => ({ children: [] }), createText: () => ({}), createComment: () => ({}), setText() {}, setElementText() {},
  parentNode: () => null, nextSibling: () => null, insert() {}, remove() {}, patchProp() {},
})
function mount(factory) {
  let contract
  const app = renderer.createApp({ setup() { contract = factory(); return () => h('div') } })
  app.use(createPinia())
  app.mount({ children: [] }); mounts.push(app)
  return { contract, unmount: () => { app.unmount(); mounts = mounts.filter((item) => item !== app) } }
}

// 编译实际 SFC（单文件组件）的 setup 并在 Vue 宿主中挂载，验证保存逻辑而不是源码关键词。
// 仅将子 UI 组件替身化；业务模块、公共请求和本机 HTTP 都执行真实代码，不做浏览器/布局验收。
const setupCache = new Map()
async function loadSFCSetup(relative) {
  const url = new URL(relative, import.meta.url)
  if (setupCache.has(url.href)) return setupCache.get(url.href)
  const source = await readFile(url, 'utf8'), { descriptor, errors } = parse(source, { filename: url.pathname })
  assert.equal(errors.length, 0)
  const compiled = compileScript(descriptor, { id: url.href }).content
  const imports = [...compiled.matchAll(/^import[\s\S]*?from ['"]([^'"]+)['"];?\s*$/gm)]
  const statements = [], modules = []
  for (const [statement, specifier] of imports) {
    let module
    if (specifier.endsWith('.vue') || specifier.endsWith('/core/apps')) module = { default: {}, openApp() {} }
    else {
      let target = specifier
      if (specifier.startsWith('.')) {
        const resolved = new URL(specifier, url)
        if (!extname(resolved.pathname)) resolved.pathname += specifier.endsWith('/api') ? '/index.js' : '.js'
        target = resolved.href
      }
      module = await import(target)
    }
    const clause = statement.slice('import'.length, statement.lastIndexOf('from')).trim(), index = modules.push(module) - 1
    if (clause.startsWith('{')) statements.push(`const ${clause.replace(/\bas\b/g, ':')} = modules[${index}]`)
    else statements.push(`const ${clause} = modules[${index}].default`)
  }
  const body = compiled.replace(/^import[\s\S]*?from ['"]([^'"]+)['"];?\s*$/gm, '').replace('export default', 'return')
  const component = new Function('modules', statements.join('\n') + '\n' + body)(modules)
  const setup = (props = {}) => component.setup(props, { expose() {}, emit() {} })
  setupCache.set(url.href, setup)
  return setup
}
const eventually = (predicate, label = '异步状态完成') => new Promise((resolve, reject) => {
  const deadline = Date.now() + 5000
  const check = () => predicate() ? resolve() : Date.now() > deadline ? reject(new Error(label)) : setImmediate(check)
  check()
})

before(async () => {
  server = createServer(async (req, res) => {
    let raw = ''; for await (const chunk of req) raw += chunk
    const url = new URL(req.url, 'http://127.0.0.1')
    const body = raw ? JSON.parse(raw) : undefined
    const record = { path: url.pathname, query: Object.fromEntries(url.searchParams), method: req.method, headers: req.headers, body }
    requests.push(record)
    const path = url.pathname.replace('/api/v1', '')
    if (path === '/fixture/slow' || path === '/fixture/old-401') {
      const key = url.searchParams.get('key'); const pending = delay(key); observe(key, record); await pending
      if (path.endsWith('401')) return fail(res, 401, 'OLD_SESSION')
      return ok(res, { key })
    }
    if (path === '/fixture/slow-body') {
      const key = url.searchParams.get('key'), pending = delay(key)
      res.writeHead(200, { 'Content-Type': 'application/json' }); res.write('{"code":0,"data":'); observe(key, record)
      await pending; res.end('{"late":true}}'); return
    }
    if (path === '/fixture/http-error') return fail(res, 409, 'SETTINGS_VERSION_CONFLICT', { current_version: 'other' })
    if (path === '/fixture/business-error') return ok(res, null) && undefined
    if (path === '/fixture/business') { res.writeHead(200, { 'Content-Type': 'application/json' }); res.end(JSON.stringify({ code: 7, reason: 'POLICY_REFUSED', details: { rule: 4 }, message: '合成拒绝' })); return }
    if (path === '/fixture/raw-401') { res.writeHead(401, { 'Content-Type': 'application/json' }); res.end('{"error":"synthetic upstream"}'); return }
    if (path === '/fixture/401') return fail(res, 401, 'UNAUTHORIZED')
    if (path === '/fixture/etag') {
      if (req.headers['if-none-match'] === '"fixture-etag"') { res.writeHead(304, { ETag: '"fixture-etag"' }); res.end(); return }
      return ok(res, { items: [1] }, { ETag: '"fixture-etag"' })
    }
    if (path === '/admin/gateway-lite/config') return ok(res, { region: 'default', gateway_code: '', redis_prefix: '', admin_login_path: '/synthetic', login_title: '', login_subtitle: '', control_plane_url: '', control_plane_token_configured: true, admin_sync_key_configured: true, legacy_snapshot_scheduler_authority: false,
      control_plane_timeout_ms: 1000, runtime_health_interval_seconds: 15, runtime_active_window_seconds: 300, config_sync_interval_seconds: 30, cache_invalidation_interval_seconds: 5, usage_queue_pending_alert_threshold: 1000, usage_queue_dead_alert_threshold: 1, allow_live: false })
    if (path === '/admin/settings') {
      if (req.method === 'GET') return reply(path, req.method, record, res, settings)
      if (!body.version) return fail(res, 428, 'SETTINGS_VERSION_REQUIRED')
      if (body.version !== settings.version) return fail(res, 409, 'SETTINGS_VERSION_CONFLICT', { current_version: settings.version })
      const { version, password, turnstile_secret_key, ...part } = body
      settings = { ...settings, ...part, version: String(Number(version) + 1), ...(password ? { password_configured: true } : {}), ...(turnstile_secret_key ? { turnstile_secret_key_configured: true } : {}) }
      if (settings.site_name) settings.site_name = settings.site_name.trim()
      return reply(path, req.method, record, res, settings)
    }
    if (path === '/user/totp/step-up') {
      if (body?.code !== '123456') return fail(res, 400, 'INVALID_TOTP')
      const token = `synthetic-ticket-${++ticketNumber}`; validTickets.add(token)
      return ok(res, { verified: true, token, action_scope: body.action_scope })
    }
    if (path === '/admin/audit-logs/clear') {
      const token = req.headers['x-step-up-token']
      if (!token) return fail(res, 403, 'STEP_UP_REQUIRED')
      if (!validTickets.delete(token)) return fail(res, 403, 'STEP_UP_INVALID')
      return ok(res, { deleted: 12 })
    }
    if (path === '/admin/accounts') {
      const key = url.searchParams.get('search')
      if (key?.startsWith('delayed-')) { const pending = delay(key); observe(key, record); await pending }
      observe('accounts', record)
      return ok(res, { items: [{ id: 1, name: key || '合成账号' }], total: 1 }, { ETag: `"${key || 'all'}"` })
    }
    if (path === '/admin/groups/all') {
      if (groupsFailure) return fail(res, 503, 'SYNTHETIC_GROUPS_UNAVAILABLE')
      return reply(path, req.method, record, res, fixtureGroups)
    }
    if (path === '/admin/channels') {
      return ok(res, req.method === 'GET' ? { items: [], total: 0 } : { id: 1, ...body })
    }
    if (path === '/admin/audit-logs') {
      const cursor = url.searchParams.get('before_id')
      return ok(res, { items: [{ id: cursor ? Number(cursor) : 100 }], next_cursor: cursor === '80' ? null : cursor === '90' ? 80 : 90 })
    }
    if (path === '/admin/risk-control/config') {
      if (req.method === 'GET') return reply(path, req.method, record, res, risk)
      const { ai, ...part } = body
      risk = { ...risk, ...part, ...(ai ? { ai: { ...risk.ai, ...ai } } : {}) }
      if (risk.keyword_blocking_mode === 'ai_only') risk.sample_rate = 100
      risk.pre_hash_check_enabled = false
      if (ai?.clear_api_key) risk.ai.api_key_configured = false
      delete risk.ai.api_key; delete risk.ai.clear_api_key
      return reply(path, req.method, record, res, risk)
    }
    if (path === '/admin/risk-control/hashes') return ok(res, hashDeleteResult)
    if (path === '/admin/risk-control/hashes/all') return ok(res, hashClearResult)
    if (path === '/admin/risk-control/logs') return ok(res, { items: [], total: 0, page: 1, page_size: 20 })
    if (/^\/admin\/risk-control\/logs\/\d+$/.test(path)) return ok(res, { id: Number(path.split('/').at(-1)), content_fingerprint: 'a'.repeat(64), full_input_text: '合成完整提取文本' })
    if (path === '/admin/risk-control/status') return ok(res, { enabled: risk.enabled, risk_control_enabled: risk.enabled, mode: risk.mode, flagged_hash_count: 0 })
    if (path === '/admin/risk-control/keyword-catalog/status') return ok(res, { catalog_version: 'synthetic', pack_count: 0, keyword_count: 0 })
    if (path === '/admin/risk-control/ai/metrics') return ok(res, { configured: risk.ai.api_key_configured, store_ready: false, model: risk.ai.model, external_data_consent: risk.ai.external_data_consent })
    if (path === '/admin/risk-control/ai/test') {
      assert.deepEqual(Object.keys(body), ['case_id'])
      const verdict = ['block', 'long_tail'].includes(body.case_id) ? 'block' : 'allow'
      const uncovered_fields = body.case_id === 'unsupported_media' ? ['input[0].content[1]:image_url'] : body.case_id === 'uncovered_field' ? ['root:vendor_extension'] : []
      return ok(res, { policy_version: risk.ai.policy_version, verdict, categories: verdict === 'block' ? ['violence'] : [], reason: '合成整体判定',
        phase: 'completed', provider_called: true, provider_http_status: 200,
        coverage: { complete: true, fragment_count: 1, image_count: 0, text_runes: 12, reviewed_count: 1, uncovered_fields }, penalty_state: 'none' })
    }
    return ok(res, record)
  })
  server.listen(0, '127.0.0.1'); await once(server, 'listening')
  window.location.origin = `http://127.0.0.1:${server.address().port}`
})
beforeEach(() => {
  globalThis.fetch = nativeFetch; resetRequestSession('test-case'); storage.clear(); setToken('synthetic-admin')
  settings = defaultSettings(); risk = defaultRisk(); validTickets = new Set(); ticketNumber = 0
  fixtureGroups = []; groupsFailure = false; hashDeleteResult = { deleted: true }; hashClearResult = { deleted: 3 }
  received.clear(); waiting.clear(); gates.clear(); responsePauses.clear(); completed.clear()
  requests.length = 0; notifications.length = 0; confirmations.length = 0; mounts = []; document.hidden = false
})
afterEach(() => { for (const app of [...mounts]) app.unmount(); mounts = []; for (const gate of gates.values()) gate.resolve() })
after(async () => { globalThis.fetch = nativeFetch; Modal.confirm = originalConfirm; for (const gate of gates.values()) gate.resolve(); server.closeAllConnections(); await new Promise((r) => server.close(r)) })

// 串行用例可共享会话替身；CLI 文件并行上限为 2。
test('真实 fetch 合并自定义头、当前身份及显式 false/0/[] 查询', async () => {
  const value = await request('/fixture/echo', { method: 'POST', headers: { 'X-Step-Up-Token': 'synthetic-only', Authorization: 'ignored' }, body: { enabled: false }, query: { zero: 0, no: false, omitted: undefined } })
  assert.equal(value.headers['x-step-up-token'], 'synthetic-only'); assert.equal(value.headers.authorization, 'Bearer synthetic-admin')
  assert.equal(value.headers['content-type'], 'application/json'); assert.deepEqual(value.query, { zero: '0', no: 'false' })
})
test('HTTP 与业务错误完整保留原因、字段及请求编号', async () => {
  await assert.rejects(request('/fixture/http-error'), (e) => e instanceof ApiError && e.status === 409 && e.reason === 'SETTINGS_VERSION_CONFLICT' && e.details.current_version === 'other' && e.requestId === 'synthetic-request')
  await assert.rejects(request('/fixture/business'), (e) => e.status === 200 && e.code === 7 && e.reason === 'POLICY_REFUSED' && e.details.rule === 4)
})
test('AbortSignal 真正取消等待中的 HTTP 请求', async () => {
  const controller = new AbortController(), result = request('/fixture/slow', { signal: controller.signal, query: { key: 'abort' } })
  const rejected = assert.rejects(result, { name: 'AbortError' }); await seen('abort'); controller.abort(); release('abort'); await rejected
})
test('会话替换时晚到的旧 401 不能注销新账号（网络忽略取消）', async () => {
  globalThis.fetch = (url, options) => nativeFetch(url, { ...options, signal: undefined })
  const old = request('/fixture/old-401', { query: { key: 'old-401' } }), rejected = assert.rejects(old, { name: 'AbortError' })
  await seen('old-401'); setToken('synthetic-new-admin'); release('old-401'); await rejected; assert.equal(getToken(), 'synthetic-new-admin')
})
test('收到 headers 后的慢 body 也按会话代际隔离', async () => {
  globalThis.fetch = (url, options) => nativeFetch(url, { ...options, signal: undefined })
  const old = request('/fixture/slow-body', { query: { key: 'old-body' } }), rejected = assert.rejects(old, { name: 'AbortError' })
  await seen('old-body'); setToken('synthetic-next'); release('old-body'); await rejected
})
test('当前网关 401 清身份；上游透传 401 保留身份', async () => {
  await assert.rejects(request('/fixture/raw-401'), (e) => e.status === 401); assert.equal(getToken(), 'synthetic-admin')
  await assert.rejects(request('/fixture/401'), (e) => e.status === 401); assert.equal(getToken(), '')
})
test('storage-change 仅本页代际清理，不修改共享凭据', () => {
  localStorage.setItem('w12-user', '{"id":"synthetic-user"}')
  let called = 0; const stop = subscribeSessionReset(() => called++)
  const before = getSessionEpoch(); resetRequestSession('storage-change'); stop()
  assert.equal(getSessionEpoch(), before + 1); assert.equal(called, 1)
  assert.equal(getToken(), 'synthetic-admin'); assert.equal(localStorage.getItem('w12-user'), '{"id":"synthetic-user"}')
})
test('ETag / 304 通过公共请求保留元信息', async () => {
  const first = await requestWithMeta('/fixture/etag', { method: 'GET' })
  const next = await requestWithMeta('/fixture/etag', { method: 'GET', allowNotModified: true, headers: { 'If-None-Match': first.headers.get('ETag') } })
  assert.equal(next.status, 304); assert.equal(next.notModified, true); assert.equal(next.data, null)
})
test('设置 sparse 更新保留遗漏，写入 false/0/[]/空文本，空秘密省略，修订冲突可复核', async () => {
  const start = await readSettings()
  const updated = await saveSettingsPart(start.version, { site_name: '', totp_enabled: false, count: 0, ids: [], password: '' })
  assert.equal(updated.version, '1'); assert.equal(updated.site_name, ''); assert.equal(updated.totp_enabled, false)
  assert.equal(updated.count, 0); assert.deepEqual(updated.ids, []); assert.equal(updated.password_configured, true)
  await assert.rejects(saveSettingsPart(start.version, { site_name: '旧草稿' }), (e) => e.status === 409 && e.reason === 'SETTINGS_VERSION_CONFLICT')
  await assert.rejects(saveSettingsPart('', {}), (e) => e.reason === 'SETTINGS_VERSION_REQUIRED')
})
test('设置组合函数真实冲突不替换草稿或修订，关窗中止实例', async () => {
  const { contract, unmount } = mount(() => useSettingsContract())
  const draft = await contract.read(); await saveSettingsPart(draft.version, { site_name: '另一管理员' }); draft.site_name = '当前草稿'
  await assert.rejects(contract.save({ site_name: draft.site_name }), (e) => e.status === 409)
  assert.equal(draft.site_name, '当前草稿'); assert.equal(contract.version.value, '0'); assert.match(contract.conflict.value, /草稿已保留/)
  unmount(); assert.equal(contract.lifetime.signal.aborted, true)
})
test('设置读取按最新顺序采用修订，保存取消旧读取不回退版本', async () => {
  const { contract: c } = mount(() => useSettingsContract())
  await c.read()
  globalThis.fetch = (url, options) => nativeFetch(url, { ...options, signal: undefined })
  pauseResponse('/admin/settings', 'GET', 'settings-old-read')
  const old = c.read(), rejected = assert.rejects(old, { name: 'AbortError' })
  await seen('settings-old-read')
  await saveSettingsPart('0', { site_name: '另一管理员修改' })
  assert.equal((await c.read()).version, '1')
  release('settings-old-read'); await rejected; assert.equal(c.version.value, '1')
  pauseResponse('/admin/settings', 'GET', 'settings-save-vs-read')
  const pending = c.read(), cancelled = assert.rejects(pending, { name: 'AbortError' })
  await seen('settings-save-vs-read')
  assert.equal((await c.save({ site_name: '新写入' })).version, '2')
  release('settings-save-vs-read'); await cancelled; assert.equal(c.version.value, '2')
})
test('四设置标签执行真实 setup 和 PATCH，迟到回显保留保存期间编辑与新秘密', async () => {
  for (const [name, fieldRef, field, saveName] of [
    ['GeneralTab', 's', 'site_name', 'save'], ['FeaturesTab', 'fs', 'cyber_policy_user_allowlist', 'saveFs'],
    ['SecurityTab', 'sec', 'turnstile_site_key', 'saveSec'], ['GatewayTab', 's', 'gateway_custom_error_message', 'saveErrMsg'],
  ]) {
    const setup = await loadSFCSetup(`../components/settings/${name}.vue`)
    const { contract: c, unmount } = mount(setup)
    await eventually(() => c[fieldRef].value !== null && !c.loading.value, `${name} 加载未完成`)
    c[fieldRef].value[field] = '本次提交'
    if (name === 'SecurityTab') c.sec.value.turnstile_secret_key = 'synthetic-old-secret'
    if (name === 'GatewayTab') c.s.value.identity_patch_prompt = '其他卡草稿'
    const key = `draft-${name}`; pauseResponse('/admin/settings', 'PATCH', key)
    const saving = c[saveName](); await seen(key)
    c[fieldRef].value[field] = '保存期间新编辑'
    if (name === 'SecurityTab') c.sec.value.turnstile_secret_key = 'synthetic-new-secret'
    release(key); await saving
    assert.equal(c[fieldRef].value[field], '保存期间新编辑')
    assert.equal(c.contract.version.value, settings.version)
    if (name === 'SecurityTab') {
      assert.equal(c.sec.value.turnstile_secret_key, 'synthetic-new-secret')
      assert.equal(c.sec.value.turnstile_secret_key_configured, true)
    }
    if (name === 'GatewayTab') assert.equal(c.s.value.identity_patch_prompt, '其他卡草稿')
    const written = requests.findLast((r) => r.method === 'PATCH' && r.path.endsWith('/settings'))
    assert.equal(written.body[field], '本次提交'); assert.equal(written.body.version, String(Number(settings.version) - 1))
    unmount(); assert.equal(c[fieldRef].value, null)
  }
})

test('审计正确验证码只换一次票据，并真实发送清理头；重复拒绝', async () => {
  assert.deepEqual(await clearAuditLogs('123456'), { deleted: 12 })
  const validation = requests.find((r) => r.path.endsWith('/step-up')), clear = requests.find((r) => r.path.endsWith('/clear'))
  assert.equal(validation.body.action_scope, 'audit_logs.clear'); assert.equal(clear.headers['x-step-up-token'], 'synthetic-ticket-1')
  await assert.rejects(request('/admin/audit-logs/clear', { headers: { 'X-Step-Up-Token': clear.headers['x-step-up-token'] } }), (e) => e.stepUpBlocked === 'STEP_UP_INVALID')
})
test('审计无凭证/错误验证码不能伪装清理成功', async () => {
  await assert.rejects(clearAuditLogs(''), (e) => e.stepUpRequired === true)
  await assert.rejects(clearAuditLogs('000000'), (e) => e.reason === 'INVALID_TOTP')
  assert.equal(requests.filter((r) => r.path.endsWith('/clear')).length, 1)
})
test('最新查询和旧 finally 不覆盖新筛选（真实迟到 HTTP）', async () => {
  const lane = createLatestRequest(), values = []; let finished = 0
  const old = runLatest(lane, () => request('/fixture/slow', { query: { key: 'latest-old' } }), { apply: (v) => values.push(v.key), finish: () => finished++ })
  await seen('latest-old')
  const next = runLatest(lane, () => request('/fixture/slow', { query: { key: 'latest-new' } }), { apply: (v) => values.push(v.key), finish: () => finished++ })
  await seen('latest-new'); release('latest-old'); await old; assert.equal(finished, 0)
  release('latest-new'); await next; assert.deepEqual(values, ['latest-new']); assert.equal(finished, 1); lane.dispose()
})
test('账号普通列表和 ETag 刷新共用最新查询，卸载不接受旧数据', async () => {
  const { contract: q, unmount } = mount(() => useAccountsQuery({ value: false }))
  await seen('accounts'); q.fSearch.value = 'delayed-old'; const old = q.load(); await seen('delayed-old')
  q.fSearch.value = 'delayed-new'; const next = q.load(); await seen('delayed-new')
  release('delayed-new'); await next; release('delayed-old'); await old
  assert.equal(q.rows.value[0].name, 'delayed-new'); assert.equal(q.total.value, 1); assert.equal(q.loading.value, false)
  unmount(); assert.deepEqual(q.rows.value, [])
})
test('首次平台勾选启用，禁用保留定价草稿；渠道体转换维持金额与配置', () => {
  const platforms = [], section = setChannelPlatform(platforms, 'openai', true)
  assert.equal(section.enabled, true); section.group_ids = [2]; section.model_mapping = { 'demo*': 'target' }
  section.model_pricing = [{ models: ['demo*'], billing_mode: 'token', input_price: 1, output_price: 2, intervals: [] }]
  setChannelPlatform(platforms, 'openai', false); assert.equal(section.model_pricing.length, 1); setChannelPlatform(platforms, 'openai', true)
  const form = { name: '合成渠道', description: '', status: 'active', platforms, stats_rules: [], billing_model_source: 'requested' }
  assert.equal(validateChannelForm(form), null)
  const body = channelFormToBody(form, { id: 5, features_config: { retained: true } })
  assert.equal(body.id, 5); assert.equal(body.model_pricing[0].input_price, 0.000001); assert.equal(body.features_config.retained, true)
})
test('渠道编辑分组读取失败不得丢绑定保存，重试回填并阻止旧弹窗回填', async () => {
  fixtureGroups = [{ id: 2, name: '合成分组', platform: 'openai' }]
  const setup = await loadSFCSetup('../apps/Channels.vue'), { contract: c } = mount(setup)
  await eventually(() => c.groupsReady.value)
  const channel = { id: 7, name: '合成渠道', group_ids: [2], model_mapping: {}, model_pricing: [], account_stats_pricing_rules: [] }
  groupsFailure = true; c.openEdit(channel)
  await eventually(() => !!c.editLoadError.value && !c.editLoading.value)
  const writes = () => requests.filter((r) => r.method === 'PUT' && r.path.startsWith('/api/v1/admin/channels/')).length
  const count = writes(); await c.save(); assert.equal(writes(), count)
  groupsFailure = false; c.openEdit(channel)
  await eventually(() => c.groupsReady.value && !c.editLoading.value)
  assert.deepEqual(c.form.value.platforms[0].group_ids, [2])
  pauseResponse('/admin/groups/all', 'GET', 'channels-old-edit')
  c.openEdit(channel); await seen('channels-old-edit')
  c.openCreate(); c.form.value.name = '新建草稿'; c.togglePlatform('openai', true)
  await eventually(() => c.groupsReady.value); release('channels-old-edit'); await settledResponse('channels-old-edit')
  assert.equal(c.form.value.name, '新建草稿'); assert.equal(c.form.value.platforms[0].enabled, true)
  assert.deepEqual(c.form.value.platforms[0].group_ids, [])
})
test('审计游标前进和回退执行实际组件，不重复返回当前页', async () => {
  const setup = await loadSFCSetup('../apps/AuditLogs.vue'), { contract: c } = mount(setup)
  await eventually(() => !c.loading.value && c.rows.value.length === 1)
  assert.equal(c.rows.value[0].id, 100)
  await c.load('more'); assert.equal(c.rows.value[0].id, 90)
  await c.load('more'); assert.equal(c.rows.value[0].id, 80)
  await c.load('prev'); assert.equal(c.rows.value[0].id, 90)
  await c.load('prev'); assert.equal(c.rows.value[0].id, 100)
  assert.equal(c.cursorStack.value.length, 0)
})

test('历史指纹单删执行布尔合同，批量清空执行数量合同，缺失结果不假成功', async () => {
  const fingerprint = 'a'.repeat(64)
  const logsSetup = await loadSFCSetup('../components/risk/RiskLogsPanel.vue')
  const { contract: logs } = mount(logsSetup)
  await eventually(() => !logs.logsLoading.value)
  await logs.openDetail({ id: 1 })
  assert.equal(logs.detailRow.value.content_fingerprint, fingerprint)
  assert.equal(requests.at(-1).path, '/api/v1/admin/risk-control/logs/1')
  for (const [response, expected] of [[{ deleted: true }, 'success'], [{ deleted: false }, 'info'], [{ deleted: 1 }, 'error'], [{}, 'error']]) {
    hashDeleteResult = response; notifications.length = 0
    logs.deleteDetailFingerprint(); await confirmations.at(-1).onOk()
    assert.equal(notifications.length, 1); assert.equal(notifications[0].name, expected)
    const written = requests.findLast((r) => r.path === '/api/v1/admin/risk-control/hashes')
    assert.equal(written.method, 'DELETE'); assert.deepEqual(written.body, { input_hash: fingerprint })
  }
  const fingerprintsSetup = await loadSFCSetup('../components/risk/RiskFingerprints.vue')
  const { contract: marks } = mount(fingerprintsSetup)
  await eventually(() => !marks.loading.value && marks.count.value === 0)
  for (const [response, expected] of [[{ deleted: true }, 'success'], [{ deleted: false }, 'info'], [{ deleted: 1 }, 'error'], [{}, 'error']]) {
    hashDeleteResult = response; marks.input.value = fingerprint; notifications.length = 0
    await marks.remove(false)
    assert.equal(notifications.length, 1); assert.equal(notifications[0].name, expected)
    assert.equal(marks.input.value, expected === 'error' ? fingerprint : '')
  }
  for (const [response, expected] of [[{ deleted: 3 }, 'success'], [{ deleted: 0 }, 'success'], [{ deleted: true }, 'error'], [{ deleted: -1 }, 'error'], [{}, 'error']]) {
    hashClearResult = response; notifications.length = 0
    await marks.remove(true)
    assert.equal(notifications.length, 1); assert.equal(notifications[0].name, expected)
    assert.equal(requests.at(-1).method, expected === 'success' ? 'GET' : 'DELETE')
  }
})

test('用量队列、缓存与 inflight 跨会话隔离，旧续体不写新缓存', async () => {
  const epoch = getSessionEpoch(); writeUsageCache('id', { value: '旧' }, epoch)
  const first = deferred(), work = enqueueUsage(() => first.promise), rejected = assert.rejects(work, { name: 'AbortError' })
  const oldInflight = deferred(); trackUsageInflight('id', oldInflight.promise)
  resetRequestSession('switched'); assert.equal(readUsageCache('id'), null); assert.equal(writeUsageCache('id', { value: '迟到' }, epoch), false)
  const nextInflight = deferred(); trackUsageInflight('id', nextInflight.promise); oldInflight.resolve('旧'); await oldInflight.promise; await Promise.resolve()
  assert.equal(readUsageInflight('id'), nextInflight.promise)
  first.resolve('旧'); await rejected; nextInflight.resolve('新')
})
test('AI 差异白名单排除旧预算/价格和只读字段，图片关闭与清密钥显式保存', async () => {
  const saved = await readRiskConfig(), draft = clone(saved)
  assert.equal(saved.ai.daily_budget_usd_micros, undefined)
  assert.equal(saved.ai.input_price_per_million_usd_micros, undefined)
  assert.equal(saved.ai.output_price_per_million_usd_micros, undefined)
  draft.ai.daily_budget_usd_micros = 0; draft.ai.input_price_per_million_usd_micros = 99; draft.ai.output_price_per_million_usd_micros = 99
  draft.ai.api_key_masked = '变更掩码'; draft.ai.api_key = '不能回写的返回秘密'; draft.local_keyword_only = false
  assert.deepEqual(buildRiskConfigPatch(draft, saved), {})
  draft.ai.image_review_enabled = false
  const body = buildRiskConfigPatch(draft, saved, { clearAPIKey: true, apiKey: '不应提交' })
  assert.deepEqual(body, { ai: { image_review_enabled: false, clear_api_key: true } })
  const updated = await writeRiskConfig(body)
  assert.equal(updated.ai.api_key_configured, false); assert.equal(updated.ai.image_review_enabled, false)
  assert.equal(updated.ai.daily_budget_usd_micros, undefined)
})
test('AI 合成测试按新整体判定返回，外链与未知字段只记未覆盖，HTTP 200 不等于通过', async () => {
  assert.equal(presentAIDecision(await testRiskAI('allow')).label, '通过')
  assert.equal(presentAIDecision(await testRiskAI('block')).label, '违规')
  for (const caseId of ['unsupported_media', 'uncovered_field']) {
    const result = await testRiskAI(caseId)
    assert.equal(presentAIDecision(result).label, '通过'); assert.equal(result.provider_called, true)
    assert.equal(result.phase, 'completed'); assert.equal(result.coverage.uncovered_fields.length, 1)
    assert.deepEqual(result.categories, []); assert.equal(result.error_code, undefined)
  }
  assert.equal(presentAIDecision({ verdict: 'error', provider_called: true, phase: 'provider', error_code: 'ai_transport', coverage: { complete: false } }).label, '未判定')
  assert.equal(presentAIDecision({ verdict: 'allow', provider_called: true, coverage: { complete: false } }).label, '未判定')
  assert.equal(presentAIDecision({ allowed: true }).label, '未判定')
  assert.equal(requests.filter((r) => r.path === '/api/v1/admin/risk-control/ai/test').length, 4)
})
test('AI 保存期间新编辑与新密钥保留，已保存基线采用后端规范化值', async () => {
  const { contract: c } = mount(useRiskConfig)
  await c.load()
  c.cfg.value.keyword_blocking_mode = 'ai_only'; c.cfg.value.mode = 'shadow'; c.cfg.value.ai.model = '本次模型'
  c.apiKey.value = 'synthetic-submitted-key'
  pauseResponse('/admin/risk-control/config', 'PUT', 'ai-save-draft')
  const saving = c.save(); await seen('ai-save-draft')
  c.cfg.value.ai.model = '保存期间新模型'; c.cfg.value.blocked_keywords = ['新关键词']; c.apiKey.value = 'synthetic-new-key'
  release('ai-save-draft'); await saving
  assert.equal(c.saved.value.ai.model, '本次模型'); assert.equal(c.cfg.value.ai.model, '保存期间新模型')
  assert.deepEqual(c.cfg.value.blocked_keywords, ['新关键词']); assert.deepEqual(c.saved.value.blocked_keywords, [])
  assert.equal(c.cfg.value.sample_rate, 100); assert.equal(c.apiKey.value, 'synthetic-new-key')
  const patch = buildRiskConfigPatch(c.cfg.value, c.saved.value, { apiKey: c.apiKey.value })
  assert.equal(patch.ai.model, '保存期间新模型'); assert.equal(patch.ai.api_key, 'synthetic-new-key')
  assert.equal(patch.ai.api_key_masked, undefined)
})
test('AI 保存取消旧配置读取，迟到旧配置不能覆盖新基线和草稿', async () => {
  const { contract: c } = mount(useRiskConfig); await c.load()
  globalThis.fetch = (url, options) => nativeFetch(url, { ...options, signal: undefined })
  pauseResponse('/admin/risk-control/config', 'GET', 'ai-old-config')
  const reading = c.load(); await seen('ai-old-config')
  c.cfg.value.ai.model = '最新写入模型'; await c.save()
  release('ai-old-config'); await reading
  assert.equal(c.saved.value.ai.model, '最新写入模型'); assert.equal(c.cfg.value.ai.model, '最新写入模型')
  assert.equal(c.loading.value, false); assert.equal(c.saving.value, false)
})
test('用量并发满载时切换会话拒绝待队列并按真实释放恢复容量', async () => {
  const active = [deferred(), deferred(), deferred()], started = []
  const running = active.map((gate, i) => enqueueUsage(() => { started.push(i); return gate.promise }))
  const oldAssertions = running.map((work) => assert.rejects(work, { name: 'AbortError' }))
  await eventually(() => started.length === 3)
  let waitingRan = false
  const waitingWork = enqueueUsage(() => { waitingRan = true; return '旧待队列' })
  const waitingRejected = assert.rejects(waitingWork, { name: 'AbortError' })
  resetRequestSession('new-account'); await waitingRejected; assert.equal(waitingRan, false)
  let newRan = false
  const fresh = enqueueUsage(() => { newRan = true; return '新会话' })
  assert.equal(newRan, false)
  active[0].resolve('旧'); assert.equal(await fresh, '新会话')
  active[1].resolve('旧'); active[2].resolve('旧'); await Promise.all(oldAssertions)
})
test('AI 配置组合函数按规范化真实回显更新基线，关键词独立保存保留AI草稿', async () => {
  const { contract: c, unmount } = mount(useRiskConfig)
  await c.load(); c.cfg.value.ai.model = '未保存模型'
  await c.saveKeywords({ blocked_keywords: ['synthetic-keyword'] })
  assert.equal(c.cfg.value.ai.model, '未保存模型'); assert.equal(c.saved.value.ai.model, 'deepseek-flash')
  c.cfg.value.keyword_blocking_mode = 'ai_only'; c.cfg.value.mode = 'shadow'; await c.save()
  assert.equal(c.saved.value.sample_rate, 100); assert.equal(c.cfg.value.pre_hash_check_enabled, false)
  assert.equal(c.metrics.value.store_ready, false); assert.equal(c.apiKey.value, '')
  assert.equal(requests.some((r) => r.body?.ai?.api_key_masked), false)
  unmount(); assert.equal(c.cfg.value, null); assert.equal(c.lifetime.signal.aborted, true)
})
