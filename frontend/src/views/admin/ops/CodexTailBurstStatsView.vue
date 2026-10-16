<template>
  <AppLayout>
    <div class="mx-auto max-w-[1500px] space-y-5 pb-12">
      <section class="card overflow-hidden">
        <div class="flex flex-col gap-4 border-b border-gray-100 px-5 py-4 dark:border-dark-700 sm:flex-row sm:items-start sm:justify-between sm:px-6">
          <div class="flex min-w-0 items-start gap-3">
            <span class="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-amber-50 text-amber-600 dark:bg-amber-900/20 dark:text-amber-300">
              <Icon name="fire" size="md" />
            </span>
            <div class="min-w-0">
              <h1 class="text-xl font-bold text-gray-900 dark:text-white">{{ t('admin.ops.tailBurstStats.title') }}</h1>
              <p class="mt-1 max-w-3xl text-sm leading-6 text-gray-500 dark:text-gray-400">
                {{ t('admin.ops.tailBurstStats.description') }}
              </p>
            </div>
          </div>
          <button type="button" class="btn btn-secondary btn-sm shrink-0" :disabled="loading" @click="loadStats">
            <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
            {{ loading ? t('admin.ops.tailBurstStats.refreshing') : t('common.refresh') }}
          </button>
        </div>

        <div class="flex flex-wrap items-end gap-3 bg-gray-50/70 px-5 py-4 dark:bg-dark-900/45 sm:px-6">
          <div class="min-w-[230px] flex-1 sm:flex-none">
            <label class="mb-1.5 block text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('admin.ops.tailBurstStats.dateRange') }}</label>
            <DateRangePicker :start-date="startDate" :end-date="endDate" @change="onDateRangeChange" />
          </div>
          <div class="w-full sm:w-32">
            <label for="tail-burst-account" class="mb-1.5 block text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('admin.ops.tailBurstStats.account') }}</label>
            <input
              id="tail-burst-account"
              v-model="accountInput"
              type="number"
              min="1"
              inputmode="numeric"
              :placeholder="t('admin.ops.tailBurstStats.accountPlaceholder')"
              class="input w-full"
              @keyup.enter="applyFilters"
            />
          </div>
          <div class="w-full sm:w-32">
            <label class="mb-1.5 block text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('admin.ops.tailBurstStats.window') }}</label>
            <Select v-model="windowFilter" :options="windowOptions" :aria-label="t('admin.ops.tailBurstStats.window')" />
          </div>
          <div class="w-full sm:w-40">
            <label class="mb-1.5 block text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('admin.ops.tailBurstStats.reason') }}</label>
            <Select v-model="reasonFilter" :options="reasonOptions" :aria-label="t('admin.ops.tailBurstStats.reason')" />
          </div>
          <div class="w-full sm:w-32">
            <label class="mb-1.5 block text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('admin.ops.tailBurstStats.status') }}</label>
            <Select v-model="statusFilter" :options="statusOptions" :aria-label="t('admin.ops.tailBurstStats.status')" />
          </div>
          <div class="flex w-full items-center justify-end gap-2 sm:w-auto">
            <button type="button" class="btn btn-secondary" :disabled="loading" @click="resetFilters">
              {{ t('admin.ops.tailBurstStats.reset') }}
            </button>
            <button type="button" class="btn btn-primary" :disabled="loading" @click="applyFilters">
              <Icon name="filter" size="sm" class="mr-1.5" />
              {{ t('admin.ops.tailBurstStats.apply') }}
            </button>
          </div>
        </div>
      </section>

      <div v-if="errorMessage" class="flex items-start gap-3 rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-900/60 dark:bg-red-950/30 dark:text-red-300" role="alert">
        <Icon name="exclamationTriangle" size="sm" class="mt-0.5 shrink-0" />
        <div class="min-w-0 flex-1">
          <div class="font-medium">{{ t('admin.ops.tailBurstStats.loadFailed') }}</div>
          <div class="mt-0.5 break-words">{{ errorMessage }}</div>
        </div>
        <button type="button" class="text-xs font-semibold underline underline-offset-2" @click="loadStats">{{ t('common.retry') }}</button>
      </div>

      <div v-if="loading && !hasLoadedOnce" class="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <div v-for="index in 4" :key="index" class="h-28 animate-pulse rounded-xl bg-gray-100 dark:bg-dark-800"></div>
      </div>

      <div v-else class="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <div v-for="card in summaryCards" :key="card.key" class="rounded-xl border border-gray-200 bg-white p-4 shadow-sm dark:border-dark-700 dark:bg-dark-800">
          <div class="flex items-center justify-between gap-3">
            <span class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ card.label }}</span>
            <span class="flex h-8 w-8 items-center justify-center rounded-lg" :class="card.iconClass"><Icon :name="card.icon" size="sm" /></span>
          </div>
          <div class="mt-3 text-2xl font-bold tabular-nums text-gray-900 dark:text-white">{{ card.value }}</div>
        </div>
      </div>

      <section class="card p-5 sm:p-6">
        <div class="mb-4 flex flex-wrap items-start justify-between gap-2">
          <div>
            <h2 class="text-base font-bold text-gray-900 dark:text-white">{{ t('admin.ops.tailBurstStats.trendTitle') }}</h2>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ops.tailBurstStats.trendDescription') }}</p>
          </div>
          <span v-if="lastUpdated" class="text-xs text-gray-400 dark:text-gray-500">{{ t('admin.ops.tailBurstStats.updatedAt', { time: formatDateTime(lastUpdated) }) }}</span>
        </div>
        <div v-if="loading && !hasLoadedOnce" class="flex h-64 items-center justify-center text-sm text-gray-500 dark:text-gray-400">{{ t('admin.ops.tailBurstStats.loading') }}</div>
        <div v-else-if="chartData && trend.length" class="h-64 sm:h-72">
          <Line :data="chartData" :options="chartOptions" />
        </div>
        <div v-else class="flex h-64 flex-col items-center justify-center rounded-xl bg-gray-50 text-sm text-gray-500 dark:bg-dark-900 dark:text-gray-400">
          <Icon name="chart" size="lg" class="mb-2 text-gray-300 dark:text-dark-600" />
          {{ t('admin.ops.tailBurstStats.emptyTrend') }}
        </div>
      </section>

      <section class="card overflow-hidden">
        <div class="flex flex-wrap items-center justify-between gap-2 border-b border-gray-100 px-5 py-4 dark:border-dark-700 sm:px-6">
          <div>
            <h2 class="text-base font-bold text-gray-900 dark:text-white">{{ t('admin.ops.tailBurstStats.sessionsTitle') }}</h2>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ops.tailBurstStats.sessionsHint', { total }) }}</p>
          </div>
          <span class="badge badge-warning">{{ t('admin.ops.tailBurstStats.badge') }}</span>
        </div>

        <div v-if="loading && !hasLoadedOnce" class="space-y-3 p-5">
          <div v-for="index in 5" :key="index" class="h-12 animate-pulse rounded-lg bg-gray-100 dark:bg-dark-800"></div>
        </div>
        <div v-else-if="items.length === 0" class="px-5 py-14 text-center text-sm text-gray-500 dark:text-gray-400">
          <Icon name="inbox" size="lg" class="mx-auto mb-3 text-gray-300 dark:text-dark-600" />
          {{ t('admin.ops.tailBurstStats.empty') }}
        </div>
        <template v-else>
          <div class="hidden overflow-x-auto md:block">
            <table class="min-w-full text-left text-sm">
              <thead class="bg-gray-50 text-xs text-gray-500 dark:bg-dark-900/70 dark:text-gray-400">
                <tr>
                  <th class="whitespace-nowrap px-5 py-3 font-semibold">{{ t('admin.ops.tailBurstStats.columns.account') }}</th>
                  <th class="whitespace-nowrap px-5 py-3 font-semibold">{{ t('admin.ops.tailBurstStats.columns.startedAt') }}</th>
                  <th class="whitespace-nowrap px-5 py-3 font-semibold">{{ t('admin.ops.tailBurstStats.columns.window') }}</th>
                  <th class="whitespace-nowrap px-5 py-3 font-semibold">{{ t('admin.ops.tailBurstStats.columns.reason') }}</th>
                  <th class="whitespace-nowrap px-5 py-3 font-semibold">{{ t('admin.ops.tailBurstStats.columns.delta') }}</th>
                  <th class="whitespace-nowrap px-5 py-3 font-semibold">{{ t('admin.ops.tailBurstStats.columns.requests') }}</th>
                  <th class="whitespace-nowrap px-5 py-3 font-semibold">{{ t('admin.ops.tailBurstStats.columns.tokens') }}</th>
                  <th class="whitespace-nowrap px-5 py-3 font-semibold">{{ t('admin.ops.tailBurstStats.columns.status') }}</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-gray-100 dark:divide-dark-800">
                <tr v-for="item in items" :key="item.session_id" class="text-gray-700 dark:text-gray-200">
                  <td class="max-w-[220px] px-5 py-3"><div class="truncate font-medium" :title="item.account_name">{{ item.account_name || `#${item.account_id}` }}</div><div class="text-xs text-gray-400">#{{ item.account_id }}</div></td>
                  <td class="whitespace-nowrap px-5 py-3 text-xs text-gray-500 dark:text-gray-400">{{ formatDateTime(item.started_at) }}<div v-if="item.ended_at" class="mt-0.5">→ {{ formatDateTime(item.ended_at) }}</div><div v-else class="mt-0.5 text-amber-600 dark:text-amber-400">{{ t('admin.ops.tailBurstStats.active') }}</div></td>
                  <td class="px-5 py-3"><span class="badge badge-gray">{{ formatWindow(item.window) }}</span></td>
                  <td class="max-w-[160px] px-5 py-3 text-xs">{{ formatReason(item.reason) }}</td>
                  <td class="whitespace-nowrap px-5 py-3 font-mono text-xs">{{ formatPercent(item.delta_percent) }}</td>
                  <td class="px-5 py-3 tabular-nums">{{ formatInteger(item.requests) }}</td>
                  <td class="px-5 py-3 tabular-nums">{{ formatTokens(item.tokens) }}</td>
                  <td class="px-5 py-3"><span class="badge" :class="item.status === 'active' ? 'badge-warning' : 'badge-success'">{{ formatStatus(item.status) }}</span></td>
                </tr>
              </tbody>
            </table>
          </div>

          <div class="divide-y divide-gray-100 dark:divide-dark-800 md:hidden">
            <article v-for="item in items" :key="item.session_id" class="space-y-3 p-4">
              <div class="flex items-start justify-between gap-3">
                <div class="min-w-0"><div class="truncate text-sm font-semibold text-gray-900 dark:text-white">{{ item.account_name || `#${item.account_id}` }}</div><div class="text-xs text-gray-400">#{{ item.account_id }}</div></div>
                <span class="badge shrink-0" :class="item.status === 'active' ? 'badge-warning' : 'badge-success'">{{ formatStatus(item.status) }}</span>
              </div>
              <dl class="grid grid-cols-2 gap-x-4 gap-y-2 text-xs">
                <div><dt class="text-gray-400">{{ t('admin.ops.tailBurstStats.columns.startedAt') }}</dt><dd class="mt-0.5 text-gray-700 dark:text-gray-200">{{ formatDateTime(item.started_at) }}</dd></div>
                <div><dt class="text-gray-400">{{ t('admin.ops.tailBurstStats.columns.window') }}</dt><dd class="mt-0.5 text-gray-700 dark:text-gray-200">{{ formatWindow(item.window) }}</dd></div>
                <div><dt class="text-gray-400">{{ t('admin.ops.tailBurstStats.columns.reason') }}</dt><dd class="mt-0.5 text-gray-700 dark:text-gray-200">{{ formatReason(item.reason) }}</dd></div>
                <div><dt class="text-gray-400">{{ t('admin.ops.tailBurstStats.columns.delta') }}</dt><dd class="mt-0.5 font-mono text-gray-700 dark:text-gray-200">{{ formatPercent(item.delta_percent) }}</dd></div>
                <div><dt class="text-gray-400">{{ t('admin.ops.tailBurstStats.columns.requests') }}</dt><dd class="mt-0.5 tabular-nums text-gray-700 dark:text-gray-200">{{ formatInteger(item.requests) }}</dd></div>
                <div><dt class="text-gray-400">{{ t('admin.ops.tailBurstStats.columns.tokens') }}</dt><dd class="mt-0.5 tabular-nums text-gray-700 dark:text-gray-200">{{ formatTokens(item.tokens) }}</dd></div>
              </dl>
            </article>
          </div>

          <Pagination
            v-if="total > 0"
            :page="page"
            :total="total"
            :page-size="pageSize"
            :show-page-size-selector="true"
            @update:page="onPageChange"
            @update:page-size="onPageSizeChange"
          />
        </template>
      </section>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Chart as ChartJS,
  CategoryScale,
  Filler,
  LineElement,
  LinearScale,
  PointElement,
  Tooltip,
  type ChartData,
  type ChartOptions,
} from 'chart.js'
import { Line } from 'vue-chartjs'
import AppLayout from '@/components/layout/GatewayLiteLayout.vue'
import DateRangePicker from '@/components/common/DateRangePicker.vue'
import Icon from '@/components/icons/Icon.vue'
import Pagination from '@/components/common/Pagination.vue'
import Select from '@/components/common/Select.vue'
import {
  opsAPI,
  type OpsCodexTailBurstReason,
  type OpsCodexTailBurstSessionStatus,
  type OpsCodexTailBurstStatsResponse,
  type OpsCodexTailBurstWindow,
} from '@/api/admin/ops'
import { extractApiErrorMessage } from '@/utils/apiError'
import {
  addDaysToDateInput,
  dateInputBoundaryToISOString,
  formatDefaultTimeZoneDateInput,
} from '@/utils/timezone'
import { formatDateTime } from '@/utils/format'
import { formatTokenCount } from '@/utils/format'

