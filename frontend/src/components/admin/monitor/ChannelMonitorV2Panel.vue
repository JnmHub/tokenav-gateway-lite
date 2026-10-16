<template>
  <div class="space-y-5">
    <section class="card p-5 sm:p-6">
      <div class="flex flex-wrap items-start justify-between gap-4">
        <div>
          <div class="flex items-center gap-2">
            <span class="inline-flex h-9 w-9 items-center justify-center rounded-xl bg-primary-50 text-primary-600 dark:bg-primary-500/10 dark:text-primary-300">
              <Icon name="chart" size="sm" />
            </span>
            <div>
              <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ localText('真实流量监控', 'Real traffic monitor') }}</h2>
              <p class="text-xs text-gray-500 dark:text-gray-400">{{ statusText }}</p>
            </div>
          </div>
        </div>
        <div class="flex flex-wrap items-center gap-2">
          <div class="tabs inline-flex">
            <button v-for="item in ranges" :key="item.value" type="button" class="tab text-xs" :class="range === item.value ? 'tab-active' : ''" @click="range = item.value">
              {{ item.label }}
            </button>
          </div>
          <button type="button" class="btn btn-secondary btn-icon" :title="localText('刷新', 'Refresh')" :disabled="loading" @click="loadData">
            <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
          </button>
        </div>
      </div>
    </section>

    <section v-if="snapshot" class="grid gap-3 sm:grid-cols-2 xl:grid-cols-5">
      <article v-for="metric in summaryMetrics" :key="metric.label" class="card p-4">
        <p class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ metric.label }}</p>
        <strong class="mt-2 block text-2xl font-bold tabular-nums text-gray-900 dark:text-white">{{ metric.value }}</strong>
        <span class="mt-1 block text-xs text-gray-400">{{ metric.detail }}</span>
      </article>
    </section>

    <section class="card overflow-hidden">
      <div class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 px-5 py-4 dark:border-dark-700">
        <div>
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ localText('请求趋势', 'Request trend') }}</h3>
          <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">{{ localText('按时间展示真实成功和失败请求，不产生额外上游流量。', 'Real successful and failed requests over time, without extra upstream traffic.') }}</p>
        </div>
        <span v-if="snapshot?.coverage.bootstrap?.active" class="badge badge-primary">{{ localText(`历史回填 ${snapshot.coverage.bootstrap.progress_percent}%`, `Backfill ${snapshot.coverage.bootstrap.progress_percent}%`) }}</span>
      </div>
      <div class="h-72 p-4">
        <Line v-if="trendData" :data="trendData" :options="trendOptions" />
        <div v-else class="flex h-full items-center justify-center text-sm text-gray-400">{{ loading ? localText('正在加载…', 'Loading…') : localText('暂无真实请求数据', 'No real request data') }}</div>
      </div>
    </section>

    <section class="card overflow-hidden">
      <div class="border-b border-gray-100 px-5 pt-4 dark:border-dark-700">
        <div class="tabs inline-flex" role="tablist">
          <button v-for="tab in tabs" :key="tab.value" type="button" class="tab" :class="activeTab === tab.value ? 'tab-active' : ''" @click="activeTab = tab.value">
            {{ tab.label }}
          </button>
        </div>
      </div>
      <div class="overflow-x-auto p-4 sm:p-5">
        <table class="table min-w-[720px]">
          <thead>
            <tr v-if="activeTab === 'models'"><th>{{ localText('平台 / 模型', 'Platform / model') }}</th><th>{{ localText('请求数', 'Requests') }}</th><th>{{ localText('成功率', 'Success') }}</th><th>TTFT P50</th><th>RPM</th></tr>
            <tr v-else-if="activeTab === 'errors'"><th>{{ localText('错误类别', 'Error category') }}</th><th>{{ localText('数量', 'Count') }}</th><th>{{ localText('占比', 'Rate') }}</th><th>{{ localText('计入健康分', 'Included in health') }}</th></tr>
            <tr v-else><th>{{ localText('用户', 'User') }}</th><th>{{ localText('请求数', 'Requests') }}</th><th>{{ localText('成功率', 'Success') }}</th><th>{{ localText('Token 数', 'Tokens') }}</th><th>RPM</th></tr>
          </thead>
          <tbody>
            <template v-if="activeTab === 'models'">
              <tr v-for="row in models" :key="`${row.platform}:${row.model}`"><td><span class="text-xs text-gray-400">{{ row.platform }}</span><strong class="ml-2 text-gray-900 dark:text-white">{{ row.model }}</strong></td><td>{{ number(row.metrics.request_count) }}</td><td>{{ percent(1 - row.metrics.error_rate) }}</td><td>{{ milliseconds(row.metrics.ttft.p50_ms) }}</td><td>{{ decimal(row.metrics.rpm) }}</td></tr>
            </template>
            <template v-else-if="activeTab === 'errors'">
              <tr v-for="row in errors" :key="row.category"><td>{{ errorLabel(row.category) }}</td><td>{{ number(row.count) }}</td><td>{{ percent(row.rate) }}</td><td><span class="badge" :class="row.ignored ? 'badge-secondary' : 'badge-success'">{{ row.ignored ? localText('忽略', 'Ignored') : localText('计入', 'Included') }}</span></td></tr>
            </template>
            <template v-else>
              <tr v-for="row in users" :key="row.user_id || row.rank"><td><strong class="text-gray-900 dark:text-white">{{ row.username || row.email || row.display_label }}</strong></td><td>{{ number(row.metrics.request_count) }}</td><td>{{ percent(1 - row.metrics.error_rate) }}</td><td>{{ number(row.metrics.token_count) }}</td><td>{{ decimal(row.metrics.rpm) }}</td></tr>
            </template>
          </tbody>
        </table>
        <div v-if="activeRows.length === 0" class="py-10 text-center text-sm text-gray-400">{{ loading ? localText('正在加载…', 'Loading…') : localText('暂无数据', 'No data') }}</div>
      </div>
    </section>

    <section class="card p-5 sm:p-6">
      <div class="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ localText('V2 聚合设置', 'V2 aggregation settings') }}</h3>
          <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ localText('关闭后停止聚合；已生成的历史数据不会删除。', 'Disabling stops aggregation without deleting history.') }}</p>
        </div>
        <button type="button" class="btn btn-primary" :disabled="saving || !config" @click="saveConfig">{{ saving ? localText('保存中…', 'Saving…') : localText('保存设置', 'Save settings') }}</button>
      </div>
      <div v-if="config" class="mt-5 grid gap-4 sm:grid-cols-2">
        <div class="setting-row rounded-xl border border-gray-200 px-4 dark:border-dark-700"><div><h3>{{ localText('启用被动聚合', 'Enable passive aggregation') }}</h3><p>{{ localText('只读取真实请求和错误日志。', 'Reads only real request and error logs.') }}</p></div><Toggle v-model="config.enabled" /></div>
        <label class="field"><span>{{ localText('刷新频率', 'Refresh interval') }}</span><select v-model.number="config.refresh_interval_seconds" class="input"><option :value="60">{{ localText('每 1 分钟', 'Every minute') }}</option><option :value="300">{{ localText('每 5 分钟', 'Every 5 minutes') }}</option></select></label>
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Chart as ChartJS, CategoryScale, Filler, Legend, LinearScale, LineElement, PointElement, Tooltip } from 'chart.js'
import { Line } from 'vue-chartjs'
import Icon from '@/components/icons/Icon.vue'
import Toggle from '@/components/common/Toggle.vue'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import * as api from '@/api/channelMonitorV2'
import type { MonitorConfig, MonitorErrorRow, MonitorModelRow, MonitorRange, MonitorSnapshot, MonitorUserRow } from '@/api/channelMonitorV2'

