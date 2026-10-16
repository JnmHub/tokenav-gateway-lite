<template>
  <BaseDialog
    :show="show"
    :title="text('异步图片任务配置', 'Asynchronous image task settings')"
    width="wide"
    @close="emit('close')"
  >
    <div v-if="loading" class="flex min-h-48 items-center justify-center gap-2 text-sm text-gray-500 dark:text-gray-400">
      <span class="h-5 w-5 animate-spin rounded-full border-2 border-gray-200 border-b-primary-600 dark:border-dark-600 dark:border-b-primary-400" />
      {{ text('正在读取配置', 'Loading settings') }}
    </div>

    <div v-else-if="!draft || !config" class="rounded-xl border border-red-200 bg-red-50 p-4 text-sm text-red-700 dark:border-red-900 dark:bg-red-950/30 dark:text-red-300">
      <strong>{{ text('配置读取失败', 'Failed to load settings') }}</strong>
      <p class="mt-1">{{ error || text('请检查轻量网关状态后重试。', 'Check the gateway status and retry.') }}</p>
      <button type="button" class="btn btn-secondary btn-sm mt-3" @click="emit('retry')">
        <Icon name="refresh" size="sm" />
        {{ text('重试', 'Retry') }}
      </button>
    </div>

    <div v-else class="space-y-5">
      <div class="grid gap-3 sm:grid-cols-3">
        <article class="status-card">
          <Icon :name="config.effective_enabled ? 'checkCircle' : 'clock'" size="sm" :class="config.effective_enabled ? 'text-emerald-500' : 'text-amber-500'" />
          <div>
            <p>{{ text('当前进程', 'Current process') }}</p>
            <strong>{{ config.effective_enabled ? text('队列运行中', 'Queue running') : text('队列未运行', 'Queue stopped') }}</strong>
          </div>
        </article>
        <article class="status-card">
          <Icon :name="encryptionReady ? 'checkCircle' : 'lock'" size="sm" :class="encryptionReady ? 'text-emerald-500' : 'text-amber-500'" />
          <div>
            <p>{{ text('固定加密密钥', 'Persistent encryption key') }}</p>
            <strong>{{ encryptionReady ? text('已准备', 'Ready') : text('尚未配置', 'Not configured') }}</strong>
          </div>
        </article>
        <article class="status-card">
          <Icon :name="draft.storage.type === 's3' ? 'cloud' : 'server'" size="sm" class="text-primary-500" />
          <div>
            <p>{{ text('产物存储', 'Artifact storage') }}</p>
            <strong>{{ draft.storage.type === 's3' ? 'S3 / MinIO' : text('本机目录', 'Local directory') }}</strong>
          </div>
        </article>
      </div>

      <div v-if="error" role="alert" class="rounded-xl border border-red-200 bg-red-50 p-4 text-sm text-red-700 dark:border-red-900 dark:bg-red-950/30 dark:text-red-300">
        <strong>{{ text('保存失败', 'Save failed') }}</strong>
        <p class="mt-1 break-words">{{ error }}</p>
      </div>

      <div v-if="configLocked" role="status" class="rounded-xl border border-amber-200 bg-amber-50 p-4 text-sm text-amber-800 dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-300">
        <strong>{{ config.read_only ? text('当前配置由环境变量托管，只能查看', 'Settings are managed by environment variables and are read-only') : text('配置文件当前不可写，只能查看', 'The config file is not writable') }}</strong>
        <p class="mt-1 leading-5">
          {{ config.read_only
            ? text('请在服务器环境变量中修改后重启轻量网关，页面不会提供无效的保存按钮。', 'Change the server environment variables and restart gateway-lite. Saving is disabled here to avoid false success.')
            : text('请先修复配置文件的所属用户或写入权限，再返回此页面保存。', 'Fix the config file owner or write permission before saving here.') }}
        </p>
        <p v-if="config.env_overrides.length" class="mt-2 break-all font-mono text-xs">{{ config.env_overrides.join(', ') }}</p>
      </div>

      <div v-if="config.degraded_reason" role="alert" class="rounded-xl border border-red-200 bg-red-50 p-4 text-sm text-red-700 dark:border-red-900 dark:bg-red-950/30 dark:text-red-300">
        <strong>{{ text('当前配置处于降级状态', 'The current configuration is degraded') }}</strong>
        <p class="mt-1 break-words">{{ config.degraded_reason }}</p>
      </div>

      <fieldset class="m-0 min-w-0 space-y-5 border-0 p-0" :disabled="configLocked || saving">

      <section class="config-section">
        <div class="setting-row">
          <div>
            <h3>{{ text('启用异步图片任务', 'Enable asynchronous image tasks') }}</h3>
            <p>{{ text('关闭时普通同步生图不受影响；保存不会自动替你打开此开关。', 'Synchronous image generation remains available while this is off. Saving never enables it automatically.') }}</p>
          </div>
          <Toggle v-model="draft.enabled" :disabled="saving" :aria-label="text('启用异步图片任务', 'Enable asynchronous image tasks')" />
        </div>

        <div v-if="draft.enabled && !config.encryption_key_configured" class="mt-4 rounded-xl border border-amber-200 bg-amber-50 p-4 dark:border-amber-900 dark:bg-amber-950/30">
          <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
            <div>
              <p class="text-sm font-semibold text-amber-900 dark:text-amber-200">{{ text('启用前需要固定加密密钥', 'A persistent encryption key is required') }}</p>
              <p class="mt-1 text-xs leading-5 text-amber-700 dark:text-amber-300">{{ text('点击一次即可让服务端在保存时安全生成；密钥原文不会返回到浏览器。', 'Generate it safely on the server when saving. The raw key is never returned to the browser.') }}</p>
            </div>
            <button type="button" class="btn btn-sm shrink-0" :class="generateEncryptionKey ? 'btn-secondary' : 'btn-primary'" :disabled="config.encryption_key_read_only" @click="generateEncryptionKey = !generateEncryptionKey">
              <Icon :name="generateEncryptionKey ? 'checkCircle' : 'sparkles'" size="sm" />
              {{ config.encryption_key_read_only
                ? text('请在环境变量中配置', 'Configure in environment')
                : generateEncryptionKey ? text('已选择保存时生成', 'Will generate on save') : text('一键生成', 'Generate') }}
            </button>
          </div>
        </div>
      </section>

      <section class="config-section">
        <div class="section-heading">
          <div>
            <h3>{{ text('运行规模', 'Runtime size') }}</h3>
            <p>{{ text('直接选择常用档位，后续仍可在高级设置里微调。', 'Choose a common preset, then fine-tune it under advanced settings if needed.') }}</p>
          </div>
        </div>
        <div class="mt-3 grid gap-2 sm:grid-cols-3">
          <button
            v-for="preset in performancePresets"
            :key="preset.key"
            type="button"
            class="choice-card"
            :class="activePerformancePreset === preset.key && 'choice-card-active'"
            :aria-pressed="activePerformancePreset === preset.key"
            @click="applyPerformancePreset(preset.key)"
          >
            <span class="font-semibold">{{ preset.label }}</span>
            <small>{{ preset.hint }}</small>
          </button>
        </div>

        <div class="mt-4 grid gap-4 sm:grid-cols-2">
          <div>
            <p class="field-label">{{ text('单任务最多图片', 'Maximum images per task') }}</p>
            <div class="preset-buttons">
              <button v-for="value in [1, 4, 8, 16]" :key="value" type="button" class="preset-button" :class="draft.max_images_per_task === value && 'active'" :aria-pressed="draft.max_images_per_task === value" :aria-label="text(`单任务最多 ${value} 张图片`, `Maximum ${value} images per task`)" @click="draft.max_images_per_task = value">{{ value }}</button>
            </div>
          </div>
          <div>
            <p class="field-label">{{ text('结果保留天数', 'Result retention days') }}</p>
            <div class="preset-buttons">
              <button v-for="value in [1, 7, 30, 90]" :key="value" type="button" class="preset-button" :class="draft.result_retention_days === value && 'active'" :aria-pressed="draft.result_retention_days === value" :aria-label="text(`结果保留 ${value} 天`, `Retain results for ${value} days`)" @click="draft.result_retention_days = value">{{ value }} {{ text('天', 'd') }}</button>
            </div>
          </div>
        </div>
      </section>

      <section class="config-section">
        <div class="section-heading">
          <div>
            <h3>{{ text('产物存储', 'Artifact storage') }}</h3>
            <p>{{ text('单节点可用本机目录；多个网关共同消费队列时必须使用 S3 / MinIO。', 'Use a local directory for one node. Multiple gateway nodes must use S3 / MinIO.') }}</p>
          </div>
        </div>
        <div class="mt-3 grid gap-2 sm:grid-cols-2">
          <button type="button" class="choice-card text-left" :class="draft.storage.type === 'local' && 'choice-card-active'" :aria-pressed="draft.storage.type === 'local'" @click="draft.storage.type = 'local'">
            <span class="flex items-center gap-2 font-semibold"><Icon name="server" size="sm" />{{ text('本机存储', 'Local storage') }}</span>
            <small>{{ text('适合单台轻量网关，配置最简单', 'Best for one gateway and simplest to configure') }}</small>
          </button>
          <button type="button" class="choice-card text-left" :class="draft.storage.type === 's3' && 'choice-card-active'" :aria-pressed="draft.storage.type === 's3'" @click="draft.storage.type = 's3'">
            <span class="flex items-center gap-2 font-semibold"><Icon name="cloud" size="sm" />S3 / MinIO</span>
            <small>{{ text('适合集群，共享图片产物', 'Shared artifacts for clustered gateways') }}</small>
          </button>
        </div>

        <label v-if="draft.storage.type === 'local'" class="field mt-4">
          <span>{{ text('本机目录', 'Local directory') }}</span>
          <div class="flex flex-col gap-2 sm:flex-row">
            <input v-model.trim="draft.storage.local_path" class="input min-w-0 flex-1 font-mono text-xs" placeholder="./data/image-tasks" />
            <button type="button" class="btn btn-secondary btn-sm shrink-0" @click="draft.storage.local_path = './data/image-tasks'">{{ text('使用默认目录', 'Use default') }}</button>
          </div>
        </label>

        <div v-else class="mt-4 grid gap-4 sm:grid-cols-2">
          <label class="field sm:col-span-2"><span>Endpoint（服务地址）</span><input v-model.trim="draft.storage.s3.endpoint" class="input" placeholder="https://s3.example.com" /></label>
          <label class="field"><span>Region（区域）</span><input v-model.trim="draft.storage.s3.region" class="input" placeholder="auto" /></label>
          <label class="field"><span>Bucket（存储桶）</span><input v-model.trim="draft.storage.s3.bucket" class="input" /></label>
          <label class="field"><span>Prefix（目录前缀）</span><input v-model.trim="draft.storage.s3.prefix" class="input" placeholder="image-tasks" /></label>
          <div class="setting-row rounded-xl border border-gray-200 px-4 py-3 dark:border-dark-700">
            <div><h3>Path style</h3><p>{{ text('MinIO 通常需要开启。', 'Usually required by MinIO.') }}</p></div>
            <Toggle v-model="draft.storage.s3.force_path_style" :aria-label="text('强制 Path style', 'Force path-style requests')" />
          </div>
          <label class="field"><span>Access Key ID</span><input v-model.trim="s3AccessKeyID" class="input" autocomplete="off" :placeholder="config.desired.storage.s3.access_key_id_configured ? text('已配置，留空不修改', 'Configured; leave blank to keep') : text('未配置', 'Not configured')" /></label>
          <label class="field"><span>Secret Access Key</span><input v-model="s3SecretAccessKey" class="input" type="password" autocomplete="new-password" :placeholder="config.desired.storage.s3.secret_access_key_configured ? text('已配置，留空不修改', 'Configured; leave blank to keep') : text('未配置', 'Not configured')" /></label>
        </div>
      </section>

      <details class="rounded-xl border border-gray-200 bg-gray-50/60 p-4 dark:border-dark-700 dark:bg-dark-900/40">
        <summary class="cursor-pointer select-none text-sm font-semibold text-gray-800 dark:text-gray-100">{{ text('高级设置', 'Advanced settings') }}</summary>
        <p class="mt-2 text-xs text-gray-500 dark:text-gray-400">{{ text('默认值适合大多数节点；只有明确需要时再调整。', 'Defaults suit most nodes. Change these values only when necessary.') }}</p>
        <div class="mt-4 grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          <label class="field"><span>{{ text('工作线程', 'Workers') }} (1–64)</span><input v-model.number="draft.worker_count" class="input" type="number" min="1" max="64" /></label>
          <label class="field"><span>{{ text('队列容量', 'Queue capacity') }} (1–100000)</span><input v-model.number="draft.queue_capacity" class="input" type="number" min="1" max="100000" /></label>
          <label class="field"><span>{{ text('领取批次', 'Claim batch') }} (1–500)</span><input v-model.number="draft.claim_batch_size" class="input" type="number" min="1" max="500" /></label>
          <label class="field"><span>{{ text('单用户并发', 'Per-user concurrency') }} (1–16)</span><input v-model.number="draft.per_user_concurrency" class="input" type="number" min="1" max="16" /></label>
          <label class="field"><span>{{ text('最大尝试次数', 'Maximum attempts') }} (1–10)</span><input v-model.number="draft.max_attempts" class="input" type="number" min="1" max="10" /></label>
          <label class="field"><span>{{ text('轮询间隔（毫秒）', 'Poll interval (ms)') }}</span><input v-model.number="draft.poll_interval_milliseconds" class="input" type="number" min="100" max="60000" /></label>
          <label class="field"><span>{{ text('执行超时（秒）', 'Execution timeout (s)') }}</span><input v-model.number="draft.execution_timeout_seconds" class="input" type="number" min="10" max="3600" /></label>
          <label class="field"><span>{{ text('任务锁定时长（秒）', 'Claim TTL (s)') }}</span><input v-model.number="draft.claim_ttl_seconds" class="input" type="number" min="190" max="7200" /></label>
          <label class="field"><span>{{ text('下载链接有效期（秒）', 'Download URL TTL (s)') }}</span><input v-model.number="draft.download_url_ttl_seconds" class="input" type="number" min="60" max="86400" /></label>
          <label class="field"><span>{{ text('请求体上限（MiB）', 'Request limit (MiB)') }}</span><input v-model.number="maxRequestMiB" class="input" type="number" min="0.016" max="4" step="0.25" /></label>
          <label class="field"><span>{{ text('单图上限（MiB）', 'Per-image limit (MiB)') }}</span><input v-model.number="maxArtifactMiB" class="input" type="number" min="1" max="100" /></label>
          <label class="field"><span>{{ text('单任务产物上限（MiB）', 'Per-task artifact limit (MiB)') }}</span><input v-model.number="maxTaskArtifactMiB" class="input" type="number" min="1" max="500" /></label>
        </div>
      </details>

      </fieldset>

      <div v-if="validationErrors.length" role="alert" class="rounded-xl border border-amber-200 bg-amber-50 p-4 text-sm text-amber-800 dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-300">
        <strong>{{ text('还需要处理以下配置', 'Complete these settings first') }}</strong>
        <ul class="mt-2 list-disc space-y-1 pl-5">
          <li v-for="message in validationErrors" :key="message">{{ message }}</li>
        </ul>
      </div>

      <div class="rounded-xl border border-gray-200 bg-gray-50 p-4 text-xs leading-5 text-gray-600 dark:border-dark-700 dark:bg-dark-900/50 dark:text-gray-300">
        <p><strong>{{ text('配置文件：', 'Config file:') }}</strong><span class="break-all font-mono">{{ config.config_path || 'config.yaml' }}</span></p>
        <p class="mt-1 text-amber-700 dark:text-amber-300">{{ text('点击保存后会先安全写入配置；写入成功且需要生效时，页面才会自动重启轻量网关并等待恢复。', 'The page saves the config first, then restarts gateway-lite only after a successful save and waits for recovery.') }}</p>
      </div>
    </div>

    <template #footer>
      <div class="grid w-full grid-cols-2 gap-2 sm:flex sm:justify-end">
        <button type="button" class="btn btn-secondary" :disabled="saving" @click="emit('close')">{{ text('取消', 'Cancel') }}</button>
        <button type="button" class="btn btn-primary" :disabled="!canSave" @click="submit">
          <span v-if="saving" class="h-4 w-4 animate-spin rounded-full border-2 border-white/40 border-b-white" />
          <Icon v-else name="checkCircle" size="sm" />
          {{ saving ? text('保存中', 'Saving') : text('保存并重启', 'Save and restart') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Toggle from '@/components/common/Toggle.vue'
import Icon from '@/components/icons/Icon.vue'
import type {
  ImageTaskAdminConfig,
  ImageTaskDesiredConfig,
  UpdateImageTaskConfigRequest,
} from '@/api/admin/imageTasks'

const props = defineProps<{
  show: boolean
  config: ImageTaskAdminConfig | null
  loading: boolean
  saving: boolean
  error?: string
}>()

const emit = defineEmits<{
  (event: 'close'): void
  (event: 'retry'): void
  (event: 'save', payload: UpdateImageTaskConfigRequest): void
}>()

const { locale } = useI18n()
const draft = ref<ImageTaskDesiredConfig | null>(null)
const generateEncryptionKey = ref(false)
const s3AccessKeyID = ref('')
const s3SecretAccessKey = ref('')
const mib = 1024 * 1024

const text = (zh: string, en: string) => locale.value.toLowerCase().startsWith('zh') ? zh : en

const performancePresets = computed(() => [
  { key: 'light', label: text('轻量', 'Light'), hint: text('1 线程 · 128 队列', '1 worker · 128 queue') },
  { key: 'standard', label: text('标准（推荐）', 'Standard (recommended)'), hint: text('2 线程 · 256 队列', '2 workers · 256 queue') },
  { key: 'high', label: text('高吞吐', 'High throughput'), hint: text('4 线程 · 1024 队列', '4 workers · 1024 queue') },
] as const)

const activePerformancePreset = computed(() => {
  if (!draft.value) return ''
  const value = draft.value
  if (value.worker_count === 1 && value.queue_capacity === 128 && value.claim_batch_size === 8 && value.per_user_concurrency === 1) return 'light'
  if (value.worker_count === 2 && value.queue_capacity === 256 && value.claim_batch_size === 16 && value.per_user_concurrency === 1) return 'standard'
  if (value.worker_count === 4 && value.queue_capacity === 1024 && value.claim_batch_size === 32 && value.per_user_concurrency === 2) return 'high'
  return ''
})

const encryptionReady = computed(() => Boolean(props.config?.encryption_key_configured || generateEncryptionKey.value))
const configLocked = computed(() => Boolean(props.config?.read_only || !props.config?.config_writable))

const maxRequestMiB = computed({
  get: () => Number(((draft.value?.max_request_bytes || 0) / mib).toFixed(3)),
  set: (value: number) => { if (draft.value) draft.value.max_request_bytes = Math.round(Number(value || 0) * mib) },
})
const maxArtifactMiB = computed({
  get: () => Number(((draft.value?.max_artifact_bytes || 0) / mib).toFixed(2)),
  set: (value: number) => { if (draft.value) draft.value.max_artifact_bytes = Math.round(Number(value || 0) * mib) },
})
const maxTaskArtifactMiB = computed({
  get: () => Number(((draft.value?.max_task_artifact_bytes || 0) / mib).toFixed(2)),
  set: (value: number) => { if (draft.value) draft.value.max_task_artifact_bytes = Math.round(Number(value || 0) * mib) },
})

function inRange(value: number, min: number, max: number) {
  return Number.isFinite(value) && value >= min && value <= max
}

const validationErrors = computed(() => {
  const value = draft.value
  if (!value) return []
  const errors: string[] = []
  if (!inRange(value.worker_count, 1, 64)) errors.push(text('工作线程必须在 1–64 之间。', 'Workers must be between 1 and 64.'))
  if (!inRange(value.queue_capacity, 1, 100000)) errors.push(text('队列容量必须在 1–100000 之间。', 'Queue capacity must be between 1 and 100000.'))
  if (!inRange(value.claim_batch_size, 1, 500)) errors.push(text('领取批次必须在 1–500 之间。', 'Claim batch must be between 1 and 500.'))
  if (!inRange(value.per_user_concurrency, 1, 16)) errors.push(text('单用户并发必须在 1–16 之间。', 'Per-user concurrency must be between 1 and 16.'))
  if (!inRange(value.max_images_per_task, 1, 16)) errors.push(text('单任务图片数必须在 1–16 之间。', 'Images per task must be between 1 and 16.'))
  if (!inRange(value.max_attempts, 1, 10)) errors.push(text('最大尝试次数必须在 1–10 之间。', 'Maximum attempts must be between 1 and 10.'))
  if (!inRange(value.execution_timeout_seconds, 10, 3600)) errors.push(text('执行超时必须在 10–3600 秒之间。', 'Execution timeout must be between 10 and 3600 seconds.'))
  if (value.claim_ttl_seconds < value.execution_timeout_seconds + 180 || value.claim_ttl_seconds > 7200) errors.push(text('任务锁定时长必须比执行超时至少多 180 秒，且不能超过 7200 秒。', 'Claim TTL must exceed execution timeout by at least 180 seconds and stay within 7200 seconds.'))
  if (!inRange(value.poll_interval_milliseconds, 100, 60000)) errors.push(text('轮询间隔必须在 100–60000 毫秒之间。', 'Poll interval must be between 100 and 60000 milliseconds.'))
  if (!inRange(value.result_retention_days, 1, 90)) errors.push(text('结果保留必须在 1–90 天之间。', 'Result retention must be between 1 and 90 days.'))
  if (!inRange(value.download_url_ttl_seconds, 60, 86400)) errors.push(text('下载链接有效期必须在 60–86400 秒之间。', 'Download URL TTL must be between 60 and 86400 seconds.'))
  if (!inRange(value.max_request_bytes, 16 * 1024, 4 * mib)) errors.push(text('请求体上限必须在 16 KiB–4 MiB 之间。', 'Request limit must be between 16 KiB and 4 MiB.'))
  if (!inRange(value.max_artifact_bytes, mib, 100 * mib)) errors.push(text('单图上限必须在 1–100 MiB 之间。', 'Per-image limit must be between 1 and 100 MiB.'))
  if (value.max_task_artifact_bytes < value.max_artifact_bytes || value.max_task_artifact_bytes > 500 * mib) errors.push(text('单任务产物上限不能小于单图上限，且不能超过 500 MiB。', 'Per-task artifact limit cannot be below the per-image limit or exceed 500 MiB.'))
  if (value.enabled) {
    if (!encryptionReady.value) {
      errors.push(props.config?.encryption_key_read_only
        ? text('固定加密密钥由环境变量托管，请先在服务器中配置。', 'The persistent encryption key is managed by the environment; configure it on the server first.')
        : text('请先选择“一键生成”固定加密密钥。', 'Generate a persistent encryption key first.'))
    }
    if (value.storage.type === 'local' && !value.storage.local_path.trim()) errors.push(text('请填写本机存储目录。', 'Enter a local storage directory.'))
    if (value.storage.type === 's3') {
      if (!value.storage.s3.bucket.trim()) errors.push(text('请填写 S3 / MinIO 存储桶。', 'Enter an S3 / MinIO bucket.'))
      if (!props.config?.desired.storage.s3.access_key_id_configured && !s3AccessKeyID.value.trim()) errors.push(text('请填写 Access Key ID。', 'Enter an Access Key ID.'))
      if (!props.config?.desired.storage.s3.secret_access_key_configured && !s3SecretAccessKey.value) errors.push(text('请填写 Secret Access Key。', 'Enter a Secret Access Key.'))
    }
  }
  return errors
})

const canSave = computed(() => Boolean(draft.value && !props.loading && !props.saving && !configLocked.value && validationErrors.value.length === 0))

function cloneDesired(source: ImageTaskDesiredConfig): ImageTaskDesiredConfig {
  return {
    ...source,
    storage: {
      type: source.storage?.type === 's3' ? 's3' : 'local',
      local_path: source.storage?.local_path || './data/image-tasks',
      s3: {
        endpoint: source.storage?.s3?.endpoint || '',
        region: source.storage?.s3?.region || 'auto',
        bucket: source.storage?.s3?.bucket || '',
        prefix: source.storage?.s3?.prefix || 'image-tasks',
        force_path_style: Boolean(source.storage?.s3?.force_path_style),
        access_key_id_configured: Boolean(source.storage?.s3?.access_key_id_configured),
        secret_access_key_configured: Boolean(source.storage?.s3?.secret_access_key_configured),
      },
    },
  }
}

function resetDraft() {
  draft.value = props.config?.desired ? cloneDesired(props.config.desired) : null
  generateEncryptionKey.value = false
  s3AccessKeyID.value = ''
  s3SecretAccessKey.value = ''
}

function applyPerformancePreset(key: 'light' | 'standard' | 'high') {
  if (!draft.value) return
  const presets = {
    light: { worker_count: 1, queue_capacity: 128, claim_batch_size: 8, per_user_concurrency: 1 },
    standard: { worker_count: 2, queue_capacity: 256, claim_batch_size: 16, per_user_concurrency: 1 },
    high: { worker_count: 4, queue_capacity: 1024, claim_batch_size: 32, per_user_concurrency: 2 },
  }
  Object.assign(draft.value, presets[key])
}

function submit() {
  if (!draft.value || !props.config || !canSave.value) return
  const value = draft.value
  emit('save', {
    expected_revision: props.config.revision,
    generate_encryption_key: generateEncryptionKey.value,
    desired: {
      enabled: value.enabled,
      worker_count: value.worker_count,
      queue_capacity: value.queue_capacity,
      claim_batch_size: value.claim_batch_size,
      per_user_concurrency: value.per_user_concurrency,
      max_images_per_task: value.max_images_per_task,
      max_attempts: value.max_attempts,
      execution_timeout_seconds: value.execution_timeout_seconds,
      claim_ttl_seconds: value.claim_ttl_seconds,
      poll_interval_milliseconds: value.poll_interval_milliseconds,
      result_retention_days: value.result_retention_days,
      download_url_ttl_seconds: value.download_url_ttl_seconds,
      max_request_bytes: value.max_request_bytes,
      max_artifact_bytes: value.max_artifact_bytes,
      max_task_artifact_bytes: value.max_task_artifact_bytes,
      storage: {
        type: value.storage.type,
        local_path: value.storage.local_path.trim(),
        s3: {
          endpoint: value.storage.s3.endpoint.trim(),
          region: value.storage.s3.region.trim() || 'auto',
          bucket: value.storage.s3.bucket.trim(),
          prefix: value.storage.s3.prefix.trim() || 'image-tasks',
          force_path_style: value.storage.s3.force_path_style,
          access_key_id: s3AccessKeyID.value.trim() || '',
          secret_access_key: s3SecretAccessKey.value || '',
        },
      },
    },
  })
}

watch(
  () => [props.show, props.config?.revision] as const,
  ([show]) => { if (show) resetDraft() },
  { immediate: true },
)
</script>

<style scoped>
.status-card {
  @apply flex min-w-0 items-start gap-3 rounded-xl border border-gray-200 bg-gray-50/70 p-3 dark:border-dark-700 dark:bg-dark-900/50;
}

.status-card p {
  @apply text-[11px] text-gray-500 dark:text-gray-400;
}

.status-card strong {
  @apply mt-0.5 block text-sm font-semibold text-gray-900 dark:text-white;
}

.config-section {
  @apply rounded-xl border border-gray-200 p-4 dark:border-dark-700;
}

.section-heading h3,
.setting-row h3 {
  @apply text-sm font-semibold text-gray-900 dark:text-white;
}

.section-heading p,
.setting-row p {
  @apply mt-1 text-xs leading-5 text-gray-500 dark:text-gray-400;
}

.setting-row {
  @apply flex items-start justify-between gap-4;
}

.choice-card {
  @apply flex min-h-[72px] flex-col justify-center gap-1 rounded-xl border border-gray-200 bg-white px-4 py-3 text-gray-700 transition-colors hover:border-gray-300 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-200 dark:hover:border-dark-500;
}

.choice-card small {
  @apply text-xs font-normal leading-5 text-gray-500 dark:text-gray-400;
}

.choice-card-active {
  @apply border-primary-500 bg-primary-50 text-primary-700 ring-1 ring-primary-500/20 dark:border-primary-500 dark:bg-primary-900/20 dark:text-primary-300;
}

.field {
  @apply flex min-w-0 flex-col gap-2;
}

.field > span,
.field-label {
  @apply text-xs font-semibold text-gray-600 dark:text-gray-300;
}

.preset-buttons {
  @apply mt-2 flex flex-wrap gap-2;
}

.preset-button {
  @apply min-h-9 rounded-lg border border-gray-200 bg-white px-3 text-xs font-semibold text-gray-600 transition-colors hover:border-primary-300 hover:text-primary-600 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-300 dark:hover:border-primary-600 dark:hover:text-primary-300;
}

.preset-button.active {
  @apply border-primary-500 bg-primary-50 text-primary-700 dark:bg-primary-900/20 dark:text-primary-300;
}

@media (max-width: 390px) {
  .config-section {
    @apply p-3;
  }
}
</style>
