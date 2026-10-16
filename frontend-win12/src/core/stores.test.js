import test from 'node:test'
import assert from 'node:assert/strict'
import { createPinia, setActivePinia } from 'pinia'
import { useWindowsStore, useAuthStore } from './stores.js'
import { rectOf } from './windowState.js'
import { getSessionEpoch, resetRequestSession } from '../api/session.js'
import { setToken, getToken } from '../api/index.js'

const items = new Map()
globalThis.localStorage = { getItem: (key) => items.get(key) ?? null, setItem: (key, value) => items.set(key, String(value)), removeItem: (key) => items.delete(key) }
globalThis.window = new EventTarget()
const app = { id: 'groups', size: { w: 700, h: 500 } }
const setup = () => { setActivePinia(createPinia()); const store = useWindowsStore(); store.area = { x: 0, y: 0, w: 1400, h: 800 }; return store }

test('关闭动画期间重开取消旧任务，同一实例不会迟到删除', async () => {
  const store = setup(), id = store.open(app)
  await store.close(id)
  assert.equal(store.windows[0].state, 'closing')
  assert.equal(store.open(app), id)
  await new Promise(resolve => setTimeout(resolve, 200))
  assert.equal(store.windows.length, 1); assert.equal(store.windows[0].state, 'normal')
  store.reset()
})
test('确认关闭的等待期间重开、重置使旧确认无效，重复关闭只询问一次', async () => {
  const store = setup(), id = store.open(app)
  let finish, count = 0
  store.registerCloseGuard(id, () => { count++; return new Promise(resolve => { finish = resolve }) })
  const close = store.close(id)
  await store.close(id)
  assert.equal(count, 1)
  store.open(app); finish(true); await close
  assert.equal(store.windows[0].state, 'normal')
  const second = store.close(id); store.reset(); finish(true); await second
  assert.equal(store.windows.length, 0)
})
test('连续最小化不会覆盖恢复态，关闭重开保留最大化', async () => {
  const store = setup(), id = store.open(app)
  store.toggleMax(id); store.minimize(id); store.minimize(id); store.focus(id)
  assert.equal(store.windows[0].state, 'maximized')
  store.minimize(id); await store.close(id); store.open(app)
  assert.equal(store.windows[0].state, 'maximized')
  store.reset()
})
test('持续手机模式保留桌面矩形；回桌面限制到新工作区', () => {
  const store = setup(), id = store.open(app), desktop = rectOf(store.windows[0])
  store.updateWorkspace({ x: 0, y: 44, w: 390, h: 730 }, 390)
  assert.equal(store.mobile, true); assert.deepEqual(rectOf(store.windows[0]), desktop)
  store.showApps(); store.focus(id); assert.equal(store.appsOpen, false)
  store.updateWorkspace({ x: 0, y: 0, w: 800, h: 520 }, 800)
  assert.ok(store.windows[0].x + store.windows[0].w <= 800)
  assert.ok(store.windows[0].y + store.windows[0].h <= 520)
  store.reset()
})
test('聚焦多窗口和注销重置清空窗口、菜单、最近记录', () => {
  const store = setup(), first = store.open(app), second = store.open({ ...app, id: 'settings' })
  for (let n = 0; n < 100; n++) store.focus(n % 2 ? first : second)
  assert.equal(Math.max(...store.windows.map(w => w.z)), 110)
  store.minimize(store.focusedId); assert.ok(store.focusedId)
  store.startOpen = true; store.showApps(); store.reset()
  assert.equal(store.focusedId, null); assert.equal(store.startOpen, false); assert.equal(store.appsOpen, false); assert.deepEqual(store.recentIds, [])
})
test('退出递增一次并清凭据；外部标签变化只清本页，不写共享新身份', () => {
  const store = setup(), auth = useAuthStore()
  auth.initSession()
  auth.acceptLogin({ access_token: 'local-test-token', user: { id: 1 } }); store.open(app)
  const before = getSessionEpoch(); auth.logout()
  assert.equal(getSessionEpoch(), before + 1); assert.equal(getToken(), ''); assert.equal(auth.user, null); assert.equal(store.windows.length, 0)
  auth.logout(); assert.equal(getSessionEpoch(), before + 1)
  // 直接模拟浏览器 storage 同步；这些是假凭据且不写实际文件。
  items.set('w12-token', 'new-tab-token'); items.set('w12-user', JSON.stringify({ id: 2 }))
  auth.user = { id: 1 }; store.open(app); auth.invalidateLocalSession()
  assert.equal(getToken(), 'new-tab-token'); assert.equal(items.get('w12-user'), '{"id":2}')
  assert.equal(auth.user, null); assert.equal(store.windows.length, 0)
  assert.equal(auth.sessionEpoch, getSessionEpoch())
  auth.disposeSession(); items.clear(); resetRequestSession('test-cleanup')
})