ChartJS.register(CategoryScale, Filler, Legend, LinearScale, LineElement, PointElement, Tooltip)

const { locale } = useI18n()
const appStore = useAppStore()
const range = ref<MonitorRange>('90m')
const snapshot = ref<MonitorSnapshot | null>(null)
const models = ref<MonitorModelRow[]>([])
const errors = ref<MonitorErrorRow[]>([])
const users = ref<MonitorUserRow[]>([])
const config = ref<MonitorConfig | null>(null)
const activeTab = ref<'models' | 'errors' | 'users'>('models')
const loading = ref(false)
const saving = ref(false)
let controller: AbortController | null = null

const localText = (zh: string, en: string) => locale.value.toLowerCase().startsWith('zh') ? zh : en
const ranges = computed(() => [{ value: '90m' as const, label: localText('90 分钟', '90m') }, { value: '24h' as const, label: localText('24 小时', '24h') }, { value: '7d' as const, label: localText('7 天', '7d') }, { value: '30d' as const, label: localText('30 天', '30d') }])
const tabs = computed(() => [{ value: 'models' as const, label: localText('模型排行', 'Models') }, { value: 'errors' as const, label: localText('错误分类', 'Errors') }, { value: 'users' as const, label: localText('用户排行', 'Users') }])
const activeRows = computed(() => activeTab.value === 'models' ? models.value : activeTab.value === 'errors' ? errors.value : users.value)
const summaryMetrics = computed(() => snapshot.value ? [
  { label: localText('请求总数', 'Requests'), value: number(snapshot.value.metrics.request_count), detail: localText(`失败 ${number(snapshot.value.metrics.error_requests)}`, `${number(snapshot.value.metrics.error_requests)} failed`) },
  { label: localText('成功率', 'Success rate'), value: percent(1 - snapshot.value.metrics.error_rate), detail: localText('基于真实终态', 'From real final states') },
  { label: 'TTFT P50', value: milliseconds(snapshot.value.metrics.ttft.p50_ms), detail: localText('首个可见内容', 'First visible output') },
  { label: 'RPM', value: decimal(snapshot.value.metrics.rpm), detail: localText('每分钟请求', 'Requests per minute') },
  { label: 'TPM', value: number(snapshot.value.metrics.tpm), detail: localText('每分钟 Token', 'Tokens per minute') },
] : [])
const statusText = computed(() => snapshot.value?.coverage.data_through ? localText(`数据更新至 ${formatTime(snapshot.value.coverage.data_through)}`, `Updated through ${formatTime(snapshot.value.coverage.data_through)}`) : localText('等待真实请求聚合', 'Waiting for real traffic aggregation'))
const trendData = computed(() => {
  const rows = snapshot.value?.trend || []
  if (!rows.length) return null
  return { labels: rows.map(row => formatTime(row.bucket_start)), datasets: [
    { label: localText('成功', 'Success'), data: rows.map(row => row.metrics.success_requests), borderColor: '#10b981', backgroundColor: 'rgba(16,185,129,.12)', fill: true, tension: .35, pointRadius: 0 },
    { label: localText('失败', 'Failed'), data: rows.map(row => row.metrics.error_requests), borderColor: '#ef4444', backgroundColor: 'rgba(239,68,68,.06)', fill: true, tension: .35, pointRadius: 0 },
  ] }
})
const trendOptions = { responsive: true, maintainAspectRatio: false, interaction: { intersect: false, mode: 'index' as const }, plugins: { legend: { display: true, position: 'bottom' as const } }, scales: { x: { grid: { display: false }, ticks: { maxTicksLimit: 8 } }, y: { beginAtZero: true, grid: { color: 'rgba(148,163,184,.12)' } } } }

