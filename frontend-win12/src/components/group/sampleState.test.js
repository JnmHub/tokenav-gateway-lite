import test, { before, after, beforeEach, afterEach } from 'node:test'
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { once } from 'node:events'
import { readFile } from 'node:fs/promises'
import { extname } from 'node:path'
import { parse, compileScript } from 'vue/compiler-sfc'
import { createRenderer, h, reactive, nextTick, watch, defineComponent } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { message, Modal } from 'ant-design-vue'
import { useGroupEditor } from './useGroupEditor.js'
import { pricingConfigToForm, pricingFormToRequest, validatePricingForm } from './pricing.js'
import { useWindowsStore } from '../../core/stores.js'
import { provideWindowContext, useDiscardGuard } from '../../core/windowContext.js'
import { setToken } from '../../api/index.js'
import { resetRequestSession } from '../../api/session.js'

// 实际组件 setup + 公共请求 + 本机 HTTP。仅 UI 宿主替身化，不冒充浏览器布局验收。
const storage = new Map(), events = new EventTarget()
globalThis.localStorage = { getItem: key => storage.get(key) ?? null, setItem: (key, value) => storage.set(key, String(value)), removeItem: key => storage.delete(key) }
globalThis.window = Object.assign(events, { location: { origin: '' } })
globalThis.document = Object.assign(new EventTarget(), { hidden: false, activeElement: null, body: {}, querySelector: () => null })
globalThis.requestAnimationFrame = fn => setTimeout(fn, 0)
globalThis.cancelAnimationFrame = clearTimeout
const notifications = [], confirmations = []
const originalConfirm = Modal.confirm, originalFetch = globalThis.fetch
for (const key of ['error', 'success', 'warning']) message[key] = text => notifications.push({ key, text })
Modal.confirm = options => { const dialog = { options, destroyed: false, destroy() { this.destroyed = true } }; confirmations.push(dialog); return dialog }
const renderer = createRenderer({ createElement: () => ({}), createText: () => ({}), createComment: () => ({}), setText() {}, setElementText() {}, parentNode: () => null, nextSibling: () => null, insert() {}, remove() {}, patchProp() {} })
let mounts = [], server, writes, readFailure, accountGate, rateGate, previewWins, fixtures
const deferred = () => { let resolve; const promise = new Promise(r => { resolve = r }); return { promise, resolve } }
const until = (check, label = '异步状态未完成') => new Promise((resolve, reject) => {
  const deadline = Date.now() + 3000
  const tick = () => check() ? resolve() : Date.now() >= deadline ? reject(new Error(label)) : setImmediate(tick)
  tick()
})
const appDefinition = { id: 'groups', name: '分组管理', size: { w: 700, h: 500 }, minSize: { w: 350, h: 240 } }
function mount(factory) {
  let state
  const app = renderer.createApp({ setup() { state = factory(); return () => h('div') } })
  const pinia = createPinia(); setActivePinia(pinia); app.use(pinia); app.mount({}); mounts.push(app)
  return { state, unmount() { app.unmount(); mounts = mounts.filter(current => current !== app) } }
}
async function component(relative) {
  const url = new URL(relative, import.meta.url), source = await readFile(url, 'utf8')
  const { descriptor, errors } = parse(source, { filename: url.pathname }); assert.deepEqual(errors, [])
  const code = compileScript(descriptor, { id: url.href }).content
  const modules = [], bindings = []
  for (const [statement, specifier] of code.matchAll(/^import[\s\S]*?from ['"]([^'"]+)['"];?\s*$/gm)) {
    let module
    if (specifier.endsWith('.vue')) module = { default: {} }
    else if (specifier.endsWith('/core/apps')) module = { getApp: () => appDefinition }
    else {
      let target = specifier
      if (specifier.startsWith('.')) { const resolved = new URL(specifier, url); if (!extname(resolved.pathname)) resolved.pathname += '.js'; target = resolved.href }
      module = await import(target)
    }
    const clause = statement.slice(6, statement.lastIndexOf('from')).trim(), index = modules.push(module) - 1
    bindings.push(clause.startsWith('{') ? `const ${clause.replace(/\bas\b/g, ':')} = modules[${index}]` : `const ${clause} = modules[${index}].default`)
  }
  const body = code.replace(/^import[\s\S]*?from ['"]([^'"]+)['"];?\s*$/gm, '').replace('export default', 'return')
  const compiled = new Function('modules', bindings.join('\n') + '\n' + body)(modules)
  return (props, emit = () => {}) => compiled.setup(props, { expose() {}, emit })
}
function openProps(id = 7) { return reactive({ open: false, group: { id, name: '合成分组', rate_multiplier: 1, platform: 'anthropic' } }) }
const group = (id = 7) => ({ id, name: '合成分组', platform: 'anthropic', model_routing_enabled: true, model_routing: { 'claude-*': [11, 12] } })
const ok = (res, data) => { res.writeHead(200, { 'Content-Type': 'application/json' }); res.end(JSON.stringify({ code: 0, data })) }
before(async () => {
  server = createServer(async (req, res) => {
    let raw = ''; for await (const chunk of req) raw += chunk
    const path = new URL(req.url, 'http://127.0.0.1').pathname.replace('/api/v1', ''), body = raw ? JSON.parse(raw) : null
    if (req.method !== 'GET') writes.push({ path, method: req.method, body })
    if (/\/accounts\/\d+$/.test(path)) { if (accountGate) { const gate = accountGate; gate.started = true; await gate.promise } return ok(res, { name: `账号 ${path.split('/').at(-1)}` }) }
    if (path.endsWith('/rate-multipliers') && req.method === 'GET') {
      if (rateGate) { const gate = rateGate; gate.started = true; await gate.promise }
      if (readFailure) { res.writeHead(503, { 'Content-Type': 'application/json' }); res.end(JSON.stringify({ code: 503, message: '合成读取失败' })); return }
      return ok(res, fixtures)
    }
    if (path.endsWith('/pricing')) return ok(res, { enabled: false, model_pricing: [] })
    if (path === '/admin/groups/all') return ok(res, [{ id: 8, name: '兜底候选', status: 'active', platform: 'anthropic', fallback_group_id_on_invalid_request: null }])
    if (path === '/admin/settings/composite-groups') return ok(res, { enabled: true })
    if (path.endsWith('/models-list-candidates')) return ok(res, { models: ['gpt-fixture'] })
    if (path.endsWith('/composite-routes/preview')) return ok(res, { candidate_wins: previewWins })
    if (path.endsWith('/composite-routes') && req.method === 'GET') return ok(res, [])
    return ok(res, {})
  })
  server.listen(0, '127.0.0.1'); await once(server, 'listening'); window.location.origin = `http://127.0.0.1:${server.address().port}`
})
beforeEach(() => {
  resetRequestSession('sample-test'); storage.clear(); setToken('synthetic-only'); writes = []; readFailure = false; accountGate = null; rateGate = null; previewWins = true
  fixtures = [{ user_id: 1, rate_multiplier: 1.25, rpm_override: 20 }]; notifications.length = 0; confirmations.length = 0; document.hidden = false; document.activeElement = null
})
afterEach(() => { accountGate?.resolve(); rateGate?.resolve(); for (const app of mounts) app.unmount(); mounts = [] })
after(async () => { Modal.confirm = originalConfirm; globalThis.fetch = originalFetch; server.closeAllConnections(); await new Promise(resolve => server.close(resolve)) })

