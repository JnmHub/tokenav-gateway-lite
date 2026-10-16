<script setup>
/* V2 被动统计面板（对照旧 ChannelMonitorV2Panel.vue，接口字段经真实后端核实）
 * 数据源：GET/PUT /admin/channel-monitor-v2/{config,snapshot,models,errors,users}?range=
 * 结构：范围切换（90m/24h/7d/30d）→ 5 汇总卡（总请求+失败/成功率/TTFT P50/RPM/TPM）
 *   → 成功/失败趋势图（echarts 按需引入，零新增依赖）→ 模型/错误/用户 3 表格
 *   → V2 聚合设置（启用开关 + 刷新频率 60s/300s + 保存）
 * 后端约束：refresh_interval_seconds 只接受 60/300；保存带 version，冲突返回 409。
 * 注意：读取接口在 V1 模式下被后端 403 保护（channelMonitorModeV2Guard），
 * 父组件已在 V1 模式下隐藏本面板。 */
import { ref, computed, onMounted, onUnmounted, nextTick, watch } from 'vue'
import { message } from 'ant-design-vue'
import { ReloadOutlined } from '@ant-design/icons-vue'
import * as echarts from 'echarts/core'
import { LineChart } from 'echarts/charts'
import { GridComponent, TooltipComponent, LegendComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import {
  getV2Config, updateV2Config, getV2Snapshot, getV2Models, getV2Errors, getV2Users,
} from './api'
import {
  V2_RANGES, V2_REFRESH_OPTIONS, errorLabel,
  fmtNum, fmtDec, fmtPercent, fmtMs, fmtTime,
} from './constants'

echarts.use([LineChart, GridComponent, TooltipComponent, LegendComponent, CanvasRenderer])

const range = ref('90m')
const loading = ref(false)
const saving = ref(false)
const snapshot = ref(null)
const models = ref([])
const errors = ref([])
const users = ref([])
const config = ref(null)

/* 加载序号守卫：范围快速切换时丢弃过期响应（替代旧 AbortController 语义） */
let loadSeq = 0

async function loadData() {
  const seq = ++loadSeq
  loading.value = true
  try {
    const [cfg, snap, mdl, err, usr] = await Promise.all([
      getV2Config(), getV2Snapshot(range.value), getV2Models(range.value),
      getV2Errors(range.value), getV2Users(range.value),
    ])
    if (seq !== loadSeq) return // 已被更新的请求覆盖
    config.value = cfg
    snapshot.value = snap
    models.value = mdl.items || []
    errors.value = err.items || []
    users.value = usr.items || []
    await nextTick()
    renderChart()
    // 开窗动画（transform scale）期间 echarts 首测的容器宽可能偏小，
    // 动画结束后（约 260ms）按真实宽度重设一次 canvas
    setTimeout(() => chart?.resize(), 300)
  } catch (e) {
    if (seq !== loadSeq) return
    message.error(`渠道监控读取失败：${e.message}`)
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

/* ---------- 汇总卡（字段对照 MonitorMetric JSON） ---------- */
const summaryCards = computed(() => {
  const m = snapshot.value?.metrics
  if (!m) return []
  return [
    { label: '请求总数', value: fmtNum(m.request_count), detail: `失败 ${fmtNum(m.error_requests)}` },
    { label: '成功率', value: fmtPercent(1 - m.error_rate), detail: '基于真实终态' },
    { label: 'TTFT P50', value: fmtMs(m.ttft?.p50_ms), detail: '首个可见内容' },
    { label: 'RPM', value: fmtDec(m.rpm), detail: '每分钟请求' },
    { label: 'TPM', value: fmtNum(m.tpm), detail: '每分钟 Token' },
  ]
})

const statusText = computed(() => {
  const through = snapshot.value?.coverage?.data_through
  return through ? `数据更新至 ${fmtTime(through)}` : '等待真实请求聚合'
})

/* ---------- 趋势图（成功/失败双序列面积图，配色沿用旧实现） ---------- */
const chartEl = ref(null)
let chart = null

function buildOption() {
  const rows = snapshot.value?.trend || []
  if (!rows.length) return null
  const mk = (name, key, color, areaColor) => ({
    name,
    type: 'line',
    smooth: true,
    showSymbol: false,
    data: rows.map((r) => r.metrics[key] || 0),
    itemStyle: { color },
    lineStyle: { width: 2, color },
    areaStyle: { color: areaColor },
  })
  return {
    grid: { left: 48, right: 14, top: 30, bottom: 24 },
    legend: { top: 0, left: 'center', icon: 'circle', itemWidth: 8, itemHeight: 8, textStyle: { color: '#8a8a8a', fontSize: 11 } },
    tooltip: { trigger: 'axis', confine: true },
    xAxis: {
      type: 'category',
      data: rows.map((r) => fmtTime(r.bucket_start)),
      axisLabel: { color: '#8a8a8a', fontSize: 10 },
      axisLine: { lineStyle: { color: 'rgba(128,128,128,.3)' } },
      axisTick: { show: false },
    },
    yAxis: {
      type: 'value',
      beginAtZero: true,
      axisLabel: { color: '#8a8a8a', fontSize: 10 },
      splitLine: { lineStyle: { color: 'rgba(128,128,128,.18)' } },
    },
    series: [
      mk('成功', 'success_requests', '#10b981', 'rgba(16,185,129,.12)'),
      mk('失败', 'error_requests', '#ef4444', 'rgba(239,68,68,.06)'),
    ],
  }
}

function renderChart() {
  const opt = buildOption()
  if (!chartEl.value) return
  if (!chart) chart = echarts.init(chartEl.value)
  chart.setOption(opt || { xAxis: { show: false }, yAxis: { show: false }, series: [] }, true)
}

const onResize = () => chart?.resize()

/* ---------- 3 表格 ---------- */
const activeTab = ref('models')

const modelColumns = [
  { title: '平台 / 模型', key: 'model', ellipsis: true },
  { title: '请求数', key: 'requests', width: 90, align: 'right' },
  { title: '成功率', key: 'success', width: 90, align: 'right' },
  { title: 'TTFT P50', key: 'ttft', width: 100, align: 'right' },
  { title: 'RPM', key: 'rpm', width: 90, align: 'right' },
]
const errorColumns = [
  { title: '错误类别', key: 'category', ellipsis: true },
  { title: '数量', dataIndex: 'count', key: 'count', width: 90, align: 'right' },
  { title: '占比', key: 'rate', width: 90, align: 'right' },
  { title: '计入健康分', key: 'ignored', width: 110 },
]
const userColumns = [
  { title: '用户', key: 'user', ellipsis: true },
  { title: '请求数', key: 'requests', width: 90, align: 'right' },
  { title: '成功率', key: 'success', width: 90, align: 'right' },
  { title: 'Token 数', key: 'tokens', width: 100, align: 'right' },
  { title: 'RPM', key: 'rpm', width: 90, align: 'right' },
]
const tabColumns = computed(() => ({ models: modelColumns, errors: errorColumns, users: userColumns }[activeTab.value]))
const tabRows = computed(() => ({ models: models.value, errors: errors.value, users: users.value })[activeTab.value])

const userLabel = (r) => r.username || r.email || r.display_label || (r.user_id != null ? `#${r.user_id}` : '-')
/* 三表数据无统一主键：模型用 platform:model、错误用 category、用户用 user_id/rank */
const tabRowKey = (r) =>
  activeTab.value === 'models' ? `${r.platform}:${r.model}`
    : activeTab.value === 'errors' ? r.category
      : String(r.user_id ?? r.rank)

/* ---------- 聚合设置 ---------- */
async function saveConfig() {
  if (!config.value || saving.value) return
  saving.value = true
  try {
    // 全量回传（含 version，后端并发冲突检测；refresh 只允许 60/300）
    config.value = await updateV2Config(config.value)
    message.success('监控设置已保存')
  } catch (e) {
    message.error(`保存失败：${e.message}`)
  } finally {
    saving.value = false
  }
}

watch(range, loadData)
onMounted(() => {
  window.addEventListener('resize', onResize)
  loadData()
})
onUnmounted(() => {
  window.removeEventListener('resize', onResize)
  chart?.dispose()
  chart = null
})
</script>

<template>
  <div class="v2">
    <!-- 头部：标题 + 状态 + 范围切换 + 刷新 -->
    <div class="v2-head">
      <div class="v2-title">
        <span class="v2-name">真实流量监控</span>
        <span class="v2-status">{{ statusText }}</span>
      </div>
      <div class="v2-ops">
        <a-segmented v-model:value="range" :options="V2_RANGES" :disabled="loading" />
        <a-button size="small" :loading="loading" title="刷新" @click="loadData">
          <template #icon><ReloadOutlined /></template>
        </a-button>
      </div>
    </div>

    <!-- 5 汇总卡 -->
    <div v-if="summaryCards.length" class="stats">
      <div v-for="c in summaryCards" :key="c.label" class="stat-card">
        <div class="stat-label">{{ c.label }}</div>
        <div class="stat-value">{{ c.value }}</div>
        <div class="stat-detail">{{ c.detail }}</div>
      </div>
    </div>

    <!-- 请求趋势 -->
    <div class="v2-card">
      <div class="v2-card-head">
        <div>
          <div class="v2-card-t">请求趋势</div>
          <div class="v2-card-d">按时间展示真实成功和失败请求，不产生额外上游流量。</div>
        </div>
        <a-tag v-if="snapshot?.coverage?.bootstrap?.active" color="blue">
          历史回填 {{ snapshot.coverage.bootstrap.progress_percent }}%
        </a-tag>
      </div>
      <div class="chart-wrap">
        <div v-show="snapshot?.trend?.length" ref="chartEl" class="chart" />
        <div v-if="!snapshot?.trend?.length" class="chart-empty">
          {{ loading ? '正在加载…' : '暂无真实请求数据' }}
        </div>
      </div>
    </div>

    <!-- 模型 / 错误 / 用户 3 表格 -->
    <div class="v2-card">
      <a-tabs v-model:active-key="activeTab" size="small" class="v2-tabs">
        <a-tab-pane key="models" tab="模型排行" />
        <a-tab-pane key="errors" tab="错误分类" />
        <a-tab-pane key="users" tab="用户排行" />
      </a-tabs>
      <a-table
        :columns="tabColumns"
        :data-source="tabRows"
        :loading="loading"
        :pagination="false"
        size="small"
        :row-key="tabRowKey"
        :scroll="{ x: 'max-content' }"
        class="v2-table"
      >
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'model'">
            <span class="cell-platform">{{ record.platform }}</span>
            <span class="cell-model">{{ record.model }}</span>
          </template>
          <template v-else-if="column.key === 'category'">
            {{ errorLabel(record.category) }}
          </template>
          <template v-else-if="column.key === 'rate'">
            {{ fmtPercent(record.rate) }}
          </template>
          <template v-else-if="column.key === 'ignored'">
            <!-- ignored=true 表示该类错误不计入 error_rate / 健康分（字段经后端核实） -->
            <a-tag :color="record.ignored ? 'default' : 'green'">{{ record.ignored ? '忽略' : '计入' }}</a-tag>
          </template>
          <template v-else-if="column.key === 'user'">
            {{ userLabel(record) }}
          </template>
          <template v-else-if="column.key === 'requests'">
            {{ fmtNum(record.metrics.request_count) }}
          </template>
          <template v-else-if="column.key === 'success'">
            {{ fmtPercent(1 - record.metrics.error_rate) }}
          </template>
          <template v-else-if="column.key === 'ttft'">
            {{ fmtMs(record.metrics.ttft?.p50_ms) }}
          </template>
          <template v-else-if="column.key === 'rpm'">
            {{ fmtDec(record.metrics.rpm) }}
          </template>
          <template v-else-if="column.key === 'tokens'">
            {{ fmtNum(record.metrics.token_count) }}
          </template>
        </template>
        <template #emptyText>
          <span class="cell-empty">{{ loading ? '正在加载…' : '暂无数据' }}</span>
        </template>
      </a-table>
    </div>

    <!-- V2 聚合设置 -->
    <div class="v2-card">
      <div class="v2-card-head no-border">
        <div>
          <div class="v2-card-t">V2 聚合设置</div>
          <div class="v2-card-d">关闭后停止聚合；已生成的历史数据不会删除。</div>
        </div>
        <a-button type="primary" :loading="saving" :disabled="!config" @click="saveConfig">保存设置</a-button>
      </div>
      <div v-if="config" class="cfg-grid">
        <div class="frow">
          <div class="frow-main">
            <div class="frow-t">启用被动聚合</div>
            <div class="frow-d">只读取真实请求和错误日志。</div>
          </div>
          <a-switch v-model:checked="config.enabled" />
        </div>
        <div class="frow">
          <div class="frow-main">
            <div class="frow-t">刷新频率</div>
            <div class="frow-d">聚合快照的刷新间隔。</div>
          </div>
          <a-select v-model:value="config.refresh_interval_seconds" :options="V2_REFRESH_OPTIONS" style="width: 130px" />
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* 根节点单列显式轨道：minmax(0,1fr) 禁止 grid 子项以 min-content
 * 撑宽轨道（旧 auto 轨道让 v2-head/图表卡溢出容器，开窗时可见） */
.v2 { display: grid; grid-template-columns: minmax(0, 1fr); gap: 12px; min-width: 0; }
.v2-head {
  display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 10px;
  padding: 10px 12px; background: var(--card); border-radius: 10px;
}
.v2-title { display: flex; align-items: baseline; gap: 10px; min-width: 0; }
.v2-name { font-size: 14px; font-weight: 500; }
.v2-status { font-size: 12px; color: var(--text2); }
.v2-ops { display: flex; align-items: center; gap: 8px; }

/* 汇总卡：5 列栅格，窄屏自适应降列 */
.stats { display: grid; grid-template-columns: repeat(5, 1fr); gap: 8px; }
.stat-card { padding: 10px 12px; background: var(--card); border-radius: 10px; min-width: 0; }
.stat-label { font-size: 12px; color: var(--text2); }
.stat-value { font-size: 22px; font-weight: 600; margin-top: 2px; font-variant-numeric: tabular-nums; }
.stat-detail { font-size: 11.5px; color: var(--text2); margin-top: 1px; }

/* 通用卡片 */
.v2-card { background: var(--card); border-radius: 10px; padding: 12px 14px; }
.v2-card-head {
  display: flex; align-items: center; justify-content: space-between; gap: 10px;
  padding-bottom: 10px; border-bottom: 1px solid var(--hr);
}
.v2-card-head.no-border { border-bottom: none; padding-bottom: 0; }
.v2-card-t { font-size: 13px; font-weight: 500; }
.v2-card-d { font-size: 12px; color: var(--text2); margin-top: 2px; }

/* 图表容器：固定高度供 echarts 正确测量 */
.chart-wrap { position: relative; height: 260px; margin-top: 10px; }
.chart { width: 100%; height: 100%; }
.chart-empty {
  position: absolute; inset: 0; display: flex; align-items: center; justify-content: center;
  font-size: 12px; color: var(--text2);
}

/* 表格 */
.v2-tabs { margin-bottom: 4px; }
.v2-tabs :deep(.ant-tabs-tab) { padding: 6px 2px; }
.cell-platform { font-size: 11.5px; color: var(--text2); margin-right: 8px; }
.cell-model { font-size: 12.5px; font-weight: 500; }
.cell-empty { font-size: 12px; color: var(--text2); }

/* 设置行 */
.cfg-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; margin-top: 10px; }
.frow {
  display: flex; align-items: center; justify-content: space-between; gap: 16px;
  padding: 8px 10px; border-radius: 8px; min-height: 40px;
  border: 1px solid var(--hr);
}
.frow:hover { filter: brightness(1.03); }
.frow-main { min-width: 0; }
.frow-t { font-size: 13px; }
.frow-d { font-size: 12px; color: var(--text2); margin-top: 1px; }

@media (max-width: 960px) {
  .stats { grid-template-columns: repeat(3, 1fr); }
}
@media (max-width: 640px) {
  .stats { grid-template-columns: repeat(2, 1fr); }
  .cfg-grid { grid-template-columns: 1fr; }
}
</style>
