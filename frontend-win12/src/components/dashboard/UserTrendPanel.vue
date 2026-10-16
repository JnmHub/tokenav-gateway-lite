<script setup>
/* 用户维度趋势 + 批量用户用量（补入口）
 * 接口 1：GET /admin/dashboard/users-trend
 *   实测 query：start_date / end_date（YYYY-MM-DD）/ granularity（day|hour）/ limit（默认 12）
 *   实测响应 data：{ start_date, end_date, granularity, trend: [{ date, user_id, email, username, requests, tokens, cost, actual_cost }] }
 * 接口 2：POST /admin/dashboard/users-usage
 *   实测请求体：{ user_ids: [1,2] }（仅此字段）
 *   实测响应 data：{ stats: { "2": { user_id, today_actual_cost, total_actual_cost,
 *                    by_platform: [{ platform, today_actual_cost, total_actual_cost }] } } }
 *   用量列口径固定为「今日 / 累计」，由表格勾选行去重后的 user id 触发查询。 */
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
const usage = ref({})     // user_id(字符串) → { today_actual_cost, total_actual_cost, by_platform }
const selected = ref([])  // 行 key：`${date}#${user_id}`
const limit = ref(12)

const rowKey = (r) => `${r.date}#${r.user_id}`
const userLabel = (p) => (p.username && String(p.username).trim()) || (p.email && String(p.email).trim()) || `用户 ${p.user_id}`
const rows = computed(() => trend.value.map((r) => ({ ...r, usage: usage.value[String(r.user_id)] || null })))
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
const totals = computed(() => ({
  users: new Set(trend.value.map((r) => r.user_id)).size,
  req: trend.value.reduce((s, r) => s + (Number(r.requests) || 0), 0),
  tokens: trend.value.reduce((s, r) => s + (Number(r.tokens) || 0), 0),
  actual: trend.value.reduce((s, r) => s + (Number(r.actual_cost) || 0), 0),
}))
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
    const res = await request('/admin/dashboard/users-trend', {
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
    message.error(`用户维度趋势加载失败：${e.message}`)
  } finally {
    loading.value = false
  }
}
onMounted(load)
watch(() => [props.startDate, props.endDate, props.granularity, limit.value], load)

const fetchUsage = async () => {
  const ids = selectedIds.value
  if (!ids.length) { message.warning('请先勾选要查询的用户行'); return }
  usageLoading.value = true
  try {
    const res = await request('/admin/dashboard/users-usage', { body: { user_ids: ids } })
    usage.value = res?.stats || {}
    message.success(`已获取 ${ids.length} 个用户的用量`)
  } catch (e) {
    message.error(`用户用量查询失败：${e.message}`)
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
const platforms = (u) => (Array.isArray(u?.by_platform) ? u.by_platform : [])

const columns = [
  { title: '日期', dataIndex: 'date', key: 'date', width: 104 },
  { title: '用户', key: 'user', ellipsis: true },
  { title: '请求', key: 'requests', width: 76, align: 'right' },
  { title: 'Token', key: 'tokens', width: 96, align: 'right' },
  { title: '区间实际成本', key: 'actual', width: 116, align: 'right' },
  { title: '今日实际成本', key: 'today', width: 112, align: 'right' },
  { title: '累计实际成本', key: 'total', width: 112, align: 'right' },
  { title: '平台明细（今日/累计）', key: 'platform', width: 200 },
]
</script>

<template>
  <div class="panel">
    <div class="phead">
      <h3>用户维度趋势</h3>
      <span class="hint">GET /admin/dashboard/users-trend · {{ meta.start_date || '-' }} ~ {{ meta.end_date || '-' }} · {{ meta.granularity }}</span>
      <div class="actions">
        <a-select v-model:value="limit" size="small" style="width: 104px" :options="[{ value: 12, label: 'Top 12' }, { value: 24, label: 'Top 24' }, { value: 50, label: 'Top 50' }]" />
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
        查询选中用户用量（{{ selectedIds.length }}）
      </a-button>
      <span class="hint">POST /admin/dashboard/users-usage · 仅支持 { user_ids: [] }，返回「今日 / 累计 + 平台明细」</span>
    </div>

    <div v-if="usageTotals.count" class="sum">
      已查询 {{ usageTotals.count }} 个用户：今日实际成本 ${{ fmtCost(usageTotals.today) }} · 累计实际成本 ${{ fmtCost(usageTotals.total) }}
    </div>
    <div v-else-if="selected.length" class="sum">已勾选 {{ selectedIds.length }} 个用户，点「查询选中用户用量」获取今日/累计成本</div>

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
      :scroll="{ x: 940, y: 288 }"
    >
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'user'">
          <span class="uname">{{ userLabel(record) }}</span>
          <span class="uid">#{{ record.user_id }}</span>
        </template>
        <template v-else-if="column.key === 'requests'">{{ fmt(record.requests) }}</template>
        <template v-else-if="column.key === 'tokens'">{{ fmtTokens(record.tokens) }}</template>
        <template v-else-if="column.key === 'actual'"><span class="c-ok">${{ fmtCost(record.actual_cost) }}</span></template>
        <template v-else-if="column.key === 'today'">
          <span v-if="record.usage" class="c-ok">${{ fmtCost(record.usage.today_actual_cost) }}</span>
          <span v-else class="dim">未查询</span>
        </template>
        <template v-else-if="column.key === 'total'">
          <span v-if="record.usage" class="c-ok">${{ fmtCost(record.usage.total_actual_cost) }}</span>
          <span v-else class="dim">-</span>
        </template>
        <template v-else-if="column.key === 'platform'">
          <template v-if="record.usage && platforms(record.usage).length">
            <span v-for="p in platforms(record.usage)" :key="p.platform" class="pf">
              {{ p.platform }} {{ fmtCost(p.today_actual_cost) }} / {{ fmtCost(p.total_actual_cost) }}
            </span>
          </template>
          <span v-else class="dim">{{ record.usage ? '无平台明细' : '-' }}</span>
        </template>
      </template>
      <template #empty>
        <span class="empty">该时间范围内无用户维度数据（区域 {{ meta.start_date || props.startDate }} ~ {{ meta.end_date || props.endDate }}）</span>
      </template>
    </a-table>

    <div v-if="rows.length" class="foot">
      本区间合计：{{ totals.users }} 个用户 · {{ fmt(totals.req) }} 次请求 · {{ fmtTokens(totals.tokens) }} Token · 实际成本 ${{ fmtCost(totals.actual) }}
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
.uname { color: var(--text); }
.uid { margin-left: 6px; font-size: 11px; color: var(--text2); }
.c-ok { color: #2f9e63; }
.dim { color: var(--text2); font-size: 11px; }
.pf { display: inline-block; margin: 0 4px 2px 0; padding: 0 6px; font-size: 11px; color: var(--text2); border: 1px solid var(--hr); border-radius: 4px; }
.foot { margin-top: 8px; font-size: 12px; color: var(--text2); }
.empty { font-size: 12px; color: var(--text2); }
</style>
