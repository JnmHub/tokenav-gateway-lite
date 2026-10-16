<template>
  <div class="flex min-w-[7.75rem] items-center justify-between gap-2">
    <div class="min-w-0 space-y-1">
      <p class="whitespace-nowrap text-[11px] text-gray-500 dark:text-gray-400">
        {{ t('admin.accounts.upstreamBilling.manualRate') }}
        <span class="font-mono font-semibold text-gray-700 dark:text-gray-200">{{ manualRate }}</span>
      </p>
      <HelpTooltip v-if="eligible" width-class="w-72">
        <template #trigger>
          <button
            type="button"
            class="flex max-w-full items-center gap-1 border-b border-dotted border-gray-300 text-left text-xs font-medium dark:border-dark-600"
            :class="statusClass"
          >
            <span class="text-gray-500 dark:text-gray-400">{{ t('admin.accounts.upstreamBilling.detectedRate') }}</span>
            <span class="truncate font-mono">{{ detectedValue }}</span>
          </button>
        </template>
        <div class="space-y-1.5 text-xs">
          <p>{{ t('admin.accounts.upstreamBilling.status') }}：{{ statusLabel }}</p>
          <template v-if="snapshot?.data">
            <p>{{ t('admin.accounts.upstreamBilling.groupRate') }}：{{ formatRate(snapshot.data.group_rate_multiplier) }}</p>
            <p v-if="snapshot.data.user_rate_multiplier != null">
              {{ t('admin.accounts.upstreamBilling.userRate') }}：{{ formatRate(snapshot.data.user_rate_multiplier) }}
            </p>
            <p>{{ t('admin.accounts.upstreamBilling.effectiveRate') }}：{{ formatRate(snapshot.data.effective_rate_multiplier) }}</p>
          </template>
          <p>{{ t('admin.accounts.upstreamBilling.updatedAt') }}：{{ formatDate(snapshot?.received_at) }}</p>
          <p class="border-t border-white/15 pt-1.5 text-gray-300">
            {{ t('admin.accounts.upstreamBilling.costOnlyHint') }}
          </p>
        </div>
      </HelpTooltip>
    </div>
    <button
      v-if="eligible"
      type="button"
      class="inline-flex h-7 w-7 flex-shrink-0 items-center justify-center rounded-md border border-blue-200 text-blue-600 transition-colors hover:bg-blue-50 disabled:cursor-not-allowed disabled:opacity-50 dark:border-blue-800 dark:text-blue-400 dark:hover:bg-blue-900/30"
      :disabled="probing"
      :aria-label="t('admin.accounts.upstreamBilling.manualProbe')"
      :title="t('admin.accounts.upstreamBilling.manualProbe')"
      @click="$emit('probe')"
    >
      <Icon name="refresh" size="xs" :class="{ 'animate-spin': probing }" />
    </button>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import Icon from '@/components/icons/Icon.vue'
import { formatDateTime } from '@/utils/format'
import type { Account } from '@/types'

const props = withDefaults(defineProps<{
  account: Account
  probing?: boolean
}>(), {
  probing: false
})

defineEmits<{
  (event: 'probe'): void
}>()

const { t } = useI18n()
const eligible = computed(() =>
  props.account.platform === 'openai' &&
  props.account.type === 'apikey' &&
  props.account.extra?.upstream_provider === 'sub2api'
)
const snapshot = computed(() => props.account.extra?.upstream_billing_probe)
const manualRate = computed(() => formatRate(props.account.rate_multiplier ?? 1))
const stale = computed(() => {
  const value = snapshot.value?.fresh_until
  if (!value) return false
  const timestamp = Date.parse(value)
  return Number.isFinite(timestamp) && timestamp <= Date.now()
})
const validDetectedRate = computed(() => {
  if (!snapshot.value?.data || stale.value || snapshot.value.status === 'unsupported') return null
  const value = snapshot.value.data.effective_rate_multiplier
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : null
})
const statusLabel = computed(() => {
  if (!snapshot.value) return t('admin.accounts.upstreamBilling.notProbed')
  if (snapshot.value.status === 'unsupported') return t('admin.accounts.upstreamBilling.unsupported')
  if (stale.value) return t('admin.accounts.upstreamBilling.stale')
  if (snapshot.value.status === 'failed') return t('admin.accounts.upstreamBilling.failed')
  return t('admin.accounts.upstreamBilling.ok')
})
const detectedValue = computed(() => validDetectedRate.value == null ? statusLabel.value : formatRate(validDetectedRate.value))
const statusClass = computed(() => {
  if (validDetectedRate.value != null && snapshot.value?.status === 'ok') return 'text-emerald-600 dark:text-emerald-400'
  if (snapshot.value?.status === 'failed') return 'text-red-600 dark:text-red-400'
  if (stale.value) return 'text-amber-600 dark:text-amber-400'
  return 'text-gray-500 dark:text-gray-400'
})

function formatRate(value: number): string {
  if (!Number.isFinite(value)) return '-'
  return `${Number(value.toPrecision(8))}x`
}

function formatDate(value?: string): string {
  if (!value) return '-'
  const timestamp = Date.parse(value)
  if (!Number.isFinite(timestamp)) return '-'
  return formatDateTime(new Date(timestamp)) || '-'
}
</script>
