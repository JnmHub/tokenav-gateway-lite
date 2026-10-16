<script setup>
/* Codex 尾部爆发统计整页（三期）：a-drawer 内整页形态
 * 契约来源：旧 frontend/src/views/admin/ops/CodexTailBurstStatsView.vue + frontend/src/api/admin/ops.ts
 * - GET /admin/ops/codex-tail-burst/stats
 *   参数：start_time / end_time（ISO，end 为结束日次日零点 = 排他上界，覆盖全天记录）
 *         account_id / window(5h|7d) / reason(used_ratio|reset_soon|account_expiry)
 *         status(active|settled) / page / page_size（后端上限 100）
 *   响应：summary{sessions,active_sessions,settled_sessions,accounts,requests,tokens,delta_percent}
 *         trend[{date,sessions,requests,tokens,delta_percent}]
 *         items[{session_id,account_id,account_name,started_at,ended_at,window,reason,
 *                entry_percent,delta_percent,requests,tokens,status}]
 *         total/page/page_size/pages
 * - 日期范围默认 30 天（今天-29 ~ 今天），与旧实现一致 */
import { ref, computed, watch, nextTick, onUnmounted } from 'vue'
import dayjs from 'dayjs'
import { message } from 'ant-design-vue'
import { ReloadOutlined, FireOutlined, ClockCircleOutlined, BarChartOutlined, RiseOutlined } from '@ant-design/icons-vue'
import * as echarts from 'echarts/core'
import { LineChart } from 'echarts/charts'
import { GridComponent, TooltipComponent, LegendComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import { request } from '../../api'

echarts.use([LineChart, GridComponent, TooltipComponent, LegendComponent, CanvasRenderer])

const props = defineProps({ open: { type: Boolean, default: false } })
const emit = defineEmits(['update:open'])

/* ---------- 筛选状态（枚举与旧实现一致） ---------- */
const today = dayjs()
const defaultRange = [today.subtract(29, 'day'), today]
const dateRange = ref(defaultRange.map((d) => d.format('YYYY-MM-DD')))
const accountInput = ref(null)
const windowFilter = ref('')
const reasonFilter = ref('')
const statusFilter = ref('')

const WINDOW_OPTS = [{ value: '', label: '全部窗口' }, { value: '5h', label: '5h' }, { value: '7d', label: '7d' }]
const REASON_OPTS = [
  { value: '', label: '全部原因' },
  { value: 'used_ratio', label: '用量占比跃升' },
  { value: 'reset_soon', label: '配额即将重置' },
  { value: 'account_expiry', label: '账号临期' },
]
const STATUS_OPTS = [
  { value: '', label: '全部状态' },
  { value: 'active', label: '进行中' },
  { value: 'settled', label: '已结算' },
]

/* ---------- 数据 ---------- */
const loading = ref(false)
const resp = ref(null)
const items = computed(() => resp.value?.items || [])
const trend = computed(() => resp.value?.trend || [])
const summary = computed(() => resp.value?.summary || {})
const total = computed(() => Number(resp.value?.total || 0))
const page = ref(1)
const pageSize = ref(20)

const buildQuery = () => {
  const q = {
    // 起始日零点；结束日取次日零点作为排他上界（旧实现同策略，避免 23:59:59.999 后记录漏掉）
    start_time: dayjs(dateRange.value[0]).startOf('day').toISOString(),
    end_time: dayjs(dateRange.value[1]).add(1, 'day').startOf('day').toISOString(),
    page: page.value,
    page_size: pageSize.value,
  }
  if (accountInput.value && Number(accountInput.value) > 0) q.account_id = Number(accountInput.value)
  if (windowFilter.value) q.window = windowFilter.value
  if (reasonFilter.value) q.reason = reasonFilter.value
  if (statusFilter.value) q.status = statusFilter.value
  return q
}

const load = async () => {
  loading.value = true
  try {
    resp.value = await request('/admin/ops/codex-tail-burst/stats', { method: 'GET', query: buildQuery() })
  } catch (e) { message.error(`加载尾部爆发统计失败：${e.message}`) }
  finally { loading.value = false }
}

const applyFilters = () => { page.value = 1; load() }
const resetFilters = () => {
  dateRange.value = defaultRange.map((d) => d.format('YYYY-MM-DD'))
  accountInput.value = null; windowFilter.value = ''; reasonFilter.value = ''; statusFilter.value = ''
  page.value = 1
  load()
}
const onPage = (p, ps) => {
  page.value = p
  if (ps && ps !== pageSize.value) { pageSize.value = ps; page.value = 1 }
  load()
}

/* ---------- 汇总卡（4 张，字段与旧实现一致） ---------- */
const fmtInt = (v) => new Intl.NumberFormat('zh-CN').format(Number(v || 0))
const fmtPct = (v) => `${Number(v || 0).toFixed(2)}%`
const summaryCards = computed(() => [
  { key: 'sessions', label: '爆发会话', value: fmtInt(summary.value.sessions), icon: FireOutlined, color: '#f59e0b' },
  { key: 'active', label: '进行中会话', value: fmtInt(summary.value.active_sessions), icon: ClockCircleOutlined, color: '#3b82f6' },
  { key: 'requests', label: '爆发请求数', value: fmtInt(summary.value.requests), icon: BarChartOutlined, color: '#10b981' },
  { key: 'delta', label: '平均增量', value: fmtPct(summary.value.delta_percent), icon: RiseOutlined, color: '#8b5cf6' },
])

/* ---------- 展示辅助 ---------- */
const fmtTime = (t) => (t ? String(t).replace('T', ' ').slice(5, 19) : '-')
const fmtTokens = (v) => {
  const n = Number(v || 0)
  if (n >= 1e9) return `${(n / 1e9).toFixed(2)}B`
  if (n >= 1e6) return `${(n / 1e6).toFixed(2)}M`
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}K`
  return String(n)
}
const REASON_LABEL = { used_ratio: '用量占比跃升', reset_soon: '配额即将重置', account_expiry: '账号临期' }
const fmtReason = (v) => REASON_LABEL[v] || v || '-'
const fmtWindow = (v) => (v === '5h' || v === '7d' ? v : '-')

const columns = [
  { title: '账号', key: 'account', width: 170 },
  { title: '开始时间', key: 'started', width: 150 },
  { title: '窗口', key: 'window', width: 70 },
  { title: '原因', key: 'reason', width: 120 },
  { title: '增量', key: 'delta', width: 90 },
  { title: '请求数', key: 'requests', width: 90 },
  { title: 'Tokens', key: 'tokens', width: 100 },
  { title: '状态', key: 'status', width: 90 },
]

/* ---------- 趋势图（3 系列：会话/请求 左轴计数，增量 右轴百分比虚线，与旧实现同构） ---------- */
const chartEl = ref(null)
let chart = null
const AXIS_TXT = '#8a8a8a'
const SPLIT_LN = 'rgba(128,128,128,.18)'
const renderChart = () => {
  if (!chartEl.value) return
  const pts = trend.value
  const mk = (name, key, color, extra = {}) => ({
    name, type: 'line', smooth: true, showSymbol: false, data: pts.map((p) => p[key] ?? 0),
    itemStyle: { color }, lineStyle: { width: 2, color, ...(extra.dash ? { type: 'dashed' } : {}) }, ...extra,
  })
  const option = pts.length ? {
    grid: { left: 44, right: 52, top: 32, bottom: 24 },
    legend: { top: 0, left: 'center', icon: 'circle', itemWidth: 8, itemHeight: 8, textStyle: { color: AXIS_TXT, fontSize: 11 } },
    tooltip: { trigger: 'axis', confine: true },
    xAxis: { type: 'category', data: pts.map((p) => String(p.date || '').slice(5)), axisLabel: { color: AXIS_TXT, fontSize: 10 }, axisLine: { lineStyle: { color: SPLIT_LN } }, axisTick: { show: false } },
    yAxis: [
      { type: 'value', minInterval: 1, axisLabel: { color: AXIS_TXT, fontSize: 10 }, splitLine: { lineStyle: { color: SPLIT_LN } } },
      { type: 'value', position: 'right', splitLine: { show: false }, axisLabel: { color: '#8b5cf6', fontSize: 10, formatter: '{value}%' } },
    ],
    series: [
      { ...mk('会话数', 'sessions', '#f59e0b'), areaStyle: { color: { type: 'linear', x: 0, y: 0, x2: 0, y2: 1, colorStops: [{ offset: 0, color: '#f59e0b33' }, { offset: 1, color: '#f59e0b00' }] } } },
      mk('请求数', 'requests', '#10b981'),
      mk('增量 %', 'delta_percent', '#8b5cf6', { yAxisIndex: 1, dash: true }),
    ],
  } : {
    xAxis: { show: false }, yAxis: [{ show: false }, { show: false }], series: [],
    graphic: [{ type: 'text', left: 'center', top: 'middle', style: { text: '暂无趋势数据', fill: AXIS_TXT, fontSize: 12 } }],
  }
  if (!chart) chart = echarts.init(chartEl.value)
  chart.setOption(option, true)
}
const onResize = () => chart?.resize()
watch(() => [props.open, resp.value], ([open]) => {
  if (!open) return
  nextTick(() => { renderChart(); window.addEventListener('resize', onResize) })
})
const close = () => {
  window.removeEventListener('resize', onResize)
  emit('update:open', false)
}
onUnmounted(() => { window.removeEventListener('resize', onResize); chart?.dispose(); chart = null })

/* 抽屉打开即拉取首屏；关闭再打开保持筛选并刷新 */
watch(() => props.open, (v) => { if (v && !resp.value) load() })

/* 抽屉宽度：桌面 94%，窄屏全屏 */
const drawerWidth = ref(window.innerWidth < 760 ? '100%' : '94%')
const onWinResize = () => { drawerWidth.value = window.innerWidth < 760 ? '100%' : '94%' }
window.addEventListener('resize', onWinResize)
onUnmounted(() => window.removeEventListener('resize', onWinResize))
</script>

<template>
  <a-drawer :open="open" :width="drawerWidth" title="Codex 尾部爆发统计" class="tb-drawer" @close="close">
    <div class="tb">
      <!-- 筛选区：日期范围 + 账号 ID + 窗口 + 原因 + 状态 + 重置/应用 -->
      <div class="flt card">
        <div class="f-item">
          <label>日期范围（默认 30 天）</label>
          <a-range-picker v-model:value="dateRange" value-format="YYYY-MM-DD" format="YYYY-MM-DD" size="small" style="width: 230px" :allow-clear="false" />
        </div>
        <div class="f-item">
          <label>账号 ID</label>
          <a-input-number v-model:value="accountInput" :min="1" size="small" placeholder="全部账号" style="width: 110px" @press-enter="applyFilters" />
        </div>
        <div class="f-item">
          <label>窗口</label>
          <a-select v-model:value="windowFilter" :options="WINDOW_OPTS" size="small" style="width: 104px" />
        </div>
        <div class="f-item">
          <label>原因</label>
          <a-select v-model:value="reasonFilter" :options="REASON_OPTS" size="small" style="width: 128px" />
        </div>
        <div class="f-item">
          <label>状态</label>
          <a-select v-model:value="statusFilter" :options="STATUS_OPTS" size="small" style="width: 104px" />
        </div>
        <div class="f-btns">
          <a-button size="small" :disabled="loading" @click="resetFilters">重置</a-button>
          <a-button size="small" type="primary" :loading="loading" @click="applyFilters">应用</a-button>
        </div>
      </div>

      <!-- 4 汇总卡 -->
      <div class="sums">
        <div v-for="c in summaryCards" :key="c.key" class="sum card">
          <div class="sum-h">
            <span class="sum-l">{{ c.label }}</span>
            <component :is="c.icon" class="sum-i" :style="{ color: c.color }" />
          </div>
          <div class="sum-v">{{ c.value }}</div>
        </div>
      </div>

      <!-- 3 系列趋势图 -->
      <div class="tcard card">
        <div class="sec-h">趋势 <span class="sub">会话 / 请求（左轴）· 增量 %（右轴虚线）</span></div>
        <div ref="chartEl" class="chart"></div>
      </div>

      <!-- 8 列会话表 + 分页 -->
      <div class="card">
        <div class="sec-h" style="padding: 10px 14px 0">
          爆发会话明细 <span class="sub">共 {{ fmtInt(total) }} 条</span>
          <a-button size="small" style="margin-left: auto" :loading="loading" @click="load"><template #icon><ReloadOutlined /></template></a-button>
        </div>
        <a-table
          :columns="columns" :data-source="items" row-key="session_id" size="small" :loading="loading"
          :scroll="{ x: 'max-content' }" :pagination="false" class="tb-table"
        >
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'account'">
              <div class="acc">{{ record.account_name || `#${record.account_id}` }}</div>
              <div class="acc-id">#{{ record.account_id }}</div>
            </template>
            <template v-else-if="column.key === 'started'">
              <div class="dim">{{ fmtTime(record.started_at) }}</div>
              <div v-if="record.ended_at" class="dim">→ {{ fmtTime(record.ended_at) }}</div>
              <div v-else class="active-tag">进行中</div>
            </template>
            <template v-else-if="column.key === 'window'"><a-tag style="margin: 0">{{ fmtWindow(record.window) }}</a-tag></template>
            <template v-else-if="column.key === 'reason'"><span class="dim">{{ fmtReason(record.reason) }}</span></template>
            <template v-else-if="column.key === 'delta'"><span class="mono">{{ fmtPct(record.delta_percent) }}</span></template>
            <template v-else-if="column.key === 'requests'">{{ fmtInt(record.requests) }}</template>
            <template v-else-if="column.key === 'tokens'">{{ fmtTokens(record.tokens) }}</template>
            <template v-else-if="column.key === 'status'">
              <a-tag :color="record.status === 'active' ? 'orange' : 'green'" style="margin: 0">{{ record.status === 'active' ? '进行中' : '已结算' }}</a-tag>
            </template>
          </template>
        </a-table>
        <div class="pg">
          <a-pagination
            :current="page" :page-size="pageSize" :total="total" size="small"
            :page-size-options="['10', '20', '50', '100']" show-size-changer
            :show-total="(t) => `共 ${t} 条`" @change="onPage" @show-size-change="onPage"
          />
        </div>
        <div v-if="!loading && items.length === 0" class="empty">当前筛选范围暂无爆发会话</div>
      </div>
    </div>
  </a-drawer>
</template>

<style scoped>
.card { background: var(--card); border: 1px solid var(--hr); border-radius: 10px; }
.flt { display: flex; flex-wrap: wrap; align-items: flex-end; gap: 10px 14px; padding: 10px 14px; margin-bottom: 10px; }
.f-item { display: flex; flex-direction: column; gap: 4px; }
.f-item label { font-size: 11px; color: var(--text2); }
.f-btns { display: flex; gap: 8px; margin-left: auto; }
.sums { display: grid; grid-template-columns: repeat(auto-fit, minmax(160px, 1fr)); gap: 10px; margin-bottom: 10px; }
.sum { padding: 10px 14px; }
.sum-h { display: flex; align-items: center; justify-content: space-between; }
.sum-l { font-size: 11px; color: var(--text2); }
.sum-i { font-size: 16px; }
.sum-v { font-size: 22px; font-weight: 600; margin-top: 4px; font-variant-numeric: tabular-nums; }
.tcard { padding: 10px 14px 6px; margin-bottom: 10px; }
.sec-h { display: flex; align-items: center; gap: 10px; font-size: 13px; font-weight: 500; margin-bottom: 6px; }
.sub { font-size: 11px; font-weight: 400; color: var(--text2); }
.chart { width: 100%; height: 240px; }
.acc { font-size: 12px; font-weight: 500; }
.acc-id { font-size: 11px; color: var(--text2); }
.dim { font-size: 12px; color: var(--text2); }
.active-tag { font-size: 11px; color: #f59e0b; }
.mono { font-family: Consolas, monospace; font-size: 12px; }
.pg { display: flex; justify-content: flex-end; padding: 10px 14px; }
.empty { padding: 14px 0 18px; text-align: center; font-size: 12px; color: var(--text2); }
.tb-table :deep(.ant-table-placeholder) { display: none; }
@media (max-width: 720px) { .f-btns { margin-left: 0; } .chart { height: 200px; } }
</style>
