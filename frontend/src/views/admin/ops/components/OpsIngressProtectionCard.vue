<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import {
  opsAPI,
  type OpsIngressRejectionReason,
  type OpsIngressRejectionSnapshot,
  type OpsPanelRateLimitPreset,
  type OpsPanelRateLimitRuntimeResponse,
  type OpsPanelRateLimitRuntimeStats,
  type OpsPanelRateLimitSettings
} from '@/api/admin/ops'
import { formatNumber } from '@/utils/format'
import { getDefaultTimeZoneDateParts } from '@/utils/timezone'

interface Props {
  refreshToken?: number
}

const props = withDefaults(defineProps<Props>(), {
  refreshToken: 0
})

const { t } = useI18n()

const loadingConfig = ref(false)
const loadingRejections = ref(false)
const saving = ref(false)
const config = ref<OpsPanelRateLimitSettings | null>(null)
const runtimeStats = ref<OpsPanelRateLimitRuntimeStats | null>(null)
const snapshot = ref<OpsIngressRejectionSnapshot | null>(null)
const configError = ref('')
const rejectionError = ref('')
const successMessage = ref('')
const customDraft = ref({ user_rpm: 240, heavy_rpm: 60, window_seconds: 60 })

const presetOptions = computed<Array<{
  value: OpsPanelRateLimitPreset
  label: string
  hint: string
}>>(() => [
  {
    value: 'default',
    label: t('admin.ops.ingressProtection.presets.default'),
    hint: t('admin.ops.ingressProtection.presets.defaultHint')
  },
  {
    value: 'relaxed',
    label: t('admin.ops.ingressProtection.presets.relaxed'),
    hint: t('admin.ops.ingressProtection.presets.relaxedHint')
  },
  {
    value: 'strict',
    label: t('admin.ops.ingressProtection.presets.strict'),
    hint: t('admin.ops.ingressProtection.presets.strictHint')
  },
  {
    value: 'custom',
    label: t('admin.ops.ingressProtection.presets.custom'),
    hint: t('admin.ops.ingressProtection.presets.customHint')
  }
])

const visibleTrend = computed(() => (snapshot.value?.trend ?? []).slice(-30))
const trendMax = computed(() => Math.max(1, ...visibleTrend.value.map((point) => point.total)))

function errorText(error: unknown, fallbackKey: string): string {
  if (error && typeof error === 'object') {
    const candidate = error as { message?: string; response?: { data?: { detail?: string } } }
    return candidate.response?.data?.detail || candidate.message || t(fallbackKey)
  }
  return t(fallbackKey)
}

function formatPercent(value?: number | null): string {
  if (typeof value !== 'number' || !Number.isFinite(value)) return '0.00%'
  return `${value.toFixed(value >= 10 ? 1 : 2)}%`
}

function reasonLabel(reason: OpsIngressRejectionReason): string {
  return t(`admin.ops.ingressProtection.reasons.${reason}`)
}

function trendHeight(total: number): string {
  if (total <= 0) return '4%'
  return `${Math.max(10, Math.round((total / trendMax.value) * 100))}%`
}

function trendTime(value: string): string {
  try {
    const parts = getDefaultTimeZoneDateParts(value)
    return `${String(parts.hour).padStart(2, '0')}:${String(parts.minute).padStart(2, '0')}`
  } catch {
    return '--:--'
  }
}

async function loadConfig() {
  loadingConfig.value = true
  configError.value = ''
  try {
    applyRuntimeResponse(await opsAPI.getPanelRateLimitSettings())
  } catch (error) {
    configError.value = errorText(error, 'admin.ops.ingressProtection.loadConfigFailed')
  } finally {
    loadingConfig.value = false
  }
}

function applyRuntimeResponse(result: OpsPanelRateLimitRuntimeResponse) {
  config.value = result.settings
  runtimeStats.value = result.runtime_stats
  customDraft.value = {
    user_rpm: result.settings.user_rpm,
    heavy_rpm: result.settings.heavy_rpm,
    window_seconds: result.settings.window_seconds
  }
}

async function loadRejections() {
  loadingRejections.value = true
  rejectionError.value = ''
  try {
    snapshot.value = await opsAPI.getIngressRejections(60)
  } catch (error) {
    rejectionError.value = errorText(error, 'admin.ops.ingressProtection.loadRejectionsFailed')
  } finally {
    loadingRejections.value = false
  }
}

function refreshAll() {
  void Promise.all([loadConfig(), loadRejections()])
}

