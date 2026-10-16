<template>
  <AppLayout>
    <div class="mx-auto max-w-[1500px] space-y-5">
      <section class="card overflow-hidden">
        <header class="flex flex-col gap-4 border-b border-gray-100 px-5 py-4 dark:border-dark-700 lg:flex-row lg:items-center lg:justify-between">
          <div>
            <div class="flex flex-wrap items-center gap-2">
              <h1 class="text-xl font-semibold text-gray-900 dark:text-white">{{ text('异步图片任务', 'Asynchronous image tasks') }}</h1>
              <span v-if="configState" :class="queueStatusTone" class="badge">
                {{ queueStatusLabel }}
              </span>
            </div>
            <p class="mt-1 max-w-3xl text-sm text-gray-500 dark:text-gray-400">
              {{ text('查看独立图片 worker（工作线程）的队列、结算和产物状态。失败重放不会重复生成已经成功的图片。', 'Inspect the isolated image worker queue, settlement, and artifacts. Replaying settlement does not regenerate an already successful image.') }}
            </p>
          </div>
          <div class="flex flex-wrap items-center gap-2">
            <button type="button" class="btn btn-secondary btn-sm" :disabled="serviceRestarting" @click="showConfigDialog = true">
              <Icon name="cog" size="sm" />
              {{ text('配置', 'Settings') }}
            </button>
            <label class="inline-flex min-h-9 items-center gap-2 rounded-lg border border-gray-200 px-3 text-xs font-medium text-gray-600 dark:border-dark-700 dark:text-gray-300" :class="!queueRunning && 'cursor-not-allowed opacity-50'">
              <input v-model="autoRefresh" type="checkbox" :disabled="!queueRunning" class="rounded border-gray-300 text-primary-600 focus:ring-primary-500" />
              {{ text('5 秒自动刷新', 'Refresh every 5s') }}
            </label>
            <button type="button" class="btn btn-secondary btn-sm" :disabled="loading || serviceRestarting" @click="refresh(false)">
              <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
              {{ loading ? text('刷新中', 'Refreshing') : text('刷新', 'Refresh') }}
            </button>
            <button type="button" class="btn btn-secondary btn-sm" :disabled="actionLoading || serviceRestarting || !runtime?.config.enabled" @click="cleanup">
              <Icon name="trash" size="sm" />
              {{ text('清理过期', 'Clean expired') }}
            </button>
          </div>
        </header>

        <div v-if="errorMessage" role="alert" class="m-5 rounded-lg border border-red-200 bg-red-50 p-4 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">
          <strong>{{ text('任务队列读取失败', 'Failed to load image tasks') }}</strong>
          <p class="mt-1 break-words">{{ errorMessage }}</p>
          <button type="button" class="btn btn-secondary btn-sm mt-3" @click="refresh(false)">{{ text('重试', 'Retry') }}</button>
        </div>

        <div v-if="successMessage" role="status" class="m-5 rounded-lg border border-emerald-200 bg-emerald-50 p-3 text-sm text-emerald-700 dark:border-emerald-800 dark:bg-emerald-900/20 dark:text-emerald-300">
          {{ successMessage }}
        </div>

        <div v-if="serviceRestarting" role="status" aria-live="polite" class="m-5 flex items-start gap-3 rounded-xl border border-blue-200 bg-blue-50 p-4 text-sm text-blue-700 dark:border-blue-900 dark:bg-blue-950/30 dark:text-blue-300">
          <span class="mt-0.5 h-4 w-4 shrink-0 animate-spin rounded-full border-2 border-blue-200 border-b-blue-600 dark:border-blue-800 dark:border-b-blue-300" />
          <div>
            <strong>{{ text('配置已保存，轻量网关正在重启', 'Settings saved; gateway-lite is restarting') }}</strong>
            <p class="mt-1">{{ text('页面会自动等待服务恢复并核对实际生效状态，不需要手动刷新。', 'The page is waiting for recovery and will verify the effective state automatically.') }}</p>
          </div>
        </div>

        <div v-if="restartErrorMessage" role="alert" class="m-5 rounded-xl border border-red-200 bg-red-50 p-4 text-sm text-red-700 dark:border-red-900 dark:bg-red-950/30 dark:text-red-300">
          <strong>{{ text('自动重启未完成', 'Automatic restart did not complete') }}</strong>
          <p class="mt-1 break-words">{{ restartErrorMessage }}</p>
          <button type="button" class="btn btn-secondary btn-sm mt-3" :disabled="serviceRestarting" @click="retryRestart">
            <Icon :name="restartDegraded ? 'cog' : 'refresh'" size="sm" />
            {{ restartDegraded ? text('返回配置', 'Back to settings') : text('重新启动并检查', 'Restart and check again') }}
          </button>
        </div>

        <div v-if="loading && !configState" class="grid gap-3 p-5 sm:grid-cols-2 xl:grid-cols-4">
          <div v-for="index in 4" :key="index" class="h-28 animate-pulse rounded-xl bg-gray-100 dark:bg-dark-800"></div>
        </div>

        <template v-else-if="runtime">
          <div class="grid gap-3 p-5 sm:grid-cols-2 xl:grid-cols-4">
            <article v-for="metric in metricCards" :key="metric.label" class="rounded-xl border border-gray-200 bg-gray-50/70 p-4 dark:border-dark-700 dark:bg-dark-800/45">
              <p class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ metric.label }}</p>
              <p class="mt-2 text-2xl font-bold tabular-nums text-gray-900 dark:text-white">{{ metric.value }}</p>
              <p class="mt-1 text-xs text-gray-400 dark:text-gray-500">{{ metric.hint }}</p>
            </article>
          </div>

        </template>

        <div v-if="configState && !queueRunning" class="m-5 rounded-xl border border-amber-200 bg-amber-50 p-5 text-sm text-amber-800 dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-300">
          <div class="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
            <div class="flex items-start gap-3">
              <Icon name="exclamationTriangle" class="mt-0.5 shrink-0" size="sm" />
              <div>
                <strong>{{ disabledStateTitle }}</strong>
                <p class="mt-1 leading-6">{{ disabledStateDescription }}</p>
              </div>
            </div>
            <button type="button" class="btn btn-primary btn-sm shrink-0" :disabled="serviceRestarting" @click="showConfigDialog = true">
              <Icon name="cog" size="sm" />
              {{ text('立即配置', 'Configure now') }}
            </button>
          </div>
        </div>
      </section>

      <section class="card overflow-hidden">
        <div class="flex flex-col gap-3 border-b border-gray-100 px-5 py-4 dark:border-dark-700 lg:flex-row lg:items-center lg:justify-between">
          <div>
            <h2 class="font-semibold text-gray-900 dark:text-white">{{ text('任务流水', 'Task activity') }}</h2>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">{{ text('状态筛选只查询当前节点，点击任务查看结算与错误详情。', 'Status filters query this node only. Select a task to inspect settlement and errors.') }}</p>
          </div>
          <div class="flex flex-wrap gap-2" role="group" :aria-label="text('任务状态筛选', 'Task state filter')">
            <button
              v-for="option in stateOptions"
              :key="option.value"
              type="button"
              :disabled="!queueRunning"
              :aria-pressed="stateFilter === option.value"
              class="min-h-9 rounded-lg border px-3 text-xs font-medium transition-colors"
              :class="[
                stateFilter === option.value ? 'border-primary-500 bg-primary-50 text-primary-700 dark:bg-primary-900/20 dark:text-primary-300' : 'border-gray-200 bg-white text-gray-600 hover:border-gray-300 dark:border-dark-700 dark:bg-dark-900 dark:text-gray-300',
                !queueRunning && 'cursor-not-allowed opacity-50',
              ]"
              @click="selectState(option.value)"
            >
              {{ option.label }}
            </button>
          </div>
        </div>

        <div v-if="configState && !queueRunning" class="px-5 py-14 text-center">
          <span class="mx-auto flex h-12 w-12 items-center justify-center rounded-xl bg-amber-50 text-amber-500 dark:bg-amber-950/30 dark:text-amber-300"><Icon name="clock" /></span>
          <h3 class="mt-3 font-medium text-gray-900 dark:text-white">{{ text('任务队列尚未运行', 'The task queue is not running') }}</h3>
          <p class="mx-auto mt-1 max-w-xl text-sm leading-6 text-gray-500 dark:text-gray-400">{{ text('关闭状态不会查询任务接口，因此也不会把正常关闭误报成 503 故障。配置并重启轻量网关后，任务流水会自动显示。', 'Task APIs are not queried while the queue is off, so a normal disabled state is never shown as a 503 failure. Configure and restart gateway-lite to view activity.') }}</p>
        </div>

        <div v-else-if="loading && tasks.length === 0" class="space-y-3 p-5">
          <div v-for="index in 5" :key="index" class="h-16 animate-pulse rounded-lg bg-gray-100 dark:bg-dark-800"></div>
        </div>

        <div v-else-if="tasks.length === 0 && !errorMessage" class="px-5 py-16 text-center">
          <span class="mx-auto flex h-12 w-12 items-center justify-center rounded-xl bg-gray-100 text-gray-400 dark:bg-dark-800"><Icon name="sparkles" /></span>
          <h3 class="mt-3 font-medium text-gray-900 dark:text-white">{{ text('暂无符合条件的任务', 'No matching tasks') }}</h3>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ text('客户端使用 Prefer: respond-async 提交生图请求后，任务会出现在这里。', 'Tasks appear here after a client submits an image request with Prefer: respond-async.') }}</p>
        </div>

        <div v-else class="grid min-w-0 lg:grid-cols-[minmax(0,1fr)_360px]">
          <div class="min-w-0 overflow-hidden border-b border-gray-100 dark:border-dark-700 lg:border-b-0 lg:border-r">
            <div class="hidden overflow-x-auto md:block">
              <table class="w-full text-left text-sm">
                <thead class="bg-gray-50 text-[11px] uppercase tracking-wider text-gray-500 dark:bg-dark-800/60 dark:text-gray-400">
                  <tr>
                    <th class="px-4 py-3 font-medium">{{ text('任务 / 模型', 'Task / model') }}</th>
                    <th class="px-4 py-3 font-medium">{{ text('状态', 'State') }}</th>
                    <th class="px-4 py-3 font-medium">{{ text('进度', 'Progress') }}</th>
                    <th class="px-4 py-3 font-medium">{{ text('金额', 'Amount') }}</th>
                    <th class="px-4 py-3 font-medium">{{ text('更新时间', 'Updated') }}</th>
                  </tr>
                </thead>
                <tbody class="divide-y divide-gray-100 dark:divide-dark-800">
                  <tr v-for="task in tasks" :key="task.id" class="cursor-pointer transition-colors hover:bg-gray-50 dark:hover:bg-dark-800/50" :class="selected?.id === task.id ? 'bg-primary-50/60 dark:bg-primary-900/10' : ''" tabindex="0" @click="selectTask(task)" @keydown.enter="selectTask(task)">
                    <td class="px-4 py-3">
                      <p class="max-w-[240px] truncate font-medium text-gray-900 dark:text-white" :title="task.id">{{ shortID(task.id) }}</p>
                      <p class="mt-0.5 max-w-[240px] truncate text-xs text-gray-500 dark:text-gray-400">{{ task.model }} · {{ task.image_count }} {{ text('张', 'image(s)') }}</p>
                    </td>
                    <td class="px-4 py-3"><span class="badge" :class="stateTone(task.state)">{{ stateLabel(task.state) }}</span></td>
                    <td class="px-4 py-3"><span class="tabular-nums text-gray-700 dark:text-gray-200">{{ task.progress }}%</span></td>
                    <td class="px-4 py-3 tabular-nums text-gray-700 dark:text-gray-200">${{ money(displayAmount(task)) }}</td>
                    <td class="whitespace-nowrap px-4 py-3 text-xs text-gray-500 dark:text-gray-400">{{ dateTime(task.updated_at) }}</td>
                  </tr>
                </tbody>
              </table>
            </div>

            <div class="divide-y divide-gray-100 dark:divide-dark-800 md:hidden">
              <button v-for="task in tasks" :key="task.id" type="button" class="block w-full p-4 text-left" :class="selected?.id === task.id ? 'bg-primary-50/60 dark:bg-primary-900/10' : ''" @click="selectTask(task)">
                <div class="flex items-start justify-between gap-3">
                  <div class="min-w-0"><p class="truncate font-medium text-gray-900 dark:text-white">{{ task.model }}</p><p class="mt-1 truncate text-xs text-gray-500 dark:text-gray-400">{{ shortID(task.id) }}</p></div>
                  <span class="badge shrink-0" :class="stateTone(task.state)">{{ stateLabel(task.state) }}</span>
                </div>
                <div class="mt-3 flex items-center justify-between text-xs text-gray-500 dark:text-gray-400"><span>{{ task.progress }}% · {{ task.image_count }} {{ text('张', 'image(s)') }}</span><span>${{ money(displayAmount(task)) }}</span></div>
              </button>
            </div>

            <div v-if="nextCursor" class="border-t border-gray-100 p-4 text-center dark:border-dark-800">
              <button type="button" class="btn btn-secondary btn-sm" :disabled="loadingMore" @click="loadMore">{{ loadingMore ? text('加载中', 'Loading') : text('加载更多', 'Load more') }}</button>
            </div>
          </div>

          <aside class="min-w-0 bg-gray-50/60 p-5 dark:bg-dark-800/20">
            <template v-if="selected">
              <div class="flex items-start justify-between gap-3">
                <div class="min-w-0"><p class="text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-gray-400">{{ text('任务详情', 'Task details') }}</p><h3 class="mt-1 truncate font-semibold text-gray-900 dark:text-white" :title="selected.id">{{ shortID(selected.id) }}</h3></div>
                <button v-if="canReplay(selected)" type="button" class="btn btn-secondary btn-sm shrink-0" :disabled="actionLoading" @click="replay(selected)"><Icon name="refresh" size="sm" />{{ text('重放', 'Replay') }}</button>
              </div>
              <dl class="mt-4 space-y-0 rounded-xl border border-gray-200 bg-white px-4 dark:border-dark-700 dark:bg-dark-900">
                <div v-for="row in detailRows" :key="row.label" class="flex min-w-0 items-start justify-between gap-4 border-b border-gray-100 py-3 last:border-b-0 dark:border-dark-800"><dt class="shrink-0 text-xs text-gray-500 dark:text-gray-400">{{ row.label }}</dt><dd class="min-w-0 break-all text-right text-xs font-medium text-gray-900 dark:text-white">{{ row.value }}</dd></div>
              </dl>
              <div v-if="selected.error_message || selected.error_code" class="mt-4 rounded-lg border border-red-200 bg-red-50 p-3 text-xs text-red-700 dark:border-red-900 dark:bg-red-950/30 dark:text-red-300"><strong>{{ selected.error_code || text('执行失败', 'Execution failed') }}</strong><p class="mt-1">{{ selected.error_message || text('请检查上游账号和任务日志。', 'Check the upstream account and task logs.') }}</p></div>
              <div v-if="selected.artifacts.length" class="mt-4"><p class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ text('产物', 'Artifacts') }}</p><div class="mt-2 space-y-2"><div v-for="artifact in selected.artifacts" :key="artifact.sha256" class="rounded-lg border border-gray-200 bg-white p-3 dark:border-dark-700 dark:bg-dark-900"><p class="truncate text-xs font-medium text-gray-900 dark:text-white" :title="artifact.file_name">{{ artifact.file_name }}</p><p class="mt-1 text-[11px] text-gray-500 dark:text-gray-400">{{ bytes(artifact.size_bytes) }} · {{ artifact.content_type }}</p></div></div></div>
            </template>
            <div v-else class="py-14 text-center text-sm text-gray-500 dark:text-gray-400"><Icon name="eye" class="mx-auto mb-3 text-gray-400" /><p>{{ text('选择一条任务查看详情', 'Select a task to inspect details') }}</p></div>
          </aside>
        </div>
      </section>
    </div>

    <ImageTaskConfigDialog
      :show="showConfigDialog"
      :config="configState"
      :loading="configLoading"
      :saving="configSaving"
      :error="configError"
      @close="closeConfigDialog"
      @retry="loadConfig"
      @save="saveConfig"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/GatewayLiteLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import ImageTaskConfigDialog from '@/views/admin/image-tasks/ImageTaskConfigDialog.vue'
