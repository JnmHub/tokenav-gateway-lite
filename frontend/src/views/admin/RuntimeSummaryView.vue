<template>
  <AppLayout>
    <div class="mx-auto max-w-[1500px] space-y-5">
      <section class="card overflow-hidden">
        <div class="flex flex-col gap-4 border-b border-gray-100 px-5 py-4 dark:border-dark-700 sm:flex-row sm:items-center sm:justify-between">
          <div>
            <div class="flex flex-wrap items-center gap-2">
              <h1 class="text-xl font-semibold text-gray-900 dark:text-white">
                {{ localText('运行配置', 'Runtime configuration') }}
              </h1>
              <span class="badge badge-success">{{ localText('指标只读', 'Read-only metrics') }}</span>
              <span class="badge badge-primary">{{ localText('调度与计费队列可热更新', 'Scheduler and billing queues hot update') }}</span>
            </div>
            <p class="mt-1 max-w-3xl text-sm text-gray-500 dark:text-gray-400">
              {{ localText('查看当前进程真正生效的连接池、队列、超时与调度参数。只有下方调度和计费队列白名单可以热更新；页面不会显示任何密码、密钥与连接地址。', 'Inspect effective pools, queues, timeouts, and scheduling values. Only the scheduling and billing queue allowlists below can be hot-updated; no passwords, secrets, or connection addresses are displayed.') }}
            </p>
          </div>
          <button type="button" class="btn btn-secondary btn-sm shrink-0" :disabled="loading" @click="loadSummary">
            <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
            {{ loading ? localText('刷新中', 'Refreshing') : localText('刷新', 'Refresh') }}
          </button>
        </div>

        <div v-if="errorMessage" class="m-5 rounded-lg border border-red-200 bg-red-50 p-4 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">
          <div class="font-medium">{{ localText('运行配置读取失败', 'Failed to load runtime configuration') }}</div>
          <div class="mt-1">{{ errorMessage }}</div>
        </div>

        <div v-if="loading && !summary" class="grid gap-4 p-5 sm:grid-cols-2 xl:grid-cols-3">
          <div v-for="index in 6" :key="index" class="h-44 animate-pulse rounded-xl bg-gray-100 dark:bg-dark-800"></div>
        </div>

        <div v-else-if="!summary && !errorMessage" class="px-5 py-16 text-center text-sm text-gray-500 dark:text-gray-400">
          {{ localText('暂无运行配置', 'No runtime configuration is available') }}
        </div>

        <div v-else-if="summary" class="space-y-5 p-5">
          <div class="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
            <div v-for="item in overviewItems" :key="item.label" class="rounded-xl border border-gray-200 bg-gray-50/70 p-4 dark:border-dark-700 dark:bg-dark-800/45">
              <div class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ item.label }}</div>
              <div class="mt-2 break-words text-lg font-semibold text-gray-900 dark:text-white">{{ item.value }}</div>
            </div>
          </div>

          <div class="grid items-start gap-4 lg:grid-cols-2">
            <section v-for="section in sections" :key="section.key" class="rounded-xl border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900">
              <div class="flex items-start justify-between gap-3 border-b border-gray-100 px-4 py-3 dark:border-dark-800">
                <div class="flex min-w-0 items-center gap-3">
                  <span class="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-primary-50 text-primary-600 dark:bg-primary-900/20 dark:text-primary-400">
                    <Icon :name="section.icon" size="sm" />
                  </span>
                  <div class="min-w-0">
                    <h2 class="font-semibold text-gray-900 dark:text-white">{{ section.title }}</h2>
                    <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">{{ section.description }}</p>
                  </div>
                </div>
                <span class="badge badge-gray shrink-0" :title="sourceDescription(section.source)">
                  {{ sourceLabel(section.source) }}
                </span>
              </div>
              <dl class="grid gap-x-5 px-4 py-2 sm:grid-cols-2">
                <div v-for="item in section.items" :key="item.label" class="flex min-w-0 items-center justify-between gap-3 border-b border-gray-100 py-3 last:border-b-0 dark:border-dark-800 sm:[&:nth-last-child(-n+2)]:border-b-0">
                  <dt class="text-sm text-gray-500 dark:text-gray-400">{{ item.label }}</dt>
                  <dd class="break-all text-right text-sm font-semibold text-gray-900 dark:text-white" :class="item.tone === 'warning' ? 'text-amber-600 dark:text-amber-400' : ''">
                    {{ item.value }}
                  </dd>
                </div>
              </dl>
            </section>
          </div>

          <BillingCacheRuntimeCard />

          <FakeFirstTokenCard />

          <OpenAISchedulerRuntimePolicyCard />

          <div class="flex items-start gap-3 rounded-lg border border-blue-200 bg-blue-50 p-4 text-sm text-blue-800 dark:border-blue-900 dark:bg-blue-950/30 dark:text-blue-300">
            <Icon name="infoCircle" size="sm" class="mt-0.5 shrink-0" />
            <p>{{ localText('连接池和超时保持只读；计费队列只能点击三档预设，高级调度只能修改经过范围校验的白名单字段，两者都使用修订号避免并发覆盖。', 'Pools and timeouts remain read-only. Billing queues only allow three presets, while scheduling only changes range-validated allowlisted fields; both use revisions to prevent concurrent overwrites.') }}</p>
          </div>
        </div>
      </section>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/GatewayLiteLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import BillingCacheRuntimeCard from '@/views/admin/settings/BillingCacheRuntimeCard.vue'
