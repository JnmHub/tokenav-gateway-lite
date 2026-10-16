<script setup>
/* 仪表盘（对照旧 views/admin/DashboardView.vue 的 snapshot-v2 契约补齐）
 * 主接口：GET /admin/dashboard/snapshot-v2
 *   query：start_date / end_date（YYYY-MM-DD，end 含当天）/ granularity=hour|day
 *          include_stats / include_trend / include_model_stats / include_group_stats
 *          include_users_trend / users_trend_limit（一次快照带回统计卡 + 趋势 + 模型 + 用户趋势）
 * 排行接口：GET /admin/dashboard/users-ranking（start_date / end_date / limit）
 * 字段名全部沿用旧前端 types 定义，实测确认：
 *   stats：today/total_requests、today/total_tokens、today/total_{actual,account,}cost、
 *          rpm/tpm、average_duration_ms、active_users、total_users、today_new_users、
 *          total/active_api_keys、total/normal/error/ratelimit/overload_accounts、uptime
 *   trend[]：date, requests, input_tokens, output_tokens, cache_creation_tokens,
 *            cache_read_tokens, total_tokens, cost, actual_cost
 *   models[]：model, requests, total_tokens, cost, actual_cost, account_cost
 *   users_trend[]：date, user_id, username, email, requests, tokens, cost, actual_cost
 *   ranking[]：user_id, email, actual_cost, requests, tokens（附 total_actual_cost/requests/tokens）
 * 图表：echarts 为 package.json 既有依赖（零新增），按需引入折线；
 *       模型分布 / 消费排行用 antd 表格 + CSS 条形呈现 */
