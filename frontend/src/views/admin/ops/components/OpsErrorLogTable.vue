<template>
  <div class="flex h-full min-h-0 flex-col bg-white dark:bg-dark-900">
    <!-- Loading State -->
    <div v-if="loading" class="flex flex-1 items-center justify-center py-10">
      <div class="h-8 w-8 animate-spin rounded-full border-b-2 border-primary-600"></div>
    </div>

    <!-- Table Container -->
    <div v-else class="flex min-h-0 flex-1 flex-col">
      <div v-if="showToolbar" class="flex flex-shrink-0 flex-wrap items-center gap-2 border-b border-gray-200 bg-gray-50/70 p-3 dark:border-dark-700 dark:bg-dark-800/60">
        <div class="min-w-[220px] flex-1">
          <div class="relative">
            <Icon name="search" size="sm" class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-gray-400" />
            <input
              :value="filters.q"
              type="text"
              class="w-full rounded-lg border border-gray-200 bg-white py-2 pl-9 pr-3 text-xs font-medium text-gray-700 outline-none transition focus:border-primary-500 focus:ring-2 focus:ring-primary-500/20 dark:border-dark-600 dark:bg-dark-900 dark:text-gray-200"
              :placeholder="t('admin.ops.errorLog.searchPlaceholder')"
              @input="updateFilter('q', ($event.target as HTMLInputElement).value)"
            />
          </div>
        </div>
        <div class="w-32 compact-select">
          <Select :model-value="filters.statusCode" :options="statusCodeOptions" @update:model-value="updateFilter('statusCode', $event as ErrorStatusFilter)" />
        </div>
        <div class="w-32 compact-select">
          <Select :model-value="filters.phase" :options="phaseOptions" @update:model-value="updateFilter('phase', String($event ?? ''))" />
        </div>
        <div class="w-36 compact-select">
          <Select :model-value="filters.category" :options="categoryOptions" @update:model-value="updateFilter('category', String($event ?? ''))" />
        </div>
        <div class="w-32 compact-select">
          <Select :model-value="filters.errorOwner" :options="ownerOptions" @update:model-value="updateFilter('errorOwner', String($event ?? ''))" />
        </div>
        <div class="w-40 compact-select">
          <Select :model-value="filters.sort" :options="sortOptions" @update:model-value="updateFilter('sort', $event as ErrorSortFilter)" />
        </div>
        <div class="w-32 compact-select">
          <Select :model-value="filters.viewMode" :options="viewModeOptions" @update:model-value="updateFilter('viewMode', $event as ErrorViewFilter)" />
        </div>
        <button type="button" class="btn btn-secondary btn-sm" @click="emit('resetFilters')">
          {{ t('common.reset') }}
        </button>
        <div class="relative" ref="columnDropdownRef">
          <button type="button" class="btn btn-secondary btn-sm" @click="showColumnDropdown = !showColumnDropdown">
            <Icon name="grid" size="sm" />
            <span>{{ t('admin.ops.errorLog.columnSettings') }}</span>
          </button>
          <div
            v-if="showColumnDropdown"
            class="absolute right-0 top-full z-50 mt-1 max-h-80 w-48 overflow-y-auto rounded-lg border border-gray-200 bg-white py-1 shadow-lg dark:border-dark-600 dark:bg-dark-800"
          >
            <button
              v-for="col in toggleableColumns"
              :key="col.key"
              type="button"
              class="flex w-full items-center justify-between px-4 py-2 text-left text-sm text-gray-700 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-dark-700"
              @click="toggleColumn(col.key)"
            >
              <span>{{ col.label }}</span>
              <Icon v-if="isColumnVisible(col.key)" name="check" size="sm" class="text-primary-500" :stroke-width="2" />
            </button>
          </div>
        </div>
      </div>
      <div class="min-h-0 flex-1 overflow-auto border-b border-gray-200 dark:border-dark-700">
        <table class="w-full border-separate border-spacing-0">
          <thead class="sticky top-0 z-10 bg-gray-50 dark:bg-dark-800">
            <tr>
              <th v-if="isColumnVisible('time')" class="border-b border-gray-200 px-4 py-2.5 text-left text-[11px] font-bold uppercase tracking-wider text-gray-500 dark:border-dark-700 dark:text-dark-400">
                {{ t('admin.ops.errorLog.time') }}
              </th>
              <th v-if="isColumnVisible('type')" class="border-b border-gray-200 px-4 py-2.5 text-left text-[11px] font-bold uppercase tracking-wider text-gray-500 dark:border-dark-700 dark:text-dark-400">
                {{ t('admin.ops.errorLog.type') }}
              </th>
              <th v-if="isColumnVisible('category')" class="border-b border-gray-200 px-4 py-2.5 text-left text-[11px] font-bold uppercase tracking-wider text-gray-500 dark:border-dark-700 dark:text-dark-400">
                {{ t('usage.errors.category') }}
              </th>
              <th v-if="isColumnVisible('endpoint')" class="border-b border-gray-200 px-4 py-2.5 text-left text-[11px] font-bold uppercase tracking-wider text-gray-500 dark:border-dark-700 dark:text-dark-400">
                {{ t('admin.ops.errorLog.endpoint') }}
              </th>
              <th v-if="isColumnVisible('platform')" class="border-b border-gray-200 px-4 py-2.5 text-left text-[11px] font-bold uppercase tracking-wider text-gray-500 dark:border-dark-700 dark:text-dark-400">
                {{ t('admin.ops.errorLog.platform') }}
              </th>
              <th v-if="isColumnVisible('model')" class="border-b border-gray-200 px-4 py-2.5 text-left text-[11px] font-bold uppercase tracking-wider text-gray-500 dark:border-dark-700 dark:text-dark-400">
                {{ t('admin.ops.errorLog.model') }}
              </th>
              <th v-if="isColumnVisible('group')" class="border-b border-gray-200 px-4 py-2.5 text-left text-[11px] font-bold uppercase tracking-wider text-gray-500 dark:border-dark-700 dark:text-dark-400">
                {{ t('admin.ops.errorLog.group') }}
              </th>
              <th v-if="isColumnVisible('user')" class="border-b border-gray-200 px-4 py-2.5 text-left text-[11px] font-bold uppercase tracking-wider text-gray-500 dark:border-dark-700 dark:text-dark-400">
                {{ t('admin.ops.errorLog.user') }}
              </th>
              <th v-if="isColumnVisible('apiKey')" class="border-b border-gray-200 px-4 py-2.5 text-left text-[11px] font-bold uppercase tracking-wider text-gray-500 dark:border-dark-700 dark:text-dark-400">
                {{ t('admin.ops.errorLog.apiKey') }}
              </th>
              <th v-if="isColumnVisible('account')" class="border-b border-gray-200 px-4 py-2.5 text-left text-[11px] font-bold uppercase tracking-wider text-gray-500 dark:border-dark-700 dark:text-dark-400">
                {{ t('admin.ops.errorLog.account') }}
              </th>
              <th v-if="isColumnVisible('status')" class="border-b border-gray-200 px-4 py-2.5 text-left text-[11px] font-bold uppercase tracking-wider text-gray-500 dark:border-dark-700 dark:text-dark-400">
                {{ t('admin.ops.errorLog.status') }}
              </th>
              <th v-if="isColumnVisible('message')" class="border-b border-gray-200 px-4 py-2.5 text-left text-[11px] font-bold uppercase tracking-wider text-gray-500 dark:border-dark-700 dark:text-dark-400">
                {{ t('admin.ops.errorLog.message') }}
              </th>
              <th v-if="isColumnVisible('actions')" class="border-b border-gray-200 px-4 py-2.5 text-right text-[11px] font-bold uppercase tracking-wider text-gray-500 dark:border-dark-700 dark:text-dark-400">
                {{ t('admin.ops.errorLog.action') }}
              </th>
            </tr>
          </thead>
          <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
            <tr v-if="rows.length === 0">
              <td :colspan="visibleColumns.length" class="py-12 text-center text-sm text-gray-400 dark:text-dark-500">
                {{ t('admin.ops.errorLog.noErrors') }}
              </td>
            </tr>

            <tr
              v-for="log in rows"
              :key="log.id"
              class="group cursor-pointer transition-colors hover:bg-gray-50/80 dark:hover:bg-dark-800/50"
              @click="emit('openErrorDetail', log.id)"
            >
              <!-- Time -->
              <td v-if="isColumnVisible('time')" class="whitespace-nowrap px-4 py-2">
                <el-tooltip :content="log.request_id || log.client_request_id" placement="top" :show-after="500">
                  <span class="font-mono text-xs font-medium text-gray-900 dark:text-gray-200">
                    {{ formatDateTime(log.created_at).split(' ')[1] }}
                  </span>
                </el-tooltip>
              </td>

              <!-- Type -->
              <td v-if="isColumnVisible('type')" class="whitespace-nowrap px-4 py-2">
                <span
                  :class="[
                    'inline-flex items-center rounded px-1.5 py-0.5 text-[10px] font-bold ring-1 ring-inset',
                    getTypeBadge(log).className
                  ]"
                >
                  {{ getTypeBadge(log).label }}
                </span>
              </td>

              <!-- Stable category derived from phase + error type -->
              <td v-if="isColumnVisible('category')" class="whitespace-nowrap px-4 py-2">
                <span
                  class="inline-flex items-center rounded px-1.5 py-0.5 text-[10px] font-bold ring-1 ring-inset"
                  :class="ERROR_CATEGORY_BADGE_CLASSES[mapErrorCategory(log.phase, log.type)]"
                >
                  {{ t(`usage.errors.categories.${mapErrorCategory(log.phase, log.type)}`) }}
                </span>
              </td>

              <!-- Endpoint -->
              <td v-if="isColumnVisible('endpoint')" class="px-4 py-2">
                <div class="max-w-[160px]">
                  <el-tooltip v-if="log.inbound_endpoint" :content="formatEndpointTooltip(log)" placement="top" :show-after="500">
                    <span class="truncate font-mono text-[11px] text-gray-700 dark:text-gray-300">
                      {{ log.inbound_endpoint }}
                    </span>
                  </el-tooltip>
                  <span v-else class="text-xs text-gray-400">-</span>
                </div>
              </td>

              <!-- Platform -->
              <td v-if="isColumnVisible('platform')" class="whitespace-nowrap px-4 py-2">
                <span class="inline-flex items-center rounded bg-gray-100 px-1.5 py-0.5 text-[10px] font-bold uppercase text-gray-600 dark:bg-dark-700 dark:text-gray-300">
                  {{ log.platform || '-' }}
                </span>
              </td>

              <!-- Model -->
              <td v-if="isColumnVisible('model')" class="px-4 py-2">
                <div class="max-w-[160px]">
                  <template v-if="hasModelMapping(log)">
                    <el-tooltip :content="modelMappingTooltip(log)" placement="top" :show-after="500">
                      <span class="flex items-center gap-1 truncate font-mono text-[11px] text-gray-700 dark:text-gray-300">
                        <span class="truncate">{{ log.requested_model }}</span>
                        <span class="flex-shrink-0 text-gray-400">→</span>
                        <span class="truncate text-primary-600 dark:text-primary-400">{{ log.upstream_model }}</span>
                      </span>
                    </el-tooltip>
                  </template>
                  <template v-else>
                    <span v-if="displayModel(log)" class="truncate font-mono text-[11px] text-gray-700 dark:text-gray-300" :title="displayModel(log)">
                      {{ displayModel(log) }}
                    </span>
                    <span v-else class="text-xs text-gray-400">-</span>
                  </template>
                </div>
              </td>

              <!-- Group -->
              <td v-if="isColumnVisible('group')" class="px-4 py-2">
                 <el-tooltip v-if="log.group_id" :content="t('admin.ops.errorLog.id') + ' ' + log.group_id" placement="top" :show-after="500">
                  <span class="max-w-[100px] truncate text-xs font-medium text-gray-900 dark:text-gray-200">
                    {{ log.group_name || '-' }}
                  </span>
                </el-tooltip>
                <span v-else class="text-xs text-gray-400">-</span>
              </td>

              <!-- User -->
              <td v-if="isColumnVisible('user')" class="px-4 py-2">
                <el-tooltip v-if="log.user_id" :content="t('admin.ops.errorLog.userId') + ' ' + log.user_id" placement="top" :show-after="500">
                  <span class="block max-w-[140px] truncate text-xs font-medium text-gray-900 dark:text-gray-200">
                    {{ log.user_email || '-' }}
                  </span>
                </el-tooltip>
                <span v-else class="text-xs text-gray-400">-</span>
              </td>

              <!-- API Key -->
              <td v-if="isColumnVisible('apiKey')" class="px-4 py-2">
                <div v-if="log.api_key_id || log.api_key_name" class="flex max-w-[140px] items-center gap-1">
                  <span class="truncate text-xs font-medium text-gray-900 dark:text-gray-200" :title="log.api_key_name || ('#' + log.api_key_id)">
                    {{ log.api_key_name || ('#' + log.api_key_id) }}
                  </span>
                  <span
                    v-if="log.api_key_deleted"
                    class="flex-shrink-0 rounded px-1 py-0.5 text-[9px] font-bold ring-1 ring-inset bg-red-50 text-red-700 ring-red-600/20 dark:bg-red-900/30 dark:text-red-400 dark:ring-red-500/30"
                  >
                    {{ t('admin.ops.errorLog.keyDeletedBadge') }}
                  </span>
                </div>
                <span v-else class="text-xs text-gray-400">-</span>
              </td>

              <!-- Account -->
              <td v-if="isColumnVisible('account')" class="px-4 py-2">
                <el-tooltip v-if="log.account_id" :content="t('admin.ops.errorLog.accountId') + ' ' + log.account_id" placement="top" :show-after="500">
                  <span class="block max-w-[120px] truncate text-xs font-medium text-gray-900 dark:text-gray-200">
                    {{ log.account_name || '-' }}
                  </span>
                </el-tooltip>
                <span v-else class="text-xs text-gray-400">-</span>
              </td>

              <!-- Status -->
              <td v-if="isColumnVisible('status')" class="whitespace-nowrap px-4 py-2">
                <div class="flex items-center gap-1.5">
                  <span
                    :class="[
                      'inline-flex items-center rounded px-1.5 py-0.5 text-[10px] font-bold ring-1 ring-inset',
                      getStatusClass(log.status_code)
                    ]"
                  >
                    {{ log.status_code }}
                  </span>
                  <span
                    v-if="log.severity"
                    :class="['rounded px-1.5 py-0.5 text-[10px] font-bold', getSeverityClass(log.severity)]"
                  >
                    {{ log.severity }}
                  </span>
                  <span
                    v-if="log.request_type != null && log.request_type > 0"
                    class="rounded bg-gray-100 px-1.5 py-0.5 text-[10px] font-bold text-gray-600 dark:bg-dark-700 dark:text-gray-300"
                  >
                    {{ formatRequestType(log.request_type) }}
                  </span>
                </div>
              </td>

              <!-- Message (Response Content) -->
              <td v-if="isColumnVisible('message')" class="px-4 py-2">
                <div class="max-w-[200px]">
                  <p class="truncate text-[11px] font-medium text-gray-600 dark:text-gray-400" :title="log.message">
                    {{ formatSmartMessage(log.message) || '-' }}
                  </p>
                </div>
              </td>

              <!-- Actions -->
              <td v-if="isColumnVisible('actions')" class="whitespace-nowrap px-4 py-2 text-right" @click.stop>
                <div class="flex items-center justify-end gap-3">
                  <button type="button" class="text-primary-600 hover:text-primary-700 dark:text-primary-400 text-xs font-bold" @click="emit('openErrorDetail', log.id)">
                    {{ t('admin.ops.errorLog.details') }}
                  </button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- Pagination -->
      <div class="bg-gray-50/50 dark:bg-dark-800/50">
        <Pagination
          v-if="total > 0"
          :total="total"
          :page="page"
          :page-size="pageSize"
          @update:page="emit('update:page', $event)"
          @update:pageSize="emit('update:pageSize', $event)"
        />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Pagination from '@/components/common/Pagination.vue'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import type { OpsErrorLog } from '@/api/admin/ops'