import FakeFirstTokenCard from '@/views/admin/settings/FakeFirstTokenCard.vue'
import OpenAISchedulerRuntimePolicyCard from '@/views/admin/settings/OpenAISchedulerRuntimePolicyCard.vue'
import { getGatewayRuntimeSummary, type GatewayRuntimeSummary } from '@/api/admin/runtimeSummary'
import { extractApiErrorMessage } from '@/utils/apiError'

type RuntimeIcon = InstanceType<typeof Icon>['$props']['name']
interface RuntimeDisplayItem {
  label: string
  value: string
  tone?: 'warning'
}
interface RuntimeDisplaySection {
  key: string
  title: string
  description: string
  icon: RuntimeIcon
  source: string
  items: RuntimeDisplayItem[]
}

const { locale } = useI18n()
const summary = ref<GatewayRuntimeSummary | null>(null)
const loading = ref(false)
const errorMessage = ref('')

const localText = (zh: string, en: string) => locale.value.toLowerCase().startsWith('zh') ? zh : en
const yesNo = (value: boolean) => value ? localText('开启', 'Enabled') : localText('关闭', 'Disabled')
const numberText = (value: number) => new Intl.NumberFormat(locale.value).format(value || 0)
const milliseconds = (value: number) => value > 0 ? `${numberText(value)} ms` : localText('关闭 / 不限制', 'Disabled / unlimited')
const seconds = (value: number) => value > 0 ? `${numberText(value)} s` : localText('关闭 / 不限制', 'Disabled / unlimited')
const minutes = (value: number) => value > 0 ? `${numberText(value)} min` : localText('关闭 / 不限制', 'Disabled / unlimited')

function formatUptime(totalSeconds: number): string {
  const secondsValue = Math.max(0, Math.floor(totalSeconds || 0))
  const days = Math.floor(secondsValue / 86400)
  const hours = Math.floor((secondsValue % 86400) / 3600)
  const minutesValue = Math.floor((secondsValue % 3600) / 60)
  if (days > 0) return localText(`${days} 天 ${hours} 小时`, `${days}d ${hours}h`)
  if (hours > 0) return localText(`${hours} 小时 ${minutesValue} 分钟`, `${hours}h ${minutesValue}m`)
  return localText(`${minutesValue} 分钟`, `${minutesValue}m`)
}

function sourceLabel(source: string): string {
  if (source === 'runtime_memory') return localText('运行内存', 'Runtime')
  if (source === 'startup_config') return localText('启动配置', 'Startup')
  if (source === 'database_setting_and_startup_config') return localText('数据库 + 启动', 'Database + startup')
  if (source === 'runtime_memory_and_startup_config') return localText('内存 + 启动', 'Runtime + startup')
  return localText('系统内置', 'Built-in')
}

function sourceDescription(source: string): string {
  if (source === 'runtime_memory') return localText('当前进程内的实时值', 'Live process value')
  if (source === 'startup_config') return localText('环境变量、配置文件与默认值合并后的启动值', 'Effective value merged from environment, config file, and defaults at startup')
  if (source === 'database_setting_and_startup_config') return localText('动态数据库设置与启动配置共同决定', 'Combined database setting and startup configuration')
  if (source === 'runtime_memory_and_startup_config') return localText('实时队列状态与启动容量共同展示', 'Live queue state with startup capacity')
  return localText('编译进程序的固定行为', 'Behavior compiled into the service')
}

const overviewItems = computed<RuntimeDisplayItem[]>(() => summary.value ? [
  { label: localText('服务', 'Service'), value: summary.value.service.name },
  { label: localText('版本', 'Version'), value: summary.value.service.version },
  { label: localText('运行环境', 'Runtime'), value: summary.value.service.runtime },
  { label: localText('连续运行', 'Uptime'), value: formatUptime(summary.value.service.uptime_seconds) },
] : [])

