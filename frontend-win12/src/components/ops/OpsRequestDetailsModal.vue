<script setup>
/* 请求明细弹窗（三级下钻·第三级）
 * 契约来源：旧 OpsRequestDetailsModal.vue：
 * - GET /admin/ops/requests：start_time/end_time（成对必传，按 time_range 换算）+
 *   page/page_size/kind(all|success|error)/sort(created_at_desc|duration_desc)/platform/group_id
 * - 响应 { items: [{ kind, created_at, request_id, platform, model, duration_ms,
 *     status_code, error_id, phase, severity, message, stream, user_id, api_key_id,
 *     account_id, group_id }], total, page, page_size, pages }
 * - 8 列：时间/类型/平台/模型/耗时/状态/request_id(可复制)/操作(错误→查看错误详情)
 * - 时间范围变化时以“当前时刻”重算窗口（旧实现 buildTimeParams 同语义） */
import { ref, computed, watch } from 'vue'
import { message } from 'ant-design-vue'
import { ReloadOutlined, CopyOutlined } from '@ant-design/icons-vue'
import { request } from '../../api'

const props = defineProps({
  open: { type: Boolean, default: false },
  title: { type: String, default: '请求明细' },
  timeRange: { type: String, default: '1h' },
  customStart: { type: String, default: '' },
  customEnd: { type: String, default: '' },
  platform: { type: String, default: '' },
  groupId: { type: Number, default: null },
  preset: { type: Object, default: () => ({ kind: 'all', sort: 'created_at_desc' }) },
})
const emit = defineEmits(['update:open', 'openErrorDetail'])

const loading = ref(false)
const items = ref([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(10)

const TR_MIN = { '5m': 5, '30m': 30, '1h': 60, '6h': 360, '24h': 1440 }
const buildTime = () => {
  if (props.timeRange === 'custom' && props.customStart && props.customEnd) {
    return { start_time: props.customStart, end_time: props.customEnd }
  }
  const min = TR_MIN[props.timeRange] || 60
  return {
    start_time: new Date(Date.now() - min * 60000).toISOString(),
    end_time: new Date().toISOString(),
  }
}

const fetchRows = async () => {
  if (!props.open) return
  loading.value = true
  try {
    const query = {
      ...buildTime(),
      page: page.value, page_size: pageSize.value,
      kind: props.preset.kind || 'all',
      sort: props.preset.sort || 'created_at_desc',
    }
    if (props.platform) query.platform = props.platform
    if (props.groupId) query.group_id = props.groupId
    const data = await request('/admin/ops/requests', { method: 'GET', query })
    items.value = data.items || []
    total.value = data.total || 0
  } catch (e) {
    items.value = []; total.value = 0
    message.error(`请求明细加载失败：${e.message}`)
  } finally { loading.value = false }
}

watch(() => props.open, (v) => { if (v) { page.value = 1; fetchRows() } })
watch(() => [props.timeRange, props.customStart, props.customEnd, props.platform, props.groupId, props.preset], () => {
  if (!props.open) return
  page.value = 1; fetchRows()
})

const copyId = async (id) => {
  try { await navigator.clipboard.writeText(id); message.success('request_id 已复制') }
  catch { message.error('复制失败') }
}
const openError = (row) => {
  if (!row.error_id) return
  emit('update:open', false)
  emit('openErrorDetail', row.error_id, 'request')
}

const fmtTime = (t) => (t ? String(t).replace('T', ' ').slice(5, 19) : '-')
const rangeLabel = computed(() => {
  if (props.timeRange === 'custom') return '自定义范围'
  const min = TR_MIN[props.timeRange] || 60
  return min >= 60 ? `近 ${Math.round(min / 60)} 小时` : `近 ${min} 分钟`
})

const columns = [
  { title: '时间', key: 'time', width: 112 },
  { title: '类型', key: 'kind', width: 68 },
  { title: '平台', key: 'platform', width: 82 },
  { title: '模型', dataIndex: 'model', key: 'model', width: 160, ellipsis: true },
  { title: '耗时', key: 'dur', width: 88 },
  { title: '状态', key: 'code', width: 72 },
  { title: 'request_id', key: 'rid', width: 250 },
  { title: '操作', key: 'act', width: 92 },
]
</script>

<template>
  <a-modal
    :open="open" width="1080px" style="top: 40px" :footer="null"
    :title="title" @cancel="emit('update:open', false)"
  >
    <div class="bar">
      <span class="cnt">{{ rangeLabel }} · 共 {{ total.toLocaleString('zh-CN') }} 条</span>
      <div style="flex: 1"></div>
      <a-button size="small" :loading="loading" @click="fetchRows"><template #icon><ReloadOutlined /></template></a-button>
    </div>
    <a-table
      :columns="columns" :data-source="items" row-key="request_id" size="small" :loading="loading"
      :scroll="{ x: 'max-content' }" :pagination="{
        current: page, pageSize, total, showSizeChanger: false, showTotal: (t) => `共 ${t} 条`,
        onChange: (p) => { page = p; fetchRows() },
      }"
    >
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'time'">{{ fmtTime(record.created_at) }}</template>
        <template v-else-if="column.key === 'kind'">
          <a-tag :color="record.kind === 'error' ? 'red' : 'green'" style="margin: 0">{{ record.kind === 'error' ? '错误' : '成功' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'platform'">{{ (record.platform || 'unknown').toUpperCase() }}</template>
        <template v-else-if="column.key === 'dur'">{{ typeof record.duration_ms === 'number' ? `${record.duration_ms} ms` : '-' }}</template>
        <template v-else-if="column.key === 'code'">
          <a-tag v-if="record.status_code" :color="record.status_code >= 500 ? 'red' : record.status_code >= 400 ? 'orange' : 'green'" style="margin: 0">{{ record.status_code }}</a-tag>
          <span v-else>-</span>
        </template>
        <template v-else-if="column.key === 'rid'">
          <div v-if="record.request_id" class="rid">
            <span class="rid-txt" :title="record.request_id">{{ record.request_id }}</span>
            <a-button size="small" type="text" style="height: 22px; padding: 0 4px" @click="copyId(record.request_id)">
              <template #icon><CopyOutlined /></template>
            </a-button>
          </div>
          <span v-else>-</span>
        </template>
        <template v-else-if="column.key === 'act'">
          <a v-if="record.kind === 'error' && record.error_id" style="font-size: 12px" @click="openError(record)">查看错误</a>
          <span v-else style="color: var(--text2)">-</span>
        </template>
      </template>
    </a-table>
  </a-modal>
</template>

<style scoped>
.bar { display: flex; align-items: center; gap: 8px; margin-bottom: 10px; }
.cnt { font-size: 12px; color: var(--text2); }
.rid { display: flex; align-items: center; gap: 4px; min-width: 0; }
.rid-txt { min-width: 0; max-width: 190px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-family: Consolas, monospace; font-size: 11px; }
</style>