ChartJS.register(CategoryScale, LinearScale, PointElement, LineElement, Tooltip, Filler)

const { t, locale } = useI18n()
const nowDate = formatDefaultTimeZoneDateInput()
const defaultStartDate = addDaysToDateInput(nowDate, -29)

const startDate = ref(defaultStartDate)
const endDate = ref(nowDate)
const accountInput = ref('')
const windowFilter = ref<OpsCodexTailBurstWindow | ''>('')
const reasonFilter = ref<OpsCodexTailBurstReason | ''>('')
const statusFilter = ref<OpsCodexTailBurstSessionStatus | ''>('')
const page = ref(1)
const pageSize = ref(20)
const loading = ref(false)
const hasLoadedOnce = ref(false)
const errorMessage = ref('')
const response = ref<OpsCodexTailBurstStatsResponse | null>(null)
let abortController: AbortController | null = null

const items = computed(() => response.value?.items ?? [])
const trend = computed(() => response.value?.trend ?? [])
const summary = computed(() => response.value?.summary)
const total = computed(() => Number(response.value?.total ?? 0))
const lastUpdated = ref<Date | null>(null)

const windowOptions = computed(() => [
  { value: '', label: t('admin.ops.tailBurstStats.all') },
  { value: '5h', label: '5h' },
  { value: '7d', label: '7d' },
])
const reasonOptions = computed(() => [
  { value: '', label: t('admin.ops.tailBurstStats.all') },
  { value: 'used_ratio', label: t('admin.ops.tailBurstStats.reasons.usedRatio') },
  { value: 'reset_soon', label: t('admin.ops.tailBurstStats.reasons.resetSoon') },
  { value: 'account_expiry', label: t('admin.ops.tailBurstStats.reasons.accountExpiry') },
])
const statusOptions = computed(() => [
  { value: '', label: t('admin.ops.tailBurstStats.all') },
  { value: 'active', label: t('admin.ops.tailBurstStats.statuses.active') },
  { value: 'settled', label: t('admin.ops.tailBurstStats.statuses.settled') },
])

