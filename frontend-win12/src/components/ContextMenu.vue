<script setup>
import { computed, ref, onMounted, onUnmounted, nextTick } from 'vue'

/* 右键菜单同样支持触控外点及键盘：方向键循环，Home/End定位，Escape返回原焦点。 */
const props = defineProps({ pos: { type: Object, default: null }, items: { type: Array, required: true } })
const emit = defineEmits(['close'])
const menu = ref(null), origin = ref(null), measured = ref({ w: 230, h: 40 }), active = ref(0)
const enabledItems = computed(() => props.items.filter(item => item !== '-' && !item.disabled))
const buttons = () => [...(menu.value?.querySelectorAll('button:not(:disabled)') || [])]
const close = (restore = false) => { if (restore && origin.value?.isConnected) origin.value.focus({ preventScroll: true }); emit('close') }
const outside = event => { if (!menu.value?.contains(event.target)) close() }
const blurred = () => close()
const focusItem = index => {
  const list = buttons()
  if (!list.length) return
  active.value = (index + list.length) % list.length
  list[active.value].focus({ preventScroll: true })
}
const onKey = event => {
  if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); close(true) }
  else if (event.key === 'Tab') close()
  else if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) {
    event.preventDefault()
    focusItem(event.key === 'Home' ? 0 : event.key === 'End' ? enabledItems.value.length - 1 : active.value + (event.key === 'ArrowDown' ? 1 : -1))
  }
}
const activate = item => { if (item.disabled) return; close(true); item.onClick?.() }
onMounted(async () => {
  origin.value = document.activeElement
  await nextTick()
  if (!menu.value) return
  const rect = menu.value.getBoundingClientRect(); measured.value = { w: rect.width, h: rect.height }
  focusItem(0)
  window.addEventListener('pointerdown', outside); window.addEventListener('blur', blurred)
})
onUnmounted(() => { window.removeEventListener('pointerdown', outside); window.removeEventListener('blur', blurred) })
const x = computed(() => Math.max(0, Math.min(props.pos?.x ?? 0, window.innerWidth - measured.value.w - 8)))
const y = computed(() => Math.max(0, Math.min(props.pos?.y ?? 0, window.innerHeight - measured.value.h - 8)))
</script>

<template>
  <div
    v-if="pos"
    ref="menu" role="menu" aria-label="快捷操作" class="w12-ctx"
    :style="{ left: x + 'px', top: y + 'px' }"
    @pointerdown.stop @keydown="onKey"
    @contextmenu.prevent
  >
    <template v-for="(it, i) in items" :key="i">
      <div v-if="it === '-'" role="separator" class="w12-ctx-sep" />
      <button v-else type="button" role="menuitem" class="w12-ctx-item" :disabled="it.disabled"
        tabindex="-1" @focus="active = enabledItems.indexOf(it)" @click="activate(it)">
        <component :is="it.icon" />
        {{ it.label }}
      </button>
    </template>
  </div>
</template>
