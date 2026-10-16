<template>
  <AppLayout>
    <div class="space-y-5">
      <section class="card p-5 sm:p-6">
        <div class="flex flex-wrap items-start justify-between gap-4">
          <div>
            <h1 class="text-xl font-bold text-gray-900 dark:text-white">{{ t('admin.channelMonitor.title') }}</h1>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
              {{ monitorMode === 'v2' ? localText('根据真实网关请求被动统计，不额外消耗上游额度。', 'Passive statistics from real gateway traffic without extra upstream usage.') : t('admin.channelMonitor.description') }}
            </p>
          </div>
          <div class="tabs inline-flex" role="tablist">
            <button type="button" class="tab" :class="activeView === 'metrics' ? 'tab-active' : ''" @click="activeView = 'metrics'">{{ localText('真实流量 V2', 'Real traffic V2') }}</button>
            <button type="button" class="tab" :class="activeView === 'legacy' ? 'tab-active' : ''" @click="activeView = 'legacy'">{{ monitorMode === 'v1' ? localText('主动探测 V1', 'Active probes V1') : localText('V1 历史配置', 'V1 history') }}</button>
          </div>
        </div>
      </section>

      <ChannelMonitorV2Panel v-if="activeView === 'metrics' && monitorMode === 'v2'" />
      <div v-else-if="activeView === 'metrics'" class="card p-8 text-center">
        <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ localText('当前使用 V1 主动探测', 'V1 active probes are currently enabled') }}</h2>
        <p class="mt-2 text-sm text-gray-500 dark:text-gray-400">{{ localText('请在系统设置中切换到 V2 被动统计后查看真实流量指标。', 'Switch to V2 passive statistics in system settings to view real traffic metrics.') }}</p>
      </div>

    <TablePageLayout v-else>
      <template #filters>
        <MonitorFiltersBar
          v-model:search="searchQuery"
          v-model:provider="providerFilter"
          v-model:enabled="enabledFilter"
          :loading="loading"
          @reload="reload"
          @create="openCreateDialog"
          @manage-templates="showTemplateManager = true"
          @search-input="handleSearch"
        />
      </template>

      <template #table>
        <DataTable :columns="columns" :data="monitors" :loading="loading">
          <template #cell-name="{ row, value }">
            <div class="flex items-center gap-1.5">
              <span class="font-medium text-gray-900 dark:text-white">{{ value }}</span>
              <HelpTooltip v-if="row.api_key_decrypt_failed" :content="t('admin.channelMonitor.apiKeyDecryptFailed')">
                <Icon name="exclamationTriangle" size="sm" class="text-red-500" />
              </HelpTooltip>
            </div>
          </template>

          <template #cell-provider="{ row }">
            <span class="inline-flex items-center rounded-md px-2 py-0.5 text-xs font-medium" :class="providerBadgeClass(row.provider)">
              {{ providerLabel(row.provider) }}
            </span>
          </template>

          <template #cell-primary_model="{ row }">
            <MonitorPrimaryModelCell :row="row" />
          </template>

          <template #cell-availability_7d="{ row }">
            <span class="text-sm text-gray-900 dark:text-gray-100">{{ formatAvailability(row) }}</span>
          </template>

          <template #cell-latency="{ row }">
            <span class="text-sm text-gray-900 dark:text-gray-100">{{ formatLatency(row.primary_latency_ms) }}</span>
          </template>

          <template #cell-enabled="{ row }">
            <Toggle :modelValue="row.enabled" @update:modelValue="toggleEnabled(row)" />
          </template>

          <template #cell-actions="{ row }">
            <MonitorActionsCell
              :row="row"
              :running="runningId === row.id"
              :duplicating="duplicatingId === row.id"
              @run="handleRunNow"
              @duplicate="handleDuplicate"
              @edit="openEditDialog"
              @delete="handleDelete"
            />
          </template>

          <template #empty>
            <EmptyState
              :title="t('admin.channelMonitor.noMonitorsYet')"
              :description="t('admin.channelMonitor.createFirstMonitor')"
              :action-text="t('admin.channelMonitor.createButton')"
              @action="openCreateDialog"
            />
          </template>
        </DataTable>
      </template>

      <template #pagination>
        <Pagination
          v-if="pagination.total > 0"
          :page="pagination.page"
          :total="pagination.total"
          :page-size="pagination.page_size"
          @update:page="onPageChange"
          @update:pageSize="onPageSizeChange"
        />
      </template>
    </TablePageLayout>
    </div>

    <MonitorFormDialog
      :show="showDialog"
      :monitor="editing"
      @close="closeDialog"
      @saved="reload"
    />

    <MonitorTemplateManagerDialog
      :show="showTemplateManager"
      @close="showTemplateManager = false"
      @updated="reload"
    />

    <MonitorRunResultDialog
      :show="showRunResult"
      :results="runResults"
      @close="showRunResult = false"
    />

    <ConfirmDialog
      :show="showDuplicateDialog"
      :title="t('admin.channelMonitor.duplicateConfirmTitle')"
      :message="duplicateConfirmMessage"
      :confirm-text="t('admin.channelMonitor.duplicate')"
      :cancel-text="t('common.cancel')"
      @confirm="confirmDuplicate"
      @cancel="closeDuplicateDialog"
    />

    <ConfirmDialog
      :show="showDeleteDialog"
      :title="t('common.delete')"
      :message="deleteConfirmMessage"
      :confirm-text="t('common.delete')"
      :cancel-text="t('common.cancel')"
      :danger="true"
      @confirm="confirmDelete"
      @cancel="showDeleteDialog = false"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import { adminAPI } from '@/api/admin'
