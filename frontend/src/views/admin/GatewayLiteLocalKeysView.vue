<template>
  <AppLayout>
    <div class="space-y-6">
      <div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div class="flex flex-wrap items-center gap-3 text-sm text-gray-500 dark:text-gray-400">
          <span class="badge badge-success">{{ localText(`${activeCount} 个启用`, `${activeCount} active`) }}</span>
          <span>{{ localText("本地密钥不会使用主站套餐、余额或额度租约。", "Local keys do not use control-plane plans, balances, or quota leases.") }}</span>
        </div>
        <div class="flex items-center gap-2">
          <div ref="columnSettingsRef" class="relative">
            <button
              type="button"
              class="btn btn-secondary"
              :title="localText('列设置', 'Columns')"
              :aria-expanded="columnSettingsOpen"
              @click.stop="columnSettingsOpen = !columnSettingsOpen"
            >
              <Icon name="grid" size="sm" />
              {{ localText("列设置", "Columns") }}
            </button>
            <div
              v-if="columnSettingsOpen"
              class="absolute right-0 z-50 mt-2 w-44 rounded-lg border border-gray-200 bg-white p-2 shadow-lg dark:border-dark-600 dark:bg-dark-800"
              @click.stop
            >
              <button
                type="button"
                data-column-key="id"
                class="flex w-full items-center justify-between rounded-md px-3 py-2 text-sm text-gray-700 transition-colors hover:bg-gray-100 dark:text-gray-200 dark:hover:bg-gray-700"
                @click="toggleColumn('id')"
              >
                <span>ID</span>
                <Icon v-if="isColumnVisible('id')" name="check" size="sm" class="text-primary-500" />
              </button>
            </div>
          </div>
          <button type="button" class="btn btn-primary" :disabled="loading || activeGroups.length === 0" @click="openCreate">
            <Icon name="plus" size="sm" />
            {{ localText("创建本地密钥", "Create local key") }}
          </button>
        </div>
      </div>

      <section class="card overflow-hidden">
        <div v-if="loading" class="space-y-3 p-6">
          <div v-for="index in 5" :key="index" class="h-12 animate-pulse rounded bg-gray-100 dark:bg-dark-800"></div>
        </div>

        <div v-else-if="loadError" class="flex flex-col items-center px-6 py-16 text-center">
          <div class="mb-4 flex h-12 w-12 items-center justify-center rounded-lg bg-red-50 text-red-600 dark:bg-red-900/20 dark:text-red-400">
            <Icon name="exclamationTriangle" size="lg" />
          </div>
          <h2 class="text-base font-semibold text-gray-900 dark:text-white">
            {{ localText("本地密钥加载失败", "Failed to load local keys") }}
          </h2>
          <p class="mt-2 max-w-md text-sm text-gray-500 dark:text-gray-400">{{ loadError }}</p>
          <button type="button" class="btn btn-secondary mt-5" @click="loadData">
            <Icon name="refresh" size="sm" />
            {{ localText("重新加载", "Retry") }}
          </button>
        </div>

        <div v-else-if="keys.length === 0" class="flex flex-col items-center px-6 py-16 text-center">
          <div class="mb-4 flex h-12 w-12 items-center justify-center rounded-lg bg-primary-50 text-primary-600 dark:bg-primary-900/20 dark:text-primary-400">
            <Icon name="key" size="lg" />
          </div>
          <h2 class="text-base font-semibold text-gray-900 dark:text-white">
            {{ localText("还没有本地密钥", "No local keys yet") }}
          </h2>
          <p class="mt-2 max-w-md text-sm text-gray-500 dark:text-gray-400">
            {{ activeGroups.length === 0
              ? localText("请先创建并启用一个分组，本地密钥必须绑定分组。", "Create and enable a group first; every local key must bind a group.")
              : localText("创建后可直接调用当前轻量网关，不经过主站鉴权与额度链路。", "Once created, the key calls this gateway directly without control-plane authentication or quota handling.") }}
          </p>
        </div>

        <div v-else class="overflow-x-auto">
          <table class="min-w-full divide-y divide-gray-200 dark:divide-dark-700">
            <thead class="bg-gray-50 dark:bg-dark-800/60">
              <tr>
                <th v-if="isColumnVisible('id')" class="px-6 py-3 text-left text-xs font-semibold uppercase text-gray-500 dark:text-gray-400">ID</th>
                <th class="px-6 py-3 text-left text-xs font-semibold uppercase text-gray-500 dark:text-gray-400">{{ localText("名称 / 密钥", "Name / Key") }}</th>
                <th class="px-6 py-3 text-left text-xs font-semibold uppercase text-gray-500 dark:text-gray-400">{{ localText("分组", "Group") }}</th>
                <th class="px-6 py-3 text-left text-xs font-semibold uppercase text-gray-500 dark:text-gray-400">{{ localText("访问限制", "Access rules") }}</th>
                <th class="px-6 py-3 text-left text-xs font-semibold uppercase text-gray-500 dark:text-gray-400">{{ localText("最近使用", "Last used") }}</th>
                <th class="px-6 py-3 text-right text-xs font-semibold uppercase text-gray-500 dark:text-gray-400">{{ localText("操作", "Actions") }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 bg-white dark:divide-dark-800 dark:bg-dark-900">
              <tr v-for="item in keys" :key="item.id" class="hover:bg-gray-50/70 dark:hover:bg-dark-800/40">
                <td v-if="isColumnVisible('id')" class="whitespace-nowrap px-6 py-4 font-mono text-xs text-gray-500 dark:text-gray-400">#{{ item.id }}</td>
                <td class="px-6 py-4">
                  <div class="flex items-start gap-3">
                    <span class="mt-0.5 h-2.5 w-2.5 rounded-full" :class="item.status === 'active' ? 'bg-emerald-500' : 'bg-gray-300 dark:bg-dark-600'"></span>
                    <div class="min-w-0">
                      <div class="font-medium text-gray-900 dark:text-white">{{ item.name }}</div>
                      <code class="mt-1 block text-xs text-gray-500 dark:text-gray-400">{{ item.key_masked }}</code>
                    </div>
                  </div>
                </td>
                <td class="px-6 py-4">
                  <div class="font-medium text-gray-800 dark:text-gray-200">{{ item.group_name || `#${item.group_id}` }}</div>
                  <span class="badge badge-gray mt-1">{{ item.platform || '-' }}</span>
                </td>
                <td class="px-6 py-4 text-sm text-gray-600 dark:text-gray-300">
                  <div>{{ localText("白名单", "Allow") }}: {{ ruleCount(item.ip_whitelist) || localText("不限", "Any") }}</div>
                  <div class="mt-1">{{ localText("黑名单", "Block") }}: {{ ruleCount(item.ip_blacklist) }}</div>
                </td>
                <td class="whitespace-nowrap px-6 py-4 text-sm text-gray-500 dark:text-gray-400">
                  {{ formatDate(item.last_used_at) }}
                </td>
                <td class="px-6 py-4">
                  <div class="flex justify-end gap-2">
                    <button
                      type="button"
                      class="btn btn-secondary btn-icon"
                      :disabled="revealingKeyID === item.id"
                      :title="localText('复制完整密钥', 'Copy full key')"
                      :aria-label="localText('复制完整密钥', 'Copy full key')"
                      @click="revealAndCopySecret(item)"
                    >
                      <span v-if="revealingKeyID === item.id" class="spinner-sm"></span>
                      <Icon v-else name="copy" size="sm" />
                    </button>
                    <button type="button" class="btn btn-secondary btn-icon" :title="item.status === 'active' ? localText('停用', 'Disable') : localText('启用', 'Enable')" @click="toggleStatus(item)">
                      <Icon :name="item.status === 'active' ? 'xCircle' : 'checkCircle'" size="sm" />
                    </button>
                    <button type="button" class="btn btn-secondary btn-icon" :title="localText('编辑', 'Edit')" @click="openEdit(item)">
                      <Icon name="edit" size="sm" />
                    </button>
                    <button type="button" class="btn btn-secondary btn-icon text-red-600 dark:text-red-400" :title="localText('删除', 'Delete')" @click="requestDelete(item)">
                      <Icon name="trash" size="sm" />
                    </button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </div>

    <BaseDialog :show="formOpen" :title="editingKey ? localText('编辑本地密钥', 'Edit local key') : localText('创建本地密钥', 'Create local key')" width="normal" @close="closeForm">
      <form id="gateway-local-key-form" class="space-y-5" @submit.prevent="saveKey">
        <div>
          <label for="gateway-local-key-name" class="input-label">{{ localText("名称", "Name") }}</label>
          <input id="gateway-local-key-name" v-model.trim="form.name" class="input" maxlength="100" required :placeholder="localText('例如：本机自动化', 'For example: local automation')" />
        </div>
        <div>
          <div class="input-label">{{ localText("绑定分组", "Bound group") }}</div>
          <Select v-model="form.group_id" :options="groupOptions" :placeholder="localText('选择一个启用分组', 'Select an active group')" searchable />
        </div>
        <div v-if="editingKey" class="setting-row rounded-lg border border-gray-200 px-4 py-3 dark:border-dark-700">
          <div>
            <h3>{{ localText("启用密钥", "Enable key") }}</h3>
            <p>{{ localText("停用后下一次请求立即返回鉴权失败。", "The next request is rejected immediately after disabling.") }}</p>
          </div>
          <Toggle v-model="form.active" />
        </div>
        <div class="grid gap-5 sm:grid-cols-2">
          <div class="min-w-0">
            <label for="gateway-local-key-allowlist" class="input-label">{{ localText("IP 白名单", "IP allowlist") }}</label>
            <textarea id="gateway-local-key-allowlist" v-model="form.ip_whitelist" class="input min-h-[132px] resize-y font-mono text-xs leading-5" :placeholder="localText('每行一个 IP 或 CIDR；留空表示不限', 'One IP or CIDR per line; blank allows any')"></textarea>
          </div>
          <div class="min-w-0">
            <label for="gateway-local-key-blocklist" class="input-label">{{ localText("IP 黑名单", "IP blocklist") }}</label>
            <textarea id="gateway-local-key-blocklist" v-model="form.ip_blacklist" class="input min-h-[132px] resize-y font-mono text-xs leading-5" :placeholder="localText('每行一个 IP 或 CIDR', 'One IP or CIDR per line')"></textarea>
          </div>
        </div>
      </form>
      <template #footer>
        <div class="flex justify-end gap-3">
          <button type="button" class="btn btn-secondary" :disabled="saving" @click="closeForm">{{ localText("取消", "Cancel") }}</button>
          <button type="submit" form="gateway-local-key-form" class="btn btn-primary" :disabled="saving || !form.name || !form.group_id">
            <span v-if="saving" class="spinner-sm"></span>
            {{ saving ? localText("保存中", "Saving") : localText("保存", "Save") }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <BaseDialog :show="Boolean(createdSecret)" :title="localText('本地密钥已创建', 'Local key created')" width="normal" :close-on-click-outside="false" @close="createdSecret = ''">
      <div class="space-y-4">
        <div class="rounded-lg border border-amber-200 bg-amber-50 p-4 text-sm text-amber-700 dark:border-amber-800 dark:bg-amber-900/20 dark:text-amber-300">
          {{ localText("请妥善保管；关闭后仍可在列表中再次复制。", "Store it safely; you can copy it again from the list after closing.") }}
        </div>
        <code class="block select-all break-all rounded-lg border border-gray-200 bg-gray-50 p-4 font-mono text-sm text-gray-900 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-100">{{ createdSecret }}</code>
      </div>
      <template #footer>
        <div class="flex justify-end gap-3">
          <button type="button" class="btn btn-secondary" @click="createdSecret = ''">{{ localText("关闭", "Close") }}</button>
          <button type="button" class="btn btn-primary" @click="copySecret">
            <Icon name="copy" size="sm" />
            {{ localText("复制密钥", "Copy key") }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <ConfirmDialog
      :show="Boolean(deleteTarget)"
      :title="localText('删除本地密钥', 'Delete local key')"
      :message="localText('删除后该密钥立即且永久失效，无法恢复。', 'The key becomes permanently invalid immediately and cannot be restored.')"
      danger
      @confirm="confirmDelete"
      @cancel="deleteTarget = null"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/GatewayLiteLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Select from '@/components/common/Select.vue'
import Toggle from '@/components/common/Toggle.vue'
import Icon from '@/components/icons/Icon.vue'
import { adminAPI } from '@/api'
import type { AdminGroup } from '@/types'
import {
  normalizeGatewayLiteLocalKey,
  normalizeGatewayLiteLocalKeyList,
  type GatewayLiteLocalKey,
} from '@/api/admin/gatewayLiteLocalKeys'
import { useAppStore } from '@/stores/app'
import { useClipboard } from '@/composables/useClipboard'
import { extractApiErrorMessage } from '@/utils/apiError'
import { formatDateTime as formatDateTimeValue } from '@/utils/format'

const { locale } = useI18n()
const appStore = useAppStore()
const { copyToClipboard } = useClipboard()
const keys = ref<GatewayLiteLocalKey[]>([])
const groups = ref<AdminGroup[]>([])
const loading = ref(true)
const loadError = ref('')
const saving = ref(false)
const formOpen = ref(false)
const editingKey = ref<GatewayLiteLocalKey | null>(null)
const createdSecret = ref('')
const revealingKeyID = ref<number | null>(null)
const deleteTarget = ref<GatewayLiteLocalKey | null>(null)
const columnSettingsOpen = ref(false)
const columnSettingsRef = ref<HTMLElement | null>(null)

const HIDDEN_COLUMNS_KEY = 'gateway-lite-local-keys-hidden-columns'
const DEFAULT_HIDDEN_COLUMNS = ['id']
const hiddenColumns = reactive<Set<string>>(new Set(DEFAULT_HIDDEN_COLUMNS))

const form = reactive({
  name: '',
  group_id: null as number | null,
  active: true,
  ip_whitelist: '',
  ip_blacklist: '',
})

const activeCount = computed(() => keys.value.filter((item) => item.status === 'active').length)
const activeGroups = computed(() => groups.value.filter((group) => group.status === 'active'))
const groupOptions = computed(() => activeGroups.value.map((group) => ({
  value: group.id,
  label: `${group.name} · ${group.platform}${group.local_only ? ` · ${localText('仅本地', 'Local only')}` : ''}`,
})))

function loadColumnSettings(): void {
  hiddenColumns.clear()
  try {
    const saved = localStorage.getItem(HIDDEN_COLUMNS_KEY)
    const parsed = saved ? JSON.parse(saved) : DEFAULT_HIDDEN_COLUMNS
    const keys = Array.isArray(parsed) ? parsed : DEFAULT_HIDDEN_COLUMNS
    if (keys.includes('id')) hiddenColumns.add('id')
    if (!saved) saveColumnSettings()
  } catch {
    DEFAULT_HIDDEN_COLUMNS.forEach((key) => hiddenColumns.add(key))
  }
}

function saveColumnSettings(): void {
  try {
    localStorage.setItem(HIDDEN_COLUMNS_KEY, JSON.stringify([...hiddenColumns]))
  } catch {
    // 浏览器禁用存储时仍保留当前会话内的列选择，不影响密钥管理主流程。
  }
}

function isColumnVisible(key: string): boolean {
  return !hiddenColumns.has(key)
}

function toggleColumn(key: string): void {
  if (key !== 'id') return
  if (hiddenColumns.has(key)) hiddenColumns.delete(key)
  else hiddenColumns.add(key)
  saveColumnSettings()
}

function handleDocumentClick(event: MouseEvent): void {
  const target = event.target as Node
  if (columnSettingsRef.value && !columnSettingsRef.value.contains(target)) {
    columnSettingsOpen.value = false
  }
}

function localText(zh: string, en: string): string {
  return locale.value.startsWith('zh') ? zh : en
}

function formatDate(value?: string | null): string {
  if (!value) return localText('从未使用', 'Never')
  return formatDateTimeValue(value, undefined, locale.value) || '-'
}

function parseIPRules(value: string): string[] {
  return Array.from(new Set(value.split(/[\n,]/).map((item) => item.trim()).filter(Boolean)))
}

function ruleCount(value: unknown): number {
  return Array.isArray(value) ? value.length : 0
}

async function loadData(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    const [localKeys, activeGroups] = await Promise.all([
      adminAPI.gatewayLiteLocalKeys.list(),
      adminAPI.groups.getAll(),
    ])
    keys.value = normalizeGatewayLiteLocalKeyList(localKeys)
    groups.value = Array.isArray(activeGroups) ? activeGroups : []
  } catch (error: unknown) {
    const fallback = localText('读取本地密钥失败，请稍后重试。', 'Failed to load local keys. Please try again.')
    const message = extractApiErrorMessage(error, fallback)
    loadError.value = fallback
    keys.value = []
    groups.value = []
    appStore.showError(message)
  } finally {
    loading.value = false
  }
}

function resetForm(): void {
  form.name = ''
  form.group_id = activeGroups.value[0]?.id ?? null
  form.active = true
  form.ip_whitelist = ''
  form.ip_blacklist = ''
}

function openCreate(): void {
  editingKey.value = null
  resetForm()
  formOpen.value = true
}

function openEdit(item: GatewayLiteLocalKey): void {
  const normalized = normalizeGatewayLiteLocalKey(item)
  editingKey.value = normalized
  form.name = normalized.name
  form.group_id = normalized.group_id
  form.active = normalized.status === 'active'
  form.ip_whitelist = normalized.ip_whitelist.join('\n')
  form.ip_blacklist = normalized.ip_blacklist.join('\n')
  formOpen.value = true
}

function closeForm(): void {
  if (saving.value) return
  formOpen.value = false
  editingKey.value = null
}

async function saveKey(): Promise<void> {
  if (!form.group_id) return
  saving.value = true
  try {
    const payload = {
      name: form.name.trim(),
      group_id: form.group_id,
      ip_whitelist: parseIPRules(form.ip_whitelist),
      ip_blacklist: parseIPRules(form.ip_blacklist),
    }
    if (editingKey.value) {
      const updated = await adminAPI.gatewayLiteLocalKeys.update(editingKey.value.id, {
        ...payload,
        status: form.active ? 'active' : 'disabled',
      })
      const normalized = normalizeGatewayLiteLocalKey(updated)
      keys.value = keys.value.map((item) => (item.id === normalized.id ? normalized : item))
      appStore.showSuccess(localText('本地密钥已更新', 'Local key updated'))
    } else {
      const created = normalizeGatewayLiteLocalKey(await adminAPI.gatewayLiteLocalKeys.create(payload))
      createdSecret.value = created.secret || ''
      keys.value = [{ ...created, secret: undefined }, ...keys.value]
      appStore.showSuccess(localText('本地密钥已创建', 'Local key created'))
    }
    formOpen.value = false
    editingKey.value = null
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, localText('保存本地密钥失败', 'Failed to save local key')))
  } finally {
    saving.value = false
  }
}