async function saveConfig(next: OpsPanelRateLimitSettings, successKey: string) {
  if (saving.value) return
  saving.value = true
  configError.value = ''
  successMessage.value = ''
  try {
    applyRuntimeResponse(await opsAPI.updatePanelRateLimitSettings(next))
    successMessage.value = t(successKey)
  } catch (error) {
    configError.value = errorText(error, 'admin.ops.ingressProtection.saveFailed')
  } finally {
    saving.value = false
  }
}

async function setEnabled(enabled: boolean) {
  if (!config.value) return
  await saveConfig({ ...config.value, enabled }, 'admin.ops.ingressProtection.switchSaved')
}

async function selectPreset(preset: OpsPanelRateLimitPreset) {
  if (!config.value || config.value.preset === preset) return
  await saveConfig(
    { ...config.value, preset },
    'admin.ops.ingressProtection.presetSaved'
  )
}

async function saveCustomConfig() {
  if (!config.value) return
  await saveConfig(
    {
      ...config.value,
      preset: 'custom',
      user_rpm: Number(customDraft.value.user_rpm),
      heavy_rpm: Number(customDraft.value.heavy_rpm),
      window_seconds: Number(customDraft.value.window_seconds)
    },
    'admin.ops.ingressProtection.customSaved'
  )
}

async function resetConfig() {
  if (saving.value) return
  saving.value = true
  configError.value = ''
  successMessage.value = ''
  try {
    applyRuntimeResponse(await opsAPI.resetPanelRateLimitSettings())
    successMessage.value = t('admin.ops.ingressProtection.resetSuccess')
  } catch (error) {
    configError.value = errorText(error, 'admin.ops.ingressProtection.resetFailed')
  } finally {
    saving.value = false
  }
}

onMounted(() => {
  refreshAll()
})

watch(
  () => props.refreshToken,
  () => {
    void loadRejections()
  }
)
</script>

