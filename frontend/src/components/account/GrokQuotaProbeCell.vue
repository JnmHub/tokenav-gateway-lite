<template>
  <div v-if="visible" class="space-y-1">
    <div
      v-if="eligibility"
      class="flex flex-wrap items-center gap-1.5 text-[10px]"
      data-testid="grok-media-eligibility"
    >
      <span :class="eligibilityBadgeClass">
        {{ t(eligibilityStateKey) }}
      </span>
      <span class="text-gray-500 dark:text-gray-400">
        {{ t(eligibilitySourceKey) }}
      </span>
      <span
        v-if="eligibility.stale"
        class="rounded bg-amber-100 px-1.5 py-0.5 font-medium text-amber-700 dark:bg-amber-900/30 dark:text-amber-300"
      >
        {{ t('admin.accounts.usageWindow.grokMediaEligibilityStale') }}
      </span>
    </div>
    <div v-if="eligibilityEvidence" class="text-[10px] text-gray-500 dark:text-gray-400">
      {{ eligibilityEvidence }}
    </div>

    <div v-if="canProbe" class="flex flex-wrap items-center gap-1.5">
      <button
        type="button"
        class="inline-flex items-center gap-0.5 rounded px-1.5 py-0.5 text-[10px] font-medium text-cyan-700 transition-colors hover:bg-cyan-50 disabled:cursor-not-allowed disabled:opacity-50 dark:text-cyan-300 dark:hover:bg-cyan-900/30"
        :disabled="loading"
        :title="t('admin.accounts.usageWindow.grokProbeTooltip')"
        @click="handleProbe"
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
        {{ t('admin.accounts.usageWindow.grokProbe') }}
      </button>

      <button
        type="button"
        class="inline-flex cursor-not-allowed items-center gap-0.5 rounded px-1.5 py-0.5 text-[10px] font-medium text-gray-400 opacity-70 dark:text-gray-500"
        disabled
        :title="t('admin.accounts.usageWindow.grokResetUnsupportedTooltip')"
      >
        {{ t('admin.accounts.usageWindow.grokResetUnsupported') }}
      </button>
    </div>

    <div v-if="summary" class="text-[10px] text-gray-600 dark:text-gray-300">
      {{ summary }}
    </div>
    <div v-if="displayError" class="truncate text-[10px] text-red-600 dark:text-red-400" :title="displayError">
      {{ truncatedError }}
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { formatDateTime } from '@/utils/format'
import { adminAPI } from '@/api/admin'
import type { GrokQuotaProbeResult, GrokQuotaWindow } from '@/api/admin/grok'
import type { Account } from '@/types'

const props = defineProps<{
  account: Account
}>()

const { t } = useI18n()

const visible = computed(() => props.account.platform === 'grok')
const canProbe = computed(() => props.account.type === 'oauth')
const loading = ref(false)
const error = ref<string | null>(null)
const data = ref<GrokQuotaProbeResult | null>(null)
const eligibility = computed(() => props.account.grok_media_eligibility || null)
const eligibilityStateKey = computed(() => {
  switch (eligibility.value?.state) {
    case 'eligible':
      return 'admin.accounts.usageWindow.grokMediaEligibilityEligible'
    case 'ineligible':
      return 'admin.accounts.usageWindow.grokMediaEligibilityIneligible'
    default:
      return 'admin.accounts.usageWindow.grokMediaEligibilityUnknown'
  }
})
const eligibilitySourceKey = computed(() =>
  eligibility.value?.source === 'override'
    ? 'admin.accounts.usageWindow.grokMediaEligibilityManual'
    : 'admin.accounts.usageWindow.grokMediaEligibilityAutomatic'
)
const eligibilityBadgeClass = computed(() => {
  const base = 'rounded px-1.5 py-0.5 font-medium'
  switch (eligibility.value?.state) {
    case 'eligible':
      return `${base} bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300`
    case 'ineligible':
      return `${base} bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300`
    default:
      return `${base} bg-gray-100 text-gray-600 dark:bg-dark-600 dark:text-gray-300`
  }
})
const eligibilityEvidence = computed(() => {
  const current = eligibility.value
  if (!current) return ''
  const parts: string[] = []
  if (current.weekly_status_code) {
    parts.push(t('admin.accounts.usageWindow.grokMediaEligibilityWeeklyStatus', { status: current.weekly_status_code }))
  }
  if (current.monthly_status_code) {
    parts.push(t('admin.accounts.usageWindow.grokMediaEligibilityMonthlyStatus', { status: current.monthly_status_code }))
  }
  const timestamp = current.probed_at || current.observed_at
  if (timestamp) {
    const formatted = formatDateTime(timestamp)
    if (formatted) parts.push(t('admin.accounts.usageWindow.grokMediaEligibilityProbeTime', { time: formatted }))
  }
  return parts.join(' | ')
})

