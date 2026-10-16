<script setup>
import { computed, ref, watch, onMounted, onBeforeUnmount, nextTick } from 'vue'
import { ConfigProvider, Modal } from 'ant-design-vue'
import { MinusOutlined, BorderOutlined, BlockOutlined, CloseOutlined } from '@ant-design/icons-vue'
import { getApp } from '../core/apps'
import { useWindowsStore } from '../core/stores'
import { rectOf, displayRect, detachRect, gestureResult, snapRect } from '../core/windowState'
import { provideWindowContext } from '../core/windowContext'
import AppIcon from './AppIcon.vue'

const props = defineProps({ win: { type: Object, required: true } })
const store = useWindowsStore()
const app = computed(() => getApp(props.win.appId))
const el = ref(null), layer = ref(null), drag = ref(null), shown = ref(false), lastFocus = ref(null)
const focused = computed(() => store.focusedId === props.win.id)
const hidden = computed(() => props.win.min || props.win.closing || (store.mobile && (!focused.value || store.appsOpen)))
const visibleRect = computed(() => displayRect(props.win, store.area, store.mobile))
const minimum = computed(() => app.value.minSize || { w: 360, h: 240 })
const dirtyChecks = new Set(), busyChecks = new Set()
const getContainer = () => layer.value || el.value || document.body
provideWindowContext({ id: props.win.id, getContainer, active: computed(() => !hidden.value), focused,
  restoreFocus: () => restoreFocus(), contains: target => !!el.value?.contains(target),
  target: computed(() => props.win.target), registerDirty(fn) { dirtyChecks.add(fn); return () => dirtyChecks.delete(fn) },
  registerBusy(fn) { busyChecks.add(fn); return () => busyChecks.delete(fn) } })
const askDiscard = () => {
  // 保存请求未完成时关闭窗口会让结果与草稿脱节，直接保留窗口等待完成。
  if ([...busyChecks].some(check => check())) return Promise.resolve(false)
  if (![...dirtyChecks].some((check) => check())) return Promise.resolve(true)
  store.focus(props.win.id)
  return new Promise(resolve => {
    const settle = allowed => {
      resolve(allowed && ![...busyChecks].some(check => check()))
      if (finishDiscard === settle) { finishDiscard = null; discardDialog = null }
    }
    finishDiscard = settle
    discardDialog = Modal.confirm({ title: '放弃未保存的修改？', content: '关闭窗口后，这些修改不会保存。',
      wrapClassName: 'w12-design w12-dialog', okText: '放弃修改', cancelText: '继续编辑', getContainer,
      onOk: () => settle(true), onCancel: () => settle(false) })
  })
}
let unregisterGuard, entryFrame, discardDialog, finishDiscard
onMounted(() => {
  unregisterGuard = store.registerCloseGuard(props.win.id, askDiscard)
  entryFrame = requestAnimationFrame(() => { shown.value = true })
  window.addEventListener('blur', cancelGesture)
  document.addEventListener('visibilitychange', visibilityChanged)
  restoreFocus()
})
const applyRect = (rect) => {
  if (el.value) Object.assign(el.value.style, { left: `${rect.x}px`, top: `${rect.y}px`, width: `${rect.w}px`, height: `${rect.h}px` })
}
const begin = (event, mode, direction = '') => {
  if (drag.value || event.button !== 0 || store.mobile || hidden.value || event.target.closest('button')) return
  if (mode === 'resize' && props.win.state === 'maximized') return
  event.preventDefault(); event.stopPropagation(); store.focus(props.win.id)
  const original = { ...props.win }, point = { x: event.clientX, y: event.clientY }
  const rect = mode === 'move' && ['maximized', 'snapped'].includes(original.state)
    ? detachRect(original, point, store.area, minimum.value) : rectOf(original)
  drag.value = { mode, direction, start: point, rect, originalRect: rectOf(original), original,
    pointerId: event.pointerId, node: event.currentTarget, lastPoint: point, moved: false }
  try { event.currentTarget.setPointerCapture(event.pointerId) } catch { cancelGesture() }
}
const onPointerMove = (event) => {
  if (!drag.value || drag.value.pointerId !== event.pointerId) return
  const point = { x: event.clientX, y: event.clientY }
  drag.value.lastPoint = point
  if (!drag.value.moved && Math.hypot(point.x - drag.value.start.x, point.y - drag.value.start.y) < 4) return
  drag.value.moved = true
  const result = gestureResult(drag.value, point, store.area, minimum.value)
  applyRect(result.rect); store.snapPreview = result.snap ? { side: result.snap, ...snapRect(result.snap, store.area) } : null
}
const finishGesture = (cancelled = false) => {
  const gesture = drag.value
  if (!gesture) return
  drag.value = null; store.snapPreview = null
  if (cancelled) applyRect(visibleRect.value)
  else if (gesture.moved || Math.hypot(gesture.lastPoint.x - gesture.start.x, gesture.lastPoint.y - gesture.start.y) >= 4) {
    const result = gestureResult(gesture, gesture.lastPoint, store.area, minimum.value)
    // 先保留普通拖动矩形再吸附，确保还原尺寸与指针位置一致。
    store.setRect(props.win.id, result.rect)
    if (result.snap) store.snap(props.win.id, result.snap)
  }
  if (gesture.node.hasPointerCapture?.(gesture.pointerId)) gesture.node.releasePointerCapture(gesture.pointerId)
}
function cancelGesture() { finishGesture(true) }
function visibilityChanged() { if (document.hidden) cancelGesture() }
const pointerCancelled = event => { if (event.pointerId === drag.value?.pointerId) cancelGesture() }
const onPointerUp = (event) => {
  if (drag.value?.pointerId !== event.pointerId) return
  drag.value.lastPoint = { x: event.clientX, y: event.clientY }; finishGesture()
}
const restoreFocus = async () => {
  await nextTick()
  // focus 异步恢复期间另一个窗口可能已置顶；不可抢回用户的新焦点。
  if (!focused.value || hidden.value || !el.value) return
  if (!el.value.contains(document.activeElement)) {
    const target = lastFocus.value
    if (target?.isConnected && el.value.contains(target) && !target.closest('[inert], [aria-hidden="true"]')) target.focus({ preventScroll: true })
    else el.value?.focus({ preventScroll: true })
  }
  el.value.dispatchEvent(new CustomEvent('w12-window-visible', { bubbles: true }))
}
watch([focused, hidden], ([active, concealed]) => {
  if (!active || concealed) cancelGesture()
  if (concealed) { if (el.value?.contains(document.activeElement)) document.activeElement.blur(); return }
  if (active) restoreFocus()
})
watch(() => store.mobile, cancelGesture)
watch(() => store.area, cancelGesture)
const onFocusIn = event => { lastFocus.value = event.target; if (!hidden.value && !focused.value) store.focus(props.win.id) }
onBeforeUnmount(() => { const dialog = discardDialog; finishDiscard?.(false); dialog?.destroy(); cancelGesture(); unregisterGuard?.(); cancelAnimationFrame(entryFrame); window.removeEventListener('blur', cancelGesture); document.removeEventListener('visibilitychange', visibilityChanged) })
const classes = computed(() => ({ 'w12-win': true, show: shown.value && !props.win.closing, foc: focused.value,
  min: props.win.min, max: props.win.max, snapped: props.win.state === 'snapped', closing: props.win.closing,
  'is-mobile': store.mobile, 'is-hidden': hidden.value, 'is-dragging': !!drag.value }))