import { restartService } from '@/api/admin/system'
import {
  cleanupImageTasks,
  getImageTask,
  getImageTaskConfig,
  getImageTaskMetrics,
  listImageTasks,
  replayImageTask,
  updateImageTaskConfig,
  type ImageTaskAdminConfig,
  type ImageTask,
  type ImageTaskMetricsResponse,
  type ImageTaskState,
  type UpdateImageTaskConfigRequest,
} from '@/api/admin/imageTasks'
import { extractApiErrorMessage } from '@/utils/apiError'

const { locale } = useI18n()
const runtime = ref<ImageTaskMetricsResponse | null>(null)
const tasks = ref<ImageTask[]>([])
const selected = ref<ImageTask | null>(null)
const stateFilter = ref('')
const nextCursor = ref('')
const loading = ref(false)
const loadingMore = ref(false)
const actionLoading = ref(false)
const autoRefresh = ref(true)
const errorMessage = ref('')
const successMessage = ref('')
const configState = ref<ImageTaskAdminConfig | null>(null)
const configLoading = ref(false)
const configSaving = ref(false)
const configError = ref('')
const showConfigDialog = ref(false)
const serviceRestarting = ref(false)
const restartErrorMessage = ref('')
const restartDegraded = ref(false)
const pendingDesiredEnabled = ref(false)
let refreshTimer: ReturnType<typeof setInterval> | undefined