async function loadData() {
  controller?.abort()
  const request = new AbortController()
  controller = request
  loading.value = true
  const filter = { range: range.value }
  try {
    const [nextConfig, nextSnapshot, nextModels, nextErrors, nextUsers] = await Promise.all([api.getConfig(), api.getSnapshot(filter, request.signal), api.getModels(filter, request.signal), api.getErrors(filter, request.signal), api.getUsers(filter, request.signal)])
    if (request.signal.aborted) return
    config.value = nextConfig
    snapshot.value = nextSnapshot
    models.value = nextModels
    errors.value = nextErrors
    users.value = nextUsers
  } catch (error) {
    if ((error as { code?: string }).code !== 'ERR_CANCELED') appStore.showError(extractApiErrorMessage(error, localText('渠道监控读取失败', 'Failed to load channel monitor')))
  } finally {
    if (controller === request) loading.value = false
  }
}
async function saveConfig() {
  if (!config.value || saving.value) return
  saving.value = true
  try { config.value = await api.updateConfig(config.value); appStore.showSuccess(localText('监控设置已保存', 'Monitor settings saved')) } catch (error) { appStore.showError(extractApiErrorMessage(error, localText('保存失败', 'Save failed'))) } finally { saving.value = false }
}
function number(value: number) { return new Intl.NumberFormat(locale.value, { maximumFractionDigits: 1 }).format(value || 0) }
function decimal(value: number) { return new Intl.NumberFormat(locale.value, { maximumFractionDigits: 2 }).format(value || 0) }
function percent(value: number) { return `${Math.max(0, value || 0) * 100 < 0.1 && value > 0 ? '<0.1' : (Math.max(0, value || 0) * 100).toFixed(1)}%` }
function milliseconds(value: number | null) { return value == null ? '—' : `${number(value)} ms` }
function formatTime(value: string) { return new Intl.DateTimeFormat(locale.value, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }).format(new Date(value)) }
function errorLabel(value: string) { const labels: Record<string, string> = { authentication: localText('认证失败', 'Authentication'), context_limit: localText('上下文超限', 'Context limit'), content_policy: localText('内容策略', 'Content policy'), rate_or_capacity: localText('限流或容量', 'Rate or capacity'), timeout: localText('超时', 'Timeout'), transport_or_stream: localText('传输或流错误', 'Transport or stream'), upstream_5xx: localText('上游 5xx', 'Upstream 5xx'), internal: localText('内部错误', 'Internal'), other: localText('其他', 'Other') }; return labels[value] || value }

watch(range, loadData)
onMounted(loadData)
onBeforeUnmount(() => controller?.abort())
</script>