import { getSeverityClass, formatDateTime } from '../utils/opsFormatters'
import {
  ERROR_CATEGORY_BADGE_CLASSES,
  FILTERABLE_ERROR_CATEGORIES,
  mapErrorCategory
} from '@/utils/errorCategory'

const { t } = useI18n()

export type ErrorStatusFilter = number | 'other' | null
export type ErrorViewFilter = 'errors' | 'excluded' | 'all'
export type ErrorSortFilter = 'created_at_desc' | 'created_at_asc' | 'status_code_desc' | 'status_code_asc' | 'severity_desc' | 'severity_asc' | 'category_desc' | 'category_asc'

export interface OpsErrorTableFilters {
  q: string
  statusCode: ErrorStatusFilter
  phase: string
  category: string
  errorOwner: string
  sort: ErrorSortFilter
  viewMode: ErrorViewFilter
}

function isUpstreamRow(log: OpsErrorLog): boolean {
  const phase = String(log.phase || '').toLowerCase()
  const owner = String(log.error_owner || '').toLowerCase()
  return phase === 'upstream' && owner === 'provider'
}

function formatEndpointTooltip(log: OpsErrorLog): string {
  const parts: string[] = []
  if (log.inbound_endpoint) parts.push(`Inbound: ${log.inbound_endpoint}`)
  if (log.upstream_endpoint) parts.push(`Upstream: ${log.upstream_endpoint}`)
  return parts.join('\n') || ''
}

