<template>
  <div v-if="visible" data-testid="cn-provider-balance" class="space-y-1">
    <div class="flex flex-wrap items-center gap-1.5">
      <button
        type="button"
        :class="[
          'inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[10px] font-medium transition-colors hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-50 dark:hover:bg-dark-600',
          platformTextClass(account.platform)
        ]"
        :disabled="loading"
        :aria-label="t('admin.accounts.cnProviders.balance')"
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
        {{ balanceLabel }}
      </button>
      <span
        v-if="balanceLow"
        class="inline-flex items-center rounded bg-red-100 px-1 py-0.5 text-[10px] font-medium text-red-700 dark:bg-red-900/30 dark:text-red-300"
      >
        {{ t('admin.accounts.cnProviders.balanceLow') }}
      </span>
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
import type { CNProviderBalanceEntry, CNProviderBalanceResult } from '@/api/admin/cnProviders'
import type { Account } from '@/types'
import { platformTextClass } from '@/utils/platformColors'
import { cnBalanceCellVisible } from './credentialsBuilder'

const props = defineProps<{ account: Account }>()
const { t } = useI18n()
const loading = ref(false)
const error = ref('')
const result = ref<CNProviderBalanceResult | null>(null)

const accountMode = computed(() => {
  const mode = props.account.credentials?.account_mode
  return typeof mode === 'string' ? mode : ''
})
const visible = computed(() => cnBalanceCellVisible(props.account.platform, accountMode.value))
const extraKey = (suffix: string) => `${props.account.platform}_${suffix}`
const balanceLow = computed(() => props.account.extra?.[extraKey('balance_low')] === true)

const snapshotEntries = computed<CNProviderBalanceEntry[]>(() => {
  const multiple = props.account.extra?.[extraKey('balances')]
  if (Array.isArray(multiple)) {
    const entries = multiple.flatMap((item): CNProviderBalanceEntry[] => {
      if (!item || typeof item !== 'object') return []
      const { currency, balance } = item as Record<string, unknown>
      return typeof currency === 'string' && typeof balance === 'number'
        ? [{ currency, balance }]
        : []
    })
    if (entries.length) return entries
  }
  const balance = props.account.extra?.[extraKey('balance')]
  const currency = props.account.extra?.[extraKey('balance_currency')]
  return typeof balance === 'number'
    ? [{ currency: typeof currency === 'string' ? currency : '', balance }]
    : []
})
const entries = computed<CNProviderBalanceEntry[]>(() => {
  if (!result.value?.success) return snapshotEntries.value
  if (result.value.balances?.length) return result.value.balances
  return [{ currency: result.value.currency || '', balance: result.value.balance }]
})
const formatEntry = (entry: CNProviderBalanceEntry) => {
  const value = entry.balance >= 100 ? entry.balance.toFixed(0) : entry.balance.toFixed(2)
  return `${entry.currency || '¥'} ${value}`
}
const balanceLabel = computed(() => entries.value.length
  ? entries.value.map(formatEntry).join(' · ')
  : t('admin.accounts.cnProviders.balance'))
const errorMessage = (cause: unknown) => {
  const candidate = cause as { message?: string; reason?: string }
  return candidate?.message || candidate?.reason || t('common.error')
}

async function probe() {
  if (loading.value) return
  loading.value = true
  error.value = ''
  try {
    const response = await adminAPI.cnProviders.queryBalance(props.account.id)
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