const text = (zh: string, en: string) => locale.value.toLowerCase().startsWith('zh') ? zh : en
const stateOptions = computed(() => [
  { value: '', label: text('全部', 'All') },
  { value: 'queued', label: text('排队', 'Queued') },
  { value: 'running', label: text('执行中', 'Running') },
  { value: 'succeeded', label: text('成功', 'Succeeded') },
  { value: 'failed', label: text('失败', 'Failed') },
  { value: 'cancelled', label: text('已取消', 'Cancelled') },
  { value: 'expired', label: text('已过期', 'Expired') },
])
const queueRunning = computed(() => Boolean(configState.value?.effective_enabled))
const queueStatusLabel = computed(() => {
  if (serviceRestarting.value) return text('正在重启', 'Restarting')
  const state = configState.value
  if (!state) return text('状态未知', 'Status unknown')
  if (state.restart_required && state.desired.enabled !== state.effective_enabled) {
    return state.desired.enabled ? text('待重启开启', 'Restart to enable') : text('待重启关闭', 'Restart to disable')
  }
  return state.effective_enabled ? text('队列运行中', 'Queue running') : text('队列已关闭', 'Queue disabled')
})
const queueStatusTone = computed(() => {
  if (serviceRestarting.value) return 'badge-warning'
  const state = configState.value
  if (!state) return 'badge-gray'
  if (state.restart_required && state.desired.enabled !== state.effective_enabled) return 'badge-warning'
  return state.effective_enabled ? 'badge-success' : 'badge-gray'
})
const disabledStateTitle = computed(() => {
  if (configState.value?.desired.enabled && configState.value.restart_required) {
    return text('配置已开启，正在等待重启', 'Enabled in config; restart required')
  }
  return text('异步图片任务当前未启用', 'Asynchronous image tasks are disabled')
})
const disabledStateDescription = computed(() => {
  if (configState.value?.desired.enabled && configState.value.restart_required) {
    return text('配置已经写入文件；重启轻量网关后才会创建 worker（工作线程）并开始消费队列。普通同步生图仍可照常使用。', 'The config is saved. Restart gateway-lite to create workers and consume the queue. Synchronous image generation remains available.')
  }
  return text('普通同步生图仍按原路径工作。点击“立即配置”即可选择本机或 S3 / MinIO 存储、运行规模和保留时间。', 'Synchronous image generation still works. Configure storage, runtime size, and retention when you are ready.')
})
const metricCards = computed(() => runtime.value ? [
  { label: text('排队任务', 'Queue depth'), value: String(runtime.value.metrics.queue_depth), hint: `${text('容量', 'Capacity')} ${runtime.value.metrics.queue_capacity}` },
  { label: text('正在执行', 'Running workers'), value: String(runtime.value.metrics.running_workers), hint: `${text('工作线程', 'Workers')} ${runtime.value.metrics.workers}` },
  { label: text('单用户并发', 'Per-user concurrency'), value: String(runtime.value.config.per_user_concurrency), hint: `${text('单任务最多', 'Max images')} ${runtime.value.config.max_images_per_task} ${text('张', 'images')}` },
  { label: text('结果保留', 'Result retention'), value: duration(runtime.value.config.result_retention), hint: `${text('单图上限', 'Per artifact')} ${bytes(runtime.value.config.max_artifact_bytes)}` },
] : [])
const detailRows = computed(() => selected.value ? [
  { label: text('模型', 'Model'), value: selected.value.model },
  { label: text('任务状态', 'Task state'), value: stateLabel(selected.value.state) },
  { label: text('结算状态', 'Settlement'), value: settlementLabel(selected.value.settlement_state) },
  { label: text('执行次数', 'Attempts'), value: `${selected.value.attempt} / ${selected.value.max_attempts}` },
  { label: text('预留 / 实扣', 'Reserved / actual'), value: `$${money(selected.value.reserved_amount)} / $${money(selected.value.actual_amount)}` },
  { label: text('创建时间', 'Created'), value: dateTime(selected.value.created_at) },
  { label: text('完成时间', 'Completed'), value: selected.value.completed_at ? dateTime(selected.value.completed_at) : '—' },
] : [])