test('路由同步保留 ID；迟到名称不覆盖手改、删除、新增或重置 dirty 基线', async () => {
  accountGate = deferred(); const { state: c } = mount(() => useGroupEditor())
  await c.openEdit(group()); assert.deepEqual(c.routingRules.value[0].accounts.map(a => a.id), [11, 12])
  await until(() => accountGate.started && c.pricingReady.value)
  assert.equal(c.isDirty.value, false)
  c.routingRules.value = [{ pattern: '手改-*', accounts: [{ id: 12, name: '#12' }, { id: 99, name: '新选择' }] }]
  accountGate.resolve(); await until(() => c.routingRules.value[0].accounts[0].name === '账号 12')
  assert.equal(c.routingRules.value[0].pattern, '手改-*'); assert.deepEqual(c.routingRules.value[0].accounts.map(a => a.id), [12, 99]); assert.equal(c.isDirty.value, true)
  await c.save(); assert.deepEqual(writes.find(w => w.path === '/admin/groups/7').body.model_routing, { '手改-*': [12, 99] })
})
test('仅补显示名不会产生脏草稿；关闭/换组后的旧账号名不能回填新规则', async () => {
  const { state: c } = mount(() => useGroupEditor())
  await c.openEdit(group()); await until(() => c.routingRules.value[0].accounts[1].name === '账号 12' && c.pricingReady.value)
  assert.equal(c.isDirty.value, false)
  accountGate = deferred(); await c.openEdit(group()); await until(() => accountGate.started)
  c.closeEditor(); await c.openEdit({ ...group(8), model_routing: {} }); accountGate.resolve(); await nextTick()
  assert.deepEqual(c.routingRules.value, []); assert.equal(c.editForm.value.id, 8)
})
test('无效请求兜底明确关闭；已有组发送0清除，新建发送null', async () => {
  const { state: c } = mount(() => useGroupEditor())
  await c.openEdit({ ...group(), fallback_group_id_on_invalid_request: 8 }); await until(() => c.pricingReady.value && c.invalidFallbackOptions.value.length > 1)
  assert.deepEqual(c.invalidFallbackOptions.value[0], { value: null, label: '关闭兜底' }); c.editForm.value.fallback_group_id_on_invalid_request = null
  await c.save(); assert.equal(writes.find(w => w.path === '/admin/groups/7').body.fallback_group_id_on_invalid_request, 0)
  await c.openCreate(); c.editForm.value.name = '新分组'; await c.save()
  assert.equal(writes.find(w => w.path === '/admin/groups').body.fallback_group_id_on_invalid_request, null)
})
for (const [file, field, value, addField] of [['GroupRateMultipliersModal', 'rate_multiplier', 2, 'newRate'], ['GroupRPMOverridesModal', 'rpm_override', 0, 'newRpm']]) {
  test(`${file} 首读失败不能编辑/全量写/清空，重试后保留服务器旧条目`, async () => {
    readFailure = true; const props = openProps(), setup = await component(`./${file}.vue`), { state: c } = mount(() => setup(props))
    props.open = true; await until(() => !!c.loadError.value && !c.loading.value)
    assert.equal(c.ready.value, false); assert.equal(c.canEdit.value, false)
    c.selectedUser.value = { id: 2, email: 'synthetic@example.test' }; c[addField].value = value; c.addLocal(); await c.save()
    if (c.clearAll) await c.clearAll(); else c.clearAllLocal()
    assert.deepEqual(c.localEntries.value, []); assert.deepEqual(writes, [])
    readFailure = false; await c.load(); assert.equal(c.canEdit.value, true); c.addLocal(); assert.equal(c.localEntries.value.length, 2)
    await c.save(); const write = writes.find(w => w.method === 'PUT'); assert.equal(write.body.entries[0][field], fixtures[0][field]); assert.equal(write.body.entries[1][field], value)
    assert.equal(Object.keys(write.body.entries[0]).length, 2)
  })
  test(`${file} 换组与关开让旧读取和旧finally失效`, async () => {
    const props = openProps(), setup = await component(`./${file}.vue`), { state: c } = mount(() => setup(props))
    props.open = true; await until(() => c.ready.value)
    rateGate = deferred(); const old = c.load(); await until(() => rateGate.started)
    props.group = { ...props.group, id: 8 }; await nextTick(); assert.equal(c.ready.value, false)
    rateGate.resolve(); await old; await until(() => c.ready.value)
    props.open = false; await nextTick(); assert.equal(c.ready.value, false); assert.deepEqual(c.localEntries.value, [])
    await c.save(); assert.deepEqual(writes, [])
  })
}
test('聚合备注和自定义优先级进入真实预演/保存，非整数与败选不落库', async () => {
  const props = openProps(), setup = await component('./GroupCompositeRoutesModal.vue'), { state: c } = mount(() => setup(props))
  props.open = true; await until(() => !c.loading.value)
  Object.assign(c.form, { public_model: 'gpt-custom', notes: '路由用途', priority: -37 })
  await c.previewAndSave(); assert.equal(writes[0].body.candidate.priority, -37); assert.equal(writes[0].body.candidate.notes, '路由用途'); assert.equal(writes[1].body.notes, '路由用途')
  Object.assign(c.form, { public_model: 'gpt-custom', priority: 1.5 }); writes.length = 0; await c.previewAndSave(); assert.deepEqual(writes, [])
  c.form.priority = 101; previewWins = false; await c.previewAndSave(); assert.equal(writes.length, 1); assert.ok(writes[0].path.endsWith('/preview'))
})
test('null阶梯在客户价格和账号成本读取/校验/提交中归一，免费价格保持0', () => {
  const config = { enabled: true, model_pricing: [{ models: ['gpt-fixture'], input_price: 0, intervals: null }], apply_pricing_to_account_stats: true,
    account_stats_pricing_rules: [{ name: '成本', account_ids: [1], pricing: [{ models: ['gpt-fixture'], intervals: null }] }] }
  const form = pricingConfigToForm(config, 'openai'); assert.deepEqual(form.model_pricing[0].intervals, []); assert.deepEqual(form.account_stats_pricing_rules[0].pricing[0].intervals, [])
  form.model_pricing[0].intervals = null; form.account_stats_pricing_rules[0].pricing[0].intervals = null
  assert.equal(validatePricingForm(form, 'openai'), null); const body = pricingFormToRequest(form, 'openai', 7)
  assert.equal(body.model_pricing[0].input_price, 0); assert.deepEqual(body.model_pricing[0].intervals, []); assert.deepEqual(body.account_stats_pricing_rules[0].pricing[0].intervals, [])
})
test('W12Dialog重复取消只询问一次，旧确认不能关闭重开/换组/保存中的弹窗', async () => {
  const setup = await component('../design/W12Dialog.vue'), gate = deferred(), props = reactive({ open: true, closeKey: 7, busy: false, beforeClose: () => gate.promise }), emitted = []
  const { state: c } = mount(() => setup(props, (...args) => emitted.push(args)))
  const first = c.cancel(); await c.cancel(); assert.equal(c.pending.value, true)
  props.open = false; props.open = true; gate.resolve(true); await first; assert.deepEqual(emitted, [])
  const second = deferred(); props.beforeClose = () => second.promise; const closing = c.cancel(); props.closeKey = 8; second.resolve(true); await closing; assert.deepEqual(emitted, [])
  const third = deferred(); props.beforeClose = () => third.promise; const saving = c.cancel(); props.busy = true; third.resolve(true); await saving; assert.deepEqual(emitted, [])
  props.busy = false; props.beforeClose = () => true; await c.cancel(); assert.deepEqual(emitted.map(args => args[0]), ['update:open', 'cancel'])
})
test('所属草稿确认去重、保存期间拒绝、卸载销毁并返回false', async () => {
  const busy = { value: false }
  const { state: guard, unmount } = mount(() => useDiscardGuard(() => true, () => busy.value))
  const first = guard(), next = guard(); assert.equal(first, next); assert.equal(confirmations.length, 1)
  busy.value = true; confirmations[0].options.onOk(); assert.equal(await first, false)
  busy.value = false; const second = guard(); confirmations[0].options.onCancel(); assert.equal(confirmations.length, 2)
  unmount(); assert.equal(confirmations[1].destroyed, true); assert.equal(await second, false)
  confirmations[1].options.onOk(); assert.equal(await guard(), false)
})
test('所属W12Dialog隐藏期间不关闭；取消确认/重复弹窗保持所属容器', async () => {
  const setup = await component('../design/W12Dialog.vue'), gate = deferred(), active = { value: true }, props = reactive({ open: true, busy: false, beforeClose: () => gate.promise }), emitted = []
  let c
  const child = defineComponent({ setup() { c = setup(props, (...args) => emitted.push(args)); return () => h('div') } })
  const app = renderer.createApp({ setup() { provideWindowContext({ active }); return () => h(child) } }); app.mount({}); mounts.push(app)
  const pending = c.cancel(); active.value = false; gate.resolve(true); await pending; assert.deepEqual(emitted, [])
})
test('所属W12Dialog关闭动画只恢复当前可见置顶窗口的触发控件', async () => {
  const setup = await component('../design/W12Dialog.vue'), active = { value: true }, focused = { value: true }, target = { isConnected: true, closest: () => null, focus() { document.activeElement = this } }
  document.activeElement = target
  const props = reactive({ open: true, busy: false }); let c
  const child = defineComponent({ setup() { c = setup(props); return () => h('div') } })
  const app = renderer.createApp({ setup() { provideWindowContext({ active, focused, contains: node => node === target }); return () => h(child) } }); app.mount({}); mounts.push(app)
  props.open = false; document.activeElement = {}; focused.value = false; await c.afterClose(); assert.notEqual(document.activeElement, target)
  focused.value = true; active.value = false; await c.afterClose(); assert.notEqual(document.activeElement, target)
  active.value = true; await c.afterClose(); assert.equal(document.activeElement, target)
})
test('ContextMenu按可用项循环键盘、触控外点关闭、Escape返回原焦点', async () => {
  window.innerWidth = 390; window.innerHeight = 844
  const setup = await component('../ContextMenu.vue'), emitted = [], origin = { isConnected: true, focus() { document.activeElement = this } }
  document.activeElement = origin
  const props = reactive({ pos: { x: 388, y: 840 }, items: [{ label: '一' }, '-', { label: '禁用', disabled: true }, { label: '二' }] })
  const { state: c } = mount(() => setup(props, (...args) => emitted.push(args)))
  const buttons = [0, 1].map(index => ({ focus() { document.activeElement = this; c.active.value = index } }))
  c.menu.value = { querySelectorAll: () => buttons, getBoundingClientRect: () => ({ width: 220, height: 100 }), contains: target => buttons.includes(target) }
  await nextTick(); assert.equal(document.activeElement, buttons[0]); assert.equal(c.x.value, 162); assert.equal(c.y.value, 736)
  const key = value => ({ key: value, preventDefault() {}, stopPropagation() {} })
  c.onKey(key('ArrowUp')); assert.equal(document.activeElement, buttons[1]); c.onKey(key('ArrowDown')); assert.equal(document.activeElement, buttons[0])
  c.onKey(key('End')); assert.equal(document.activeElement, buttons[1]); c.onKey(key('Home')); assert.equal(document.activeElement, buttons[0])
  c.outside({ target: buttons[0] }); assert.equal(emitted.length, 0); c.outside({ target: {} }); assert.equal(emitted.length, 1)
  c.onKey(key('Escape')); assert.equal(document.activeElement, c.origin.value); assert.equal(emitted.length, 2)
})
function fakeWindow() {
  const node = { style: {}, isConnected: true, contains: target => target === node, focus() { document.activeElement = node }, dispatchEvent() {}, querySelector: () => null }
  return node
}
test('WindowFrame初始聚焦与键盘focusin同步置顶；异步恢复不能抢新窗口焦点', async () => {
  const setup = await component('../WindowFrame.vue')
  const { state: c } = mount(() => { const store = useWindowsStore(), id = store.open(appDefinition), props = reactive({ win: store.windows[0] }); watch(() => store.windows, () => { props.win = store.windows.find(w => w.id === id) || props.win }, { flush: 'sync' }); return { ...setup(props), store, id } })
  const node = fakeWindow(); c.el.value = node; await nextTick(); assert.equal(document.activeElement, node)
  const second = c.store.open({ ...appDefinition, id: 'other' }); await nextTick(); c.onFocusIn({ target: node }); assert.equal(c.store.focusedId, c.id)
  c.store.focus(second); const pending = c.restoreFocus(); await pending; assert.equal(c.store.focusedId, second)
  c.store.reset()
})
test('WindowFrame标题点击保留最大化；实际拖动提交普通态，失焦/取消/工作区变化不提交', async () => {
  const setup = await component('../WindowFrame.vue')
  const { state: c } = mount(() => { const store = useWindowsStore(), id = store.open(appDefinition), props = reactive({ win: store.windows[0] }); watch(() => store.windows, () => { props.win = store.windows.find(w => w.id === id) || props.win }, { flush: 'sync' }); return { ...setup(props), store, id } })
  c.el.value = fakeWindow(); await nextTick(); c.store.toggleMax(c.id)
  const pointer = { captured: false, setPointerCapture() { this.captured = true }, hasPointerCapture() { return this.captured }, releasePointerCapture() { this.captured = false } }
  const event = (x = 300, y = 60) => ({ button: 0, pointerId: 1, clientX: x, clientY: y, target: { closest: () => null }, currentTarget: pointer, preventDefault() {}, stopPropagation() {} })
  c.begin(event(), 'move'); c.onPointerUp(event()); assert.equal(c.store.windows[0].state, 'maximized'); assert.equal(pointer.captured, false)
  c.begin(event(), 'move'); c.onPointerMove(event(340, 110)); c.pointerCancelled({ pointerId: 2 }); assert.ok(c.drag.value); window.dispatchEvent(new Event('blur')); assert.equal(c.drag.value, null); assert.equal(c.store.windows[0].state, 'maximized')
  c.begin(event(), 'move'); c.onPointerMove(event(340, 110)); c.store.updateWorkspace({ x: 0, y: 0, w: 1200, h: 700 }, 1200); await nextTick(); assert.equal(c.drag.value, null)
  c.begin(event(), 'move'); c.onPointerMove(event(340, 110)); c.onPointerUp(event(340, 110)); assert.equal(c.store.windows[0].state, 'normal'); assert.equal(pointer.captured, false)
  c.store.reset()
})