function hasModelMapping(log: OpsErrorLog): boolean {
  const requested = String(log.requested_model || '').trim()
  const upstream = String(log.upstream_model || '').trim()
  return !!requested && !!upstream && requested !== upstream
}

function modelMappingTooltip(log: OpsErrorLog): string {
  const requested = String(log.requested_model || '').trim()
  const upstream = String(log.upstream_model || '').trim()
  if (!requested && !upstream) return ''
  if (requested && upstream) return `${requested} → ${upstream}`
  return upstream || requested
}

function displayModel(log: OpsErrorLog): string {
  const upstream = String(log.upstream_model || '').trim()
  if (upstream) return upstream
  const requested = String(log.requested_model || '').trim()
  if (requested) return requested
  return String(log.model || '').trim()
}

function formatRequestType(type: number | null | undefined): string {
  switch (type) {
    case 1: return t('admin.ops.errorLog.requestTypeSync')
    case 2: return t('admin.ops.errorLog.requestTypeStream')
    case 3: return t('admin.ops.errorLog.requestTypeWs')
    default: return ''
  }
}

function getTypeBadge(log: OpsErrorLog): { label: string; className: string } {
  const phase = String(log.phase || '').toLowerCase()
  const owner = String(log.error_owner || '').toLowerCase()

  if (isUpstreamRow(log)) {
    return { label: t('admin.ops.errorLog.typeUpstream'), className: 'bg-red-50 text-red-700 ring-red-600/20 dark:bg-red-900/30 dark:text-red-400 dark:ring-red-500/30' }
  }
  if (phase === 'request' && owner === 'client') {
    return { label: t('admin.ops.errorLog.typeRequest'), className: 'bg-amber-50 text-amber-700 ring-amber-600/20 dark:bg-amber-900/30 dark:text-amber-400 dark:ring-amber-500/30' }
  }
  if (phase === 'auth' && owner === 'client') {
    return { label: t('admin.ops.errorLog.typeAuth'), className: 'bg-blue-50 text-blue-700 ring-blue-600/20 dark:bg-blue-900/30 dark:text-blue-400 dark:ring-blue-500/30' }
  }
  if (phase === 'routing' && owner === 'platform') {
    return { label: t('admin.ops.errorLog.typeRouting'), className: 'bg-purple-50 text-purple-700 ring-purple-600/20 dark:bg-purple-900/30 dark:text-purple-400 dark:ring-purple-500/30' }
  }
  if (phase === 'internal' && owner === 'platform') {
    return { label: t('admin.ops.errorLog.typeInternal'), className: 'bg-gray-100 text-gray-800 ring-gray-600/20 dark:bg-dark-700 dark:text-gray-200 dark:ring-dark-500/40' }
  }

    const fallback = phase || owner || t('common.unknown')
    return { label: fallback, className: 'bg-gray-50 text-gray-700 ring-gray-600/10 dark:bg-dark-900 dark:text-gray-300 dark:ring-dark-700' }
}

