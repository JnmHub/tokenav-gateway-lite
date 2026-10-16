<script setup>
/* Key 维度趋势 + 批量 Key 用量（补入口）
 * 接口 1：GET /admin/dashboard/api-keys-trend
 *   实测 query：start_date / end_date（YYYY-MM-DD）/ granularity（day|hour）/ limit（默认 5）
 *   实测响应 data：{ start_date, end_date, granularity, trend: [{ date, api_key_id, key_name, requests, tokens }] }
 * 接口 2：POST /admin/dashboard/api-keys-usage
 *   实测请求体：{ api_key_ids: [1,2,3] }（仅此字段；多传 start_date/end_date 会被忽略）
 *   实测响应 data：{ stats: { "1": { api_key_id, today_actual_cost, total_actual_cost } } }
 *   因此用量列口径固定为「今日 / 累计」，由表格勾选行去重后的 key id 触发查询。 */
import { ref, computed, watch, onMounted } from 'vue'
import { ReloadOutlined, SearchOutlined } from '@ant-design/icons-vue'
import { message } from 'ant-design-vue'
import { request } from '../../api'

const props = defineProps({
  startDate: { type: String, default: '' },
  endDate: { type: String, default: '' },
  granularity: { type: String, default: 'day' },
})

const loading = ref(false)
const usageLoading = ref(false)
const err = ref('')
const trend = ref([])
const meta = ref({ start_date: '', end_date: '', granularity: '' })
const usage = ref({})        // api_key_id(字符串) → { today_actual_cost, total_actual_cost }
const selected = ref([])     // 行 key：`${date}#${api_key_id}`
const limit = ref(10)

const rowKey = (r) => `${r.date}#${r.api_key_id}`
const rows = computed(() => trend.value.map((r) => ({ ...r, usage: usage.value[String(r.api_key_id)] || null })))
// 勾选行去重成 key id 列表（同一 Key 可能出现在多天）
const selectedIds = computed(() => {
  const set = new Set()
  for (const k of selected.value) {
    const id = Number(String(k).split('#')[1])
    if (Number.isFinite(id)) set.add(id)
  }
  return [...set]
})
const rowSelection = computed(() => ({
  selectedRowKeys: selected.value,
  preserveSelectedRowKeys: true,
  onChange: (keys) => { selected.value = keys },
}))
const totals = computed(() => {
  const req = trend.value.reduce((s, r) => s + (Number(r.requests) || 0), 0)
  const tokens = trend.value.reduce((s, r) => s + (Number(r.tokens) || 0), 0)
  return { req, tokens, keys: new Set(trend.value.map((r) => r.api_key_id)).size }
})
const usageTotals = computed(() => {
  const list = Object.values(usage.value || {})
  return {
    count: list.length,
    today: list.reduce((s, u) => s + (Number(u?.today_actual_cost) || 0), 0),
    total: list.reduce((s, u) => s + (Number(u?.total_actual_cost) || 0), 0),
  }
})

const load = async () => {
  loading.value = true
  try {
    const res = await request('/admin/dashboard/api-keys-trend', {
      method: 'GET',
      query: { start_date: props.startDate, end_date: props.endDate, granularity: props.granularity || 'day', limit: limit.value },
    })
    trend.value = res?.trend || []
    meta.value = { start_date: res?.start_date || props.startDate, end_date: res?.end_date || props.endDate, granularity: res?.granularity || props.granularity }
    selected.value = []
    usage.value = {}
    err.value = ''
  } catch (e) {
    err.value = e.message
    trend.value = []
    message.error(`Key 维度趋势加载失败：${e.message}`)
  } finally {
    loading.value = false
  }
}
onMounted(load)
watch(() => [props.startDate, props.endDate, props.granularity, limit.value], load)

const fetchUsage = async () => {
  const ids = selectedIds.value
  if (!ids.length) { message.warning('请先勾选要查询的 Key 行'); return }
  usageLoading.value = true
  try {
    const res = await request('/admin/dashboard/api-keys-usage', { body: { api_key_ids: ids } })
    usage.value = res?.stats || {}
    message.success(`已获取 ${ids.length} 个 Key 的用量`)
  } catch (e) {
    message.error(`Key 用量查询失败：${e.message}`)
  } finally {
    usageLoading.value = false
  }
}
const selectAll = () => { selected.value = trend.value.map(rowKey) }
const clearAll = () => { selected.value = [] }

const fmt = (n) => (n === null || n === undefined || n === '' ? '-' : Number(n).toLocaleString('zh-CN'))
const fmtTokens = (n) => {
  const v = Number(n)
  if (n === null || n === undefined || !Number.isFinite(v)) return '-'
  if (Math.abs(v) >= 1e9) return (v / 1e9).toFixed(2) + 'B'
  if (Math.abs(v) >= 1e6) return (v / 1e6).toFixed(2) + 'M'
  if (Math.abs(v) >= 1e3) return (v / 1e3).toFixed(1) + 'K'
  return String(Math.round(v))
}
const fmtCost = (v) => {
  const n = Number(v)
  if (v === null || v === undefined || !Number.isFinite(n)) return '0.00'
  if (Math.abs(n) >= 1000) return (n / 1000).toFixed(2) + 'K'
  if (Math.abs(n) >= 1) return n.toFixed(2)
  if (Math.abs(n) >= 0.01) return n.toFixed(3)
  return n.toFixed(4)
}

