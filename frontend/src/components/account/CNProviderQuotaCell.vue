<template>
  <div v-if="visible" data-testid="cn-provider-quota" class="min-w-[220px] space-y-1">
    <button
      type="button"
      :class="[
        'inline-flex items-center gap-1 whitespace-nowrap rounded px-1.5 py-0.5 text-[10px] font-medium leading-4 transition-colors hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-50 dark:hover:bg-dark-600',
        platformTextClass(account.platform)
      ]"
      :disabled="loading"
      :title="t('admin.accounts.cnProviders.probeTooltip')"
      :aria-label="t('admin.accounts.cnProviders.probeTooltip')"
      @click="probe"
    >
      <svg
        class="h-2.5 w-2.5"
        :class="{ 'animate-spin': loading }"
        fill="none"
        stroke="currentColor"
        viewBox="0 0 24 24"
      >
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          stroke-width="2"
          d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"
        />
      </svg>
      {{ t('admin.accounts.cnProviders.window5h') }}/{{ t('admin.accounts.cnProviders.windowWeekly') }}
    </button>

    <div v-if="tiers.length" class="space-y-1">
      <div
        v-for="tier in tiers"
        :key="tier.window"
        data-testid="cn-provider-quota-tier"
        class="flex min-w-0 items-center gap-1.5 text-[10px] leading-4"
      >
        <span class="w-14 shrink-0 whitespace-nowrap text-gray-500 dark:text-gray-400">
          {{ windowLabel(tier.window) }}
        </span>
        <div class="h-1.5 w-16 shrink-0 overflow-hidden rounded-full bg-gray-200 dark:bg-dark-600">
          <div
            class="h-full rounded-full transition-all"
            :class="barClass(tier.used_percent)"
            :style="{ width: `${clampPercent(tier.used_percent)}%` }"
          />
        </div>
        <span :class="['shrink-0 font-medium', textClass(tier.used_percent)]">
          {{ Math.round(tier.used_percent) }}%
        </span>
        <span
          v-if="tier.reset_at"
          class="min-w-0 truncate text-gray-400 dark:text-gray-500"
          :title="tier.reset_at"
        >
          · {{ formatReset(tier.reset_at) }}
        </span>
      </div>
    </div>

    <div v-if="error" class="max-w-[220px] truncate text-[10px] text-red-600 dark:text-red-400" :title="error">
      {{ error }}
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { CNProviderQuotaProbeResult, CNQuotaTier } from '@/api/admin/cnProviders'
import type { Account } from '@/types'
import { platformTextClass } from '@/utils/platformColors'
import { cnQuotaCellVisible } from './credentialsBuilder'

const props = defineProps<{ account: Account }>()
const { t } = useI18n()
const loading = ref(false)
const error = ref('')
const result = ref<CNProviderQuotaProbeResult | null>(null)

const accountMode = computed(() => {
  const mode = props.account.credentials?.account_mode
  return typeof mode === 'string' ? mode : ''
})
const visible = computed(() => cnQuotaCellVisible(props.account.platform, accountMode.value))

const extraNumber = (key: string) => {
  const value = props.account.extra?.[key]
  return typeof value === 'number' && Number.isFinite(value) ? value : null
}
const extraString = (key: string) => {
  const value = props.account.extra?.[key]
  return typeof value === 'string' ? value : undefined
}
const snapshotTiers = computed<CNQuotaTier[]>(() => {
  const platform = props.account.platform
  const fiveHour = extraNumber(`${platform}_5h_used_percent`)
  const weekly = extraNumber(`${platform}_weekly_used_percent`)
  const items: CNQuotaTier[] = []
  if (fiveHour !== null) {
    items.push({ window: '5h', used_percent: fiveHour, reset_at: extraString(`${platform}_5h_reset_at`) })
  }
  if (weekly !== null) {
    items.push({ window: 'weekly', used_percent: weekly, reset_at: extraString(`${platform}_weekly_reset_at`) })
  }
  return items
})
const tiers = computed(() => result.value?.success ? result.value.tiers || [] : snapshotTiers.value)

const clampPercent = (percent: number) => Math.min(100, Math.max(0, percent))
const windowLabel = (window: string) => window === 'weekly'
  ? t('admin.accounts.cnProviders.windowWeekly')
  : t('admin.accounts.cnProviders.window5h')
const barClass = (percent: number) => percent >= 90 ? 'bg-red-500' : percent >= 75 ? 'bg-amber-500' : 'bg-emerald-500'
const textClass = (percent: number) => percent >= 90
  ? 'text-red-600 dark:text-red-400'
  : percent >= 75
    ? 'text-amber-600 dark:text-amber-400'
    : 'text-emerald-600 dark:text-emerald-400'
const formatReset = (value: string) => {
  const resetAt = new Date(value)
  if (Number.isNaN(resetAt.getTime())) return value
  const remaining = resetAt.getTime() - Date.now()
  if (remaining <= 0) return t('admin.accounts.cnProviders.resetSoon')
  if (remaining < 3_600_000) return `${Math.max(1, Math.round(remaining / 60_000))}m`
  const hours = Math.round(remaining / 3_600_000)
  if (hours < 48) return `${hours}h`
  return `${String(resetAt.getMonth() + 1).padStart(2, '0')}-${String(resetAt.getDate()).padStart(2, '0')}`
}
const errorMessage = (cause: unknown) => {
  const candidate = cause as { message?: string; reason?: string }
  return candidate?.message || candidate?.reason || t('common.error')
}

async function probe() {
  if (loading.value) return
  loading.value = true
  error.value = ''
  try {
    const response = await adminAPI.cnProviders.queryQuota(props.account.id)
    if (response.success) result.value = response
    else error.value = response.error || t('common.error')
  } catch (cause) {
    error.value = errorMessage(cause)
  } finally {
    loading.value = false
  }
}

watch(() => props.account.id, () => {
  result.value = null
  error.value = ''
  loading.value = false
})
</script>