import type {
  ChannelMonitor,
  CheckResult,
  ListParams,
  Provider,
} from '@/api/admin/channelMonitor'
import type { Column } from '@/components/common/types'
import AppLayout from '@/components/layout/GatewayLiteLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import Icon from '@/components/icons/Icon.vue'
import Toggle from '@/components/common/Toggle.vue'
import MonitorFiltersBar from '@/components/admin/monitor/MonitorFiltersBar.vue'
import MonitorFormDialog from '@/components/admin/monitor/MonitorFormDialog.vue'
import MonitorTemplateManagerDialog from '@/components/admin/monitor/MonitorTemplateManagerDialog.vue'
import MonitorRunResultDialog from '@/components/admin/monitor/MonitorRunResultDialog.vue'
import MonitorPrimaryModelCell from '@/components/admin/monitor/MonitorPrimaryModelCell.vue'
import MonitorActionsCell from '@/components/admin/monitor/MonitorActionsCell.vue'
import ChannelMonitorV2Panel from '@/components/admin/monitor/ChannelMonitorV2Panel.vue'
import { getPersistedPageSize } from '@/composables/usePersistedPageSize'
import { useChannelMonitorFormat } from '@/composables/useChannelMonitorFormat'

const { t, locale } = useI18n()
const appStore = useAppStore()
const localText = (zh: string, en: string) => locale.value.toLowerCase().startsWith('zh') ? zh : en
const monitorMode = computed(() => appStore.cachedPublicSettings?.channel_monitor_mode === 'v2' ? 'v2' : 'v1')
const activeView = ref<'metrics' | 'legacy'>(monitorMode.value === 'v2' ? 'metrics' : 'legacy')
const {
  providerLabel,
  providerBadgeClass,
  formatLatency,
  formatAvailability,
} = useChannelMonitorFormat()

const monitors = ref<ChannelMonitor[]>([])
const loading = ref(false)
const runningId = ref<number | null>(null)
const duplicatingId = ref<number | null>(null)
const searchQuery = ref('')
const providerFilter = ref<Provider | ''>('')
const enabledFilter = ref<'' | 'true' | 'false'>('')
const pagination = reactive({ page: 1, page_size: getPersistedPageSize(), total: 0 })

const showDialog = ref(false)
const showTemplateManager = ref(false)
const editing = ref<ChannelMonitor | null>(null)
const showDeleteDialog = ref(false)
const deleting = ref<ChannelMonitor | null>(null)
const showDuplicateDialog = ref(false)
const duplicating = ref<ChannelMonitor | null>(null)
const showRunResult = ref(false)
const runResults = ref<CheckResult[]>([])

let abortController: AbortController | null = null
let searchTimeout: ReturnType<typeof setTimeout> | null = null

const columns = computed<Column[]>(() => [
  { key: 'name', label: t('admin.channelMonitor.columns.name'), sortable: false },
  { key: 'provider', label: t('admin.channelMonitor.columns.provider'), sortable: false },
  { key: 'primary_model', label: t('admin.channelMonitor.columns.primaryModel'), sortable: false },
  { key: 'availability_7d', label: t('admin.channelMonitor.columns.availability7d'), sortable: false },
  { key: 'latency', label: t('admin.channelMonitor.columns.latency'), sortable: false },
  { key: 'enabled', label: t('admin.channelMonitor.columns.enabled'), sortable: false },
  { key: 'actions', label: t('admin.channelMonitor.columns.actions'), sortable: false },
])

