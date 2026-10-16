<template>
  <BaseDialog
    :show="show"
    :title="t('admin.accounts.grokReconcile.title')"
    width="extra-wide"
    @close="handleClose"
  >
    <div class="space-y-4">
      <div class="rounded-lg border border-gray-200 bg-gray-50 p-4 dark:border-dark-600 dark:bg-dark-700/50">
        <div class="flex items-start gap-3">
          <span class="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-primary-50 text-primary-600 dark:bg-primary-900/30 dark:text-primary-300">
            <Icon name="shield" size="sm" />
          </span>
          <div class="min-w-0">
            <p class="text-sm font-medium text-gray-900 dark:text-white">
              {{ t('admin.accounts.grokReconcile.safeTitle') }}
            </p>
            <p class="mt-1 text-sm leading-6 text-gray-500 dark:text-gray-400">
              {{ t('admin.accounts.grokReconcile.safeDescription') }}
            </p>
          </div>
        </div>
      </div>

      <div v-if="loading" class="flex min-h-44 flex-col items-center justify-center gap-3 text-sm text-gray-500 dark:text-gray-400">
        <Icon name="refresh" size="lg" class="animate-spin text-primary-500" />
        <span>{{ applying ? t('admin.accounts.grokReconcile.repairing') : t('admin.accounts.grokReconcile.scanning') }}</span>
      </div>

      <div v-else-if="error" class="rounded-lg border border-red-200 bg-red-50 p-4 dark:border-red-800/50 dark:bg-red-900/20">
        <p class="text-sm font-medium text-red-800 dark:text-red-200">{{ t('admin.accounts.grokReconcile.failed') }}</p>
        <p class="mt-1 break-words text-sm text-red-700 dark:text-red-300">{{ error }}</p>
      </div>

      <template v-else-if="summary">
        <div class="grid grid-cols-2 gap-3 md:grid-cols-4">
          <div class="rounded-lg border border-gray-200 p-3 dark:border-dark-600">
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.grokReconcile.scanned') }}</p>
            <p class="mt-1 text-xl font-semibold tabular-nums text-gray-900 dark:text-white">{{ summary.scanned }}</p>
          </div>
          <div class="rounded-lg border border-amber-200 bg-amber-50/60 p-3 dark:border-amber-800/50 dark:bg-amber-900/10">
            <p class="text-xs text-amber-700 dark:text-amber-300">{{ t('admin.accounts.grokReconcile.actionable') }}</p>
            <p class="mt-1 text-xl font-semibold tabular-nums text-amber-800 dark:text-amber-200">{{ summary.actionable }}</p>
          </div>
          <div class="rounded-lg border border-blue-200 bg-blue-50/60 p-3 dark:border-blue-800/50 dark:bg-blue-900/10">
            <p class="text-xs text-blue-700 dark:text-blue-300">{{ t('admin.accounts.grokReconcile.refresh') }}</p>
            <p class="mt-1 text-xl font-semibold tabular-nums text-blue-800 dark:text-blue-200">{{ summary.dry_run ? summary.would_refresh : summary.refreshed }}</p>
          </div>
          <div class="rounded-lg border border-red-200 bg-red-50/60 p-3 dark:border-red-800/50 dark:bg-red-900/10">
            <p class="text-xs text-red-700 dark:text-red-300">{{ t('admin.accounts.grokReconcile.block') }}</p>
            <p class="mt-1 text-xl font-semibold tabular-nums text-red-800 dark:text-red-200">{{ summary.dry_run ? summary.would_block : summary.blocked }}</p>
          </div>
        </div>

        <div v-if="summary.items.length" class="overflow-hidden rounded-lg border border-gray-200 dark:border-dark-600">
          <div class="max-h-80 overflow-auto">
            <table class="min-w-full divide-y divide-gray-200 dark:divide-dark-600">
              <thead class="sticky top-0 bg-gray-50 dark:bg-dark-700">
                <tr>
                  <th class="w-10 px-3 py-2 text-left">
                    <input
                      type="checkbox"
                      class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500"
                      :checked="allSelected"
                      :aria-label="t('common.selectAll')"
                      @change="toggleAll"
                    />
                  </th>
                  <th class="px-3 py-2 text-left text-xs font-medium text-gray-500 dark:text-gray-400">ID</th>
                  <th class="px-3 py-2 text-left text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('admin.accounts.grokReconcile.reason') }}</th>
                  <th class="px-3 py-2 text-left text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('admin.accounts.grokReconcile.action') }}</th>
                  <th class="px-3 py-2 text-left text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('admin.accounts.grokReconcile.outcome') }}</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-gray-100 bg-white dark:divide-dark-700 dark:bg-dark-800">
                <tr v-for="item in visibleItems" :key="`${item.account_id}-${item.reason}`">
                  <td class="px-3 py-2">
                    <input
                      type="checkbox"
                      class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500"
                      :checked="selectedAccountIds.has(item.account_id)"
                      :aria-label="`#${item.account_id}`"
                      @change="toggleAccount(item.account_id)"
                    />
                  </td>
                  <td class="whitespace-nowrap px-3 py-2 font-mono text-xs text-gray-700 dark:text-gray-300">#{{ item.account_id }}</td>
                  <td class="px-3 py-2 text-sm text-gray-700 dark:text-gray-300">
                    {{ reasonLabel(item.reason) }}
                    <span v-if="item.owner_account_id" class="ml-1 text-xs text-gray-400">({{ t('admin.accounts.grokReconcile.ownerAccount', { id: item.owner_account_id }) }})</span>
                  </td>
                  <td class="px-3 py-2">
                    <span :class="item.action === 'block_account' ? 'badge badge-danger' : 'badge badge-primary'">
                      {{ item.action === 'block_account' ? t('admin.accounts.grokReconcile.blockAction') : t('admin.accounts.grokReconcile.refreshAction') }}
                    </span>
                  </td>
                  <td class="px-3 py-2 text-xs text-gray-500 dark:text-gray-400">{{ outcomeLabel(item.outcome) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <div
            v-if="totalPages > 1"
            class="flex items-center justify-between gap-3 border-t border-gray-100 px-3 py-2 dark:border-dark-700"
          >
            <button
              type="button"
              class="btn btn-secondary btn-sm"
              :disabled="currentPage === 1"
              @click="currentPage -= 1"
            >
              {{ t('pagination.previous') }}
            </button>
            <span class="text-xs tabular-nums text-gray-500 dark:text-gray-400">
              {{ t('pagination.pageOf', { page: currentPage, total: totalPages }) }}
            </span>
            <button
              type="button"
              class="btn btn-secondary btn-sm"
              :disabled="currentPage === totalPages"
              @click="currentPage += 1"
            >
              {{ t('pagination.next') }}
            </button>
          </div>
        </div>

        <div v-else class="rounded-lg border border-emerald-200 bg-emerald-50 p-5 text-center dark:border-emerald-800/50 dark:bg-emerald-900/20">
          <p class="text-sm font-medium text-emerald-800 dark:text-emerald-200">{{ t('admin.accounts.grokReconcile.healthy') }}</p>
        </div>

        <div v-if="confirming" class="rounded-lg border border-amber-300 bg-amber-50 p-4 dark:border-amber-700/60 dark:bg-amber-900/20">
          <p class="text-sm font-medium text-amber-900 dark:text-amber-100">{{ t('admin.accounts.grokReconcile.confirmTitle') }}</p>
          <p class="mt-1 text-sm text-amber-800 dark:text-amber-200">{{ t('admin.accounts.grokReconcile.confirmDescription') }}</p>
          <p class="mt-2 text-xs font-medium text-amber-900 dark:text-amber-100">{{ t('admin.accounts.grokReconcile.selectedCount', { count: selectedCount }) }}</p>
        </div>
      </template>
    </div>

    <template #footer>
      <div class="flex w-full flex-wrap justify-end gap-2">
        <button class="btn btn-secondary" :disabled="loading" @click="handleClose">{{ t('common.close') }}</button>
        <button v-if="summary?.items.length" class="btn btn-secondary" :disabled="loading" @click="exportResults">
          <Icon name="download" size="sm" class="mr-1.5" />
          {{ t('common.export') }}
        </button>
        <button v-if="summary?.dry_run && summary.items.length" class="btn btn-secondary" :disabled="loading" @click="toggleAll">
          {{ allSelected ? t('common.clearSelection') : t('common.selectAll') }}
        </button>
        <button v-if="!confirming" class="btn btn-secondary" :disabled="loading" @click="scan">
          <Icon name="refresh" size="sm" class="mr-1.5" />
          {{ t('admin.accounts.grokReconcile.scanAgain') }}
        </button>
        <button
          v-if="summary?.dry_run && summary.actionable > 0 && !confirming"
          class="btn btn-primary"
          :disabled="loading || selectedCount === 0"
          @click="confirming = true"
        >
          {{ t('admin.accounts.grokReconcile.repair') }}
        </button>
        <template v-if="confirming">
          <button class="btn btn-secondary" :disabled="loading" @click="confirming = false">{{ t('common.cancel') }}</button>
          <button class="btn btn-danger" :disabled="loading" @click="applyRepair">{{ t('admin.accounts.grokReconcile.confirmRepair') }}</button>
        </template>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { saveAs } from 'file-saver'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { adminAPI } from '@/api/admin'
import type { GrokOAuthReconcileItem, GrokOAuthReconcileReason, GrokOAuthReconcileResult } from '@/api/admin/grok'
import { extractApiErrorMessage } from '@/utils/apiError'

const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{ close: []; repaired: [] }>()
const { t } = useI18n()

const loading = ref(false)
const applying = ref(false)
const confirming = ref(false)
const error = ref('')
const summary = ref<GrokOAuthReconcileResult | null>(null)
const pageSize = 100
const currentPage = ref(1)
const totalPages = computed(() => Math.max(1, Math.ceil((summary.value?.items.length ?? 0) / pageSize)))
const visibleItems = computed(() => {
  const start = (currentPage.value - 1) * pageSize
  return summary.value?.items.slice(start, start + pageSize) ?? []
})
const selectedAccountIds = ref<Set<number>>(new Set())
const selectedCount = computed(() => selectedAccountIds.value.size)
const allSelected = computed(() => {
  const items = summary.value?.items ?? []
  return items.length > 0 && items.every((item) => selectedAccountIds.value.has(item.account_id))
})

interface GrokPreviewPage {
  afterId: number
  previewToken: string
  accountIds: number[]
}

const previewPages = ref<GrokPreviewPage[]>([])

function emptySummary(dryRun: boolean): GrokOAuthReconcileResult {
  return {
    dry_run: dryRun,
    scanned: 0,
    actionable: 0,
    would_block: 0,
    would_refresh: 0,
    blocked: 0,
    refreshed: 0,
    skipped: 0,
    failed: 0,
    partial: 0,
    items: [],
    next_after_id: 0,
    has_more: false,
    preview_token: ''
  }
}

function mergePage(target: GrokOAuthReconcileResult, page: GrokOAuthReconcileResult) {
  for (const key of ['scanned', 'actionable', 'would_block', 'would_refresh', 'blocked', 'refreshed', 'skipped', 'failed', 'partial'] as const) {
    target[key] += page[key]
  }
  target.items.push(...page.items)
  target.next_after_id = page.next_after_id
  target.has_more = page.has_more
}

async function scanAllPages() {
  const aggregate = emptySummary(true)
  const pages: GrokPreviewPage[] = []
  let afterId = 0
  for (let pageNumber = 0; pageNumber < 100; pageNumber += 1) {
    const pageStart = afterId
    const page = await adminAPI.grok.reconcileOAuthAccounts(false, pageStart, 500)
    if (!page.preview_token) throw new Error(t('admin.accounts.grokReconcile.invalidPreview'))
    pages.push({
      afterId: pageStart,
      previewToken: page.preview_token,
      accountIds: page.items.map((item) => item.account_id)
    })
    mergePage(aggregate, page)
    if (!page.has_more) return { aggregate, pages }
    if (!page.next_after_id || page.next_after_id <= afterId) {
      throw new Error(t('admin.accounts.grokReconcile.invalidCursor'))
    }
    afterId = page.next_after_id
  }
  throw new Error(t('admin.accounts.grokReconcile.tooManyPages'))
}

async function applySelectedPages() {
  const aggregate = emptySummary(false)
  for (const page of previewPages.value) {
    const accountIds = page.accountIds.filter((accountId) => selectedAccountIds.value.has(accountId))
    if (accountIds.length === 0) continue
    const result = await adminAPI.grok.reconcileOAuthAccounts(true, page.afterId, 500, page.previewToken, accountIds)
    mergePage(aggregate, result)
  }
  return aggregate
}

async function scan() {
  loading.value = true
  applying.value = false
  confirming.value = false
  currentPage.value = 1
  error.value = ''
  try {
    const result = await scanAllPages()
    previewPages.value = result.pages
    summary.value = result.aggregate
    selectedAccountIds.value = new Set(result.aggregate.items.map((item) => item.account_id))
  } catch (err) {
    summary.value = null
    error.value = extractApiErrorMessage(err, t('admin.accounts.grokReconcile.failed'))
  } finally {
    loading.value = false
  }
}

async function applyRepair() {
  loading.value = true
  applying.value = true
  confirming.value = false
  error.value = ''
  try {
    summary.value = await applySelectedPages()
    currentPage.value = 1
    emit('repaired')
  } catch (err) {
    error.value = extractApiErrorMessage(err, t('admin.accounts.grokReconcile.failed'))
  } finally {
    applying.value = false
    loading.value = false
  }
}

function toggleAccount(accountId: number) {
  const next = new Set(selectedAccountIds.value)
  if (next.has(accountId)) next.delete(accountId)
  else next.add(accountId)
  selectedAccountIds.value = next
}

function toggleAll() {
  if (allSelected.value) {
    selectedAccountIds.value = new Set()
    return
  }
  selectedAccountIds.value = new Set((summary.value?.items ?? []).map((item) => item.account_id))
}

function exportResults() {
  if (!summary.value) return
  const payload = JSON.stringify({
    exported_at: new Date().toISOString(),
    summary: summary.value,
    selected_account_ids: [...selectedAccountIds.value].sort((a, b) => a - b)
  }, null, 2)
  saveAs(new Blob([payload], { type: 'application/json;charset=utf-8' }), `grok-oauth-reconcile-${Date.now()}.json`)
}

function handleClose() {
  if (!loading.value) emit('close')
}

function reasonLabel(reason: GrokOAuthReconcileReason) {
  return t(`admin.accounts.grokReconcile.reasons.${reason}`)
}

function outcomeLabel(outcome: GrokOAuthReconcileItem['outcome']) {
  return t(`admin.accounts.grokReconcile.outcomes.${outcome}`)
}

watch(
  () => props.show,
  (show) => {
    if (show) void scan()
  }
)
</script>
