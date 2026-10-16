<script setup>
/* 用户维度明细（补入口）
 * 接口：GET /admin/dashboard/user-breakdown
 *   实测 query：start_date / end_date（YYYY-MM-DD）/ limit（默认 50，服务端上限 200）
 *     另有可选过滤：group_id / model / model_source / endpoint / endpoint_type /
 *                   user_id / api_key_id / account_id / request_type / stream / billing_type
 *   实测响应 data：{ start_date, end_date, users: [{
 *     user_id, email, requests, input_tokens, output_tokens, cache_tokens,
 *     total_tokens, cost, actual_cost, account_cost }] }
 * 与 users-trend 的差异：这里是「按用户聚合的区间明细」（含 Token 分项与账号成本），
 * 趋势接口是按 (日期, 用户) 点位，故单独做表。 */
import { ref, computed, watch, onMounted } from 'vue'
import { ReloadOutlined } from '@ant-design/icons-vue'
import { message } from 'ant-design-vue'
import { request } from '../../api'

const props = defineProps({
  startDate: { type: String, default: '' },
  endDate: { type: String, default: '' },
})

const loading = ref(false)
const err = ref('')
const users = ref([])
const meta = ref({ start_date: '', end_date: '' })
const limit = ref(50)

const load = async () => {
  loading.value = true
  try {
    const res = await request('/admin/dashboard/user-breakdown', {
      method: 'GET',
      query: { start_date: props.startDate, end_date: props.endDate, limit: limit.value },
    })
    users.value = res?.users || []
    meta.value = { start_date: res?.start_date || props.startDate, end_date: res?.end_date || props.endDate }
    err.value = ''
  } catch (e) {
    err.value = e.message
    users.value = []
    message.error(`用户维度明细加载失败：${e.message}`)
  } finally {
    loading.value = false
  }
}
onMounted(load)
watch(() => [props.startDate, props.endDate, limit.value], load)

const totals = computed(() => ({
  users: users.value.length,
  req: users.value.reduce((s, r) => s + (Number(r.requests) || 0), 0),
  tokens: users.value.reduce((s, r) => s + (Number(r.total_tokens) || 0), 0),
  actual: users.value.reduce((s, r) => s + (Number(r.actual_cost) || 0), 0),
  account: users.value.reduce((s, r) => s + (Number(r.account_cost) || 0), 0),
}))

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
  { title: '用户', key: 'user', ellipsis: true },
  { title: '请求', key: 'requests', width: 76, align: 'right' },
  { title: 'Input', key: 'input', width: 88, align: 'right' },
  { title: 'Output', key: 'output', width: 88, align: 'right' },
  { title: 'Cache', key: 'cache', width: 88, align: 'right' },
  { title: 'Token 合计', key: 'tokens', width: 96, align: 'right' },
  { title: '标准成本', key: 'cost', width: 96, align: 'right' },
  { title: '实际成本', key: 'actual', width: 96, align: 'right' },
  { title: '账号成本', key: 'account', width: 96, align: 'right' },
]
</script>

<template>
  <div class="panel">
    <div class="phead">
      <h3>用户维度明细</h3>
      <span class="hint">GET /admin/dashboard/user-breakdown · {{ meta.start_date || '-' }} ~ {{ meta.end_date || '-' }}</span>
      <div class="actions">
        <a-select v-model:value="limit" size="small" style="width: 104px" :options="[{ value: 20, label: 'Top 20' }, { value: 50, label: 'Top 50' }, { value: 200, label: 'Top 200' }]" />
        <a-button size="small" :loading="loading" @click="load">
          <template #icon><ReloadOutlined /></template>
          刷新
        </a-button>
      </div>
    </div>

    <div v-if="err" class="empty">{{ err }}</div>
    <a-table
      v-else
      :columns="columns"
      :data-source="users"
      row-key="user_id"
      size="small"
      :pagination="false"
      :loading="loading"
      :scroll="{ x: 860, y: 288 }"
    >
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'user'">
          <span class="uname">{{ record.email || `用户 ${record.user_id}` }}</span>
          <span class="uid">#{{ record.user_id }}</span>
        </template>
        <template v-else-if="column.key === 'requests'">{{ fmt(record.requests) }}</template>
        <template v-else-if="column.key === 'input'">{{ fmtTokens(record.input_tokens) }}</template>
        <template v-else-if="column.key === 'output'">{{ fmtTokens(record.output_tokens) }}</template>
        <template v-else-if="column.key === 'cache'">{{ fmtTokens(record.cache_tokens) }}</template>
        <template v-else-if="column.key === 'tokens'"><b>{{ fmtTokens(record.total_tokens) }}</b></template>
        <template v-else-if="column.key === 'cost'"><span class="c-std">${{ fmtCost(record.cost) }}</span></template>
        <template v-else-if="column.key === 'actual'"><span class="c-ok">${{ fmtCost(record.actual_cost) }}</span></template>
        <template v-else-if="column.key === 'account'"><span class="c-warn">${{ fmtCost(record.account_cost) }}</span></template>
      </template>
      <template #empty>
        <span class="empty">该时间范围内无用户明细数据（区间 {{ meta.start_date || props.startDate }} ~ {{ meta.end_date || props.endDate }}）</span>
      </template>
    </a-table>

    <div v-if="users.length" class="foot">
      合计：{{ totals.users }} 个用户 · {{ fmt(totals.req) }} 次请求 · {{ fmtTokens(totals.tokens) }} Token · 实际 ${{ fmtCost(totals.actual) }} · 账号 ${{ fmtCost(totals.account) }}
    </div>
  </div>
</template>

<style scoped>
.panel { padding: 14px 16px; border-radius: 10px; background: var(--card); border: 1px solid var(--hr); }
.phead { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-bottom: 10px; }
.phead h3 { margin: 0; font-size: 13px; font-weight: 600; color: var(--text); }
.hint { font-size: 12px; color: var(--text2); }
.actions { margin-left: auto; display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.uname { color: var(--text); }
.uid { margin-left: 6px; font-size: 11px; color: var(--text2); }
.c-ok { color: #2f9e63; }
.c-warn { color: #d18a2d; }
.c-std { color: var(--text2); }
.foot { margin-top: 8px; font-size: 12px; color: var(--text2); }
.empty { font-size: 12px; color: var(--text2); }
</style>