function shortID(id: string) { return id.length > 18 ? `${id.slice(0, 8)}…${id.slice(-6)}` : id }
function money(value: number) { return Number(value || 0).toFixed(4).replace(/0+$/, '').replace(/\.$/, '') || '0' }
function displayAmount(task: ImageTask) {
  // pending 表示额度仍在预留；settled/released 都必须展示真实实扣。
  // released 的真实金额可以是 0，不能用 truthy 回退把它误显示成预留额。
  return task.settlement_state === 'pending' ? task.reserved_amount : task.actual_amount
}
function dateTime(value: string) { return value ? new Intl.DateTimeFormat(locale.value, { dateStyle: 'short', timeStyle: 'medium' }).format(new Date(value)) : '—' }
function bytes(value: number) { const size = Number(value || 0); if (size < 1024) return `${size} B`; if (size < 1048576) return `${(size / 1024).toFixed(1)} KiB`; return `${(size / 1048576).toFixed(1)} MiB` }
function duration(nanoseconds: number) { const hours = Number(nanoseconds || 0) / 3_600_000_000_000; if (hours >= 24) return `${Math.round(hours / 24)} ${text('天', 'days')}`; if (hours >= 1) return `${Math.round(hours)} ${text('小时', 'hours')}`; return `${Math.max(1, Math.round(hours * 60))} ${text('分钟', 'minutes')}` }
function stateLabel(state: string) { return stateOptions.value.find(item => item.value === state)?.label || state }
function stateTone(state: ImageTaskState) { return ({ queued: 'badge-gray', running: 'badge-primary', succeeded: 'badge-success', failed: 'badge-danger', cancelled: 'badge-warning', expired: 'badge-gray' } as const)[state] }
function settlementLabel(state: string) { if (state === 'settled') return text('已结算', 'Settled'); if (state === 'released') return text('已释放', 'Released'); return text('待结算', 'Pending') }
function canReplay(task: ImageTask) { return task.state === 'failed' || task.state === 'expired' }