interface Props {
  rows: OpsErrorLog[]
  total: number
  loading: boolean
  page: number
  pageSize: number
  filters: OpsErrorTableFilters
  showToolbar?: boolean
}

interface Emits {
  (e: 'openErrorDetail', id: number): void
  (e: 'update:page', value: number): void
  (e: 'update:pageSize', value: number): void
  (e: 'update:filters', value: OpsErrorTableFilters): void
  (e: 'resetFilters'): void
}

const props = withDefaults(defineProps<Props>(), {
  showToolbar: true
})
const emit = defineEmits<Emits>()

const statusCodeOptions = computed(() => {
  const codes = [400, 401, 403, 404, 409, 422, 429, 500, 502, 503, 504, 529]
  return [
    { value: null, label: t('admin.ops.errorLog.allStatuses') },
    ...codes.map((code) => ({ value: code, label: String(code) })),
    { value: 'other', label: t('admin.ops.errorDetails.statusCodeOther') }
  ]
})

const phaseOptions = computed(() => [
  { value: '', label: t('admin.ops.errorLog.allPhases') },
  { value: 'request', label: t('admin.ops.errorDetails.phase.request') },
  { value: 'auth', label: t('admin.ops.errorDetails.phase.auth') },
  { value: 'routing', label: t('admin.ops.errorDetails.phase.routing') },
  { value: 'upstream', label: t('admin.ops.errorDetails.phase.upstream') },
  { value: 'network', label: t('admin.ops.errorDetails.phase.network') },
  { value: 'internal', label: t('admin.ops.errorDetails.phase.internal') }
])