<template>
  <section class="card overflow-hidden p-4 md:p-5">
    <div class="mb-5 flex flex-wrap items-start justify-between gap-3">
      <div>
        <h3 class="text-sm font-bold text-gray-900 dark:text-white">
          {{ t('admin.ops.ingressProtection.title') }}
        </h3>
        <p class="mt-1 max-w-3xl text-xs leading-5 text-gray-500 dark:text-gray-400">
          {{ t('admin.ops.ingressProtection.description') }}
        </p>
      </div>
      <button
        type="button"
        class="btn btn-secondary btn-sm"
        :disabled="loadingConfig || loadingRejections"
        @click="refreshAll"
      >
        {{ t('admin.ops.ingressProtection.refresh') }}
      </button>
    </div>

    <div class="grid grid-cols-1 gap-5 xl:grid-cols-5">
      <div class="rounded-2xl border border-gray-200 bg-gray-50/70 p-4 dark:border-dark-700 dark:bg-dark-800/50 xl:col-span-2">
        <div class="flex items-center justify-between gap-4">
          <div>
            <p class="text-sm font-semibold text-gray-900 dark:text-white">
              {{ t('admin.ops.ingressProtection.rateLimitTitle') }}
            </p>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.ops.ingressProtection.rateLimitHint') }}
            </p>
          </div>
          <Toggle
            v-if="config"
            :model-value="config.enabled"
            :disabled="saving"
            data-testid="panel-rate-limit-toggle"
            @update:model-value="setEnabled"
          />
          <span v-else class="h-6 w-11 animate-pulse rounded-full bg-gray-200 dark:bg-dark-600" />
        </div>

        <div v-if="configError" class="mt-3 rounded-xl bg-red-50 px-3 py-2 text-xs text-red-600 dark:bg-red-900/20 dark:text-red-400">
          {{ configError }}
        </div>
        <div v-else-if="successMessage" class="mt-3 rounded-xl bg-emerald-50 px-3 py-2 text-xs text-emerald-700 dark:bg-emerald-900/20 dark:text-emerald-300">
          {{ successMessage }}
        </div>

        <div class="mt-4 grid grid-cols-1 gap-2 sm:grid-cols-2 2xl:grid-cols-4">
          <button
            v-for="preset in presetOptions"
            :key="preset.value"
            type="button"
            class="rounded-xl border px-3 py-3 text-left transition-colors"
            :class="config?.preset === preset.value
              ? 'border-primary-500 bg-primary-50 text-primary-700 dark:border-primary-500 dark:bg-primary-500/10 dark:text-primary-300'
              : 'border-gray-200 bg-white text-gray-700 hover:border-primary-300 hover:bg-primary-50/50 dark:border-dark-700 dark:bg-dark-900/50 dark:text-gray-200 dark:hover:border-primary-700 dark:hover:bg-primary-500/5'"
            :aria-pressed="config?.preset === preset.value"
            :data-testid="`preset-${preset.value}`"
            :disabled="saving || loadingConfig || !config"
            @click="selectPreset(preset.value)"
          >
            <span class="block text-xs font-semibold">{{ preset.label }}</span>
            <span class="mt-1 block text-[11px] leading-4 opacity-70">{{ preset.hint }}</span>
          </button>
        </div>

        <div
          v-if="config?.preset === 'custom'"
          class="mt-3 rounded-xl border border-primary-200 bg-primary-50/50 p-3 dark:border-primary-800 dark:bg-primary-500/5"
          data-testid="custom-rate-limit-fields"
        >
          <div class="grid grid-cols-1 gap-2 sm:grid-cols-3">
            <label class="text-[11px] font-medium text-gray-600 dark:text-gray-300">
              {{ t('admin.ops.ingressProtection.userRpm') }}
              <input
                v-model.number="customDraft.user_rpm"
                type="number"
                min="1"
                max="100000"
                step="10"
                class="input mt-1 w-full"
                data-testid="custom-user-rpm"
              >
            </label>
            <label class="text-[11px] font-medium text-gray-600 dark:text-gray-300">
              {{ t('admin.ops.ingressProtection.heavyRpm') }}
              <input
                v-model.number="customDraft.heavy_rpm"
                type="number"
                min="1"
                max="100000"
                step="10"
                class="input mt-1 w-full"
                data-testid="custom-heavy-rpm"
              >
            </label>
            <label class="text-[11px] font-medium text-gray-600 dark:text-gray-300">
              {{ t('admin.ops.ingressProtection.window') }}
              <select
                v-model.number="customDraft.window_seconds"
                class="input mt-1 w-full"
                data-testid="custom-window-seconds"
              >
                <option :value="10">10s</option>
                <option :value="30">30s</option>
                <option :value="60">60s</option>
                <option :value="300">5min</option>
              </select>
            </label>
          </div>
          <button
            type="button"
            class="btn btn-primary btn-sm mt-3 w-full"
            :disabled="saving"
            data-testid="save-custom-rate-limit"
            @click="saveCustomConfig"
          >
            {{ t('admin.ops.ingressProtection.saveCustom') }}
          </button>
        </div>

        <div v-if="config" class="mt-4 grid grid-cols-3 gap-2">
          <div class="rounded-xl bg-white px-3 py-2 dark:bg-dark-900/60">
            <p class="text-[11px] text-gray-500 dark:text-gray-400">{{ t('admin.ops.ingressProtection.userRpm') }}</p>
            <p class="mt-1 text-sm font-bold text-gray-900 dark:text-white">{{ formatNumber(config.user_rpm) }}</p>
          </div>
          <div class="rounded-xl bg-white px-3 py-2 dark:bg-dark-900/60">
            <p class="text-[11px] text-gray-500 dark:text-gray-400">{{ t('admin.ops.ingressProtection.heavyRpm') }}</p>
            <p class="mt-1 text-sm font-bold text-gray-900 dark:text-white">{{ formatNumber(config.heavy_rpm) }}</p>
          </div>
          <div class="rounded-xl bg-white px-3 py-2 dark:bg-dark-900/60">
            <p class="text-[11px] text-gray-500 dark:text-gray-400">{{ t('admin.ops.ingressProtection.window') }}</p>
            <p class="mt-1 text-sm font-bold text-gray-900 dark:text-white">{{ config.window_seconds }}s</p>
          </div>
        </div>

        <div v-if="runtimeStats" class="mt-3 flex flex-wrap gap-x-4 gap-y-1 text-[11px] text-gray-500 dark:text-gray-400">
          <span>{{ t('admin.ops.ingressProtection.checkedTotal') }} <strong class="text-gray-800 dark:text-gray-200">{{ formatNumber(runtimeStats.checked_total) }}</strong></span>
          <span>{{ t('admin.ops.ingressProtection.rejectedTotal') }} <strong class="text-gray-800 dark:text-gray-200">{{ formatNumber(runtimeStats.rejected_total) }}</strong></span>
          <span>{{ t('admin.ops.ingressProtection.activeBuckets') }} <strong class="text-gray-800 dark:text-gray-200">{{ formatNumber(runtimeStats.active_buckets) }}</strong></span>
        </div>

        <div class="mt-4 flex items-center justify-between gap-3 border-t border-gray-200 pt-4 dark:border-dark-700">
          <span class="text-xs text-gray-500 dark:text-gray-400">
            {{ config?.enabled ? t('admin.ops.ingressProtection.enabled') : t('admin.ops.ingressProtection.disabled') }}
          </span>
          <button
            type="button"
            class="btn btn-secondary btn-sm"
            data-testid="reset-panel-rate-limit"
            :disabled="saving || loadingConfig"
            @click="resetConfig"
          >
            {{ t('admin.ops.ingressProtection.reset') }}
          </button>
        </div>
      </div>

      <div class="rounded-2xl border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-900/30 xl:col-span-3">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <div>
            <p class="text-sm font-semibold text-gray-900 dark:text-white">
              {{ t('admin.ops.ingressProtection.rejectionTitle') }}
            </p>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.ops.ingressProtection.rejectionHint', { node: snapshot?.node || '-' }) }}
            </p>
          </div>
          <span class="rounded-full bg-gray-100 px-2.5 py-1 text-[11px] text-gray-600 dark:bg-dark-700 dark:text-gray-300">
            {{ t('admin.ops.ingressProtection.last60Minutes') }}
          </span>
        </div>

        <div v-if="rejectionError" class="mt-3 rounded-xl bg-red-50 px-3 py-2 text-xs text-red-600 dark:bg-red-900/20 dark:text-red-400">
          {{ rejectionError }}
        </div>

        <div v-if="loadingRejections && !snapshot" class="mt-4 h-48 animate-pulse rounded-xl bg-gray-100 dark:bg-dark-800" />

        <template v-else-if="snapshot">
          <div class="mt-4 grid grid-cols-3 gap-2 md:gap-3">
            <div class="rounded-xl bg-gray-50 p-3 dark:bg-dark-800/70">
              <p class="text-[11px] text-gray-500 dark:text-gray-400">{{ t('admin.ops.ingressProtection.requestCount') }}</p>
              <p class="mt-1 text-lg font-bold text-gray-900 dark:text-white">{{ formatNumber(snapshot.request_count) }}</p>
            </div>
            <div class="rounded-xl bg-amber-50 p-3 dark:bg-amber-900/15">
              <p class="text-[11px] text-amber-700/80 dark:text-amber-300/80">{{ t('admin.ops.ingressProtection.rejectionCount') }}</p>
              <p class="mt-1 text-lg font-bold text-amber-700 dark:text-amber-300">{{ formatNumber(snapshot.rejection_count) }}</p>
            </div>
            <div class="rounded-xl bg-rose-50 p-3 dark:bg-rose-900/15">
              <p class="text-[11px] text-rose-700/80 dark:text-rose-300/80">{{ t('admin.ops.ingressProtection.rejectionRate') }}</p>
              <p class="mt-1 text-lg font-bold text-rose-700 dark:text-rose-300">{{ formatPercent(snapshot.rejection_rate_percent) }}</p>
            </div>
          </div>

          <div class="mt-4">
            <div class="mb-2 flex items-center justify-between gap-2">
              <p class="text-xs font-medium text-gray-700 dark:text-gray-300">{{ t('admin.ops.ingressProtection.trend') }}</p>
              <p class="text-[11px] text-gray-400 dark:text-gray-500">{{ t('admin.ops.ingressProtection.perMinute') }}</p>
            </div>
            <div v-if="visibleTrend.length" class="flex h-28 items-end gap-1 rounded-xl bg-gray-50 px-3 pb-3 pt-4 dark:bg-dark-800/60" data-testid="ingress-rejection-trend">
              <div
                v-for="point in visibleTrend"
                :key="point.bucket_start"
                class="group relative flex h-full min-w-0 flex-1 items-end"
                :title="t('admin.ops.ingressProtection.trendPoint', { time: trendTime(point.bucket_start), count: point.total })"
              >
                <span
                  class="block w-full rounded-t-sm bg-amber-400 transition-colors group-hover:bg-amber-500 dark:bg-amber-500/80 dark:group-hover:bg-amber-400"
                  :style="{ height: trendHeight(point.total) }"
                />
              </div>
            </div>
            <div v-else class="rounded-xl bg-gray-50 py-8 text-center text-xs text-gray-500 dark:bg-dark-800/60 dark:text-gray-400">
              {{ t('admin.ops.ingressProtection.noRejections') }}
            </div>
          </div>

          <div class="mt-4">
            <p class="mb-2 text-xs font-medium text-gray-700 dark:text-gray-300">{{ t('admin.ops.ingressProtection.topReasons') }}</p>
            <div v-if="snapshot.top_reasons.length" class="flex flex-wrap gap-2">
              <span
                v-for="item in snapshot.top_reasons"
                :key="item.reason"
                class="inline-flex items-center gap-1.5 rounded-full border border-gray-200 bg-gray-50 px-2.5 py-1 text-[11px] text-gray-600 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-300"
              >
                {{ reasonLabel(item.reason) }}
                <strong class="font-semibold text-gray-900 dark:text-white">{{ formatNumber(item.count) }}</strong>
              </span>
            </div>
            <p v-else class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ops.ingressProtection.noRejections') }}</p>
          </div>
        </template>
      </div>
    </div>
  </section>
</template>