const deleteConfirmMessage = computed(() => {
  const name = deleting.value?.name || ''
  return t('admin.channelMonitor.deleteConfirm', { name })
})

const duplicateConfirmMessage = computed(() => {
  const source = duplicating.value
  if (!source) return ''
  return t('admin.channelMonitor.duplicateConfirm', {
    name: source.name,
    provider: providerLabel(source.provider),
    model: source.primary_model,
    group: source.group_name || t('admin.channelMonitor.noGroup')
  })
})

async function reload() {
  if (abortController) abortController.abort()
  const ctrl = new AbortController()
  abortController = ctrl
  loading.value = true
  try {
    const params: ListParams = {
      page: pagination.page,
      page_size: pagination.page_size,
    }
    if (providerFilter.value) params.provider = providerFilter.value
    if (enabledFilter.value === 'true') params.enabled = true
    if (enabledFilter.value === 'false') params.enabled = false
    if (searchQuery.value.trim()) params.search = searchQuery.value.trim()

    const res = await adminAPI.channelMonitor.list(params, { signal: ctrl.signal })
    if (ctrl.signal.aborted || abortController !== ctrl) return
    monitors.value = res.items || []
    pagination.total = res.total
  } catch (err: unknown) {
    const e = err as { name?: string; code?: string }
    if (e?.name === 'AbortError' || e?.code === 'ERR_CANCELED') return
    appStore.showError(extractApiErrorMessage(err, t('admin.channelMonitor.loadError')))
  } finally {
    if (abortController === ctrl) {
      loading.value = false
      abortController = null
    }
  }
}

function handleSearch() {
  if (searchTimeout) clearTimeout(searchTimeout)
  searchTimeout = setTimeout(() => {
    pagination.page = 1
    reload()
  }, 300)
}

function onPageChange(page: number) {
  pagination.page = page
  reload()
}

function onPageSizeChange(size: number) {
  pagination.page_size = size
  pagination.page = 1
  reload()
}

function openCreateDialog() {
  editing.value = null
  showDialog.value = true
}

function openEditDialog(row: ChannelMonitor) {
  editing.value = row
  showDialog.value = true
}

function closeDialog() {
  showDialog.value = false
  editing.value = null
}

async function toggleEnabled(row: ChannelMonitor) {
  const next = !row.enabled
  try {
    await adminAPI.channelMonitor.update(row.id, { enabled: next })
    row.enabled = next
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t('common.error')))
  }
}

async function handleRunNow(row: ChannelMonitor) {
  if (runningId.value != null) return
  runningId.value = row.id
  try {
    const res = await adminAPI.channelMonitor.runNow(row.id)
    runResults.value = res.results || []
    showRunResult.value = true
    appStore.showSuccess(t('admin.channelMonitor.runSuccess'))
    // Refresh row to get latest status from backend
    void reload()
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t('admin.channelMonitor.runFailed')))
  } finally {
    runningId.value = null
  }
}

function handleDuplicate(row: ChannelMonitor) {
  if (duplicatingId.value != null) return
  duplicating.value = row
  showDuplicateDialog.value = true
}

function closeDuplicateDialog() {
  showDuplicateDialog.value = false
  duplicating.value = null
}

async function confirmDuplicate() {
  const source = duplicating.value
  if (!source || duplicatingId.value != null) return
  duplicatingId.value = source.id
  try {
    const duplicate = await adminAPI.channelMonitor.duplicate(source.id)
    closeDuplicateDialog()
    await reload()
    openEditDialog(duplicate)
    appStore.showSuccess(t('admin.channelMonitor.duplicateSuccess', { name: duplicate.name }))
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t('admin.channelMonitor.duplicateFailed')))
  } finally {
    duplicatingId.value = null
  }
}

function handleDelete(row: ChannelMonitor) {
  deleting.value = row
  showDeleteDialog.value = true
}

async function confirmDelete() {
  if (!deleting.value) return
  try {
    await adminAPI.channelMonitor.del(deleting.value.id)
    appStore.showSuccess(t('admin.channelMonitor.deleteSuccess'))
    showDeleteDialog.value = false
    deleting.value = null
    reload()
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t('common.error')))
  }
}

onMounted(reload)
onUnmounted(() => {
  if (searchTimeout) clearTimeout(searchTimeout)
  abortController?.abort()
})
</script>