const categoryOptions = computed(() => [
  { value: '', label: t('usage.errors.allCategories') },
  ...FILTERABLE_ERROR_CATEGORIES.map((category) => ({
    value: category,
    label: t(`usage.errors.categories.${category}`)
  }))
])

const ownerOptions = computed(() => [
  { value: '', label: t('admin.ops.errorLog.allOwners') },
  { value: 'provider', label: t('admin.ops.errorDetails.owner.provider') },
  { value: 'client', label: t('admin.ops.errorDetails.owner.client') },
  { value: 'platform', label: t('admin.ops.errorDetails.owner.platform') }
])

const viewModeOptions = computed(() => [
  { value: 'errors', label: t('admin.ops.errorDetails.viewErrors') },
  { value: 'excluded', label: t('admin.ops.errorDetails.viewExcluded') },
  { value: 'all', label: t('common.all') }
])

const sortOptions = computed(() => [
  { value: 'created_at_desc', label: t('admin.ops.errorLog.sortNewest') },
  { value: 'created_at_asc', label: t('admin.ops.errorLog.sortOldest') },
  { value: 'status_code_desc', label: t('admin.ops.errorLog.sortStatusHigh') },
  { value: 'status_code_asc', label: t('admin.ops.errorLog.sortStatusLow') },
  { value: 'severity_desc', label: t('admin.ops.errorLog.sortSeverityHigh') },
  { value: 'severity_asc', label: t('admin.ops.errorLog.sortSeverityLow') },
  { value: 'category_asc', label: t('admin.ops.errorLog.sortCategoryAsc') },
  { value: 'category_desc', label: t('admin.ops.errorLog.sortCategoryDesc') }
])

