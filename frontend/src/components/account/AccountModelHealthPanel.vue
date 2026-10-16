<template>
  <section class="rounded-lg border border-gray-200 bg-gray-50/80 p-3 dark:border-dark-600 dark:bg-dark-800/70">
    <div class="flex items-start justify-between gap-3">
      <div>
        <h3 class="text-sm font-semibold text-gray-800 dark:text-gray-100">
          {{ t('admin.accounts.modelHealth.title') }}
        </h3>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
          {{ t('admin.accounts.modelHealth.hint') }}
        </p>
      </div>
      <button type="button" class="btn btn-secondary btn-sm shrink-0" :disabled="loading" @click="emit('refresh')">
        {{ loading ? t('admin.accounts.modelHealth.loading') : t('common.refresh') }}
      </button>
    </div>

    <div v-if="loading" class="mt-3 text-xs text-gray-500 dark:text-gray-400">
      {{ t('admin.accounts.modelHealth.loading') }}
    </div>
    <div v-else-if="!health || !health.runtime_observer_available || health.observed_models.length === 0" class="mt-3 rounded border border-dashed border-gray-300 px-3 py-2 text-xs text-gray-500 dark:border-dark-500 dark:text-gray-400">
      {{ t('admin.accounts.modelHealth.noEvidence') }}
    </div>
    <div v-else class="mt-3 space-y-2">
      <div
        v-for="item in health.observed_models"
        :key="item.model"
        class="rounded-md border border-gray-200 bg-white px-3 py-2 dark:border-dark-600 dark:bg-dark-900/60"
      >
        <div class="flex flex-wrap items-center justify-between gap-2">
          <code class="min-w-0 truncate text-xs text-gray-700 dark:text-gray-200">{{ item.model }}</code>
          <span :class="stateClass(item.state)" class="rounded-full px-2 py-0.5 text-[11px] font-medium">
            {{ stateLabel(item.state) }}
          </span>
        </div>
        <div class="mt-1 grid gap-x-3 gap-y-1 text-[11px] text-gray-500 sm:grid-cols-2 dark:text-gray-400">
          <span>{{ t('admin.accounts.modelHealth.lastSuccess') }}{{ formatTime(item.last_success_at) }}</span>
          <span>{{ t('admin.accounts.modelHealth.failures', { count: item.consecutive_failures }) }}</span>
          <span v-if="item.cooldown_until">{{ t('admin.accounts.modelHealth.cooldownUntil') }}{{ formatTime(item.cooldown_until) }}</span>
          <span v-if="item.probe_until">{{ t('admin.accounts.modelHealth.probeUntil') }}{{ formatTime(item.probe_until) }}</span>
        </div>
      </div>
    </div>

    <div v-if="health?.declarations?.length" class="mt-3 border-t border-gray-200 pt-3 dark:border-dark-600">
      <p class="text-[11px] font-medium text-gray-600 dark:text-gray-300">{{ t('admin.accounts.modelHealth.declarations') }}</p>
      <div class="mt-1 flex flex-wrap gap-1.5">
        <span
          v-for="item in health.declarations"
          :key="`${item.scope}-${item.group_id ?? 'account'}-${item.requested_model}-${item.upstream_model}`"
          class="rounded bg-gray-100 px-2 py-1 text-[11px] text-gray-600 dark:bg-dark-700 dark:text-gray-300"
        >
          {{ item.requested_model }} → {{ item.upstream_model }} · {{ item.scope === 'group' ? t('admin.accounts.modelHealth.groupScope') : t('admin.accounts.modelHealth.accountScope') }}
        </span>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { AccountModelHealthResponse, AccountModelHealthState } from '@/types'
import { formatDateTime } from '@/utils/format'

defineProps<{
  health: AccountModelHealthResponse | null
  loading: boolean
}>()

const emit = defineEmits<{
  refresh: []
}>()

const { t } = useI18n()

const formatTime = (value?: string | null) => value ? formatDateTime(new Date(value)) : t('common.none')

const stateLabel = (state: AccountModelHealthState) => t(`admin.accounts.modelHealth.states.${state}`)

const stateClass = (state: AccountModelHealthState) => {
  if (state === 'healthy') return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
  if (state === 'failing') return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
  return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
}
</script>