const formatInteger = (value: number | null | undefined) => new Intl.NumberFormat(locale.value === 'zh' ? 'zh-CN' : 'en-US').format(Number(value || 0))
const formatTokens = (value: number | null | undefined) => formatTokenCount(Number(value || 0), locale.value)
const formatPercent = (value: number | null | undefined) => `${Number(value || 0).toFixed(2)}%`
const formatWindow = (value?: string | null) => value === '5h' || value === '7d' ? value : '-'
const formatReason = (value?: string | null) => {
  if (value === 'used_ratio') return t('admin.ops.tailBurstStats.reasons.usedRatio')
  if (value === 'reset_soon') return t('admin.ops.tailBurstStats.reasons.resetSoon')
  if (value === 'account_expiry') return t('admin.ops.tailBurstStats.reasons.accountExpiry')
  return value || '-'
}
const formatStatus = (value?: string | null) => value === 'active' ? t('admin.ops.tailBurstStats.statuses.active') : t('admin.ops.tailBurstStats.statuses.settled')

const summaryCards = computed(() => [
  { key: 'sessions', label: t('admin.ops.tailBurstStats.summary.sessions'), value: formatInteger(summary.value?.sessions), icon: 'fire' as const, iconClass: 'bg-amber-50 text-amber-600 dark:bg-amber-900/20 dark:text-amber-300' },
  { key: 'active', label: t('admin.ops.tailBurstStats.summary.active'), value: formatInteger(summary.value?.active_sessions), icon: 'clock' as const, iconClass: 'bg-blue-50 text-blue-600 dark:bg-blue-900/20 dark:text-blue-300' },
  { key: 'requests', label: t('admin.ops.tailBurstStats.summary.requests'), value: formatInteger(summary.value?.requests), icon: 'chartBar' as const, iconClass: 'bg-emerald-50 text-emerald-600 dark:bg-emerald-900/20 dark:text-emerald-300' },
  { key: 'delta', label: t('admin.ops.tailBurstStats.summary.delta'), value: formatPercent(summary.value?.delta_percent), icon: 'trendingUp' as const, iconClass: 'bg-violet-50 text-violet-600 dark:bg-violet-900/20 dark:text-violet-300' },
])