async function refresh(silent = false) {
  if (!silent) loading.value = true
  errorMessage.value = ''
  try {
    const config = await getImageTaskConfig()
    configState.value = config
    configError.value = ''
    if (!config.effective_enabled) {
      runtime.value = null
      tasks.value = []
      selected.value = null
      nextCursor.value = ''
      return
    }
    const [metrics, list] = await Promise.all([getImageTaskMetrics(), listImageTasks({ state: stateFilter.value || undefined, limit: 50 })])
    runtime.value = metrics
    tasks.value = list.items || []
    nextCursor.value = list.next_cursor || ''
    if (selected.value) selected.value = tasks.value.find(item => item.id === selected.value?.id) || selected.value
  } catch (error) {
    errorMessage.value = extractApiErrorMessage(error, text('请确认管理员登录状态和轻量网关服务状态。', 'Check administrator authentication and gateway status.'))
  } finally {
    loading.value = false
  }
}

async function loadConfig() {
  configLoading.value = true
  configError.value = ''
  try {
    configState.value = await getImageTaskConfig()
  } catch (error) {
    configError.value = extractApiErrorMessage(error, text('读取异步图片任务配置失败。', 'Failed to load asynchronous image task settings.'))
  } finally {
    configLoading.value = false
  }
}

async function saveConfig(payload: UpdateImageTaskConfigRequest) {
  configSaving.value = true
  configError.value = ''
  restartErrorMessage.value = ''
  restartDegraded.value = false
  successMessage.value = ''
  try {
    configState.value = await updateImageTaskConfig(payload)
    pendingDesiredEnabled.value = configState.value.desired.enabled
    showConfigDialog.value = false
    if (configState.value.restart_required) {
      await restartAndWaitForRecovery(pendingDesiredEnabled.value)
    } else {
      successMessage.value = text('异步图片任务配置已保存并已生效。', 'Asynchronous image task settings saved and applied.')
      await refresh(true)
    }
  } catch (error) {
    configError.value = extractApiErrorMessage(error, text('保存异步图片任务配置失败。', 'Failed to save asynchronous image task settings.'))
  } finally {
    configSaving.value = false
  }
}

