<template>
  <section class="overflow-hidden rounded-xl border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900">
    <div class="flex flex-col gap-3 border-b border-gray-100 px-4 py-4 dark:border-dark-800 sm:flex-row sm:items-start sm:justify-between">
      <div class="flex min-w-0 items-start gap-3">
        <span class="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-primary-50 text-primary-600 dark:bg-primary-900/20 dark:text-primary-400">
          <Icon name="bolt" size="sm" />
        </span>
        <div>
          <div class="flex flex-wrap items-center gap-2">
            <h2 class="font-semibold text-gray-900 dark:text-white">
              {{ localText('计费缓存队列档位', 'Billing cache queue profile') }}
            </h2>
            <span v-if="runtime" class="badge badge-gray">rev {{ runtime.revision }}</span>
            <span v-if="runtime" class="badge badge-success">
              {{ presetName(runtime.preset) }}
            </span>
          </div>
          <p class="mt-1 max-w-3xl text-xs leading-5 text-gray-500 dark:text-gray-400">
            {{ localText('点击选择档位即可，不需要手填线程和缓冲区。保存时先启用新工作池，再排空旧队列。', 'Choose a profile without entering worker or buffer values. Saving starts the new pool before draining the old queues.') }}
          </p>
        </div>
      </div>
      <button type="button" class="btn btn-secondary btn-sm shrink-0" :disabled="loading || saving" @click="loadRuntime">
        <Icon name="refresh" size="xs" :class="loading ? 'animate-spin' : ''" />
        {{ localText('刷新队列', 'Refresh queues') }}
      </button>
    </div>

    <div v-if="loading && !runtime" class="grid gap-3 p-4 lg:grid-cols-3">
      <div v-for="index in 3" :key="index" class="h-40 animate-pulse rounded-xl bg-gray-100 dark:bg-dark-800" />
    </div>

    <div v-else-if="!runtime" class="p-4">
      <div class="rounded-lg border border-red-200 bg-red-50 p-4 text-sm text-red-700 dark:border-red-900 dark:bg-red-950/25 dark:text-red-300">
        <strong class="block">{{ localText('计费队列读取失败', 'Failed to load billing queues') }}</strong>
        <span class="mt-1 block">{{ message || localText('请确认管理员登录状态和网关服务状态。', 'Check administrator authentication and gateway service status.') }}</span>
        <button type="button" class="btn btn-secondary btn-sm mt-3" :disabled="loading" @click="loadRuntime">
          {{ localText('重新读取', 'Try again') }}
        </button>
      </div>
    </div>

    <div v-else class="space-y-5 p-4">
      <div role="group" :aria-label="localText('选择计费队列档位', 'Select billing queue profile')" class="grid gap-3 lg:grid-cols-3">
        <button
          v-for="profile in profiles"
          :key="profile.key"
          type="button"
          :disabled="saving || !isAvailable(profile.key)"
          :aria-pressed="selectedPreset === profile.key"
          :class="[
            'min-w-0 rounded-xl border p-4 text-left transition duration-150 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 focus-visible:ring-offset-2 dark:focus-visible:ring-offset-dark-900',
            selectedPreset === profile.key
              ? 'border-primary-500 bg-primary-50/70 ring-1 ring-primary-500 dark:border-primary-500 dark:bg-primary-950/20'
              : 'border-gray-200 bg-gray-50/60 hover:border-gray-300 dark:border-dark-700 dark:bg-dark-800/40 dark:hover:border-dark-600',
            !isAvailable(profile.key) ? 'cursor-not-allowed opacity-45' : '',
          ]"
          @click="selectedPreset = profile.key"
        >
          <span class="flex items-start justify-between gap-3">
            <span>
              <strong class="block text-base text-gray-900 dark:text-white">{{ profile.name }}</strong>
              <small class="mt-0.5 block text-gray-500 dark:text-gray-400">{{ profile.description }}</small>
            </span>
            <span v-if="profile.key === 'standard'" class="badge badge-primary shrink-0">{{ localText('推荐', 'Recommended') }}</span>
          </span>
          <dl class="mt-4 grid grid-cols-2 gap-x-4 gap-y-3">
            <div>
              <dt class="text-[11px] text-gray-500 dark:text-gray-400">{{ localText('关键线程 / 缓冲', 'Critical worker / buffer') }}</dt>
              <dd class="mt-0.5 text-sm font-semibold tabular-nums text-gray-900 dark:text-white">{{ profile.criticalWorkers }} / {{ formatNumber(profile.criticalBuffer) }}</dd>
            </div>
            <div>
              <dt class="text-[11px] text-gray-500 dark:text-gray-400">{{ localText('非关键线程 / 缓冲', 'Best-effort worker / buffer') }}</dt>
              <dd class="mt-0.5 text-sm font-semibold tabular-nums text-gray-900 dark:text-white">{{ profile.bestEffortWorkers }} / {{ formatNumber(profile.bestEffortBuffer) }}</dd>
            </div>
            <div class="col-span-2">
              <dt class="text-[11px] text-gray-500 dark:text-gray-400">{{ localText('Redis 写入超时', 'Redis write timeout') }}</dt>
              <dd class="mt-0.5 text-sm font-semibold tabular-nums text-gray-900 dark:text-white">{{ formatNumber(profile.timeoutMs) }} ms</dd>
            </div>
          </dl>
        </button>
      </div>

      <div class="rounded-xl border border-gray-200 bg-gray-50/60 p-4 dark:border-dark-700 dark:bg-dark-800/40">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <div>
            <strong class="text-sm text-gray-900 dark:text-white">{{ localText('当前进程实时状态', 'Live process state') }}</strong>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">{{ localText('只读取内存，不查询 Redis 或数据库。', 'Read from memory only; no Redis or database query is issued.') }}</p>
          </div>
          <span class="badge badge-gray">{{ formatNumber(runtime.timeout_milliseconds) }} ms</span>
        </div>
        <dl class="mt-4 grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
          <div v-for="item in runtimeItems" :key="item.label" class="rounded-lg border border-gray-200 bg-white p-3 dark:border-dark-700 dark:bg-dark-900">
            <dt class="text-xs text-gray-500 dark:text-gray-400">{{ item.label }}</dt>
            <dd class="mt-1 text-base font-semibold tabular-nums text-gray-900 dark:text-white">{{ item.value }}</dd>
          </div>
        </dl>
      </div>

      <div v-if="message" :class="['rounded-lg border p-3 text-sm', messageTone === 'success' ? 'border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-900 dark:bg-emerald-950/25 dark:text-emerald-300' : 'border-red-200 bg-red-50 text-red-700 dark:border-red-900 dark:bg-red-950/25 dark:text-red-300']">
        {{ message }}
      </div>

      <div class="flex flex-col gap-2 border-t border-gray-100 pt-4 dark:border-dark-800 sm:flex-row sm:items-center sm:justify-between">
        <p class="text-xs leading-5 text-gray-500 dark:text-gray-400">
          {{ localText('高并发档会预留更多内存；2C2G 节点建议保持标准档。', 'The high profile reserves more memory. Keep the standard profile on 2-core, 2 GB nodes.') }}
        </p>
        <button type="button" class="btn btn-primary shrink-0" :disabled="saving || !dirty" @click="saveRuntime">
          <Icon v-if="saving" name="refresh" size="xs" class="animate-spin" />
          {{ saving ? localText('切换中', 'Switching') : localText('保存并热更新', 'Save and hot update') }}
        </button>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import {
  getBillingCacheRuntimeConfig,
  updateBillingCacheRuntimeConfig,
  type BillingCachePreset,
  type BillingCacheRuntimeConfig,
} from '@/api/admin/runtimeSummary'
import { extractApiErrorMessage } from '@/utils/apiError'