const allColumns = computed(() => [
  { key: 'time', label: t('admin.ops.errorLog.time') },
  { key: 'type', label: t('admin.ops.errorLog.type') },
  { key: 'category', label: t('usage.errors.category') },
  { key: 'endpoint', label: t('admin.ops.errorLog.endpoint') },
  { key: 'platform', label: t('admin.ops.errorLog.platform') },
  { key: 'model', label: t('admin.ops.errorLog.model') },
  { key: 'group', label: t('admin.ops.errorLog.group') },
  { key: 'user', label: t('admin.ops.errorLog.user') },
  { key: 'apiKey', label: t('admin.ops.errorLog.apiKey') },
  { key: 'account', label: t('admin.ops.errorLog.account') },
  { key: 'status', label: t('admin.ops.errorLog.status') },
  { key: 'message', label: t('admin.ops.errorLog.message') },
  { key: 'actions', label: t('admin.ops.errorLog.action') }
])

const ALWAYS_VISIBLE_COLUMNS = ['time', 'actions']
const HIDDEN_COLUMNS_KEY = 'ops-error-log-hidden-columns'
const hiddenColumns = reactive<Set<string>>(new Set())
const showColumnDropdown = ref(false)
const columnDropdownRef = ref<HTMLElement | null>(null)

const toggleableColumns = computed(() =>
  allColumns.value.filter((col) => !ALWAYS_VISIBLE_COLUMNS.includes(col.key))
)