async function restartAndWaitForRecovery(expectedEnabled: boolean) {
  if (serviceRestarting.value) return
  serviceRestarting.value = true
  restartErrorMessage.value = ''
  resetTimer()

  try {
    try {
      await restartService()
    } catch (error) {
      // 连接在服务开始退出后中断属于正常现象；明确的 HTTP 错误则说明重启根本没有被接受。
      const failure = error as { status?: number; response?: { status?: number } } | null
      const status = Number(failure?.response?.status || failure?.status || 0)
      if (status > 0) throw error
    }

    const recovered = await waitForServiceRecovery(expectedEnabled)
    configState.value = recovered
    successMessage.value = text('轻量网关已重启，异步图片任务配置已生效。', 'Gateway-lite restarted and the asynchronous image task settings are active.')
    await refresh(true)
  } catch (error) {
    restartErrorMessage.value = extractApiErrorMessage(
      error,
      text('配置已经保存，但服务未在 60 秒内恢复。请检查轻量网关服务状态后重试。', 'The config was saved, but the service did not recover within 60 seconds. Check gateway-lite and retry.'),
    )
  } finally {
    serviceRestarting.value = false
    resetTimer()
  }
}

async function waitForServiceRecovery(expectedEnabled: boolean): Promise<ImageTaskAdminConfig> {
  await delay(1500)
  const deadline = Date.now() + 60_000
  while (Date.now() < deadline) {
    try {
      const health = await fetch('/health', {
        method: 'GET',
        cache: 'no-store',
        credentials: 'same-origin',
        signal: AbortSignal.timeout(Math.min(2_500, Math.max(1, deadline - Date.now()))),
      })
      if (health.ok) {
        const current = await getImageTaskConfig(
          AbortSignal.timeout(Math.min(2_500, Math.max(1, deadline - Date.now()))),
        )
        if (current.degraded_reason) {
          configState.value = current
          restartDegraded.value = true
          throw new Error(text(
            `图片任务启动失败，但轻量网关其余功能正常，系统已安全保持关闭：${current.degraded_reason}`,
            `Image tasks failed to start, while the rest of gateway-lite remains healthy. The queue was safely kept disabled: ${current.degraded_reason}`,
          ))
        }
        // 仅健康检查恢复还不够；必须确认新进程已经加载目标配置，避免旧进程尚未退出时误报成功。
        if (!current.restart_required && current.effective_enabled === expectedEnabled) return current
      }
    } catch (error) {
      if (restartDegraded.value) throw error
      // 重启窗口内连接失败是预期状态，继续轮询。
    }
    const remaining = deadline - Date.now()
    if (remaining > 0) await delay(Math.min(1000, remaining))
  }
  throw new Error(text('服务恢复超时，配置仍保留在配置文件中。', 'Service recovery timed out; the saved config remains on disk.'))
}

