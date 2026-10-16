<script setup>
/* 趋势图表 4 张（echarts 按需引入，零新增依赖）
 * 契约来源：旧 OpsThroughputTrendChart / OpsLatencyChart / OpsErrorTrendChart / OpsErrorDistributionChart：
 * - 吞吐趋势 GET /admin/ops/dashboard/throughput-trend → points[{bucket_start, qps, tps, request_count, token_consumed}]
 * - 延迟直方图 GET /admin/ops/dashboard/latency-histogram → buckets[{range, count}]
 * - 错误趋势 GET /admin/ops/dashboard/error-trend → points[{bucket_start, error_count_total, upstream_429_count, upstream_529_count, ...}]
 * - 错误分布 GET /admin/ops/dashboard/error-distribution → items[{status_code, total}]，点击扇区打开错误列表 */
import { ref, watch, onMounted, onUnmounted, nextTick } from 'vue'
import * as echarts from 'echarts/core'
import { LineChart, BarChart, PieChart } from 'echarts/charts'
import { GridComponent, TooltipComponent, LegendComponent, GraphicComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'

echarts.use([LineChart, BarChart, PieChart, GridComponent, TooltipComponent, LegendComponent, GraphicComponent, CanvasRenderer])

const props = defineProps({
  throughput: { type: Object, default: null },
  latency: { type: Object, default: null },
  errorTrend: { type: Object, default: null },
  errorDist: { type: Object, default: null },
  compact: { type: Boolean, default: false },
})
const emitEv = defineEmits(['openErrors'])

const AXIS_TXT = '#8a8a8a'
const SPLIT_LN = 'rgba(128,128,128,.18)'
const fmtT = (iso) => {
  const s = String(iso || '')
  return s.includes('T') ? s.slice(11, 16) : s.slice(5, 16)
}

/* 吞吐趋势：QPS/TPS 双序列（面积渐隐折线，与旧 Line+Filler 观感对齐） */
const thruEl = ref(null)
let thruChart = null
const renderThru = () => {
  if (!thruEl.value) return
  const pts = props.throughput?.points || []
  const xs = pts.map((p) => fmtT(p.bucket_start))
  const mk = (name, key, color) => ({
    name, type: 'line', smooth: true, showSymbol: false, data: pts.map((p) => +(p[key] || 0).toFixed(1)),
    itemStyle: { color }, lineStyle: { width: 2, color },
    areaStyle: { color: { type: 'linear', x: 0, y: 0, x2: 0, y2: 1, colorStops: [
      { offset: 0, color: color + '33' }, { offset: 1, color: color + '00' }] } },
  })
  const option = pts.length ? {
    grid: { left: 44, right: 12, top: 28, bottom: 22 },
    legend: { top: 0, left: 'center', icon: 'circle', itemWidth: 8, itemHeight: 8, textStyle: { color: AXIS_TXT, fontSize: 11 } },
    tooltip: { trigger: 'axis', confine: true },
    xAxis: { type: 'category', data: xs, axisLabel: { color: AXIS_TXT, fontSize: 10 }, axisLine: { lineStyle: { color: SPLIT_LN } }, axisTick: { show: false } },
    yAxis: { type: 'value', axisLabel: { color: AXIS_TXT, fontSize: 10 }, splitLine: { lineStyle: { color: SPLIT_LN } } },
    series: [mk('QPS', 'qps', '#3b82f6'), mk('TPS', 'tps', '#10b981')],
  } : emptyOpt()
  if (!thruChart) thruChart = echarts.init(thruEl.value)
  thruChart.setOption(option, true)
}

/* 延迟直方图：buckets {range,count} 柱状 */
const latEl = ref(null)
let latChart = null
const renderLat = () => {
  if (!latEl.value) return
  const bs = props.latency?.buckets || []
  const option = bs.length ? {
    grid: { left: 40, right: 12, top: 28, bottom: 34 },
    tooltip: { trigger: 'axis', confine: true, formatter: (ps) => {
      const p = ps[0]; const total = props.latency?.total_requests || 0
      const pctv = total ? ((p.value / total) * 100).toFixed(1) : '0.0'
      return `${p.name}<br/>${p.marker}请求数 ${p.value}（${pctv}%）` } },
    xAxis: { type: 'category', data: bs.map((b) => b.range), axisLabel: { color: AXIS_TXT, fontSize: 9, rotate: 24 }, axisLine: { lineStyle: { color: SPLIT_LN } }, axisTick: { show: false } },
    yAxis: { type: 'value', axisLabel: { color: AXIS_TXT, fontSize: 10 }, splitLine: { lineStyle: { color: SPLIT_LN } } },
    series: [{ type: 'bar', data: bs.map((b) => b.count), barMaxWidth: 26, itemStyle: { color: '#6366f1', borderRadius: [3, 3, 0, 0] } }],
  } : emptyOpt()
  if (!latChart) latChart = echarts.init(latEl.value)
  latChart.setOption(option, true)
}

/* 错误趋势：总错误 / 上游 429 / 上游 529 三序列 */
const etEl = ref(null)
let etChart = null
const renderET = () => {
  if (!etEl.value) return
  const pts = props.errorTrend?.points || []
  const xs = pts.map((p) => fmtT(p.bucket_start))
  const mk = (name, key, color) => ({
    name, type: 'line', smooth: true, showSymbol: false, data: pts.map((p) => p[key] || 0), itemStyle: { color }, lineStyle: { width: 2, color },
  })
  const option = pts.length ? {
    grid: { left: 36, right: 12, top: 28, bottom: 22 },
    legend: { top: 0, left: 'center', icon: 'circle', itemWidth: 8, itemHeight: 8, textStyle: { color: AXIS_TXT, fontSize: 11 } },
    tooltip: { trigger: 'axis', confine: true },
    xAxis: { type: 'category', data: xs, axisLabel: { color: AXIS_TXT, fontSize: 10 }, axisLine: { lineStyle: { color: SPLIT_LN } }, axisTick: { show: false } },
    yAxis: { type: 'value', minInterval: 1, axisLabel: { color: AXIS_TXT, fontSize: 10 }, splitLine: { lineStyle: { color: SPLIT_LN } } },
    series: [
      mk('总错误', 'error_count_total', '#ef4444'),
      mk('上游 429', 'upstream_429_count', '#a855f7'),
      mk('上游 529', 'upstream_529_count', '#f59e0b'),
    ],
  } : emptyOpt()
  if (!etChart) etChart = echarts.init(etEl.value)
  etChart.setOption(option, true)
}

/* 错误分布：状态码环形图 + Top 原因列表，点击打开错误列表 */
const edEl = ref(null)
let edChart = null
const renderED = () => {
  if (!edEl.value) return
  const items = (props.errorDist?.items || []).slice(0, 8)
  const COLORS = ['#ef4444', '#f59e0b', '#a855f7', '#3b82f6', '#10b981', '#6366f1', '#ec4899', '#64748b']
  const option = items.length ? {
    tooltip: { trigger: 'item', confine: true, formatter: (p) => `${p.marker}HTTP ${p.name}<br/>次数 ${p.value}（${p.percent}%）` },
    legend: { orient: 'vertical', right: 4, top: 'center', icon: 'circle', itemWidth: 8, itemHeight: 8, textStyle: { color: AXIS_TXT, fontSize: 10 } },
    series: [{
      type: 'pie', radius: ['52%', '78%'], center: ['38%', '50%'],
      label: { show: false }, data: items.map((it, i) => ({
        name: String(it.status_code), value: it.total, itemStyle: { color: COLORS[i % COLORS.length] },
      })),
    }],
  } : emptyOpt()
  if (!edChart) edChart = echarts.init(edEl.value)
  edChart.setOption(option, true)
  edChart.off('click')
  edChart.on('click', (p) => emitEv('openErrors', 'request', p.name ? Number(p.name) : null))
}

const emptyOpt = () => ({
  xAxis: { show: false }, yAxis: { show: false }, series: [],
  graphic: [{ type: 'text', left: 'center', top: 'middle', style: { text: '暂无数据', fill: AXIS_TXT, fontSize: 12 } }],
})

const renderAll = () => { renderThru(); renderLat(); renderET(); renderED() }
const onResize = () => { thruChart?.resize(); latChart?.resize(); etChart?.resize(); edChart?.resize() }
watch(() => [props.throughput, props.latency, props.errorTrend, props.errorDist], () => nextTick(renderAll))
onMounted(() => { window.addEventListener('resize', onResize); nextTick(renderAll) })
onUnmounted(() => { window.removeEventListener('resize', onResize); thruChart?.dispose(); latChart?.dispose(); etChart?.dispose(); edChart?.dispose() })
</script>

<template>
  <div class="tgrid" :class="{ compact }">
    <div class="tcard twide">
      <div class="tcard-h">吞吐趋势 <span class="tsub">QPS / TPS</span></div>
      <div ref="thruEl" class="tchart tall"></div>
    </div>
    <div class="tcard">
      <div class="tcard-h">延迟直方图 <span class="tsub">{{ latency?.total_requests ?? 0 }} 次请求</span></div>
      <div ref="latEl" class="tchart"></div>
    </div>
    <div class="tcard">
      <div class="tcard-h">错误趋势 <a class="td" @click="emitEv('openErrors', 'request')">请求错误</a><a class="td" @click="emitEv('openErrors', 'upstream')">上游错误</a></div>
      <div ref="etEl" class="tchart"></div>
    </div>
    <div class="tcard">
      <div class="tcard-h">错误分布 <span class="tsub">按状态码，共 {{ errorDist?.total ?? 0 }} 次 · 点击扇区查看</span></div>
      <div ref="edEl" class="tchart"></div>
    </div>
  </div>
</template>

<style scoped>
.tgrid { display: grid; grid-template-columns: repeat(auto-fit, minmax(320px, 1fr)); gap: 10px; }
.tcard { padding: 12px 14px 6px; border-radius: 10px; background: var(--card); border: 1px solid var(--hr); min-width: 0; }
.twide { grid-column: 1 / -1; }
.tcard-h { display: flex; align-items: center; gap: 10px; font-size: 13px; font-weight: 500; }
.tsub { font-size: 11px; font-weight: 400; color: var(--text2); }
.td { font-size: 11px; font-weight: 400; color: #3b82f6; cursor: pointer; }
.td:hover { text-decoration: underline; }
.tchart { width: 100%; height: 220px; }
.tall { height: 260px; }
@media (max-width: 720px) { .tchart, .tall { height: 190px; } }
</style>