const columns = [
  { title: '日期', dataIndex: 'date', key: 'date', width: 104 },
  { title: 'API Key', key: 'key', ellipsis: true },
  { title: '请求', key: 'requests', width: 76, align: 'right' },
  { title: 'Token', key: 'tokens', width: 96, align: 'right' },
  { title: '今日实际成本', key: 'today', width: 112, align: 'right' },
  { title: '累计实际成本', key: 'total', width: 112, align: 'right' },
]
</script>

<template>
  <div class="panel">
    <div class="phead">
      <h3>Key 维度趋势</h3>
      <span class="hint">GET /admin/dashboard/api-keys-trend · {{ meta.start_date || '-' }} ~ {{ meta.end_date || '-' }} · {{ meta.granularity }}</span>
      <div class="actions">
        <a-select v-model:value="limit" size="small" style="width: 96px" :options="[{ value: 5, label: 'Top 5' }, { value: 10, label: 'Top 10' }, { value: 20, label: 'Top 20' }]" />
        <a-button size="small" :loading="loading" @click="load">
          <template #icon><ReloadOutlined /></template>
          刷新
        </a-button>
      </div>
    </div>

    <div class="bar">
      <a-button size="small" @click="selectAll">勾选全部行</a-button>
      <a-button size="small" :disabled="!selected.length" @click="clearAll">清除勾选</a-button>
      <a-button size="small" type="primary" :loading="usageLoading" :disabled="!selectedIds.length" @click="fetchUsage">
        <template #icon><SearchOutlined /></template>
        查询选中 Key 用量（{{ selectedIds.length }}）
      </a-button>
      <span class="hint">POST /admin/dashboard/api-keys-usage · 仅支持 { api_key_ids: [] }，返回「今日 / 累计」两个口径</span>
    </div>

    <div v-if="usageTotals.count" class="sum">
      已查询 {{ usageTotals.count }} 个 Key：今日实际成本 ${{ fmtCost(usageTotals.today) }} · 累计实际成本 ${{ fmtCost(usageTotals.total) }}
    </div>
    <div v-else-if="selected.length" class="sum">已勾选 {{ selectedIds.length }} 个 Key，点「查询选中 Key 用量」获取今日/累计成本</div>

    <div v-if="err" class="empty">{{ err }}</div>
    <a-table
      v-else
      :columns="columns"
      :data-source="rows"
      :row-key="rowKey"
      :row-selection="rowSelection"
      size="small"
      :pagination="false"
      :loading="loading"
      :scroll="{ x: 700, y: 288 }"
    >
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'key'">
          <span class="keyname">{{ record.key_name || `Key ${record.api_key_id}` }}</span>
          <span class="keyid">#{{ record.api_key_id }}</span>
        </template>
        <template v-else-if="column.key === 'requests'">{{ fmt(record.requests) }}</template>
        <template v-else-if="column.key === 'tokens'">{{ fmtTokens(record.tokens) }}</template>
        <template v-else-if="column.key === 'today'">
          <span v-if="record.usage" class="c-ok">${{ fmtCost(record.usage.today_actual_cost) }}</span>
          <span v-else class="dim">未查询</span>
        </template>
        <template v-else-if="column.key === 'total'">
          <span v-if="record.usage" class="c-ok">${{ fmtCost(record.usage.total_actual_cost) }}</span>
          <span v-else class="dim">-</span>
        </template>
      </template>
      <template #empty>
        <span class="empty">该时间范围内无 Key 维度数据（区域 {{ meta.start_date || props.startDate }} ~ {{ meta.end_date || props.endDate }}）</span>
      </template>
    </a-table>

    <div v-if="rows.length" class="foot">
      本区间合计：{{ totals.keys }} 个 Key · {{ fmt(totals.req) }} 次请求 · {{ fmtTokens(totals.tokens) }} Token
    </div>
  </div>
</template>

<style scoped>
.panel { padding: 14px 16px; border-radius: 10px; background: var(--card); border: 1px solid var(--hr); }
.phead { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-bottom: 10px; }
.phead h3 { margin: 0; font-size: 13px; font-weight: 600; color: var(--text); }
.hint { font-size: 12px; color: var(--text2); }
.actions { margin-left: auto; display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.bar { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-bottom: 10px; }
.sum { font-size: 12px; color: var(--text2); margin-bottom: 8px; }
.keyname { color: var(--text); }
.keyid { margin-left: 6px; font-size: 11px; color: var(--text2); }
.c-ok { color: #2f9e63; }
.dim { color: var(--text2); font-size: 11px; }
.foot { margin-top: 8px; font-size: 12px; color: var(--text2); }
.empty { font-size: 12px; color: var(--text2); }
</style>
