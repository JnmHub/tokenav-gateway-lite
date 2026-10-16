<script setup>
/* ============================================================
 * 图像任务产物预览（Win12 重构版）
 * GET /admin/image-tasks/:id/artifacts/:index 返回二进制图片流（非 JSON，
 * Content-Type 为图片，index 从 0 开始），不能走 request()（会按 JSON 解析），
 * 必须用 fetch 带 Authorization 取 blob → URL.createObjectURL 交给 <img>。
 * 内存管理：缓存每个 index 的 objectURL；切换预览对象时释放上一个；
 * 组件卸载时释放全部缓存（onBeforeUnmount revokeObjectURL）。
 * ============================================================ */
import { ref, watch, onBeforeUnmount } from 'vue'
import { message } from 'ant-design-vue'
import { CloseOutlined } from '@ant-design/icons-vue'
import { getToken } from '../../api'

const props = defineProps({
  taskId: { type: String, default: '' },
  artifacts: { type: Array, default: () => [] }, // [{ file_name, content_type, size_bytes }]
})
const emit = defineEmits(['close'])

const loading = ref(false)
const previewUrl = ref('')
const previewIndex = ref(-1)
const previewName = ref('')
const urlCache = new Map() // index → objectURL

function revoke(url) {
  if (url) { try { URL.revokeObjectURL(url) } catch { /* 忽略重复释放 */ } }
}

/* previewUrl 永远指向 urlCache 里的条目，因此「切换预览」只清指针不 revoke；
 * revoke 只发生在两处：taskId 变化（整批作废）与组件卸载（释放全部）。 */
function releasePreview() {
  previewUrl.value = ''
  previewIndex.value = -1
}

function clearCache() {
  for (const url of urlCache.values()) revoke(url)
  urlCache.clear()
}

async function open(i) {
  if (loading.value || !props.taskId) return
  const cached = urlCache.get(i)
  if (cached) {
    releasePreview()
    previewIndex.value = i
    previewUrl.value = cached
    previewName.value = props.artifacts[i]?.file_name || `#${i}`
    return
  }
  loading.value = true
  try {
    const res = await fetch(`/api/v1/admin/image-tasks/${encodeURIComponent(props.taskId)}/artifacts/${i}`, {
      headers: { Authorization: `Bearer ${getToken()}` },
    })
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    const blob = await res.blob()
    const url = URL.createObjectURL(blob)
    urlCache.set(i, url)
    releasePreview() // 只清指针；上一个 URL 仍在缓存中可复用
    previewIndex.value = i
    previewUrl.value = url
    previewName.value = props.artifacts[i]?.file_name || `#${i}`
  } catch (e) {
    message.error(`图片加载失败：${e.message}`)
  } finally {
    loading.value = false
  }
}

function close() {
  releasePreview()
  emit('close')
}

/* 任务切换时整个缓存作废（产物属于旧任务） */
watch(() => props.taskId, () => {
  clearCache()
  releasePreview()
})

onBeforeUnmount(() => {
  clearCache()
  releasePreview()
})
</script>

<template>
  <div class="artprev">
    <!-- 缩略图列表 -->
    <div class="thumbs">
      <button
        v-for="(a, i) in artifacts" :key="i" type="button"
        class="thumb" :class="{ active: previewIndex === i }" :disabled="loading"
        :title="a.file_name" @click="open(i)"
      >
        <!-- 缩略图直接复用缓存的 objectURL，未加载过则显示文件名占位 -->
        <img v-if="urlCache.get(i)" :src="urlCache.get(i)" :alt="a.file_name" class="thumb-img" />
        <span v-else class="thumb-ph">{{ a.file_name || `产物 #${i}` }}</span>
      </button>
    </div>

    <!-- 放大预览 -->
    <div v-if="previewUrl" class="zoom">
      <div class="zoom-head">
        <span class="zoom-name" :title="previewName">{{ previewName }}</span>
        <a-button size="small" @click="close">
          <template #icon><CloseOutlined /></template>关闭预览
        </a-button>
      </div>
      <div class="zoom-body">
        <img :src="previewUrl" :alt="previewName" class="zoom-img" />
      </div>
    </div>
  </div>
</template>

<style scoped>
.artprev { display: flex; flex-direction: column; gap: 10px; }
.thumbs { display: flex; gap: 8px; flex-wrap: wrap; }
.thumb {
  width: 72px; height: 72px; border-radius: 8px; overflow: hidden; padding: 0;
  border: 1px solid var(--hr); background: var(--card); cursor: pointer;
  display: flex; align-items: center; justify-content: center;
  transition: border-color 0.15s ease;
}
.thumb:hover:not(:disabled) { border-color: rgba(0, 90, 158, 0.55); }
.thumb.active { border-color: rgba(0, 90, 158, 0.85); }
.thumb:disabled { cursor: wait; opacity: 0.7; }
.thumb-img { width: 100%; height: 100%; object-fit: cover; display: block; }
.thumb-ph {
  font-size: 10px; color: var(--text2); padding: 4px; text-align: center;
  overflow: hidden; word-break: break-all; line-height: 1.3;
}
.zoom { border: 1px solid var(--hr); border-radius: 8px; background: var(--card); }
.zoom-head {
  display: flex; align-items: center; justify-content: space-between; gap: 8px;
  padding: 8px 12px; border-bottom: 1px solid var(--hr);
}
.zoom-name { font-size: 12px; font-weight: 500; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.zoom-body { padding: 10px; display: flex; justify-content: center; }
.zoom-img { max-width: 100%; max-height: 320px; border-radius: 4px; display: block; }
</style>
