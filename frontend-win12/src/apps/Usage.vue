<script setup>
import { SearchOutlined } from '@ant-design/icons-vue'
/* 用量查询 v3（对照旧 UsageView.vue / UsageFilters.vue / UsageStatsCards.vue / dashboard 图表契约补齐）
 * 接口（路径/参数/字段全部经真实后端实测核对）：
 *  GET /admin/usage                明细分页：page/page_size/start_date/end_date/timezone/user_id/account_id/group_id/
 *                                  model/request_type/billing_type/billing_mode/service_tier
 *                                  timezone=浏览器 IANA 时区（后端按它解析日期半开区间）；
 *                                  明细行支持展开（expandedRowRender）查看低频审计字段，
 *                                  详情构建契约见 components/usage/usageDetailFormat.js（对照 AdminUsageLog）
 *  GET /admin/usage/stats          统计：筛选同上；start_date+end_date 成对时优先，否则退回 period=today|week|month
 *  GET /admin/dashboard/trend      令牌趋势：start_date/end_date/granularity=hour|day + 通用筛选
 *  GET /admin/dashboard/models     模型排行：start_date/end_date/model_source=requested + 通用筛选
 *  GET /admin/dashboard/groups     分组分布：start_date/end_date + 通用筛选
 *  GET /admin/groups/all?include_inactive=true   分组下拉（含已停用分组，旧版同语义）
 *  GET /admin/usage/search-users?q=              用户搜索（email/username，纯数字按 ID 直查）
 *  GET /admin/accounts?page&page_size&search=    账号搜索
 *  错误请求日志 → components/usage/UsageErrorPanel.vue（/admin/ops/errors，
 *                                  分类筛选用 category——后端不解析 severity，旧实现为假筛选）
 *  用量清理任务 → components/usage/UsageCleanupModal.vue（timezone 已随请求体传入）
 * 统计卡字段（旧 UsageStatsCards 同款）：total_requests / total_tokens / total_input_tokens /
 *  total_output_tokens / total_cache_creation_tokens / total_cache_read_tokens（兜底 total_cache_tokens）/
 *  average_duration_ms；成本三口径：total_actual_cost（实际）/ total_account_cost（账号）/ total_cost（标准）
 * 待迭代：状态码筛选（后端 admin/usage 暂无该参数）、端点分布图（stats.endpoints 已返回数据可直连）、明细列自定义 */