const chartData = computed<ChartData<'line'> | null>(() => {
  if (!trend.value.length) return null
  return {
    labels: trend.value.map((point) => point.date),
    datasets: [
      {
        label: t('admin.ops.tailBurstStats.chartSessions'),
        data: trend.value.map((point) => point.sessions),
        borderColor: '#f59e0b',
        backgroundColor: 'rgba(245, 158, 11, 0.12)',
        fill: true,
        tension: 0.3,
        yAxisID: 'count',
      },
      {
        label: t('admin.ops.tailBurstStats.chartRequests'),
        data: trend.value.map((point) => point.requests),
        borderColor: '#10b981',
        backgroundColor: 'rgba(16, 185, 129, 0.06)',
        fill: false,
        tension: 0.3,
        yAxisID: 'count',
      },
      {
        label: t('admin.ops.tailBurstStats.chartDelta'),
        data: trend.value.map((point) => point.delta_percent),
        borderColor: '#8b5cf6',
        backgroundColor: 'transparent',
        borderDash: [5, 4],
        fill: false,
        tension: 0.3,
        yAxisID: 'percent',
      },
    ],
  }
})

const chartOptions = computed<ChartOptions<'line'>>(() => {
  const dark = document.documentElement.classList.contains('dark')
  const text = dark ? '#d4d4d8' : '#475569'
  const grid = dark ? '#27272a' : '#e2e8f0'
  return {
    responsive: true,
    maintainAspectRatio: false,
    interaction: { intersect: false, mode: 'index' },
    plugins: {
      legend: { position: 'top', labels: { color: text, usePointStyle: true, boxWidth: 8, padding: 16 } },
      tooltip: { callbacks: { label: (context) => context.dataset.yAxisID === 'percent' ? `${context.dataset.label}: ${Number(context.raw || 0).toFixed(2)}%` : `${context.dataset.label}: ${formatInteger(Number(context.raw || 0))}` } },
    },
    scales: {
      x: { grid: { color: grid }, ticks: { color: text, maxRotation: 0, autoSkip: true, maxTicksLimit: 10 } },
      count: { beginAtZero: true, grid: { color: grid }, ticks: { color: text, precision: 0 } },
      percent: { beginAtZero: true, position: 'right', grid: { drawOnChartArea: false }, ticks: { color: '#8b5cf6', callback: (value) => `${value}%` } },
    },
  }
})

