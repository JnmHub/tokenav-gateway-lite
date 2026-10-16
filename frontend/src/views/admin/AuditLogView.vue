<template>
  <AppLayout>
    <div class="mx-auto max-w-[1600px] space-y-5">
      <section class="card overflow-hidden">
        <div class="flex flex-col gap-4 border-b border-gray-100 px-5 py-4 dark:border-dark-700 lg:flex-row lg:items-center lg:justify-between">
          <div>
            <h1 class="text-xl font-semibold text-gray-900 dark:text-white">
              {{ localText('操作日志', 'Operation audit logs') }}
            </h1>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
              {{ localText('记录后台敏感读取和管理变更；秘密值、完整请求体与完整浏览器标识不会写入日志。', 'Tracks sensitive reads and administrative changes without storing secrets, full request bodies, or full user agents.') }}
            </p>
          </div>
          <div class="flex flex-wrap gap-2">
            <button type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="loadPage">
              {{ loading ? localText('刷新中', 'Refreshing') : localText('刷新', 'Refresh') }}
            </button>
            <button type="button" class="btn btn-sm bg-red-600 text-white hover:bg-red-700" @click="showClearConfirm = true">
              {{ localText('清空日志', 'Clear logs') }}
            </button>
          </div>
        </div>

        <form class="grid gap-3 border-b border-gray-100 bg-gray-50/70 px-5 py-4 dark:border-dark-700 dark:bg-dark-800/40 md:grid-cols-2 xl:grid-cols-4" @submit.prevent="applyFilters">
          <label class="field">
            <span>{{ localText('操作者邮箱', 'Actor email') }}</span>
            <input v-model.trim="filters.actor_email" class="input" :placeholder="localText('完整邮箱', 'Exact email')" />
          </label>
          <label class="field">
            <span>{{ localText('操作类型', 'Action') }}</span>
            <input v-model.trim="filters.action" class="input font-mono" placeholder="admin.accounts.export" />
          </label>
          <label class="field">
            <span>{{ localText('资源类型', 'Resource type') }}</span>
            <input v-model.trim="filters.resource_type" class="input font-mono" placeholder="account" />
          </label>
          <label class="field">
            <span>{{ localText('资源编号', 'Resource ID') }}</span>
            <input v-model.trim="filters.resource_id" class="input font-mono" />
          </label>
          <label class="field">
            <span>{{ localText('结果', 'Result') }}</span>
            <select v-model="filters.result" class="input">
              <option value="">{{ localText('全部', 'All') }}</option>
              <option value="success">{{ localText('成功', 'Success') }}</option>
              <option value="failure">{{ localText('失败', 'Failure') }}</option>
              <option value="denied">{{ localText('拒绝', 'Denied') }}</option>
              <option value="pending">{{ localText('待确认', 'Pending') }}</option>
            </select>
          </label>
          <label class="field">
            <span>{{ localText('开始时间', 'Start time') }}</span>
            <input v-model="filters.start_time" class="input" type="datetime-local" />
          </label>
          <label class="field">
            <span>{{ localText('结束时间', 'End time') }}</span>
            <input v-model="filters.end_time" class="input" type="datetime-local" />
          </label>
          <div class="flex items-end gap-2">
            <button type="submit" class="btn btn-primary btn-sm flex-1" :disabled="loading">
              {{ localText('筛选', 'Filter') }}
            </button>
            <button type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="resetFilters">
              {{ localText('重置', 'Reset') }}
            </button>
          </div>
        </form>

        <div v-if="loadError" class="m-5 rounded-lg border border-red-200 bg-red-50 p-4 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">
          {{ loadError }}
        </div>

        <div class="w-full overflow-x-auto overscroll-x-contain">
          <table class="min-w-[1280px] w-full text-left text-sm">
            <thead class="bg-gray-50 text-xs uppercase tracking-wide text-gray-500 dark:bg-dark-800 dark:text-gray-400">
              <tr>
                <th class="px-4 py-3">ID</th>
                <th class="px-4 py-3">{{ localText('时间', 'Time') }}</th>
                <th class="px-4 py-3">{{ localText('操作者', 'Actor') }}</th>
                <th class="px-4 py-3">{{ localText('操作', 'Action') }}</th>
                <th class="px-4 py-3">{{ localText('资源', 'Resource') }}</th>
                <th class="px-4 py-3">{{ localText('结果', 'Result') }}</th>
                <th class="px-4 py-3">HTTP</th>
                <th class="px-4 py-3">{{ localText('来源 IP', 'Client IP') }}</th>
                <th class="px-4 py-3">request_id</th>
                <th class="px-4 py-3 text-right">{{ localText('详情', 'Details') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 bg-white dark:divide-dark-700 dark:bg-dark-900">
              <tr v-if="loading">
                <td colspan="10" class="px-4 py-14 text-center text-gray-500 dark:text-gray-400">
                  {{ localText('正在加载操作日志', 'Loading audit logs') }}
                </td>
              </tr>
              <tr v-else-if="items.length === 0">
                <td colspan="10" class="px-4 py-14 text-center text-gray-500 dark:text-gray-400">
                  {{ localText('当前筛选条件下没有操作日志', 'No audit logs match the current filters') }}
                </td>
              </tr>
              <tr v-for="item in items" v-else :key="item.id" class="hover:bg-gray-50 dark:hover:bg-dark-800/70">
                <td class="whitespace-nowrap px-4 py-3 font-mono text-gray-500">{{ item.id }}</td>
                <td class="whitespace-nowrap px-4 py-3 text-gray-700 dark:text-gray-200">{{ formatTimestamp(item.created_at) }}</td>
                <td class="px-4 py-3">
                  <div class="max-w-[220px] truncate font-medium text-gray-900 dark:text-white">{{ item.actor_email || '-' }}</div>
                  <div class="text-xs text-gray-500">{{ item.actor_role || '-' }} · {{ item.auth_method || '-' }}</div>
                </td>
                <td class="px-4 py-3 font-mono text-xs text-gray-700 dark:text-gray-200">{{ item.action }}</td>
                <td class="px-4 py-3 text-gray-700 dark:text-gray-200">
                  <span class="font-mono text-xs">{{ item.resource_type || '-' }}</span>
                  <span v-if="item.resource_id" class="ml-1 text-gray-500">#{{ item.resource_id }}</span>
                </td>
                <td class="px-4 py-3">
                  <span :class="resultClass(item.result)" class="inline-flex rounded-full px-2 py-1 text-xs font-semibold">
                    {{ resultLabel(item.result) }}
                  </span>
                </td>
                <td class="whitespace-nowrap px-4 py-3 font-mono text-xs">{{ item.status_code || '-' }} · {{ item.latency_ms }}ms</td>
                <td class="whitespace-nowrap px-4 py-3 font-mono text-xs">{{ item.client_ip || '-' }}</td>
                <td class="max-w-[190px] truncate px-4 py-3 font-mono text-xs" :title="item.request_id">{{ item.request_id || '-' }}</td>
                <td class="px-4 py-3 text-right">
                  <button type="button" class="btn btn-secondary btn-sm" @click="openDetail(item.id)">
                    {{ localText('查看', 'View') }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <div class="flex flex-col gap-3 border-t border-gray-100 px-5 py-4 dark:border-dark-700 sm:flex-row sm:items-center sm:justify-between">
          <span class="text-sm text-gray-500 dark:text-gray-400">
            {{ localText(`本页 ${items.length} 条，按 ID 倒序稳定分页`, `${items.length} records; stable descending ID pagination`) }}
          </span>
          <div class="flex gap-2">
            <button type="button" class="btn btn-secondary btn-sm" :disabled="loading || cursorHistory.length === 0" @click="previousPage">
              {{ localText('上一页', 'Previous') }}
            </button>
            <button type="button" class="btn btn-secondary btn-sm" :disabled="loading || !nextCursor" @click="nextPage">
              {{ localText('下一页', 'Next') }}
            </button>
          </div>
        </div>
      </section>
    </div>

    <BaseDialog :show="showDetail" :title="localText('操作日志详情', 'Audit log details')" width="wide" @close="showDetail = false">
      <div v-if="detailLoading" class="py-10 text-center text-gray-500">{{ localText('正在加载', 'Loading') }}</div>
      <div v-else-if="selected" class="space-y-4">
        <dl class="grid gap-3 text-sm sm:grid-cols-2">
          <div v-for="row in detailRows" :key="row.label" class="rounded-lg border border-gray-200 p-3 dark:border-dark-600">
            <dt class="text-xs uppercase tracking-wide text-gray-500">{{ row.label }}</dt>
            <dd class="mt-1 break-all font-mono text-gray-900 dark:text-gray-100">{{ row.value }}</dd>
          </div>
        </dl>
        <div>
          <h3 class="mb-2 text-sm font-semibold text-gray-900 dark:text-white">{{ localText('脱敏变更摘要', 'Redacted change summary') }}</h3>
          <pre class="max-h-80 overflow-auto rounded-lg bg-gray-950 p-4 text-xs text-gray-100">{{ prettyJSON(selected.summary) }}</pre>
        </div>
      </div>
    </BaseDialog>

    <BaseDialog :show="showClearConfirm" :title="localText('清空操作日志', 'Clear operation audit logs')" width="narrow" @close="showClearConfirm = false">
      <p class="text-sm leading-6 text-gray-600 dark:text-gray-300">
        {{ localText('此操作会删除当前全部操作日志，并在同一数据库事务中写入新的清理尾记录。继续时还需要输入 TOTP 验证码。', 'This deletes all current audit logs and writes a new clear trace in the same database transaction. A TOTP code is also required.') }}
      </p>
      <template #footer>
        <button type="button" class="btn btn-secondary" :disabled="clearing" @click="showClearConfirm = false">{{ localText('取消', 'Cancel') }}</button>
        <button type="button" class="btn bg-red-600 text-white hover:bg-red-700" :disabled="clearing" @click="clearLogs">
          {{ clearing ? localText('清理中', 'Clearing') : localText('继续清空', 'Continue') }}
        </button>
      </template>
    </BaseDialog>

    <TotpStepUpDialog :controller="stepUp" />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/GatewayLiteLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import TotpStepUpDialog from '@/components/auth/TotpStepUpDialog.vue'
import { auditAPI, type OperationAuditLog, type OperationAuditListParams } from '@/api/admin/audit'
import { StepUpScope, isStepUpCancelled, useStepUp } from '@/composables/useStepUp'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import { formatDateTime as formatDateTimeValue } from '@/utils/format'
import { parseDefaultTimeZoneDateTimeInput } from '@/utils/timezone'

const { locale } = useI18n()
const appStore = useAppStore()
const stepUp = useStepUp()
const items = ref<OperationAuditLog[]>([])
const loading = ref(false)
const loadError = ref('')
const nextCursor = ref<number | undefined>()
const currentCursor = ref<number | undefined>()
const cursorHistory = ref<Array<number | undefined>>([])
const showDetail = ref(false)
const detailLoading = ref(false)
const selected = ref<OperationAuditLog | null>(null)
const showClearConfirm = ref(false)
const clearing = ref(false)

const filters = reactive({
  actor_email: '',
  action: '',
  resource_type: '',
  resource_id: '',
  result: '',
  start_time: '',
  end_time: ''
})

function localText(zh: string, en: string): string {
  return locale.value.toLowerCase().startsWith('zh') ? zh : en
}

function toRFC3339(value: string): string | undefined {
  if (!value) return undefined
  return parseDefaultTimeZoneDateTimeInput(value)?.toISOString()
}

function buildParams(): OperationAuditListParams {
  return {
    before_id: currentCursor.value,
    limit: 50,
    actor_email: filters.actor_email || undefined,
    action: filters.action || undefined,
    resource_type: filters.resource_type || undefined,
    resource_id: filters.resource_id || undefined,
    result: filters.result || undefined,
    start_time: toRFC3339(filters.start_time),
    end_time: toRFC3339(filters.end_time)
  }
}

async function loadPage() {
  loading.value = true
  loadError.value = ''
  try {
    const result = await auditAPI.list(buildParams())
    items.value = result.items || []
    nextCursor.value = result.next_cursor
  } catch (error) {
    loadError.value = extractApiErrorMessage(error, localText('加载操作日志失败', 'Failed to load audit logs'))
  } finally {
    loading.value = false
  }
}

function applyFilters() {
  currentCursor.value = undefined
  cursorHistory.value = []
  void loadPage()
}

function resetFilters() {
  Object.assign(filters, {
    actor_email: '', action: '', resource_type: '', resource_id: '', result: '', start_time: '', end_time: ''
  })
  applyFilters()
}

function nextPage() {
  if (!nextCursor.value) return
  cursorHistory.value.push(currentCursor.value)
  currentCursor.value = nextCursor.value
  void loadPage()
}

function previousPage() {
  if (cursorHistory.value.length === 0) return
  currentCursor.value = cursorHistory.value.pop()
  void loadPage()
}

async function openDetail(id: number) {
  showDetail.value = true
  detailLoading.value = true
  selected.value = null
  try {
    selected.value = await auditAPI.getById(id)
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, localText('加载日志详情失败', 'Failed to load audit detail')))
    showDetail.value = false
  } finally {
    detailLoading.value = false
  }
}

async function clearLogs() {
  if (clearing.value) return
  clearing.value = true
  try {
    const result = await stepUp.run(StepUpScope.AuditClear, (token) => auditAPI.clear(token))
    showClearConfirm.value = false
    appStore.showSuccess(localText(`已清理 ${result.deleted} 条日志`, `Cleared ${result.deleted} audit logs`))
    currentCursor.value = undefined
    cursorHistory.value = []
    await loadPage()
  } catch (error) {
    if (!isStepUpCancelled(error)) {
      appStore.showError(extractApiErrorMessage(error, localText('清空操作日志失败', 'Failed to clear audit logs')))
    }
  } finally {
    clearing.value = false
  }
}

function formatTimestamp(value: string): string {
  return formatDateTimeValue(value) || value || '-'
}

function resultLabel(result: string): string {
  const labels: Record<string, [string, string]> = {
    success: ['成功', 'Success'], failure: ['失败', 'Failure'], denied: ['拒绝', 'Denied'], pending: ['待确认', 'Pending']
  }
  const label = labels[result]
  return label ? localText(label[0], label[1]) : result || '-'
}

function resultClass(result: string): string {
  if (result === 'success') return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
  if (result === 'pending') return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
  if (result === 'denied') return 'bg-violet-100 text-violet-700 dark:bg-violet-900/30 dark:text-violet-300'
  return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
}

function prettyJSON(value: unknown): string {
  return JSON.stringify(value || {}, null, 2)
}

const detailRows = computed(() => {
  const item = selected.value
  if (!item) return []
  return [
    { label: 'ID', value: String(item.id) },
    { label: localText('时间', 'Time'), value: formatTimestamp(item.created_at) },
    { label: localText('操作者', 'Actor'), value: `${item.actor_email || '-'} (${item.actor_role || '-'})` },
    { label: localText('鉴权方式', 'Auth method'), value: item.auth_method || '-' },
    { label: localText('操作', 'Action'), value: item.action || '-' },
    { label: localText('资源', 'Resource'), value: `${item.resource_type || '-'}${item.resource_id ? ` #${item.resource_id}` : ''}` },
    { label: localText('结果', 'Result'), value: `${item.result} / HTTP ${item.status_code}` },
    { label: localText('请求', 'Request'), value: `${item.method || '-'} ${item.path || '-'}` },
    { label: 'request_id', value: item.request_id || '-' },
    { label: localText('来源 IP', 'Client IP'), value: item.client_ip || '-' },
    { label: 'UA hash', value: item.ua_hash || '-' },
    { label: localText('耗时', 'Latency'), value: `${item.latency_ms}ms` }
  ]
})

onMounted(() => {
  void loadPage()
})
</script>