const visibleColumns = computed(() =>
  allColumns.value.filter((col) => ALWAYS_VISIBLE_COLUMNS.includes(col.key) || !hiddenColumns.has(col.key))
)

const isColumnVisible = (key: string) => ALWAYS_VISIBLE_COLUMNS.includes(key) || !hiddenColumns.has(key)

function persistHiddenColumns() {
  try {
    localStorage.setItem(HIDDEN_COLUMNS_KEY, JSON.stringify([...hiddenColumns]))
  } catch (err) {
    console.error('[OpsErrorLogTable] Failed to save hidden columns', err)
  }
}

function toggleColumn(key: string) {
  if (ALWAYS_VISIBLE_COLUMNS.includes(key)) return
  if (hiddenColumns.has(key)) hiddenColumns.delete(key)
  else hiddenColumns.add(key)
  persistHiddenColumns()
}

function loadHiddenColumns() {
  try {
    const raw = localStorage.getItem(HIDDEN_COLUMNS_KEY)
    if (!raw) return
    const saved = JSON.parse(raw)
    if (!Array.isArray(saved)) return
    saved.forEach((key) => {
      if (typeof key === 'string' && !ALWAYS_VISIBLE_COLUMNS.includes(key)) hiddenColumns.add(key)
    })
  } catch (err) {
    console.error('[OpsErrorLogTable] Failed to load hidden columns', err)
  }
}

function updateFilter<K extends keyof OpsErrorTableFilters>(key: K, value: OpsErrorTableFilters[K]) {
  emit('update:filters', { ...props.filters, [key]: value })
}

function handleColumnClickOutside(event: MouseEvent) {
  if (columnDropdownRef.value && !columnDropdownRef.value.contains(event.target as HTMLElement)) {
    showColumnDropdown.value = false
  }
}

onMounted(() => {
  loadHiddenColumns()
  document.addEventListener('click', handleColumnClickOutside)
})

onUnmounted(() => {
  document.removeEventListener('click', handleColumnClickOutside)
})

function getStatusClass(code: number): string {
  if (code >= 500) return 'bg-red-50 text-red-700 ring-red-600/20 dark:bg-red-900/30 dark:text-red-400 dark:ring-red-500/30'
  if (code === 429) return 'bg-purple-50 text-purple-700 ring-purple-600/20 dark:bg-purple-900/30 dark:text-purple-400 dark:ring-purple-500/30'
  if (code >= 400) return 'bg-amber-50 text-amber-700 ring-amber-600/20 dark:bg-amber-900/30 dark:text-amber-400 dark:ring-amber-500/30'
  return 'bg-gray-50 text-gray-700 ring-gray-600/20 dark:bg-gray-900/30 dark:text-gray-400 dark:ring-gray-500/30'
}

function formatSmartMessage(msg: string): string {
  if (!msg) return ''

  if (msg.startsWith('{') || msg.startsWith('[')) {
    try {
      const obj = JSON.parse(msg)
      if (obj?.error?.message) return String(obj.error.message)
      if (obj?.message) return String(obj.message)
      if (obj?.detail) return String(obj.detail)
      if (typeof obj === 'object') return JSON.stringify(obj).substring(0, 150)
    } catch {
      // ignore parse error
    }
  }

  if (msg.includes('context deadline exceeded')) return t('admin.ops.errorLog.commonErrors.contextDeadlineExceeded')
  if (msg.includes('connection refused')) return t('admin.ops.errorLog.commonErrors.connectionRefused')
  if (msg.toLowerCase().includes('rate limit')) return t('admin.ops.errorLog.commonErrors.rateLimit')

  return msg.length > 200 ? msg.substring(0, 200) + '...' : msg

}
</script>

<style>
.compact-select .select-trigger {
  @apply rounded-lg px-3 py-2 text-xs;
}
</style>
