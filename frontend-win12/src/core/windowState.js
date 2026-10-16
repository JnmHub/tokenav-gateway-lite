/* 窗口几何与状态纯函数：DOM（页面元素）手势只提交结果，避免显示位置与状态分离。
 * 桌面矩形始终保留；手机全屏是呈现方式，不能覆盖用户的桌面恢复矩形。 */
export const WINDOW_STATES = ['normal', 'minimized', 'maximized', 'snapped', 'closing']
export const WINDOW_BASE_Z = 100
export const WINDOW_Z_STEP = 10
export const MOBILE_BREAKPOINT = 768

export const clamp = (value, min, max) => Math.min(Math.max(value, min), Math.max(min, max))
const finite = (value, fallback) => Number.isFinite(Number(value)) ? Number(value) : fallback
export const rectOf = (win) => ({ x: win.x, y: win.y, w: win.w, h: win.h })

export function workArea({ width, height, x = 0, y = 0, dockTop, bottom = 0 }) {
  const w = Math.max(1, finite(width, 1280))
  const available = Math.max(1, finite(height, 800) - y - bottom)
  const h = Math.max(1, Math.min(available, Number.isFinite(dockTop) ? dockTop - y : available))
  return { x, y, w, h }
}

export function constrainRect(rect, area, minimum = { w: 360, h: 240 }) {
  const w = clamp(finite(rect.w, minimum.w), Math.min(minimum.w, area.w), area.w)
  const h = clamp(finite(rect.h, minimum.h), Math.min(minimum.h, area.h), area.h)
  return {
    x: clamp(finite(rect.x, area.x), area.x, area.x + area.w - w),
    y: clamp(finite(rect.y, area.y), area.y, area.y + area.h - h), w, h,
  }
}

export function initialRect(size, area, cascade = 0) {
  const w = Math.min(size.w, Math.max(360, area.w - 48))
  const h = Math.min(size.h, Math.max(240, area.h - 48))
  const n = cascade % 7
  return constrainRect({ w, h, x: area.x + (area.w - w) / 2 + (n - 3) * 24,
    y: area.y + (area.h - h) / 2 + (n - 3) * 20 }, area)
}

export function withState(win, state) {
  if (!WINDOW_STATES.includes(state)) throw new Error(`未知窗口状态：${state}`)
  // 旧业务使用 min/max/closing，保留派生别名而不维护第二套权威状态。
  return { ...win, state, min: state === 'minimized', max: state === 'maximized', closing: state === 'closing' }
}

export function snapRect(side, area) {
  if (side === 'top') return { ...area }
  const w = Math.floor(area.w / 2)
  return { x: side === 'right' ? area.x + w : area.x, y: area.y,
    w: side === 'right' ? area.w - w : w, h: area.h }
}

export function snapTarget(point, area, threshold = 20) {
  if (point.y <= area.y + threshold) return 'top'
  if (point.x <= area.x + threshold) return 'left'
  if (point.x >= area.x + area.w - threshold) return 'right'
  return null
}

export function maximize(win, area) {
  const restoreRect = win.state === 'normal' ? rectOf(win) : (win.restoreRect || rectOf(win))
  return withState({ ...win, ...area, restoreRect, snapSide: null }, 'maximized')
}

export function restore(win, area, minimum) {
  return withState({ ...win, ...constrainRect(win.restoreRect || win, area, minimum), snapSide: null }, 'normal')
}

export function snap(win, side, area) {
  if (side === 'top') return maximize(win, area)
  const restoreRect = win.state === 'normal' ? rectOf(win) : (win.restoreRect || rectOf(win))
  return withState({ ...win, ...snapRect(side, area), restoreRect, snapSide: side }, 'snapped')
}

export function resume(win) {
  const previous = ['normal', 'maximized', 'snapped'].includes(win.previousState) ? win.previousState : 'normal'
  return withState({ ...win, previousState: null }, previous)
}

// 所有窗口重排到有界区间，系统菜单与弹层永远不被连续 focus（置顶）推穿。
export function orderLayers(windows, focusedId) {
  const target = windows.find((w) => w.id === focusedId)
  const ordered = target ? [...windows.filter((w) => w.id !== focusedId), target] : [...windows]
  return ordered.map((w, i) => ({ ...w, z: WINDOW_BASE_Z + i * WINDOW_Z_STEP }))
}

export function topWindow(windows) {
  return [...windows].reverse().find((w) => !['minimized', 'closing'].includes(w.state)) || null
}

export function displayRect(win, area, mobile) {
  return mobile ? { ...area } : rectOf(win)
}

// 从最大化/吸附拖出时让指针仍落在相同比例的标题栏上，随后用普通拖动语义。
export function detachRect(win, point, area, minimum) {
  const restored = constrainRect(win.restoreRect || win, area, minimum)
  const fraction = clamp((point.x - win.x) / win.w, 0.08, 0.92)
  return constrainRect({ ...restored, x: point.x - restored.w * fraction, y: point.y - 20 }, area, minimum)
}

export function moveRect(rect, dx, dy, area, minimum) {
  return constrainRect({ ...rect, x: rect.x + dx, y: rect.y + dy }, area, minimum)
}

export function resizeRect(rect, direction, dx, dy, area, minimum) {
  const minW = Math.min(minimum.w, area.w)
  const minH = Math.min(minimum.h, area.h)
  let left = rect.x, right = rect.x + rect.w, top = rect.y, bottom = rect.y + rect.h
  if (direction.includes('w')) left = clamp(left + dx, area.x, right - minW)
  if (direction.includes('e')) right = clamp(right + dx, left + minW, area.x + area.w)
  if (direction.includes('n')) top = clamp(top + dy, area.y, bottom - minH)
  if (direction.includes('s')) bottom = clamp(bottom + dy, top + minH, area.y + area.h)
  return { x: left, y: top, w: right - left, h: bottom - top }
}

export function gestureResult(gesture, point, area, minimum, cancelled = false) {
  // pointercancel/失焦回到按下时状态，不能把不可确认的最后坐标当作完成手势。
  if (cancelled) return { rect: gesture.originalRect || gesture.rect, snap: null, cancelled: true }
  const dx = point.x - gesture.start.x, dy = point.y - gesture.start.y
  return {
    rect: gesture.mode === 'resize'
      ? resizeRect(gesture.rect, gesture.direction, dx, dy, area, minimum)
      : moveRect(gesture.rect, dx, dy, area, minimum),
    snap: gesture.mode === 'move' ? snapTarget(point, area) : null,
    cancelled: false,
  }
}