interface BillingCacheProfile {
  key: BillingCachePreset
  name: string
  description: string
  criticalWorkers: number
  criticalBuffer: number
  bestEffortWorkers: number
  bestEffortBuffer: number
  timeoutMs: number
}

const { locale } = useI18n()
const runtime = ref<BillingCacheRuntimeConfig | null>(null)
const selectedPreset = ref<BillingCachePreset>('standard')
const loading = ref(false)
const saving = ref(false)
const message = ref('')
const messageTone = ref<'success' | 'error'>('success')

const localText = (zh: string, en: string) => locale.value.toLowerCase().startsWith('zh') ? zh : en
const formatNumber = (value: number) => new Intl.NumberFormat(locale.value).format(value || 0)

const profiles = computed<BillingCacheProfile[]>(() => [
  { key: 'low', name: localText('低配', 'Low'), description: localText('适合测试或低流量节点', 'For test or low-traffic nodes'), criticalWorkers: 2, criticalBuffer: 512, bestEffortWorkers: 1, bestEffortBuffer: 128, timeoutMs: 2000 },
  { key: 'standard', name: localText('标准', 'Standard'), description: localText('兼顾吞吐、内存与稳定性', 'Balanced throughput, memory, and stability'), criticalWorkers: 8, criticalBuffer: 2048, bestEffortWorkers: 2, bestEffortBuffer: 512, timeoutMs: 2000 },
  { key: 'high', name: localText('高并发', 'High concurrency'), description: localText('适合资源充足的高流量节点', 'For well-provisioned high-traffic nodes'), criticalWorkers: 16, criticalBuffer: 8192, bestEffortWorkers: 4, bestEffortBuffer: 2048, timeoutMs: 1500 },
])

