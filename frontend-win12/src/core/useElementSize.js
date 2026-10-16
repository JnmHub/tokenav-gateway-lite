import { ref, onMounted, onBeforeUnmount } from 'vue'

// 容器真实尺寸同时驱动列表/图表布局；窗口拖缩不一定触发 window.resize。
export function useElementSize(element) {
  const width = ref(0), height = ref(0)
  let observer, frame = 0
  const update = () => {
    cancelAnimationFrame(frame)
    frame = requestAnimationFrame(() => {
      const rect = element.value?.getBoundingClientRect()
      if (rect) { width.value = rect.width; height.value = rect.height }
    })
  }
  onMounted(() => { update(); observer = new ResizeObserver(update); if (element.value) observer.observe(element.value) })
  onBeforeUnmount(() => { observer?.disconnect(); cancelAnimationFrame(frame) })
  return { width, height }
}
