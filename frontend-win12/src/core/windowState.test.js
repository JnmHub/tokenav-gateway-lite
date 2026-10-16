import test from 'node:test'
import assert from 'node:assert/strict'
import { workArea, constrainRect, withState, maximize, restore, snap, resume, orderLayers, rectOf, resizeRect, gestureResult, displayRect, detachRect } from './windowState.js'
import { parseAppLink } from './appLinks.js'

const area = { x: 0, y: 0, w: 1200, h: 760 }, minimum = { w: 360, h: 240 }
const rect = { x: 200, y: 120, w: 640, h: 480 }
const win = () => withState({ id: 'groups-1', ...rect, restoreRect: { ...rect } }, 'normal')
test('工作区扣除顶部安全区、任务栏与底边', () => {
  assert.deepEqual(workArea({ width: 390, height: 844, y: 44, dockTop: 756 }), { x: 0, y: 44, w: 390, h: 712 })
  assert.equal(workArea({ width: 390, height: 430, y: 44 }).h, 386)
  assert.equal(workArea({ width: 1200, height: 900, dockTop: 844, bottom: 8 }).h, 844)
})
test('过小工作区仍保留可达标题栏和完整窗口', () => {
  assert.deepEqual(constrainRect({ x: -900, y: 1000, w: 640, h: 480 }, { x: 10, y: 20, w: 320, h: 200 }, minimum), { x: 10, y: 20, w: 320, h: 200 })
})
test('最大化、吸附、最小化、还原不会丢普通矩形', () => {
  const max = maximize(win(), area)
  assert.deepEqual(rectOf(max), area)
  assert.deepEqual(rectOf(restore(max, area, minimum)), rect)
  const left = snap(max, 'left', area)
  assert.deepEqual(left.restoreRect, rect)
  assert.equal(left.w, 600)
  const minimized = withState({ ...left, previousState: left.state }, 'minimized')
  const resumed = resume(minimized)
  assert.equal(resumed.state, 'snapped')
  assert.deepEqual(rectOf(restore(resumed, area, minimum)), rect)
})
test('八方向缩放保持工作区内及最小尺寸、对边不动', () => {
  for (const dir of ['n','s','e','w','ne','nw','se','sw']) for (const delta of [-10000, -100, 100, 10000]) {
    const result = resizeRect(rect, dir, delta, delta, area, minimum)
    assert.ok(result.w >= 360 && result.h >= 240, dir)
    assert.ok(result.x >= 0 && result.y >= 0 && result.x + result.w <= 1200 && result.y + result.h <= 760, dir)
    if (dir.includes('w')) assert.equal(result.x + result.w, rect.x + rect.w)
    if (dir.includes('n')) assert.equal(result.y + result.h, rect.y + rect.h)
  }
})
test('取消拖动恢复按下时矩形并清吸附', () => {
  const gesture = { mode: 'move', start: { x: 220, y: 130 }, rect, originalRect: area }
  assert.deepEqual(gestureResult(gesture, { x: 0, y: 0 }, area, minimum, true), { rect: area, snap: null, cancelled: true })
  assert.equal(gestureResult(gesture, { x: 1, y: 400 }, area, minimum).snap, 'left')
})
test('手机呈现不改桌面矩形，从最大化拖出时指针仍在标题栏', () => {
  const original = win(), max = maximize(original, area)
  assert.deepEqual(displayRect(original, { x: 0, y: 44, w: 390, h: 730 }, true), { x: 0, y: 44, w: 390, h: 730 })
  assert.deepEqual(rectOf(original), rect)
  const detached = detachRect(max, { x: 600, y: 10 }, area, minimum)
  assert.equal(detached.x, 280); assert.equal(detached.y, 0)
})
test('反复聚焦的层级始终有界', () => {
  let windows = Array.from({ length: 19 }, (_, i) => ({ id: String(i) }))
  for (let i = 0; i < 2000; i++) windows = orderLayers(windows, String(i % 19))
  assert.equal(Math.max(...windows.map(w => w.z)), 280)
  assert.equal(new Set(windows.map(w => w.z)).size, 19)
})
test('深链只打开注册应用、合法标签和安全整数对象', () => {
  assert.equal(parseAppLink('https://local/?app=missing', ['groups']).error, '此地址指向的应用尚未提供。')
  assert.deepEqual(parseAppLink('https://local/?tab=gateway', ['settings']), { appId: 'settings', target: { tab: 'gateway', id: null } })
  assert.deepEqual(parseAppLink('https://local/#app=groups&id=42', ['groups']), { appId: 'groups', target: { tab: null, id: 42 } })
  assert.equal(parseAppLink('https://local/?app=groups&id=9999999999999999', ['groups']).target.id, null)
})