const dirty = computed(() => Boolean(runtime.value && selectedPreset.value !== runtime.value.preset))
const runtimeItems = computed(() => {
  if (!runtime.value) return []
  return [
    { label: localText('关键有效线程', 'Active critical workers'), value: `${formatNumber(runtime.value.critical_active_workers)} / ${formatNumber(runtime.value.critical_worker_count)}` },
    { label: localText('关键队列', 'Critical queue'), value: `${formatNumber(runtime.value.critical_queue_length)} / ${formatNumber(runtime.value.critical_queue_capacity || runtime.value.critical_buffer_size)}` },
    { label: localText('非关键有效线程', 'Active best-effort workers'), value: `${formatNumber(runtime.value.best_effort_active_workers)} / ${formatNumber(runtime.value.best_effort_worker_count)}` },
    { label: localText('非关键队列', 'Best-effort queue'), value: `${formatNumber(runtime.value.best_effort_queue_length)} / ${formatNumber(runtime.value.best_effort_queue_capacity || runtime.value.best_effort_buffer_size)}` },
    { label: localText('延后区', 'Deferred area'), value: `${formatNumber(runtime.value.best_effort_deferred_length)} / ${formatNumber(runtime.value.best_effort_deferred_capacity)}` },
  ]
})

function presetName(preset: BillingCachePreset): string {
  return profiles.value.find((profile) => profile.key === preset)?.name || preset
}

function isAvailable(preset: BillingCachePreset): boolean {
  return runtime.value?.available_presets.includes(preset) ?? false
}

function hydrate(value: BillingCacheRuntimeConfig) {
  runtime.value = value
  selectedPreset.value = value.preset
}

async function loadRuntime() {
  loading.value = true
  message.value = ''
  try {
    hydrate(await getBillingCacheRuntimeConfig())
  } catch (error) {
    messageTone.value = 'error'
    message.value = extractApiErrorMessage(error, localText('计费队列读取失败。', 'Failed to load billing queues.'))
  } finally {
    loading.value = false
  }
}

async function saveRuntime() {
  const previous = runtime.value
  if (!previous || !dirty.value) return
  saving.value = true
  message.value = ''
  try {
    hydrate(await updateBillingCacheRuntimeConfig({
      expected_revision: previous.revision,
      preset: selectedPreset.value,
    }))
    messageTone.value = 'success'
    message.value = localText('计费队列已热更新，旧队列已安全排空。', 'Billing queues were hot-updated and the old queues were drained safely.')
  } catch (error) {
    // 后端在持久化失败时会恢复旧工作池；界面同步恢复旧选中态，避免管理员误以为新档位已生效。
    selectedPreset.value = previous.preset
    messageTone.value = 'error'
    const reason = extractApiErrorMessage(error, localText('保存失败。', 'Save failed.'))
    message.value = `${reason} ${localText('已恢复原档位；若修订号冲突，请刷新后重试。', 'The original profile was restored. Refresh and retry after a revision conflict.')}`
  } finally {
    saving.value = false
  }
}

onMounted(loadRuntime)
</script>