// 独立控制计时器与绘制帧，证明启动出口不需要浏览器绘制；不使用真实等待或业务网络。
async function bootHarness(ready = false, reducedMotion = false) {
  const setup = await component('../BootScreen.vue')
  const original = { setTimeout, clearTimeout, requestAnimationFrame, cancelAnimationFrame, matchMedia: window.matchMedia }
  const timers = new Map(), frames = new Map(), scheduled = []
  let serial = 0
  globalThis.setTimeout = (callback, delay) => {
    const task = { id: ++serial, callback, delay }; scheduled.push(task); timers.set(task.id, task); return task.id
  }
  globalThis.clearTimeout = id => timers.delete(id)
  globalThis.requestAnimationFrame = callback => { const id = ++serial; frames.set(id, callback); return id }
  globalThis.cancelAnimationFrame = id => frames.delete(id)
  window.matchMedia = () => ({ matches: reducedMotion })
  const restore = () => {
    for (const key of ['setTimeout', 'clearTimeout', 'requestAnimationFrame', 'cancelAnimationFrame']) globalThis[key] = original[key]
    if (original.matchMedia) window.matchMedia = original.matchMedia
    else delete window.matchMedia
  }
  const props = reactive({ ready }), emitted = []
  try {
    const mounted = mount(() => setup(props, (...args) => emitted.push(args)))
    let active = true
    const unmount = () => { if (active) { active = false; mounted.unmount() } }
    return { state: mounted.state, unmount, props, emitted, timers, frames, scheduled,
      fire(task) { timers.delete(task.id); task.callback() },
      resumeFrames() { const pending = [...frames.values()]; frames.clear(); for (const callback of pending) callback(0) },
      cleanup() { try { unmount() } finally { restore() } },
    }
  } catch (error) { restore(); throw error }
}

