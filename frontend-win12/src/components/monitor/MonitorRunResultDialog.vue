<script setup>
/* 立即检测结果弹窗（对照旧 MonitorRunResultDialog.vue）
 * POST :id/run 响应 { results: CheckResult[] }，逐模型展示：
 * { model, status, latency_ms, ping_latency_ms, message, checked_at }
 * 替代旧骨架的单一 toast，让主模型 + 附加模型的逐项状态一目了然。 */
import { computed } from 'vue'
import { statusLabel, statusColor } from './constants'

const props = defineProps({
  open: { type: Boolean, default: false },
  results: { type: Array, default: () => [] },
})
const emit = defineEmits(['close'])

/* status 相同的附加模型可能重名，用 model+index 兜底 rowKey */
const rowKey = (r, i) => `${r.model}:${i}`

const summary = computed(() => {
  const list = props.results || []
  const ok = list.filter((r) => r.status === 'operational').length
  const bad = list.filter((r) => r.status === 'failed' || r.status === 'error').length
  return { total: list.length, ok, bad }
})
</script>

<template>
  <a-modal
    :open="open"
    title="检测结果"
    width="560px"
    wrap-class-name="monitor-run-result-modal"
    @cancel="emit('close')"
  >
    <p v-if="results.length" class="run-summary">
      共 {{ summary.total }} 个模型：正常 {{ summary.ok }} · 异常 {{ summary.bad }}
    </p>
    <div class="run-list">
      <div v-for="(r, i) in results" :key="rowKey(r, i)" class="run-row">
        <div class="run-main">
          <span class="run-model">{{ r.model }}</span>
          <span v-if="r.message" class="run-msg" :title="r.message">{{ r.message }}</span>
        </div>
        <div class="run-side">
          <a-tag :color="statusColor(r.status)" class="run-tag">{{ statusLabel(r.status) }}</a-tag>
          <span class="run-latency">{{ r.latency_ms != null ? `${r.latency_ms}ms` : '—' }}</span>
        </div>
      </div>
      <div v-if="!results.length" class="run-empty">本次检测没有返回任何模型结果</div>
    </div>
    <template #footer>
      <a-button type="primary" @click="emit('close')">关闭</a-button>
    </template>
  </a-modal>
</template>

<style scoped>
.run-summary { font-size: 12px; color: var(--text2); margin: 0 0 10px; }
.run-list { display: grid; gap: 6px; max-height: 46vh; overflow-y: auto; }
.run-row {
  display: flex; align-items: center; justify-content: space-between; gap: 12px;
  border: 1px solid var(--hr); border-radius: 8px; padding: 8px 12px;
  background: var(--card); min-height: 40px;
}
.run-main { min-width: 0; }
.run-model { font-size: 13px; font-weight: 500; }
.run-msg {
  display: block; font-size: 12px; color: var(--text2); margin-top: 1px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.run-side { display: flex; align-items: center; gap: 8px; flex-shrink: 0; }
.run-tag { margin: 0; }
.run-latency { font-size: 12px; color: var(--text2); min-width: 48px; text-align: right; }
.run-empty { padding: 24px 0; text-align: center; font-size: 12px; color: var(--text2); }
</style>