function onDateRangeChange(range: { startDate: string; endDate: string }) {
  startDate.value = range.startDate
  endDate.value = range.endDate
  applyFilters()
}

function resetFilters() {
  startDate.value = defaultStartDate
  endDate.value = nowDate
  accountInput.value = ''
  windowFilter.value = ''
  reasonFilter.value = ''
  statusFilter.value = ''
  page.value = 1
  void loadStats()
}

function applyFilters() {
  page.value = 1
  void loadStats()
}

function buildFilters() {
  const filters: Parameters<typeof opsAPI.getCodexTailBurstStats>[0] = {
    start_time: dateInputBoundaryToISOString(startDate.value),
    // 使用次日零点作为排他上界，覆盖结束日的全部数据库精度，避免 23:59:59.999 之后的记录被漏掉。
    end_time: dateInputBoundaryToISOString(addDaysToDateInput(endDate.value, 1)),
    window: windowFilter.value || undefined,
    reason: reasonFilter.value || undefined,
    status: statusFilter.value || undefined,
    page: page.value,
    page_size: pageSize.value,
  }
  const accountID = Number.parseInt(accountInput.value.trim(), 10)
  if (Number.isInteger(accountID) && accountID > 0) filters.account_id = accountID
  return filters
}

async function loadStats() {
  abortController?.abort()
  const controller = new AbortController()
  abortController = controller
  loading.value = true
  errorMessage.value = ''
  try {
    response.value = await opsAPI.getCodexTailBurstStats(buildFilters(), { signal: controller.signal })
    if (controller.signal.aborted) return
    lastUpdated.value = new Date()
    hasLoadedOnce.value = true
  } catch (error: unknown) {
    if (controller.signal.aborted || (error as { code?: string })?.code === 'ERR_CANCELED') return
    errorMessage.value = extractApiErrorMessage(error, t('admin.ops.tailBurstStats.loadFailed'))
  } finally {
    if (abortController === controller) {
      loading.value = false
      abortController = null
    }
  }
}

function onPageChange(nextPage: number) {
  page.value = nextPage
  void loadStats()
}

function onPageSizeChange(nextPageSize: number) {
  pageSize.value = nextPageSize
  page.value = 1
  void loadStats()
}

onMounted(() => { void loadStats() })
onUnmounted(() => { abortController?.abort() })
</script>
