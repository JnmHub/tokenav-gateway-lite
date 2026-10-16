<script setup>
import { computed, ref, shallowRef, watch, nextTick, onScopeDispose, useAttrs } from 'vue'
import { Modal } from 'ant-design-vue'
import { useWindowContext } from '../../core/windowContext'
defineOptions({ inheritAttrs: false })
const props = defineProps({ open: Boolean, beforeClose: Function, busy: Boolean, closeKey: [String, Number], wrapClassName: { type: String, default: '' } })
const emit = defineEmits(['update:open', 'cancel'])
const context = useWindowContext(), attrs = useAttrs()
const pending = ref(false)
let generation = 0, disposed = false
const trigger = shallowRef(null)
watch(() => [props.open, props.closeKey, attrs.title], ([open]) => {
  generation += 1; pending.value = false
  if (open && typeof document !== 'undefined') trigger.value = document.activeElement
}, { immediate: true, flush: 'sync' })
const afterClose = async () => {
  await nextTick()
  if (disposed || props.open || !context || context.active.value === false || context.focused?.value === false) return
  const target = trigger.value
  if (target?.isConnected && context.contains?.(target) && !target.closest('[inert], [aria-hidden="true"]')) target.focus({ preventScroll: true })
  else context.restoreFocus?.()
}
onScopeDispose(() => { disposed = true; generation += 1 })
const wrapClass = computed(() => `w12-design w12-dialog ${props.wrapClassName}`)
const cancel = async (event) => {
  // 使用受控 open，先判断草稿保护；弹窗不能先关闭再询问。
  if (!props.open || props.busy || pending.value || disposed) return
  const turn = generation
  pending.value = true
  try {
    if (props.beforeClose && !await props.beforeClose()) return
    // 旧放弃确认不能关闭后来重开的弹窗，也不能在保存/最小化后继续关闭。
    if (disposed || turn !== generation || !props.open || props.busy || context?.active.value === false) return
    emit('update:open', false); emit('cancel', event)
  } finally { if (turn === generation) pending.value = false }
}
</script>
<template>
  <Modal v-bind="$attrs" :open="open" destroy-on-close :get-container="context?.getContainer" :wrap-class-name="wrapClass" :mask-closable="false" :closable="!busy" :keyboard="!busy" :cancel-button-props="{ disabled: busy }" :focus-trigger-after-close="!context" @after-close="afterClose" @cancel="cancel">
    <template v-for="(_, name) in $slots" #[name]="scope"><slot :name="name" v-bind="scope || {}" /></template>
  </Modal>
</template>
