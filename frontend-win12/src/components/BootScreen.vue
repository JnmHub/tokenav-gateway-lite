<script setup>
import { ref, watch, onBeforeUnmount } from 'vue'
const props = defineProps({ ready: { type: Boolean, default: false } })
const emit = defineEmits(['done'])
const gone = ref(false)
let timer, generation = 0, completed = false, disposed = false
watch(() => props.ready, (ready) => {
  const turn = ++generation
  clearTimeout(timer)
  if (disposed || completed) return
  gone.value = ready
  if (!ready) return
  // 隐藏标签页可能暂停绘制帧；业务完成不能等动画帧或淡出结束才安排。
  timer = setTimeout(() => {
    // 就绪撤销、重复切换或卸载后，迟到回调不能结束新的启动状态。
    if (disposed || completed || turn !== generation || !props.ready) return
    completed = true
    emit('done')
  }, window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 0 : 120)
}, { immediate: true, flush: 'sync' })
onBeforeUnmount(() => { disposed = true; generation += 1; clearTimeout(timer) })
</script>
<template>
  <div class="w12-boot" :class="{ gone }" role="status" aria-label="正在准备后台">
    <div class="w12-boot-logo" aria-hidden="true" /><div class="w12-boot-dots" aria-hidden="true"><i v-for="(deg, i) in [0, 60, 120, 180, 240, 300]" :key="deg" :style="{ transform: `rotate(${deg}deg) translateY(-18px)`, animationDelay: `${i * 0.12}s` }" /></div>
  </div>
</template>