const sections = computed<RuntimeDisplaySection[]>(() => {
  const value = summary.value
  if (!value) return []
  return [
    {
      key: 'database', title: localText('PostgreSQL 连接池', 'PostgreSQL pool'), icon: 'database', source: value.database.source,
      description: localText('连接上限与配额批量落库', 'Pool limits and quota batch writes'),
      items: [
        { label: localText('最大连接', 'Max open'), value: numberText(value.database.max_open) },
        { label: localText('最大空闲', 'Max idle'), value: numberText(value.database.max_idle) },
        { label: localText('连接寿命', 'Max lifetime'), value: minutes(value.database.max_lifetime_minutes) },
        { label: localText('空闲寿命', 'Idle lifetime'), value: minutes(value.database.max_idle_time_minutes) },
        { label: localText('配额批量写', 'Quota flusher'), value: yesNo(value.database.quota_flusher_enabled) },
        { label: localText('单批上限', 'Flush batch'), value: numberText(value.database.quota_flush_batch_size) },
      ],
    },
    {
      key: 'cache', title: localText('Redis 连接池', 'Redis pool'), icon: 'server', source: value.cache.source,
      description: localText('鉴权、并发、额度与调度缓存', 'Authentication, concurrency, quota, and scheduling cache'),
      items: [
        { label: localText('连接池大小', 'Pool size'), value: numberText(value.cache.pool_size) },
        { label: localText('最小空闲', 'Minimum idle'), value: numberText(value.cache.min_idle) },
        { label: localText('连接超时', 'Dial timeout'), value: seconds(value.cache.dial_timeout_seconds) },
        { label: localText('读取超时', 'Read timeout'), value: seconds(value.cache.read_timeout_seconds) },
        { label: localText('写入超时', 'Write timeout'), value: seconds(value.cache.write_timeout_seconds) },
        { label: 'TLS', value: yesNo(value.cache.tls_enabled) },
      ],
    },
    {
      key: 'billing', title: localText('计费缓存队列', 'Billing cache queues'), icon: 'bolt', source: value.billing.source,
      description: localText('关键扣费与非关键缓存分队列运行', 'Separate authoritative billing and best-effort cache queues'),
      items: [
        { label: localText('当前档位 / 修订', 'Profile / revision'), value: `${value.billing.preset || '-'} / rev ${numberText(value.billing.revision)}` },
        { label: localText('关键线程', 'Critical workers'), value: numberText(value.billing.critical_workers) },
        { label: localText('关键有效线程', 'Active critical workers'), value: numberText(value.billing.critical_active_workers) },
        { label: localText('关键排队', 'Critical pending'), value: `${numberText(value.billing.critical_pending)} / ${numberText(value.billing.critical_capacity)}`, tone: value.billing.critical_pending > 0 ? 'warning' : undefined },
        { label: localText('同步兜底', 'Sync fallback'), value: numberText(value.billing.critical_fallback_total) },
        { label: localText('兜底失败', 'Fallback failures'), value: numberText(value.billing.critical_fallback_fail_total), tone: value.billing.critical_fallback_fail_total > 0 ? 'warning' : undefined },
        { label: localText('非关键线程', 'Best-effort workers'), value: numberText(value.billing.best_effort_workers) },
        { label: localText('非关键有效线程', 'Active best-effort workers'), value: numberText(value.billing.best_effort_active_workers) },
        { label: localText('非关键排队', 'Best-effort pending'), value: `${numberText(value.billing.best_effort_pending)} / ${numberText(value.billing.best_effort_capacity)}` },
        { label: localText('延后区', 'Deferred area'), value: numberText(value.billing.best_effort_deferred), tone: value.billing.best_effort_deferred > 0 ? 'warning' : undefined },
        { label: localText('非关键丢弃', 'Best-effort dropped'), value: numberText(value.billing.best_effort_dropped_total), tone: value.billing.best_effort_dropped_total > 0 ? 'warning' : undefined },
        { label: localText('同键保序等待', 'Ordering waits'), value: numberText(value.billing.ordering_wait_total) },
      ],
    },
    {
      key: 'usage', title: localText('用量队列与上报', 'Usage queue and reporting'), icon: 'chartBar', source: value.usage.source,
      description: localText('本地日志异步处理与主站批量上报', 'Local asynchronous logging and batched control-plane reporting'),
      items: [
        { label: localText('本地线程', 'Local workers'), value: numberText(value.usage.local_workers) },
        { label: localText('正在运行', 'Running'), value: numberText(value.usage.local_running) },
        { label: localText('本地排队', 'Pending'), value: `${numberText(value.usage.local_pending)} / ${numberText(value.usage.local_capacity)}`, tone: value.usage.local_pending > 0 ? 'warning' : undefined },
        { label: localText('失败 / 丢弃', 'Failed / dropped'), value: `${numberText(value.usage.local_failed)} / ${numberText(value.usage.local_dropped)}`, tone: value.usage.local_failed + value.usage.local_dropped > 0 ? 'warning' : undefined },
        { label: localText('上报线程', 'Report workers'), value: numberText(value.usage.report_workers) },
        { label: localText('上报单批', 'Report batch'), value: numberText(value.usage.report_batch_size) },
      ],
    },
    {
      key: 'timeouts', title: localText('请求与流式超时', 'Request and stream timeouts'), icon: 'clock', source: value.timeouts.source,
      description: localText('零值表示对应保护关闭或不限制', 'Zero means the related protection is disabled or unlimited'),
      items: [
        { label: localText('主站请求', 'Control plane'), value: milliseconds(value.timeouts.control_plane_ms) },
        { label: localText('通用响应头', 'Response header'), value: seconds(value.timeouts.response_header_seconds) },
        { label: localText('OpenAI 响应头', 'OpenAI header'), value: seconds(value.timeouts.openai_response_header_seconds) },
        { label: localText('OpenAI 首字', 'OpenAI first output'), value: seconds(value.timeouts.openai_first_output_seconds) },
        { label: localText('高推理首字', 'High reasoning output'), value: seconds(value.timeouts.openai_high_reasoning_output_seconds) },
        { label: localText('流间隔', 'Stream interval'), value: seconds(value.timeouts.stream_data_interval_seconds) },
      ],
    },
    {
      key: 'websocket', title: 'WebSocket', icon: 'globe', source: value.websocket.source,
      description: localText('OpenAI Responses 长连接最终生效状态', 'Effective OpenAI Responses persistent connection state'),
      items: [
        { label: localText('最终启用', 'Effective'), value: yesNo(value.websocket.effective_enabled) },
        { label: localText('强制 HTTP', 'Force HTTP'), value: yesNo(value.websocket.force_http) },
        { label: localText('新版路由', 'Mode router v2'), value: yesNo(value.websocket.mode_router_v2) },
        { label: localText('入口模式', 'Ingress mode'), value: value.websocket.ingress_mode || '-' },
        { label: localText('首包超时', 'First message'), value: seconds(value.websocket.client_first_message_seconds) },
        { label: localText('单账号连接', 'Per-account connections'), value: numberText(value.websocket.max_connections_per_account) },
      ],
    },
    {
      key: 'scheduling', title: localText('高级调度', 'Advanced scheduling'), icon: 'cpu', source: value.scheduling.source,
      description: localText('数据库热开关与启动调度参数的最终组合', 'Effective database switch combined with startup scheduling settings'),
      items: [
        { label: localText('高级调度开关', 'Advanced scheduler'), value: value.scheduling.advanced_setting_available ? yesNo(value.scheduling.advanced_enabled) : localText('读取失败', 'Unavailable'), tone: value.scheduling.advanced_setting_available ? undefined : 'warning' },
        { label: localText('粘性逃逸', 'Sticky escape'), value: yesNo(value.scheduling.sticky_escape_enabled) },
        { label: localText('首字逃逸阈值', 'TTFT escape'), value: milliseconds(value.scheduling.sticky_escape_ttft_ms) },
        { label: localText('错误率阈值', 'Error-rate threshold'), value: `${(value.scheduling.sticky_escape_error_rate * 100).toFixed(1)}%` },
        { label: localText('兜底队列', 'Fallback queue'), value: yesNo(value.scheduling.fallback_queue_enabled) },
        { label: localText('兜底等待', 'Fallback wait'), value: milliseconds(value.scheduling.fallback_wait_ms) },
        { label: localText('兜底容量', 'Fallback capacity'), value: numberText(value.scheduling.fallback_max_waiting) },
        { label: localText('批量负载读取', 'Batched load reads'), value: yesNo(value.scheduling.load_batch_enabled) },
      ],
    },
  ]
})

async function loadSummary() {
  loading.value = true
  errorMessage.value = ''
  try {
    summary.value = await getGatewayRuntimeSummary()
  } catch (error) {
    errorMessage.value = extractApiErrorMessage(error, localText('请确认管理员登录状态和网关服务状态。', 'Check administrator authentication and gateway service status.'))
  } finally {
    loading.value = false
  }
}

onMounted(loadSummary)
</script>