const motionStyle = computed(() => {
  if (!props.win.min) return {}
  const target = document.querySelector(`[data-task-app="${props.win.appId}"]`)?.getBoundingClientRect()
  const rect = visibleRect.value
  return target ? { '--min-x': `${target.x + target.width / 2 - rect.x - rect.w / 2}px`,
    '--min-y': `${target.y + target.height / 2 - rect.y - rect.h / 2}px` } : {}
})
</script>
<template>
  <section v-if="app" ref="el" :class="classes" :data-window-id="win.id" :data-app-id="win.appId"
    :inert="hidden || undefined" :aria-hidden="hidden" :aria-label="app.name" tabindex="-1"
    :style="{ left: visibleRect.x + 'px', top: visibleRect.y + 'px', width: visibleRect.w + 'px', height: visibleRect.h + 'px', zIndex: win.z, '--w12-dialog-height': Math.max(80, visibleRect.h - (store.mobile ? 44 : 40) - 32) + 'px', ...motionStyle }"
    @pointerdown.capture="!hidden && !focused && store.focus(win.id)" @focusin="onFocusIn"
    @pointercancel="pointerCancelled" @lostpointercapture="pointerCancelled" @keydown.alt.f4.prevent="store.close(win.id)">
    <template v-if="!store.mobile && !win.max">
      <div v-for="dir in ['n', 's', 'e', 'w', 'ne', 'nw', 'se', 'sw']" :key="dir" :class="`rs ${dir}`"
        @pointerdown="begin($event, 'resize', dir)" @pointermove="onPointerMove" @pointerup="onPointerUp" />
    </template>
    <header class="w12-titbar" @pointerdown="begin($event, 'move')" @pointermove="onPointerMove" @pointerup="onPointerUp"
      @dblclick="!$event.target.closest('button') && store.toggleMax(win.id)">
      <AppIcon :app="app" :size="18" /><span class="wt">{{ app.name }}</span>
      <div class="w12-wbtns">
        <button class="w12-wbtn" title="最小化" aria-label="最小化" @click="store.minimize(win.id)"><MinusOutlined /></button>
        <button v-if="!store.mobile" class="w12-wbtn" :title="win.max || win.state === 'snapped' ? '还原' : '最大化'" :aria-label="win.max ? '还原' : '最大化'" @click="store.toggleMax(win.id)">
          <BlockOutlined v-if="win.max || win.state === 'snapped'" /><BorderOutlined v-else />
        </button>
        <button class="w12-wbtn red" title="关闭" aria-label="关闭" @click="store.close(win.id)"><CloseOutlined /></button>
      </div>
    </header>
    <ConfigProvider :get-popup-container="getContainer">
      <div class="w12-content"><div :class="['app-surface', { nopad: app.padded }]"><component :is="app.component" :key="win.id" :win="win" /></div></div>
    </ConfigProvider>
    <!-- 弹层跟随所属窗口隐藏/卸载，其层级限制在窗口绘制层内。 -->
    <div ref="layer" class="w12-window-layer" />
  </section>
</template>