test('Boot未就绪保持遮罩，时间和绘制恢复都不能强制done', async () => {
  const b = await bootHarness()
  try {
    for (let tick = 0; tick < 120; tick++) { b.resumeFrames(); await nextTick() }
    assert.equal(b.state.gone.value, false); assert.equal(b.timers.size, 0); assert.deepEqual(b.emitted, [])
  } finally { b.cleanup() }
})
test('Boot隐藏页就绪时即排完成，绘制帧暂停和恢复不影响一次done', async () => {
  const b = await bootHarness()
  try {
    document.hidden = true; let painted = false
    requestAnimationFrame(() => { painted = true })
    b.props.ready = true; await nextTick()
    assert.equal(b.state.gone.value, true); assert.equal(b.frames.size, 1); assert.equal(painted, false)
    assert.equal(b.timers.size, 1); assert.equal(b.scheduled[0].delay, 120)
    b.fire(b.scheduled[0]); assert.deepEqual(b.emitted, [['done']]); assert.equal(painted, false)
    b.resumeFrames(); await nextTick(); assert.equal(painted, true); assert.deepEqual(b.emitted, [['done']])
  } finally { document.hidden = false; b.cleanup() }
})
test('Boot初始就绪和重复ready只完成一次，完成后不会重排', async () => {
  const b = await bootHarness(true)
  try {
    assert.equal(b.state.gone.value, true); assert.equal(b.frames.size, 0); assert.equal(b.timers.size, 1)
    b.props.ready = true; await nextTick(); assert.equal(b.scheduled.length, 1)
    b.fire(b.scheduled[0]); b.fire(b.scheduled[0]); assert.deepEqual(b.emitted, [['done']])
    b.props.ready = false; b.props.ready = true; await nextTick()
    assert.equal(b.timers.size, 0); assert.equal(b.scheduled.length, 1); assert.deepEqual(b.emitted, [['done']])
  } finally { b.cleanup() }
})
test('Boot减弱动态保留准备门禁并使用0毫秒非绘制帧完成', async () => {
  const b = await bootHarness(false, true)
  try {
    assert.equal(b.timers.size, 0); b.props.ready = true
    assert.equal(b.state.gone.value, true); assert.equal(b.scheduled[0].delay, 0); assert.equal(b.frames.size, 0)
    assert.deepEqual(b.emitted, []); b.fire(b.scheduled[0]); assert.deepEqual(b.emitted, [['done']])
  } finally { b.cleanup() }
})
test('Boot撤销/重新就绪使旧计时器失效，快速false→true也不能迟到完成', async () => {
  const b = await bootHarness(true)
  try {
    const old = b.scheduled[0]; b.props.ready = false
    assert.equal(b.state.gone.value, false); assert.equal(b.timers.size, 0)
    b.fire(old); assert.deepEqual(b.emitted, [])
    b.props.ready = true; const current = b.scheduled[1]
    b.props.ready = false; b.props.ready = true; const latest = b.scheduled[2]
    b.fire(old); b.fire(current); assert.deepEqual(b.emitted, []); assert.equal(b.timers.size, 1)
    b.fire(latest); assert.deepEqual(b.emitted, [['done']])
  } finally { b.cleanup() }
})
test('Boot卸载清计时器且迟到回调不能done或影响新实例', async () => {
  const oldBoot = await bootHarness(true)
  let oldTask
  try {
    oldTask = oldBoot.scheduled[0]; oldBoot.unmount()
    assert.equal(oldBoot.timers.size, 0); oldBoot.fire(oldTask); assert.deepEqual(oldBoot.emitted, [])
  } finally { oldBoot.cleanup() }
  const newBoot = await bootHarness(true)
  try {
    oldTask.callback(); assert.deepEqual(newBoot.emitted, []); assert.deepEqual(oldBoot.emitted, [])
    newBoot.fire(newBoot.scheduled[0]); assert.deepEqual(newBoot.emitted, [['done']])
  } finally { newBoot.cleanup() }
})
