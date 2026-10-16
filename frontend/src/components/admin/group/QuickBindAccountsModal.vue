<template>
  <BaseDialog :show="show" :title="t('admin.groups.bindAccounts.title')" width="wide" @close="emit('close')">
    <div v-if="group" class="space-y-4">
      <div class="flex flex-wrap items-center gap-2 rounded-lg bg-gray-50 px-3 py-2 text-sm dark:bg-dark-700">
        <span class="font-medium text-gray-900 dark:text-white">{{ group.name }}</span>
        <span class="text-xs text-gray-500 dark:text-gray-400">{{ group.platform }}</span>
        <span class="ml-auto text-xs text-primary-600 dark:text-primary-400">{{ t('admin.groups.bindAccounts.selected', { count: selectedIds.size }) }}</span>
      </div>
      <input v-model="search" class="input w-full" :placeholder="t('admin.groups.bindAccounts.placeholder')" @input="loadAccounts" />
      <div class="max-h-[360px] overflow-y-auto rounded-lg border border-gray-200 dark:border-dark-600">
        <label v-for="account in accounts" :key="account.id" class="flex cursor-pointer items-center gap-3 border-b border-gray-100 px-3 py-2.5 last:border-0 hover:bg-gray-50 dark:border-dark-700 dark:hover:bg-dark-700">
          <input type="checkbox" :checked="selectedIds.has(account.id)" class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500" @change="toggle(account.id)" />
          <span class="min-w-0 flex-1">
            <span class="block truncate text-sm text-gray-900 dark:text-white">{{ account.name }}</span>
            <span class="text-xs text-gray-400">#{{ account.id }} · {{ account.type }}</span>
          </span>
          <span :class="account.status === 'active' ? 'text-emerald-600' : 'text-gray-400'" class="text-xs">{{ account.status }}</span>
        </label>
        <div v-if="!loading && accounts.length === 0" class="px-3 py-8 text-center text-sm text-gray-400">{{ t('admin.groups.bindAccounts.empty') }}</div>
        <div v-if="loading" class="px-3 py-8 text-center text-sm text-gray-400">{{ t('admin.accounts.loading') }}</div>
      </div>
      <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.groups.bindAccounts.hint') }}</p>
    </div>
    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" @click="emit('close')">{{ t('common.cancel') }}</button>
        <button type="button" class="btn btn-primary" :disabled="saving || selectedIds.size === 0" @click="save">
          {{ saving ? t('admin.groups.bindAccounts.saving') : t('admin.groups.bindAccounts.button') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { AdminGroup, Account } from '@/types'
import BaseDialog from '@/components/common/BaseDialog.vue'

const props = defineProps<{ show: boolean; group: AdminGroup | null }>()
const emit = defineEmits<{ (event: 'close'): void; (event: 'success'): void }>()
const { t } = useI18n()
const search = ref('')
const accounts = ref<Account[]>([])
const selectedIds = ref<Set<number>>(new Set())
const loading = ref(false)
const saving = ref(false)
let searchTimer: ReturnType<typeof setTimeout> | null = null

const loadAccounts = () => {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(async () => {
    if (!props.group) return
    loading.value = true
    try {
      const result = await adminAPI.accounts.list(1, 100, {
        platform: props.group.platform === 'composite' ? undefined : props.group.platform,
        search: search.value.trim() || undefined,
        status: 'active',
        sort_by: 'priority',
        sort_order: 'asc',
      })
      accounts.value = result.items
    } finally {
      loading.value = false
    }
  }, 250)
}

const toggle = (id: number) => {
  const next = new Set(selectedIds.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  selectedIds.value = next
}

const save = async () => {
  if (!props.group || selectedIds.value.size === 0) return
  saving.value = true
  try {
    await adminAPI.groups.bindAccounts(props.group.id, [...selectedIds.value])
    emit('success')
    emit('close')
  } finally {
    saving.value = false
  }
}

watch(() => props.show, (show) => {
  if (show) {
    search.value = ''
    selectedIds.value = new Set()
    loadAccounts()
  }
})
</script>