import { ref, computed, onMounted, onUnmounted, nextTick } from 'vue'
import { ReloadOutlined } from '@ant-design/icons-vue'
import { message } from 'ant-design-vue'
import dayjs from 'dayjs'
import * as echarts from 'echarts/core'
import { LineChart } from 'echarts/charts'
import { GridComponent, TooltipComponent, LegendComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import { request } from '../api'
import { firstTokenSeverity, durationSeverity, LATENCY_COLORS, firstTokenPercent as latPercentCalc } from '../utils/latencyHealth'
import { fmt, fmt$, fmtTime, fmtTokens, fmtDur, fmtCost, REQ_TYPE_OPTS, BROWSER_TZ } from '../components/usage/usageFormat'
import UsageDetailPanel from '../components/usage/UsageDetailPanel.vue'
import UsageErrorPanel from '../components/usage/UsageErrorPanel.vue'
import UsageCleanupModal from '../components/usage/UsageCleanupModal.vue'

echarts.use([LineChart, GridComponent, TooltipComponent, LegendComponent, CanvasRenderer])

/* ---------- 筛选状态 ----------
 * 日期自由范围（start_date/end_date）替代旧固定三档；三档退化为快捷预设，
 * 后端 end_date 为半开区间（解析时 +1 天），故范围天然含当天 */
const ymd = (d) => d.format('YYYY-MM-DD')
const dates = ref([ymd(dayjs().subtract(6, 'day')), ymd(dayjs())])
const preset = ref('week')
const granularity = ref('day')
const fGroupId = ref(undefined)
const fUserId = ref(undefined)
const fAccountId = ref(undefined)
const fModel = ref('')
// 模型不一致筛选：true 时仅返回上游实际模型与映射预期不符的记录
const fMismatch = ref(false)
const fReqType = ref(undefined)
const fBillingType = ref(undefined)
const fBillingMode = ref(undefined)

// 快捷预设语义与旧 period 一致；范围 ≤1 天自动切小时粒度（旧 onDateRangeChange 规则）
const applyPreset = (p) => {
  preset.value = p
  const t = dayjs()
  if (p === 'today') { dates.value = [ymd(t), ymd(t)]; granularity.value = 'hour' }
  else if (p === 'week') { dates.value = [ymd(t.subtract(6, 'day')), ymd(t)]; granularity.value = 'day' }
  else if (p === 'month') { dates.value = [ymd(t.subtract(29, 'day')), ymd(t)]; granularity.value = 'day' }
  refreshAll()
}
const onRangeChange = (vals) => {
  // change 事件参数可能为 Dayjs 对象数组（valueFormat 只约束 v-model），统一归一化为 YYYY-MM-DD
  if (!vals || vals.length !== 2) return
  const iso = vals.map((v) => (dayjs.isDayjs(v) ? ymd(v) : String(v).slice(0, 10)))
  if (iso.some((d) => !dayjs(d).isValid())) return
  preset.value = 'custom'
  dates.value = iso
  granularity.value = dayjs(iso[1]).diff(dayjs(iso[0]), 'day', true) <= 1 ? 'hour' : 'day'
  refreshAll()
}
// segmented 的选中值必须命中选项：仅自定义范围时追加「自定义」项，避免显示空块
const presetOpts = computed(() => [
  { value: 'today', label: '今日' },
  { value: 'week', label: '近 7 天' },
  { value: 'month', label: '近 30 天' },
  ...(preset.value === 'custom' ? [{ value: 'custom', label: '自定义' }] : []),
])

// 请求类型选项移至 components/usage/usageFormat.js 的 REQ_TYPE_OPTS（主筛选与清理弹窗共用，取值同旧 UsageFilters.vue）
const billingTypeOpts = [
  { value: 0, label: '余额扣费' },
  { value: 1, label: '订阅扣费' },
]
const billingModeOpts = [
  { value: 'token', label: 'Token 计费' },
  { value: 'per_request', label: '按次计费' },
  { value: 'image', label: '图片计费' },
  { value: 'video', label: '视频计费' },
]

/* ---------- 下拉数据：分组一次性加载 / 用户与账号远程搜索（防抖 + 时序号防竞态） ---------- */
const groupOpts = ref([])
const loadGroupOpts = async () => {
  try {
    const list = await request('/admin/groups/all', { method: 'GET', query: { include_inactive: true } })
    groupOpts.value = [...(list || []).map((g) => ({ value: g.id, label: g.name }))]
  } catch (e) { message.warning(`分组选项加载失败：${e.message}（仍可按其他条件筛选）`) }
}
const debounce = (fn, ms = 300) => { let t = null; return (...a) => { clearTimeout(t); t = setTimeout(() => fn(...a), ms) } }

const userOpts = ref([])
let userSeq = 0
const doUserSearch = async (q) => {
  const cur = ++userSeq
  const kw = (q || '').trim()
  try {
    const list = kw ? await request('/admin/usage/search-users', { method: 'GET', query: { q: kw } }) : []
    if (cur !== userSeq) return
    userOpts.value = [
      ...(list || []).sort((a, b) => Number(a.deleted) - Number(b.deleted))
        .map((u) => ({ value: u.id, label: `${u.username || u.email}${u.deleted ? '（已删除）' : ''} · #${u.id}` })),
      // 纯数字输入兜底：直接按用户 ID 筛选（旧 commitUserSearch 语义）
      ...(/^\d+$/.test(kw) ? [{ value: Number(kw), label: `按 ID：${kw}` }] : []),
    ]
  } catch { if (cur === userSeq) userOpts.value = [] }
}
const onUserSearch = debounce(doUserSearch)

const accountOpts = ref([])
let accountSeq = 0
const doAccountSearch = async (q) => {
  const cur = ++accountSeq
  const kw = (q || '').trim()
  if (!kw) { if (cur === accountSeq) accountOpts.value = []; return }
  try {
    const res = await request('/admin/accounts', { method: 'GET', query: { page: 1, page_size: 20, search: kw } })
    if (cur !== accountSeq) return
    accountOpts.value = (res.items || []).map((a) => ({ value: a.id, label: `${a.name} · #${a.id}` }))
  } catch { if (cur === accountSeq) accountOpts.value = [] }
}
const onAccountSearch = debounce(doAccountSearch)

// API Key 远程搜索（GET /admin/usage/search-api-keys?q=，等价旧 UsageFilters 的 Key 筛选）
const fApiKeyId = ref(undefined)
const keyOpts = ref([])
let keySeq = 0
const doKeySearch = async (q) => {
  const cur = ++keySeq
  const kw = (q || '').trim()
  if (!kw) { if (cur === keySeq) keyOpts.value = []; return }
  try {
    const list = await request('/admin/usage/search-api-keys', { method: 'GET', query: { q: kw } })
    if (cur !== keySeq) return
    keyOpts.value = (list || []).map((k) => ({ value: k.id, label: `${k.name} · #${k.id}` }))
  } catch { if (cur === keySeq) keyOpts.value = [] }
}
const onKeySearch = debounce(doKeySearch)

// ===== 用量清理弹窗（组件：UsageCleanupModal.vue，旧 UsageCleanupDialog 等价）=====
const cleanupOpen = ref(false)

/* ---------- 查询参数组装 ----------
 * 明细/统计接口支持全部筛选；dashboard 图表接口不认识 billing_mode/service_tier，不传避免歧义。
 * timezone：明细/统计/dashboard 全部按浏览器时区解析 start_date/end_date，
 * 不传则回退服务器时区，跨时区统计口径会错（usage_handler/dashboard_handler 均实测支持） */
const filterQuery = () => ({
  start_date: dates.value[0],
  end_date: dates.value[1],
  timezone: BROWSER_TZ,
  user_id: fUserId.value,
  account_id: fAccountId.value,
  api_key_id: fApiKeyId.value,
  group_id: fGroupId.value,
  model: fModel.value.trim() || undefined,
  upstream_model_mismatch: fMismatch.value ? 'true' : undefined,
  request_type: fReqType.value,
  billing_type: fBillingType.value,
})
const statsQuery = () => ({ ...filterQuery(), billing_mode: fBillingMode.value })

/* ---------- 数据加载（每个数据域独立时序号，快速切换筛选只认最后一次响应） ---------- */
const loading = ref(false)
const rows = ref([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(50)
const stats = ref(null)
const trend = ref([])
const modelStats = ref([])
const groupStats = ref([])
const err = ref('')

let listSeq = 0
const loadList = async () => {
  const cur = ++listSeq
  loading.value = true
  try {
    const data = await request('/admin/usage', { method: 'GET', query: { page: page.value, page_size: pageSize.value, ...statsQuery() } })
    if (cur !== listSeq) return
    rows.value = data.items || []
    total.value = data.total || 0
    err.value = ''
  } catch (e) {
    if (cur !== listSeq) return
    err.value = e.message
    rows.value = []
    total.value = 0
  } finally {
    if (cur === listSeq) loading.value = false
  }
}

let statsSeq = 0
const loadStats = async () => {
  const cur = ++statsSeq
  try {
    const data = await request('/admin/usage/stats', { method: 'GET', query: statsQuery() })
    if (cur === statsSeq) stats.value = data
  } catch { if (cur === statsSeq) stats.value = null }
}

let trendSeq = 0
const trendLoading = ref(false)
const loadTrend = async () => {
  const cur = ++trendSeq
  trendLoading.value = true
  try {
    const data = await request('/admin/dashboard/trend', { method: 'GET', query: { ...filterQuery(), granularity: granularity.value } })
    if (cur !== trendSeq) return
    trend.value = data.trend || []
    await nextTick()
    renderTrend()
  } catch { if (cur === trendSeq) trend.value = [] }
  finally { if (cur === trendSeq) trendLoading.value = false }
}

let modelSeq = 0
const loadModels = async () => {
  const cur = ++modelSeq
  try {
    const data = await request('/admin/dashboard/models', { method: 'GET', query: { ...filterQuery(), model_source: 'requested' } })
    if (cur === modelSeq) modelStats.value = data.models || []
  } catch { if (cur === modelSeq) modelStats.value = [] }
}

let groupSeq = 0
const loadGroupStats = async () => {
  const cur = ++groupSeq
  try {
    const data = await request('/admin/dashboard/groups', { method: 'GET', query: filterQuery() })
    if (cur === groupSeq) groupStats.value = data.groups || []
  } catch { if (cur === groupSeq) groupStats.value = [] }
}

// 一次筛选变更刷新全部数据域，列表页码重置回第 1 页（旧 applyFilters 语义）
const refreshAll = () => {
  page.value = 1
  loadList(); loadStats(); loadTrend(); loadModels(); loadGroupStats()
}
const onModelSearch = () => refreshAll()
const onPage = (p, ps) => { page.value = p; if (ps) pageSize.value = ps; loadList() }

// ===== 延迟健康展示（对照旧 utils/latencyHealth + cell-latency）=====
// fmtDur 复用组件既有实现
const latPercent = (row) => latPercentCalc(row.first_token_ms, row.duration_ms)
const latBarTitle = (row) => {
  const total = fmtDur(row.duration_ms)
  if (row.first_token_ms == null) return '总耗时: ' + total
  return '首 Token: ' + fmtDur(row.first_token_ms) + ' (' + latPercent(row).toFixed(1) + '%); 总耗时: ' + total
}

// ===== 错误请求日志面板已拆分至 components/usage/UsageErrorPanel.vue（自请求/自筛选/自弹窗），
// severity 假筛选问题在该组件内修复（severity 后端不解析，已改为真实生效的 category 分类参数） =====
const resetFilters = () => {
  fGroupId.value = undefined; fUserId.value = undefined; fAccountId.value = undefined; fApiKeyId.value = undefined
  fModel.value = ''; fReqType.value = undefined; fBillingType.value = undefined; fBillingMode.value = undefined
  fMismatch.value = false
  applyPreset('week')
}

/* ---------- 排行（按 Token 降序 + CSS 条形占比，旧 MetricBar 语义） ---------- */
const modelsSorted = computed(() => [...modelStats.value].sort((a, b) => (b.total_tokens || 0) - (a.total_tokens || 0)))
const modelMaxTokens = computed(() => modelsSorted.value[0]?.total_tokens || 0)
const groupsSorted = computed(() => [...groupStats.value].sort((a, b) => (b.total_tokens || 0) - (a.total_tokens || 0)))
const groupMaxTokens = computed(() => groupsSorted.value[0]?.total_tokens || 0)
const modelColumns = [
  { title: '模型', dataIndex: 'model', key: 'model', ellipsis: true },
  { title: '请求', key: 'requests', width: 76, align: 'right' },
  { title: 'Token 占比', key: 'bar', width: 150 },
  { title: 'Token', key: 'tokens', width: 92, align: 'right' },
  { title: '实际成本', key: 'actual', width: 90, align: 'right' },
]
const groupColumns = [
  { title: '分组', dataIndex: 'group_name', key: 'name', ellipsis: true },
  { title: '请求', key: 'requests', width: 76, align: 'right' },
  { title: 'Token 占比', key: 'bar', width: 150 },
  { title: 'Token', key: 'tokens', width: 92, align: 'right' },
  { title: '实际成本', key: 'actual', width: 90, align: 'right' },
]

/* ---------- echarts 令牌趋势（序列与旧 TokenUsageTrend 一致：Input/Output/Cache Creation/Cache Read） ---------- */
const trendEl = ref(null)
let trendChart = null
const axisLabelFmt = (v) => (v.includes(' ') ? v.split(' ')[1] : v.slice(5))
const renderTrend = () => {
  if (!trendEl.value) return
  const t = trend.value
  const mk = (name, key, color) => ({
    name,
    type: 'line',
    smooth: true,
    showSymbol: t.length > 0 && t.length <= 48,
    data: t.map((d) => d[key] || 0),
    itemStyle: { color },
    lineStyle: { width: 2, color },
  })
  const option = t.length
    ? {
        grid: { left: 52, right: 14, top: 30, bottom: 22 },
        legend: { top: 0, left: 'center', icon: 'circle', itemWidth: 8, itemHeight: 8, textStyle: { color: '#8a8a8a', fontSize: 11 } },
        tooltip: {
          trigger: 'axis',
          confine: true,
          formatter: (params) => {
            const d = t[params[0].dataIndex]
            if (!d) return ''
            const lines = params.map((p) => `${p.marker}${p.seriesName}　${fmtTokens(p.value)}`)
            lines.push(`<span style="opacity:.65">实际 $${fmtCost(d.actual_cost)} · 标准 $${fmtCost(d.cost)} · ${fmt(d.requests)} 次</span>`)
            return `${d.date}<br/>${lines.join('<br/>')}`
          },
        },
        xAxis: {
          type: 'category',
          data: t.map((d) => d.date),
          axisLabel: { color: '#8a8a8a', fontSize: 10, formatter: axisLabelFmt },
          axisLine: { lineStyle: { color: 'rgba(128,128,128,.3)' } },
          axisTick: { show: false },
        },
        yAxis: {
          type: 'value',
          axisLabel: { color: '#8a8a8a', fontSize: 10, formatter: (v) => fmtTokens(v) },
          splitLine: { lineStyle: { color: 'rgba(128,128,128,.18)' } },
        },
        series: [
          mk('Input', 'input_tokens', '#3b82f6'),
          mk('Output', 'output_tokens', '#10b981'),
          mk('Cache Creation', 'cache_creation_tokens', '#f59e0b'),
          mk('Cache Read', 'cache_read_tokens', '#06b6d4'),
        ],
      }
    : { xAxis: { show: false }, yAxis: { show: false }, series: [] }
  if (!trendChart) trendChart = echarts.init(trendEl.value)
  trendChart.setOption(option, true)
}
const onResize = () => trendChart?.resize()

/* ---------- 明细表列（对照旧 UsageView 主表：无状态码列——usage_logs 表没有该列，
 * 状态码仅存在于错误请求表 ops/errors，见 components/usage/UsageErrorPanel.vue） ---------- */
/* 模型三态（契约来自后端 usage_log + 旧 UsageTable 的 cell-model）：
 * 1) upstream_model  = 实际发往上游的模型，映射生效时与计费模型不同；
 *    与 model 相同说明没有改模型，再显示一行只会是同名重复，必须隐藏。
 * 2) upstream_response_model = 上游响应声明的模型；
 * 3) upstream_model_mismatch = 发往上游的模型与上游响应声明不一致，
 *    这才是「渠道偷换/映射失效」的审计信号——展示的是响应模型，不是发送模型。 */
const sentUpstreamModel = (r) => (r.upstream_model || '').trim() || (r.model || '').trim()
const showSentUpstream = (r) => !!sentUpstreamModel(r) && sentUpstreamModel(r) !== (r.model || '').trim()
const showMismatch = (r) => r.upstream_model_mismatch === true && !!(r.upstream_response_model || '').trim()
const mdlTitle = (r) => {
  if (!showMismatch(r)) return r.model || ''
  return `模型不一致：请求/映射 ${r.model} → 实际上游 ${sentUpstreamModel(r) || '未知'} ↔ 上游响应声明 ${r.upstream_response_model}（可能为渠道映射未生效或上游替换模型）`
}
const columns = [
  { title: '时间', key: 'time', width: 145 },
  { title: '模型', key: 'model', width: 170, ellipsis: true },
  { title: '用户', dataIndex: 'user_id', key: 'user_id', width: 70 },
  { title: '分组', key: 'group', width: 100, ellipsis: true },
  { title: '延迟', key: 'latency', width: 190 },
  { title: '输入', key: 'in', width: 85 },
  { title: '输出', key: 'out', width: 85 },
  { title: '缓存读', key: 'cr', width: 85 },
  { title: '成本', key: 'cost', width: 85 },
  { title: '来源 IP', key: 'ip', width: 120 },
]

onMounted(() => {
  window.addEventListener('resize', onResize)
  loadGroupOpts()
  refreshAll()
})
onUnmounted(() => {
  window.removeEventListener('resize', onResize)
  trendChart?.dispose()
})
</script>

<template>
  <div class="usage">
    <!-- 工具栏行 1：时间范围（快捷预设 + 自由范围 + 趋势粒度） -->
    <div class="toolbar">
      <a-segmented v-model:value="preset" :options="presetOpts" @change="applyPreset" />
      <a-range-picker
        v-model:value="dates" value-format="YYYY-MM-DD" :allow-clear="false"
        style="width: 250px" @change="onRangeChange"
      />
      <a-segmented
        v-model:value="granularity" :options="[
          { value: 'hour', label: '按小时' },
          { value: 'day', label: '按天' },
        ]" @change="loadTrend"
      />
      <a-button @click="refreshAll"><template #icon><ReloadOutlined /></template>刷新</a-button>
      <span class="total">共 {{ fmt(total) }} 条记录</span>
      <a-button v-if="err" size="small" danger @click="refreshAll">明细加载失败，点击重试</a-button>
    </div>
    <!-- 工具栏行 2：筛选组（取值与旧 UsageFilters.vue 一致，改动即查） -->
    <div class="toolbar">
      <a-select
        v-model:value="fGroupId" :options="groupOpts" allow-clear show-search
        option-filter-prop="label" placeholder="全部分组" style="width: 150px" @change="refreshAll"
      />
      <a-select
        v-model:value="fUserId" :options="userOpts" allow-clear show-search :filter-option="false"
        placeholder="用户搜索（邮箱/ID）" style="width: 190px"
        @search="onUserSearch" @change="refreshAll"
      />
      <a-select
        v-model:value="fAccountId" :options="accountOpts" allow-clear show-search :filter-option="false"
        placeholder="账号搜索" style="width: 170px"
        @search="onAccountSearch" @change="refreshAll"
      />
      <a-select
        v-model:value="fApiKeyId" :options="keyOpts" allow-clear show-search :filter-option="false"
        placeholder="API Key 搜索" style="width: 170px"
        @search="onKeySearch" @change="refreshAll"
      />
      <a-input v-model:value="fModel" placeholder="模型，回车筛选" style="width: 170px" @press-enter="onModelSearch">
        <template #suffix><SearchOutlined class="suf-ico" @click="onModelSearch" /></template>
      </a-input>
      <!-- 模型不一致：upstream_model_mismatch=true（上游实际模型与映射预期不符的请求） -->
      <a-checkbox v-model:checked="fMismatch" @change="refreshAll">仅看模型不一致</a-checkbox>
      <a-select v-model:value="fReqType" :options="REQ_TYPE_OPTS" allow-clear placeholder="请求类型" style="width: 130px" @change="refreshAll" />
      <a-select v-model:value="fBillingType" :options="billingTypeOpts" allow-clear placeholder="计费类型" style="width: 120px" @change="refreshAll" />
      <a-select v-model:value="fBillingMode" :options="billingModeOpts" allow-clear placeholder="计费模式" style="width: 120px" @change="refreshAll" />
      <a-button @click="resetFilters">重置</a-button>
      <a-button danger @click="cleanupOpen = true">清理用量…</a-button>
    </div>

    <!-- 统计卡：字段与旧 UsageStatsCards.vue 同款，成本三口径一并展示 -->
    <div class="stats" v-if="stats">
      <div class="scard"><div class="k">请求次数</div><div class="v">{{ fmt(stats.total_requests) }}</div></div>
      <div class="scard">
        <div class="k">总令牌</div><div class="v">{{ fmtTokens(stats.total_tokens) }}</div>
        <div class="s">输入 {{ fmtTokens(stats.total_input_tokens) }} · 输出 {{ fmtTokens(stats.total_output_tokens) }}</div>
      </div>
      <div class="scard"><div class="k">缓存写令牌</div><div class="v">{{ fmtTokens(stats.total_cache_creation_tokens) }}</div></div>
      <div class="scard"><div class="k">缓存读令牌</div><div class="v">{{ fmtTokens(stats.total_cache_read_tokens ?? stats.total_cache_tokens) }}</div></div>
      <div class="scard"><div class="k">平均耗时</div><div class="v">{{ fmtDur(stats.average_duration_ms) }}</div></div>
      <div class="scard">
        <div class="k">实际成本</div><div class="v c-ok">{{ fmt$(stats.total_actual_cost) }}</div>
        <div class="s">账号 <span class="c-warn">{{ fmt$(stats.total_account_cost) }}</span> · 标准 <span class="c-std">{{ fmt$(stats.total_cost) }}</span></div>
      </div>
    </div>

    <!-- 图表区：令牌趋势（全宽）｜模型排行 / 分组分布（双列，CSS 条形） -->
    <div class="charts">
      <div class="panel chart-wide">
        <h3>令牌趋势</h3>
        <div class="chart-box">
          <div ref="trendEl" class="chart-canvas"></div>
          <div v-if="!trend.length && !trendLoading" class="chart-empty">暂无数据</div>
        <div v-else-if="!trend.length" class="chart-empty">加载中…</div>
        </div>
      </div>
      <div class="panel">
        <h3>模型用量排行（按 Token · 请求模型口径）</h3>
        <a-table
          :columns="modelColumns" :data-source="modelsSorted" row-key="model" size="small"
          :pagination="false" :scroll="{ y: 240 }"
        >
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'requests'">{{ fmt(record.requests) }}</template>
            <template v-else-if="column.key === 'bar'">
              <div class="bar-track"><div class="bar-fill" :style="{ width: modelMaxTokens ? Math.max((record.total_tokens / modelMaxTokens) * 100, 1) + '%' : '0%' }"></div></div>
            </template>
            <template v-else-if="column.key === 'tokens'">{{ fmtTokens(record.total_tokens) }}</template>
            <template v-else-if="column.key === 'actual'"><span class="c-ok">${{ fmtCost(record.actual_cost) }}</span></template>
          </template>
          <template #empty><span class="empty">暂无数据</span></template>
        </a-table>
      </div>
      <div class="panel">
        <h3>分组用量分布（按 Token）</h3>
        <a-table
          :columns="groupColumns" :data-source="groupsSorted" row-key="group_id" size="small"
          :pagination="false" :scroll="{ y: 240 }"
        >
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'requests'">{{ fmt(record.requests) }}</template>
            <template v-else-if="column.key === 'bar'">
              <div class="bar-track"><div class="bar-fill" :style="{ width: groupMaxTokens ? Math.max((record.total_tokens / groupMaxTokens) * 100, 1) + '%' : '0%' }"></div></div>
            </template>
            <template v-else-if="column.key === 'tokens'">{{ fmtTokens(record.total_tokens) }}</template>
            <template v-else-if="column.key === 'actual'"><span class="c-ok">${{ fmtCost(record.actual_cost) }}</span></template>
          </template>
          <template #empty><span class="empty">暂无数据</span></template>
        </a-table>
      </div>
    </div>

    <!-- 明细表：主列保持 10 列不变；行展开查看低频审计字段（UsageDetailPanel 小网格）。
         :expandable 显式启用展开列（antd-vue 4.2 下仅插槽不会激活 expandable，与 OpsSystemLogs 同款写法） -->
    <a-table
      :columns="columns" :data-source="rows" :loading="loading" row-key="id" size="small" :scroll="{ x: 'max-content' }"
      :expandable="{ rowExpandable: () => true }"
      :pagination="{ current: page, pageSize, total, showSizeChanger: true, onChange: onPage }"
    >
      <!-- 展开行详情：request_id/映射链/优先级策略/费率证据/切组链/缓存拆分/账号倍率/端点/身份归属 -->
      <template #expandedRowRender="{ record }">
        <UsageDetailPanel :record="record" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'time'">{{ fmtTime(record.created_at) }}</template>
        <!-- 模型列：主行 = 计费模型；映射改了模型才显示「发往上游」；上游响应模型与发送不一致才告警 -->
        <template v-else-if="column.key === 'model'">
          <div class="mdl-cell">
            <span :title="mdlTitle(record)">{{ record.model || record.platform || '-' }}</span>
            <div v-if="showSentUpstream(record)" class="mdl-sent" title="映射后实际发往上游的模型">↳ {{ sentUpstreamModel(record) }}</div>
            <div v-if="showMismatch(record)" class="mdl-mismatch" :title="mdlTitle(record)">
              <span class="mm-tag">模型不一致</span>上游响应 {{ record.upstream_response_model }}
            </div>
          </div>
        </template>
        <template v-else-if="column.key === 'group'">{{ record.effective_group_name || record.group?.name || record.requested_group_name || '-' }}</template>
        <!-- 延迟列：竖条健康分段（首 Token 段 + 剩余段）+ 三行文本，等价旧 cell-latency -->
        <template v-else-if="column.key === 'latency'">
          <div class="lat-cell">
            <span class="lat-bar" :title="latBarTitle(record)">
              <span class="lat-seg" :style="{ height: latPercent(record) + '%', background: LATENCY_COLORS[firstTokenSeverity(record.first_token_ms ?? 0)].bar }" />
              <span class="lat-seg" :style="{ height: (100 - latPercent(record)) + '%', background: LATENCY_COLORS[durationSeverity(record.duration_ms ?? 0)].bar }" />
            </span>
            <div class="lat-rows">
              <div class="lat-row">
                <span class="lat-k">首 Token</span>
                <span class="lat-v" :style="record.first_token_ms != null ? { color: LATENCY_COLORS[firstTokenSeverity(record.first_token_ms)].text } : null">{{ record.first_token_ms != null ? fmtDur(record.first_token_ms) : '-' }}</span>
                <span v-if="record.first_token_ms != null" class="lat-badge" :style="{ background: LATENCY_COLORS[firstTokenSeverity(record.first_token_ms)].badge, color: LATENCY_COLORS[firstTokenSeverity(record.first_token_ms)].text }">{{ LATENCY_COLORS[firstTokenSeverity(record.first_token_ms)].label }}</span>
              </div>
              <!-- 伪造首字：网关先发的 SSE 注释行时刻（未开启/未触发不显示） -->
              <div v-if="record.fake_first_token_ms != null" class="lat-row">
                <span class="lat-k">伪造首字</span>
                <span class="lat-v" style="color: #0e7490">{{ fmtDur(record.fake_first_token_ms) }}</span>
              </div>
              <div class="lat-row">
                <span class="lat-k">总耗时</span>
                <span class="lat-v" :style="record.duration_ms != null ? { color: LATENCY_COLORS[durationSeverity(record.duration_ms ?? 0)].text } : null">{{ record.duration_ms != null ? fmtDur(record.duration_ms) : '-' }}</span>
                <span v-if="record.duration_ms != null" class="lat-badge" :style="{ background: LATENCY_COLORS[durationSeverity(record.duration_ms ?? 0)].badge, color: LATENCY_COLORS[durationSeverity(record.duration_ms ?? 0)].text }">{{ LATENCY_COLORS[durationSeverity(record.duration_ms ?? 0)].label }}</span>
              </div>
            </div>
          </div>
        </template>
        <template v-else-if="column.key === 'in'">{{ fmt(record.input_tokens) }}</template>
        <template v-else-if="column.key === 'out'">{{ fmt(record.output_tokens) }}</template>
        <template v-else-if="column.key === 'cr'">{{ fmt(record.cache_read_tokens) }}</template>
        <template v-else-if="column.key === 'cost'">{{ fmt$(record.total_cost) }}</template>
        <template v-else-if="column.key === 'ip'">{{ record.ip_address || '-' }}</template>
      </template>
    </a-table>

    <!-- 错误请求日志（旧 UsageView「错误」tab 等价，已组件化；分类筛选真实生效） -->
    <UsageErrorPanel />

    <!-- 用量清理弹窗（旧 UsageCleanupDialog 等价，已组件化；时区随请求体传入） -->
    <UsageCleanupModal
      v-model:open="cleanupOpen" :user-id="fUserId" :account-id="fAccountId"
      :api-key-id="fApiKeyId" :group-id="fGroupId"
    />
  </div>
</template>

<style scoped>
.lat-cell { display: flex; align-items: stretch; gap: 8px; }
/* 模型列：主行 = 计费模型
 * - .mdl-sent：映射后实际发往上游的模型（仅与计费模型不同时出现）
 * - .mdl-mismatch：上游响应声明与发送模型不一致的审计告警（低饱和橙红，符合状态标签规范） */
.mdl-cell { min-width: 0; }
.mdl-sent { font-size: 11px; color: var(--text2); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; margin-top: 1px; cursor: default; }
.mdl-mismatch {
  margin-top: 2px; padding: 1px 6px; border-radius: 4px; font-size: 10px; line-height: 16px;
  color: #b54747; background: color-mix(in srgb, #b54747 12%, transparent);
  border: 1px solid color-mix(in srgb, #b54747 28%, transparent);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis; cursor: help;
}
.mdl-mismatch .mm-tag { font-weight: 500; margin-right: 4px; }
.lat-bar { display: flex; flex-direction: column; width: 4px; border-radius: 999px; overflow: hidden; flex: none; cursor: help; }
.lat-seg { display: block; width: 100%; }
.lat-rows { display: grid; grid-template-columns: max-content max-content max-content; align-items: center; column-gap: 8px; row-gap: 2px; min-width: 0; }
.lat-row { display: contents; }
.lat-k { font-size: 11px; color: var(--text2); white-space: nowrap; }
.lat-v { font-size: 12px; font-weight: 500; font-variant-numeric: tabular-nums; white-space: nowrap; }
.lat-badge { font-size: 10px; line-height: 16px; padding: 0 5px; border-radius: 4px; white-space: nowrap; }
.usage { height: 100%; overflow-y: auto; }
.toolbar { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; flex-wrap: wrap; }
.total { font-size: 12px; color: var(--text2); }
.stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 10px; margin-bottom: 10px; }
.scard { padding: 12px 14px; border-radius: 10px; background: var(--card); border: 1px solid var(--hr); }
.scard .k { font-size: 12px; color: var(--text2); }
.scard .v { margin-top: 4px; font-size: 18px; font-weight: 600; }
.scard .s { margin-top: 2px; font-size: 11px; color: var(--text2); }
.c-ok { color: #2f9e63; }
.c-warn { color: #d18a2d; }
.c-std { color: var(--text2); }
.charts { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; margin-bottom: 10px; }
.chart-wide { grid-column: 1 / -1; }
.panel { padding: 12px 14px; border-radius: 10px; background: var(--card); border: 1px solid var(--hr); }
.panel h3 { margin: 0 0 8px; font-size: 13px; font-weight: 500; color: var(--text); }
.chart-box { position: relative; height: 240px; }
.chart-canvas { position: absolute; inset: 0; }
.chart-empty { position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; font-size: 12px; color: var(--text2); }
.bar-track { height: 6px; border-radius: 3px; background: var(--hr); overflow: hidden; }
.bar-fill { height: 100%; border-radius: 3px; background: color-mix(in srgb, var(--theme-2) 72%, transparent); }
.empty { font-size: 12px; color: var(--text2); }
/* 移动端：图表区退化为单列，统计卡自适应收窄 */
@media (max-width: 900px) {
  .charts { grid-template-columns: 1fr; }
  .stats { grid-template-columns: repeat(auto-fit, minmax(130px, 1fr)); }
}
</style>