import { ref, computed, onMounted, onUnmounted, nextTick } from 'vue'
import { ReloadOutlined } from '@ant-design/icons-vue'
import { message } from 'ant-design-vue'
import dayjs from 'dayjs'
import * as echarts from 'echarts/core'
import { LineChart } from 'echarts/charts'
import { GridComponent, TooltipComponent, LegendComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import { request } from '../api'
import { useAuthStore } from '../core/stores'
import DashboardExtraPanels from '../components/dashboard/DashboardExtraPanels.vue'

echarts.use([LineChart, GridComponent, TooltipComponent, LegendComponent, CanvasRenderer])

const auth = useAuthStore()
const loading = ref(true)
const err = ref('')
const stats = ref(null)
const trend = ref([])
const models = ref([])
const usersTrend = ref([])
const ranking = ref([])
const rankTotals = ref({ actual: 0, requests: 0, tokens: 0 })

// 日期范围默认「近 24h」：起 = 昨天日期，止 = 今天日期（与旧版 getLast24HoursRangeDates 一致）
const ymd = (d) => d.format('YYYY-MM-DD')
const dates = ref([ymd(dayjs().subtract(1, 'day')), ymd(dayjs())])
// 粒度默认 hour（近 24h 场景下与旧版 onDateRangeChange 的自适应规则一致）
const granularity = ref('hour')

/* ---------- 格式化（与旧 DashboardView.vue 语义对齐） ---------- */
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
const fmtDuration = (ms) => {
  const n = Number(ms)
  if (ms === null || ms === undefined || !Number.isFinite(n)) return '-'
  return n >= 1000 ? `${(n / 1000).toFixed(2)}s` : `${Math.round(n)}ms`
}
// 趋势点显示名：username → email → 用户 ID（旧版 getDisplayName 同序）
const userLabel = (p) => (p.username && p.username.trim()) || (p.email && p.email.trim()) || `用户 ${p.user_id}`

/* ---------- 数据加载（seq 防竞态，模式同旧版 chartLoadSeq） ---------- */
let seq = 0
const load = async () => {
  const cur = ++seq
  loading.value = true
  try {
    const [start, end] = dates.value
    const snapQuery = {
      start_date: start,
      end_date: end,
      granularity: granularity.value,
      include_stats: true,
      include_trend: true,
      include_model_stats: true,
      include_group_stats: false,
      include_users_trend: true,
      users_trend_limit: 12,
    }
    const [snap, rank] = await Promise.all([
      request('/admin/dashboard/snapshot-v2', { method: 'GET', query: snapQuery }),
      request('/admin/dashboard/users-ranking', { method: 'GET', query: { start_date: start, end_date: end, limit: 12 } }),
    ])
    if (cur !== seq) return
    stats.value = snap?.stats || null
    trend.value = snap?.trend || []
    models.value = snap?.models || []
    usersTrend.value = snap?.users_trend || []
    ranking.value = rank?.ranking || []
    rankTotals.value = {
      actual: rank?.total_actual_cost || 0,
      requests: rank?.total_requests || 0,
      tokens: rank?.total_tokens || 0,
    }
    err.value = ''
    await nextTick()
    renderCharts()
  } catch (e) {
    if (cur !== seq) return
    err.value = e.message
    message.error(`仪表盘加载失败：${e.message}`)
  } finally {
    if (cur === seq) loading.value = false
  }
}

// 范围 ≤1 天自动用 hour，否则 day（旧版 onDateRangeChange 规则），然后统一刷新
const onRangeChange = (vals) => {
  if (vals && vals.length === 2) {
    dates.value = vals
    const days = dayjs(vals[1]).diff(dayjs(vals[0]), 'day', true)
    granularity.value = days <= 1 ? 'hour' : 'day'
  }
  load()
}
const onGranularityChange = () => load()

/* ---------- 模型 Top（按 Token 排序，旧版 metric 默认 tokens） ---------- */
const modelsSorted = computed(() =>
  [...models.value].sort((a, b) => (b.total_tokens || 0) - (a.total_tokens || 0)))
const modelMaxTokens = computed(() => modelsSorted.value[0]?.total_tokens || 0)
// 占比色带：Token 占比堆叠段（Top6 各占一段，其余合并为「其它」），复用 echarts 调色板保持全局同色系
const modelSegments = computed(() => {
  const total = modelsSorted.value.reduce((s, m) => s + (m.total_tokens || 0), 0)
  if (!total) return []
  const top = modelsSorted.value.slice(0, 6).map((m, i) => ({
    model: m.model, tokens: m.total_tokens || 0,
    pct: ((m.total_tokens || 0) / total) * 100, color: palette[i % palette.length],
  }))
  const restTokens = total - top.reduce((s, m) => s + m.tokens, 0)
  if (restTokens > 0) top.push({ model: '其它', tokens: restTokens, pct: (restTokens / total) * 100, color: '#9ca3af' })
  return top
})
const modelColumns = [
  { title: '模型', dataIndex: 'model', key: 'model', ellipsis: true },
  { title: '请求', key: 'requests', width: 72, align: 'right' },
  { title: 'Token 占比', key: 'bar', width: 150 },
  { title: 'Token', key: 'tokens', width: 96, align: 'right' },
  { title: '实际成本', key: 'actual', width: 92, align: 'right' },
  { title: '账号成本', key: 'account', width: 92, align: 'right' },
  { title: '标准成本', key: 'std', width: 92, align: 'right' },
]

/* ---------- 消费排行（实际成本条形） ---------- */
const rankMaxCost = computed(() => ranking.value[0]?.actual_cost || 0)
const rankColumns = [
  { title: '#', key: 'idx', width: 44 },
  { title: '用户', key: 'user', ellipsis: true },
  { title: '请求', key: 'requests', width: 72, align: 'right' },
  { title: 'Token', key: 'tokens', width: 96, align: 'right' },
  { title: '实际消费', key: 'cost', width: 210 },
]

/* ---------- echarts：Token 趋势 + 用户趋势 ---------- */
// 配色沿用旧 chart 组件的低饱和序列色，深浅主题下均可见的中性灰做轴
const palette = ['#3b82f6', '#10b981', '#f59e0b', '#ef4444', '#8b5cf6', '#ec4899', '#14b8a6', '#f97316', '#6366f1', '#84cc16', '#06b6d4', '#a855f7']
const axisLabelFmt = (v) => (v.includes(' ') ? v.split(' ')[1] : v.slice(5))
const baseGrid = { left: 52, right: 14, top: 30, bottom: 22 }
const baseAxis = {
  xAxis: (labels) => ({
    type: 'category',
    data: labels,
    axisLabel: { color: '#8a8a8a', fontSize: 10, formatter: axisLabelFmt },
    axisLine: { lineStyle: { color: 'rgba(128,128,128,.3)' } },
    axisTick: { show: false },
  }),
  yAxis: () => ({
    type: 'value',
    axisLabel: { color: '#8a8a8a', fontSize: 10, formatter: (v) => fmtTokens(v) },
    splitLine: { lineStyle: { color: 'rgba(128,128,128,.18)' } },
  }),
}

const buildTrendOption = () => {
  const t = trend.value
  if (!t.length) return null
  // 序列定义与旧 TokenUsageTrend.vue 一致：Input/Output/Cache Creation/Cache Read
  const mk = (name, key, color) => ({
    name,
    type: 'line',
    smooth: true,
    showSymbol: t.length <= 48,
    data: t.map((d) => d[key] || 0),
    itemStyle: { color },
    lineStyle: { width: 2, color },
  })
  return {
    grid: baseGrid,
    legend: { top: 0, left: 'center', icon: 'circle', itemWidth: 8, itemHeight: 8, textStyle: { color: '#8a8a8a', fontSize: 11 } },
    tooltip: {
      trigger: 'axis',
      confine: true,
      formatter: (params) => {
        const d = t[params[0].dataIndex]
        if (!d) return ''
        const rows = params.map((p) => `${p.marker}${p.seriesName}　${fmtTokens(p.value)}`)
        rows.push(`<span style="opacity:.65">实际 $${fmtCost(d.actual_cost)} · 标准 $${fmtCost(d.cost)} · ${fmt(d.requests)} 次</span>`)
        return `${d.date}<br/>${rows.join('<br/>')}`
      },
    },
    xAxis: baseAxis.xAxis(t.map((d) => d.date)),
    yAxis: baseAxis.yAxis(),
    series: [
      mk('Input', 'input_tokens', '#3b82f6'),
      mk('Output', 'output_tokens', '#10b981'),
      mk('Cache Creation', 'cache_creation_tokens', '#f59e0b'),
      mk('Cache Read', 'cache_read_tokens', '#06b6d4'),
    ],
  }
}

const buildUserOption = () => {
  const pts = usersTrend.value
  if (!pts.length) return null
  // 按 user_id 分组补零对齐日期轴（避免同名/缺天合并错位，旧版同思路）
  const allDates = [...new Set(pts.map((p) => p.date))].sort()
  const groups = new Map()
  for (const p of pts) {
    if (!groups.has(p.user_id)) groups.set(p.user_id, { name: userLabel(p), m: new Map() })
    groups.get(p.user_id).m.set(p.date, p.tokens || 0)
  }
  const series = [...groups.values()].map((g, i) => ({
    name: g.name,
    type: 'line',
    smooth: true,
    showSymbol: false,
    data: allDates.map((d) => g.m.get(d) || 0),
    itemStyle: { color: palette[i % palette.length] },
    lineStyle: { width: 1.5, color: palette[i % palette.length] },
  }))
  return {
    grid: baseGrid,
    legend: { top: 0, type: 'scroll', icon: 'circle', itemWidth: 8, itemHeight: 8, textStyle: { color: '#8a8a8a', fontSize: 11 } },
    tooltip: {
      trigger: 'axis',
      confine: true,
      formatter: (params) => {
        const rows = [...params].sort((a, b) => (b.value || 0) - (a.value || 0))
          .filter((p) => p.value > 0)
          .map((p) => `${p.marker}${p.seriesName}　${fmtTokens(p.value)}`)
        return `${params[0]?.axisValue || ''}<br/>${rows.join('<br/>') || '无数据'}`
      },
    },
    xAxis: baseAxis.xAxis(allDates),
    yAxis: baseAxis.yAxis(),
    series,
  }
}

const trendEl = ref(null)
const userEl = ref(null)
let trendChart = null
let userChart = null
const renderCharts = () => {
  const tOpt = buildTrendOption()
  const uOpt = buildUserOption()
  if (trendEl.value) {
    if (!trendChart) trendChart = echarts.init(trendEl.value)
    trendChart.setOption(tOpt || { xAxis: { show: false }, yAxis: { show: false }, series: [] }, true)
  }
  if (userEl.value) {
    if (!userChart) userChart = echarts.init(userEl.value)
    userChart.setOption(uOpt || { xAxis: { show: false }, yAxis: { show: false }, series: [] }, true)
  }
}
const onResize = () => { trendChart?.resize(); userChart?.resize() }
onMounted(() => {
  window.addEventListener('resize', onResize)
  load()
})
onUnmounted(() => {
  window.removeEventListener('resize', onResize)
  trendChart?.dispose()
  userChart?.dispose()
})
</script>

<template>
  <div class="dash">
    <div class="hero">
      <div class="title">tokenav gateway 控制台</div>
      <div class="sub">TGY · 登录用户：{{ auth.user?.username || auth.user?.email || '-' }}</div>
    </div>

    <!-- 工具栏：日期范围｜粒度｜刷新（同组控件高度对齐） -->
    <div class="toolbar">
      <a-range-picker
        v-model:value="dates"
        value-format="YYYY-MM-DD"
        :allow-clear="false"
        style="width: 250px"
        @change="onRangeChange"
      />
      <a-select
        v-model:value="granularity"
        style="width: 100px"
        :options="[{ value: 'hour', label: '按小时' }, { value: 'day', label: '按天' }]"
        @change="onGranularityChange"
      />
      <a-button :loading="loading" @click="load">
        <template #icon><ReloadOutlined /></template>
        刷新
      </a-button>
      <span v-if="stats?.stats_updated_at" class="tip">统计更新于 {{ String(stats.stats_updated_at).replace('T', ' ').slice(0, 19) }}</span>
    </div>

    <div v-if="err && !stats" class="hint err">{{ err }}</div>

    <a-spin :spinning="loading && !stats">
      <!-- 行 1：请求 / Token / 性能 -->
      <div class="grid4">
        <div class="card">
          <div class="k">今日请求</div>
          <div class="v">{{ fmt(stats?.today_requests) }}</div>
          <div class="sub"><span>累计 {{ fmt(stats?.total_requests) }}</span></div>
        </div>
        <div class="card">
          <div class="k">今日 Token</div>
          <div class="v">{{ fmtTokens(stats?.today_tokens) }}</div>
          <div class="sub">
            <span class="c-ok" title="实际扣除">${{ fmtCost(stats?.today_actual_cost) }}</span>
            <span class="sep">/</span>
            <span class="c-warn" title="账号成本">${{ fmtCost(stats?.today_account_cost) }}</span>
            <span class="sep">/</span>
            <span class="c-std" title="标准计费">${{ fmtCost(stats?.today_cost) }}</span>
          </div>
        </div>
        <div class="card">
          <div class="k">累计 Token</div>
          <div class="v">{{ fmtTokens(stats?.total_tokens) }}</div>
          <div class="sub">
            <span class="c-ok" title="实际扣除">${{ fmtCost(stats?.total_actual_cost) }}</span>
            <span class="sep">/</span>
            <span class="c-warn" title="账号成本">${{ fmtCost(stats?.total_account_cost) }}</span>
            <span class="sep">/</span>
            <span class="c-std" title="标准计费">${{ fmtCost(stats?.total_cost) }}</span>
          </div>
        </div>
        <div class="card">
          <div class="k">性能（近 5 分钟）</div>
          <div class="v">{{ fmtTokens(stats?.rpm) }} <span class="unit">RPM</span></div>
          <div class="sub"><span>{{ fmtTokens(stats?.tpm) }} TPM</span></div>
        </div>
      </div>

      <!-- 行 2：响应 / 用户 / Key / 上游账号细分 -->
      <div class="grid4" style="margin-top: 12px">
        <div class="card">
          <div class="k">平均响应时长</div>
          <div class="v">{{ fmtDuration(stats?.average_duration_ms) }}</div>
          <div class="sub"><span>活跃用户 {{ fmt(stats?.active_users) }}（当前小时 {{ fmt(stats?.hourly_active_users) }}）</span></div>
        </div>
        <div class="card">
          <div class="k">用户</div>
          <div class="v">{{ fmt(stats?.total_users) }}</div>
          <div class="sub">
            <span class="c-ok">今日新增 +{{ fmt(stats?.today_new_users) }}</span>
            <span class="sep">·</span>
            <span>今日活跃 {{ fmt(stats?.active_users) }}</span>
          </div>
        </div>
        <div class="card">
          <div class="k">API Key</div>
          <div class="v">{{ fmt(stats?.total_api_keys) }}</div>
          <div class="sub"><span class="c-ok">活跃 {{ fmt(stats?.active_api_keys) }}</span></div>
        </div>
        <div class="card">
          <div class="k">上游账号</div>
          <div class="v">{{ fmt(stats?.total_accounts) }}</div>
          <div class="sub">
            <span class="c-ok">正常 {{ fmt(stats?.normal_accounts) }}</span>
            <span class="sep">·</span>
            <span class="c-err">错误 {{ fmt(stats?.error_accounts) }}</span>
            <span class="sep">·</span>
            <span>限流 {{ fmt(stats?.ratelimit_accounts) }}</span>
            <span class="sep">·</span>
            <span>过载 {{ fmt(stats?.overload_accounts) }}</span>
          </div>
        </div>
      </div>

      <!-- 图表行：Token 趋势 | 用户趋势 -->
      <div class="charts" style="margin-top: 12px">
        <div class="panel">
          <h3>Token 趋势</h3>
          <div class="chart-box">
            <div ref="trendEl" class="chart-canvas"></div>
            <div v-if="!trend.length" class="chart-empty">暂无数据</div>
          </div>
        </div>
        <div class="panel">
          <h3>用户趋势（Top 12 · Token）</h3>
          <div class="chart-box">
            <div ref="userEl" class="chart-canvas"></div>
            <div v-if="!usersTrend.length" class="chart-empty">暂无数据</div>
          </div>
        </div>
      </div>

      <!-- 模型 Top 排行：表格 + CSS 条形 -->
      <div class="panel" style="margin-top: 12px">
        <h3>模型用量排行（按 Token）</h3>
        <!-- 占比色带：一条堆叠条呈现全部模型的 Token 生态分布，图例取 Top6 + 其它 -->
        <div v-if="modelSegments.length" class="dist-wrap">
          <div class="dist-band">
            <span
              v-for="s in modelSegments"
              :key="s.model"
              class="dist-seg"
              :style="{ width: s.pct + '%', background: s.color }"
              :title="`${s.model}  ${fmtTokens(s.tokens)}（${s.pct.toFixed(1)}%）`"
            ></span>
          </div>
          <div class="dist-legend">
            <span v-for="s in modelSegments" :key="s.model" class="dist-item">
              <i :style="{ background: s.color }"></i>{{ s.model }} <em>{{ s.pct.toFixed(0) }}%</em>
            </span>
          </div>
        </div>
        <a-table
          :columns="modelColumns"
          :data-source="modelsSorted"
          row-key="model"
          size="small"
          :pagination="false"
          :loading="loading && !modelsSorted.length"
          :scroll="{ y: 264 }"
        >
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'requests'">{{ fmt(record.requests) }}</template>
            <template v-else-if="column.key === 'bar'">
              <div class="bar-track">
                <div class="bar-fill" :style="{ width: modelMaxTokens ? Math.max((record.total_tokens / modelMaxTokens) * 100, 1) + '%' : '0%' }"></div>
              </div>
            </template>
            <template v-else-if="column.key === 'tokens'">{{ fmtTokens(record.total_tokens) }}</template>
            <template v-else-if="column.key === 'actual'"><span class="c-ok">${{ fmtCost(record.actual_cost) }}</span></template>
            <template v-else-if="column.key === 'account'"><span class="c-warn">${{ fmtCost(record.account_cost) }}</span></template>
            <template v-else-if="column.key === 'std'"><span class="c-std">${{ fmtCost(record.cost) }}</span></template>
          </template>
          <template #empty><span class="empty">暂无数据</span></template>
        </a-table>
      </div>

      <!-- 消费排行：表格 + CSS 条形 -->
      <div class="panel" style="margin-top: 12px">
        <h3>
          用户消费排行（实际成本）
          <span class="rank-sum">区间合计：${{ fmtCost(rankTotals.actual) }} · {{ fmt(rankTotals.requests) }} 次请求 · {{ fmtTokens(rankTotals.tokens) }} Token</span>
        </h3>
        <a-table
          :columns="rankColumns"
          :data-source="ranking"
          row-key="user_id"
          size="small"
          :pagination="false"
          :loading="loading && !ranking.length"
          :scroll="{ y: 264 }"
        >
          <template #bodyCell="{ column, record, index }">
            <template v-if="column.key === 'idx'">{{ index + 1 }}</template>
            <template v-else-if="column.key === 'user'">{{ record.email || `用户 ${record.user_id}` }}</template>
            <template v-else-if="column.key === 'requests'">{{ fmt(record.requests) }}</template>
            <template v-else-if="column.key === 'tokens'">{{ fmtTokens(record.tokens) }}</template>
            <template v-else-if="column.key === 'cost'">
              <div class="cost-cell">
                <div class="bar-track">
                  <div class="bar-fill" :style="{ width: rankMaxCost ? Math.max((record.actual_cost / rankMaxCost) * 100, 1) + '%' : '0%' }"></div>
                </div>
                <span class="cost-val">${{ fmtCost(record.actual_cost) }}</span>
              </div>
            </template>
          </template>
          <template #empty><span class="empty">暂无数据</span></template>
        </a-table>
      </div>
    </a-spin>

    <!-- 补充面板：后端已有但旧仪表盘无入口的 8 个接口（stats / realtime /
         api-keys-trend / api-keys-usage / users-trend / users-usage /
         user-breakdown / aggregation-backfill），拆分在 components/dashboard/ 内 -->
    <DashboardExtraPanels :start-date="dates[0]" :end-date="dates[1]" :granularity="granularity" />
  </div>
</template>

<style scoped>
.dash { display: flex; flex-direction: column; gap: 14px; height: 100%; overflow-y: auto; }
/* 规范：禁渐变/高饱和实色——横幅为低饱和主题色半透明 + 细描边 */
.hero {
  padding: 18px 22px;
  border-radius: 12px;
  background: color-mix(in srgb, var(--theme-2) 14%, transparent);
  border: 1px solid color-mix(in srgb, var(--theme-2) 30%, transparent);
}
.title { font-size: 22px; font-weight: 600; color: var(--text); }
.sub { margin-top: 4px; font-size: 13px; color: var(--text2); }

.toolbar { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.tip { font-size: 12px; color: var(--text2); }
.hint { font-size: 13px; color: var(--text2); }
.hint.err { color: #d13438; }

.grid4 { display: grid; grid-template-columns: repeat(auto-fit, minmax(210px, 1fr)); gap: 12px; }
.card { padding: 14px 16px; border-radius: 10px; background: var(--card); border: 1px solid var(--hr); }
.k { font-size: 12px; color: var(--text2); }
.v { margin-top: 6px; font-size: 20px; font-weight: 600; color: var(--text); }
.v .unit { font-size: 12px; font-weight: 400; color: var(--text2); }
.sub { margin-top: 4px; font-size: 12px; color: var(--text2); display: flex; gap: 4px; flex-wrap: wrap; }
.sep { opacity: 0.5; }
.c-ok { color: #2f9e63; }
.c-warn { color: #d18a2d; }
.c-err { color: #e05252; }
.c-std { color: var(--text2); }

.charts { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
.panel { padding: 14px 16px; border-radius: 10px; background: var(--card); border: 1px solid var(--hr); }
.panel h3 { margin: 0 0 10px; font-size: 13px; font-weight: 600; color: var(--text); }
.rank-sum { margin-left: 8px; font-size: 12px; font-weight: 400; color: var(--text2); }
.chart-box { position: relative; height: 220px; }
.chart-canvas { position: absolute; inset: 0; }
.chart-empty {
  position: absolute; inset: 0;
  display: flex; align-items: center; justify-content: center;
  font-size: 12px; color: var(--text2);
}

.bar-track { height: 6px; border-radius: 3px; background: var(--hr); overflow: hidden; }
.bar-fill { height: 100%; border-radius: 3px; background: color-mix(in srgb, var(--theme-2) 72%, transparent); }
/* 模型占比色带：低饱和段色 + 顶部图例（hover 段微亮） */
.dist-wrap { margin: 2px 0 10px; }
.dist-band { display: flex; height: 10px; border-radius: 5px; overflow: hidden; background: var(--hr); }
.dist-seg { height: 100%; transition: filter 0.12s; }
.dist-seg:hover { filter: brightness(1.15); }
.dist-legend { display: flex; flex-wrap: wrap; gap: 4px 14px; margin-top: 6px; }
.dist-item { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; color: var(--text2); }
.dist-item i { width: 8px; height: 8px; border-radius: 2px; flex: none; }
.dist-item em { font-style: normal; color: var(--text); font-variant-numeric: tabular-nums; }
.cost-cell { display: flex; align-items: center; gap: 8px; }
.cost-cell .bar-track { flex: 1; min-width: 40px; }
.cost-val { font-size: 12px; color: var(--text); white-space: nowrap; }
.empty { font-size: 12px; color: var(--text2); }

/* 移动端：图表单列、卡片单列排布 */
@media (max-width: 900px) {
  .charts { grid-template-columns: 1fr; }
  .grid4 { grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); }
}
</style>