function delay(milliseconds: number) {
  return new Promise<void>((resolve) => window.setTimeout(resolve, milliseconds))
}

async function retryRestart() {
  restartErrorMessage.value = ''
  if (restartDegraded.value) {
    restartDegraded.value = false
    showConfigDialog.value = true
    await loadConfig()
    return
  }
  await restartAndWaitForRecovery(pendingDesiredEnabled.value)
}

function closeConfigDialog() {
  if (configSaving.value) return
  showConfigDialog.value = false
  configError.value = ''
}
async function loadMore() {
  if (!nextCursor.value || loadingMore.value) return
  loadingMore.value = true
  try {
    const result = await listImageTasks({ state: stateFilter.value || undefined, limit: 50, cursor: nextCursor.value })
    const known = new Set(tasks.value.map(item => item.id))
    tasks.value.push(...(result.items || []).filter(item => !known.has(item.id)))
    nextCursor.value = result.next_cursor || ''
  } catch (error) { errorMessage.value = extractApiErrorMessage(error, text('加载更多任务失败。', 'Failed to load more tasks.')) } finally { loadingMore.value = false }
}
async function selectTask(task: ImageTask) {
  selected.value = task
  try { selected.value = await getImageTask(task.id) } catch (error) { errorMessage.value = extractApiErrorMessage(error, text('任务详情读取失败。', 'Failed to load task details.')) }
}
async function selectState(state: string) { if (stateFilter.value === state) return; stateFilter.value = state; selected.value = null; await refresh() }
async function replay(task: ImageTask) {
  if (!window.confirm(text('确认重放这个失败任务？已成功的图片只会重试结算，不会再次生成。', 'Replay this failed task? A generated image will only retry settlement and will not be generated again.'))) return
  actionLoading.value = true
  try { selected.value = await replayImageTask(task.id); successMessage.value = text('任务已重新进入队列。', 'The task has returned to the queue.'); await refresh(true) } catch (error) { errorMessage.value = extractApiErrorMessage(error, text('任务重放失败。', 'Failed to replay the task.')) } finally { actionLoading.value = false }
}
async function cleanup() {
  if (!window.confirm(text('确认清理已经过期且完成结算的任务与产物？', 'Clean expired, financially finalized tasks and artifacts?'))) return
  actionLoading.value = true
  try { const removed = await cleanupImageTasks(100); successMessage.value = text(`已清理 ${removed} 条过期任务。`, `Cleaned ${removed} expired task(s).`); await refresh(true) } catch (error) { errorMessage.value = extractApiErrorMessage(error, text('过期任务清理失败。', 'Failed to clean expired tasks.')) } finally { actionLoading.value = false }
}
function resetTimer() { if (refreshTimer) clearInterval(refreshTimer); refreshTimer = undefined; if (autoRefresh.value && queueRunning.value && !serviceRestarting.value) refreshTimer = setInterval(() => { if (!document.hidden && !loading.value && !actionLoading.value && !serviceRestarting.value) void refresh(true) }, 5000) }

watch([autoRefresh, queueRunning, serviceRestarting], resetTimer)
onMounted(() => { void refresh(); resetTimer() })
onBeforeUnmount(() => { if (refreshTimer) clearInterval(refreshTimer) })
</script>