const extractErrorMessage = (e: unknown): string => {
  const err = e as {
    message?: string
    reason?: string
    response?: { data?: { message?: string; error?: string } }
  }
  return (
    err?.message ||
    err?.reason ||
    err?.response?.data?.message ||
    err?.response?.data?.error ||
    t('common.error')
  )
}

const formatWindow = (label: string, window?: GrokQuotaWindow | null): string | null => {
  if (!window || window.limit == null || window.remaining == null) return null
  return `${label} ${window.remaining}/${window.limit}`
}

const retryAfterLabel = computed(() => {
  const seconds = data.value?.snapshot?.retry_after_seconds
  if (seconds == null || seconds <= 0) return null
  if (seconds < 60) return `${seconds}s`
  return `${Math.ceil(seconds / 60)}m`
})

const summary = computed(() => {
	const billing = data.value?.billing
  const snapshot = data.value?.snapshot
  if (!data.value) return ''
	const parts: string[] = []
	if (billing?.plan) parts.push(billing.plan)
	if (billing?.period_type === 'weekly' && billing.usage_percent != null) {
		parts.push(t('admin.accounts.usageWindow.grokBillingWeeklySummary', { percent: Math.round(billing.usage_percent) }))
	}
	if (billing?.used_percent != null) {
		parts.push(t('admin.accounts.usageWindow.grokBillingMonthlySummary', { percent: Math.round(billing.used_percent) }))
	}
	if (billing?.partial) {
		parts.push(t('admin.accounts.usageWindow.grokBillingPartialSummary'))
	}
	if (snapshot) {
		parts.push(
			...[formatWindow(t('admin.accounts.usageWindow.grokRequests'), snapshot.requests),
				formatWindow(t('admin.accounts.usageWindow.grokTokens'), snapshot.tokens)].filter(
				(value): value is string => Boolean(value)
			)
		)
	}
  if (retryAfterLabel.value) {
    parts.push(t('admin.accounts.usageWindow.grokRetryAfter', { time: retryAfterLabel.value }))
  }
	if (snapshot?.entitlement_status) {
    parts.push(snapshot.entitlement_status)
  }
  return parts.length > 0 ? parts.join(' | ') : t('admin.accounts.usageWindow.grokNoHeaders')
})

const displayError = computed(() => data.value?.probe_error || error.value)
const truncatedError = computed(() => {
	if (!displayError.value) return ''
	return displayError.value.length > 80 ? `${displayError.value.slice(0, 80)}...` : displayError.value
})

const handleProbe = async () => {
  if (loading.value) return
  loading.value = true
  error.value = null
  try {
    data.value = await adminAPI.grok.queryQuota(props.account.id)
  } catch (e) {
    error.value = extractErrorMessage(e)
  } finally {
    loading.value = false
  }
}

watch(
  () => props.account.id,
  () => {
    data.value = null
    error.value = null
    loading.value = false
  }
)
</script>