async function toggleStatus(item: GatewayLiteLocalKey): Promise<void> {
  try {
    const updated = await adminAPI.gatewayLiteLocalKeys.update(item.id, {
      status: item.status === 'active' ? 'disabled' : 'active',
    })
    const normalized = normalizeGatewayLiteLocalKey(updated)
    keys.value = keys.value.map((key) => (key.id === normalized.id ? normalized : key))
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, localText('修改密钥状态失败', 'Failed to update key status')))
  }
}

function requestDelete(item: GatewayLiteLocalKey): void {
  deleteTarget.value = item
}

async function confirmDelete(): Promise<void> {
  if (!deleteTarget.value) return
  try {
    await adminAPI.gatewayLiteLocalKeys.remove(deleteTarget.value.id)
    keys.value = keys.value.filter((item) => item.id !== deleteTarget.value?.id)
    deleteTarget.value = null
    appStore.showSuccess(localText('本地密钥已删除', 'Local key deleted'))
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, localText('删除本地密钥失败', 'Failed to delete local key')))
  }
}

async function revealAndCopySecret(item: GatewayLiteLocalKey): Promise<void> {
  if (revealingKeyID.value !== null) return
  revealingKeyID.value = item.id
  try {
    // The plaintext exists only in this local variable and is discarded immediately after
    // copying; it never enters the row model, localStorage or a long-lived reactive ref.
    const { secret } = await adminAPI.gatewayLiteLocalKeys.reveal(item.id)
    await copyToClipboard(secret, localText('完整密钥已复制', 'Full key copied'))
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, localText('读取完整密钥失败', 'Failed to reveal the full key')))
  } finally {
    revealingKeyID.value = null
  }
}

async function copySecret(): Promise<void> {
	await copyToClipboard(createdSecret.value, localText('密钥已复制', 'Key copied'))
}

onMounted(() => {
  loadColumnSettings()
  document.addEventListener('click', handleDocumentClick)
  loadData()
})

onUnmounted(() => {
  document.removeEventListener('click', handleDocumentClick)
})
</script>
