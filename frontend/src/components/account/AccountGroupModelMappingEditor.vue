<template>
  <section
    v-if="selectedGroups.length > 0"
    class="rounded-lg border border-gray-200 bg-gray-50/80 p-3 dark:border-dark-600 dark:bg-dark-800/70"
  >
    <div class="mb-3 flex items-start justify-between gap-3">
      <div>
        <h3 class="text-sm font-semibold text-gray-800 dark:text-gray-100">
          {{ t('admin.accounts.groupModelMapping.title') }}
        </h3>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
          {{ t('admin.accounts.groupModelMapping.hint') }}
        </p>
      </div>
      <span class="shrink-0 rounded-full bg-primary-100 px-2 py-0.5 text-[11px] font-medium text-primary-700 dark:bg-primary-900/30 dark:text-primary-300">
        {{ t('admin.accounts.groupModelMapping.optional') }}
      </span>
    </div>

    <div class="space-y-3">
      <div
        v-for="group in selectedGroups"
        :key="group.id"
        class="rounded-md border border-gray-200 bg-white p-3 dark:border-dark-600 dark:bg-dark-900/60"
      >
        <div class="mb-2 flex items-center justify-between gap-2">
          <div class="min-w-0">
            <div class="truncate text-sm font-medium text-gray-800 dark:text-gray-100">{{ group.name }}</div>
            <div class="text-[11px] text-gray-500 dark:text-gray-400">{{ group.platform }}</div>
          </div>
          <button
            type="button"
            class="shrink-0 text-xs font-medium text-primary-600 hover:text-primary-700 dark:text-primary-400 dark:hover:text-primary-300"
            @click="addRow(group.id)"
          >
            + {{ t('admin.accounts.groupModelMapping.add') }}
          </button>
        </div>

        <div v-if="rowsFor(group.id).length === 0" class="rounded border border-dashed border-gray-300 px-3 py-2 text-xs text-gray-500 dark:border-dark-500 dark:text-gray-400">
          {{ t('admin.accounts.groupModelMapping.empty') }}
        </div>
        <div v-else class="space-y-2">
          <div
            v-for="(row, index) in rowsFor(group.id)"
            :key="`${group.id}-${index}`"
            class="grid grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)_auto] items-center gap-2"
          >
            <input
              v-model="row.from"
              type="text"
              :list="`account-group-models-${group.id}`"
              :placeholder="t('admin.accounts.groupModelMapping.source')"
              class="input input-sm min-w-0"
              @input="emitChange"
            />
            <span class="text-xs text-gray-400">→</span>
            <input
              v-model="row.to"
              type="text"
              :list="`account-group-models-${group.id}`"
              :placeholder="t('admin.accounts.groupModelMapping.target')"
              class="input input-sm min-w-0"
              @input="emitChange"
            />
            <button
              type="button"
              class="inline-flex h-8 w-8 items-center justify-center rounded text-gray-400 hover:bg-red-50 hover:text-red-600 dark:hover:bg-red-900/20 dark:hover:text-red-400"
              :aria-label="t('admin.accounts.groupModelMapping.remove')"
              @click="removeRow(group.id, index)"
            >
              ×
            </button>
          </div>
        </div>
        <datalist :id="`account-group-models-${group.id}`">
          <option v-for="model in modelSuggestions(group)" :key="model" :value="model" />
        </datalist>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AdminGroup } from '@/types'

interface MappingRow {
  from: string
  to: string
}

interface Props {
  selectedGroupIds: number[]
  groups: AdminGroup[]
  modelValue?: Record<string, Record<string, string>>
}

const props = withDefaults(defineProps<Props>(), {
  modelValue: () => ({})
})

const emit = defineEmits<{
  'update:modelValue': [value: Record<string, Record<string, string>>]
}>()

const { t } = useI18n()
const rowsByGroup = ref<Record<string, MappingRow[]>>({})
const lastEmittedModelValue = ref<string | null>(null)

const selectedGroups = computed(() => {
  const selected = new Set(props.selectedGroupIds)
  return props.groups.filter((group) => selected.has(group.id))
})

const cloneRows = (value: Record<string, Record<string, string>> | undefined) => {
  const result: Record<string, MappingRow[]> = {}
  for (const groupID of props.selectedGroupIds) {
    const mapping = value?.[String(groupID)] || {}
    result[String(groupID)] = Object.entries(mapping).map(([from, to]) => ({ from, to }))
  }
  return result
}

const serializeMappings = (value: Record<string, Record<string, string>> | undefined) => {
  const normalized = Object.fromEntries(
    Object.entries(value || {})
      .sort(([left], [right]) => left.localeCompare(right))
      .map(([groupID, mapping]) => [
        String(groupID),
        Object.fromEntries(Object.entries(mapping || {}).sort(([left], [right]) => left.localeCompare(right)))
      ])
  )
  return JSON.stringify(normalized)
}

const syncRowsFromProps = (force = false) => {
  const incoming = serializeMappings(props.modelValue)
  if (!force && incoming === lastEmittedModelValue.value) {
    lastEmittedModelValue.value = null
    return
  }
  rowsByGroup.value = cloneRows(props.modelValue)
  lastEmittedModelValue.value = null
}

watch(
  () => props.selectedGroupIds,
  () => syncRowsFromProps(true),
  { deep: true, immediate: true }
)

watch(
  () => props.modelValue,
  () => syncRowsFromProps(),
  { deep: true }
)

const rowsFor = (groupID: number) => rowsByGroup.value[String(groupID)] || []

const addRow = (groupID: number) => {
  const key = String(groupID)
  if (!rowsByGroup.value[key]) rowsByGroup.value[key] = []
  rowsByGroup.value[key].push({ from: '', to: '' })
  emitChange()
}

const removeRow = (groupID: number, index: number) => {
  rowsFor(groupID).splice(index, 1)
  emitChange()
}

const emitChange = () => {
  const result: Record<string, Record<string, string>> = {}
  for (const groupID of props.selectedGroupIds) {
    const rows = rowsFor(groupID)
    const mapping: Record<string, string> = {}
    for (const row of rows) {
      const from = row.from.trim()
      const to = row.to.trim()
      if (from && to) mapping[from] = to
    }
    if (Object.keys(mapping).length > 0) result[String(groupID)] = mapping
  }
  lastEmittedModelValue.value = serializeMappings(result)
  emit('update:modelValue', result)
}

const modelSuggestions = (group: AdminGroup): string[] => {
  const values = new Set<string>()
  for (const [from, to] of Object.entries(group.model_mapping || {})) {
    if (from) values.add(from)
    if (to) values.add(to)
  }
  return [...values].sort()
}
</script>
