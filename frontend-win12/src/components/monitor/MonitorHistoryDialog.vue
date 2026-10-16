<script setup>
/* ============================================================
 * 渠道监控历史记录弹窗（Win12 重构版）
 * GET /admin/channel-monitors/:id/history?model=&limit=
 * 响应 { items: [{ id, model, status, latency_ms, ping_latency_ms, message, checked_at }] }
 * 模型筛选：模型清单来自行数据 extra_models（主模型永远置顶），
 * 选中后按后端 model query 精确过滤。
 * ============================================================ */
import { ref, computed, watch } from 'vue'
import { message } from 'ant-design-vue'
import { HistoryOutlined } from '@ant-design/icons-vue'
import { getMonitorHistory } from './api'
import { statusLabel, statusColor, fmtTime } from './constants'

const props = defineProps({
  open: { type: Boolean, default: false },
  monitor: { type: Object, default: null }, // 行数据（含 id/name/primary_model/extra_models）
})
const emit = defineEmits(['close'])

const loading = ref(false)
const rows = ref([])
const modelFilter = ref('') // '' = 全部（后端 model 为空即不过滤）
const LIMIT = 100 // 后端 parseHistoryLimit：空/非法回退默认 50，上限 200

/* 可选模型 = 主模型 + 附加模型（去重）；'' 代表全部 */
const modelOptions = computed(() => {
  const list = [props.monitor?.primary_model, ...(props.monitor?.extra_models || [])]
    .filter((m, i, arr) => m && arr.indexOf(m) === i)
  return [{ value: '', label: '全部模型' }, ...list.map((m) => ({ value: m, label: m }))]
})

async function load() {
  if (!props.monitor?.id) return
  loading.value = true
  try {
    const query = { limit: LIMIT }
    if (modelFilter.value) query.model = modelFilter.value
    const data = await getMonitorHistory(props.monitor.id, query)
    rows.value = data.items || []
  } catch (e) {
    message.error(`历史记录读取失败：${e.message}`)
    rows.value = []
  } finally {
    loading.value = false
  }
}

watch(
  () => [props.open, props.monitor?.id],
  ([open]) => {
    if (!open) return
    modelFilter.value = ''
    rows.value = []
    load()
  },
)

const columns = [
  { title: '模型', dataIndex: 'model', key: 'model', ellipsis: true },
  { title: '状态', key: 'status', width: 76 },
  { title: '延迟', key: 'latency', width: 80, align: 'right' },
  { title: 'Ping', key: 'ping', width: 70, align: 'right' },
  { title: '信息', dataIndex: 'message', key: 'message', ellipsis: true },
  { title: '检测时间', key: 'checked_at', width: 110, align: 'right' },
]
</script>

<template>
  <a-modal
    :open="open"
    :title="`检测历史${monitor?.name ? ` · ${monitor.name}` : ''}`"
    width="720px"
    :footer="null"
    @cancel="emit('close')"
  >
    <div class="mh-bar">
      <a-select
        v-model:value="modelFilter"
        :options="modelOptions"
        style="width: 220px"
        size="small"
        @change="load"
      />
      <span class="mh-count">共 {{ rows.length }} 条</span>
      <span style="flex: 1"></span>
      <a-button size="small" :loading="loading" @click="load">
        <template #icon><HistoryOutlined /></template>刷新
      </a-button>
    </div>
    <a-table
      :columns="columns"
      :data-source="rows"
      :loading="loading"
      row-key="id"
      size="small"
      :scroll="{ x: 'max-content', y: 380 }"
      :pagination="{ pageSize: 10, size: 'small', showTotal: (t) => `共 ${t} 条` }"
    >
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'status'">
          <a-tag :color="statusColor(record.status)" class="mh-tag">{{ statusLabel(record.status) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'latency'">
          {{ record.latency_ms != null ? `${record.latency_ms}ms` : '—' }}
        </template>
        <template v-else-if="column.key === 'ping'">
          {{ record.ping_latency_ms != null ? `${record.ping_latency_ms}ms` : '—' }}
        </template>
        <template v-else-if="column.key === 'message'">
          <span class="mh-msg" :title="record.message">{{ record.message || '—' }}</span>
        </template>
        <template v-else-if="column.key === 'checked_at'">{{ fmtTime(record.checked_at) }}</template>
      </template>
      <template #emptyText>
        <div class="mh-empty">该监控暂无检测历史记录</div>
      </template>
    </a-table>
  </a-modal>
</template>

<style scoped>
.mh-bar { display: flex; align-items: center; gap: 8px; margin-bottom: 10px; }
.mh-count { font-size: 12px; color: var(--text2); }
.mh-tag { margin-inline-end: 0; border-radius: 4px; }
.mh-msg { font-size: 12px; color: var(--text2); }
.mh-empty { padding: 24px 0; font-size: 12px; color: var(--text2); }
</style>
