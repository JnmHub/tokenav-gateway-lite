<template>
  <section class="space-y-3 border-t pt-4" data-test="group-model-mapping">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div>
        <label class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.groups.modelMapping.title') }}</label>
        <p class="mt-1 text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('admin.groups.modelMapping.hint') }}</p>
      </div>
      <button type="button" class="btn btn-secondary btn-sm inline-flex items-center gap-1.5" @click="addRow">
        <Icon name="plus" size="sm" />
        {{ t('admin.groups.modelMapping.add') }}
      </button>
    </div>

    <div v-if="modelValue.length === 0" class="rounded-lg border border-dashed border-gray-200 px-4 py-5 text-center text-xs text-gray-500 dark:border-dark-600 dark:text-gray-400">
      {{ t('admin.groups.modelMapping.empty') }}
    </div>

    <div v-else class="space-y-2">
      <template v-for="(row, index) in modelValue" :key="index">
        <!-- 单目标行：源 -> 目标 -->
        <div
          v-if="!row.split"
          class="grid grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)_auto_auto] items-center gap-2 rounded-lg border border-gray-200 bg-gray-50/50 p-2 dark:border-dark-600 dark:bg-dark-800/40 max-sm:grid-cols-[minmax(0,1fr)_auto]"
        >
          <Select
            :model-value="row.from"
            :options="modelOptions"
            searchable
            creatable
            :creatable-prefix="t('admin.groups.modelMapping.useCustom')"
            :placeholder="t('admin.groups.modelMapping.source')"
            :search-placeholder="t('admin.groups.modelMapping.searchSource')"
            :aria-label="t('admin.groups.modelMapping.source')"
            @update:model-value="updateRow(index, 'from', $event)"
          />
          <Icon name="arrowRight" size="sm" class="text-gray-400 max-sm:rotate-90" />
          <Select
            :model-value="row.to"
            :options="modelOptions"
            searchable
            creatable
            :creatable-prefix="t('admin.groups.modelMapping.useCustom')"
            :placeholder="t('admin.groups.modelMapping.target')"
            :search-placeholder="t('admin.groups.modelMapping.searchTarget')"
            :aria-label="t('admin.groups.modelMapping.target')"
            @update:model-value="updateRow(index, 'to', $event)"
          />
          <button
            v-if="splitAvailable"
            type="button"
            class="rounded-lg px-2 py-1 text-xs text-gray-500 transition-colors hover:bg-primary-50 hover:text-primary-600 dark:text-gray-400 dark:hover:bg-primary-900/20"
            :aria-label="t('admin.groups.modelMapping.toSplit')"
            :title="t('admin.groups.modelMapping.toSplit')"
            @click="convertToSplit(index)"
          >
            {{ t('admin.groups.modelMapping.split') }}
          </button>
          <span v-else class="w-1" />
          <button
            type="button"
            class="rounded-lg p-2 text-gray-400 transition-colors hover:bg-red-50 hover:text-red-500 focus:outline-none focus:ring-2 focus:ring-red-500/30 dark:hover:bg-red-900/20"
            :aria-label="t('admin.groups.modelMapping.remove')"
            @click="removeRow(index)"
          >
            <Icon name="trash" size="sm" />
          </button>
        </div>

        <!-- 智能分流行：源 + 带权重目标列表 -->
        <div
          v-else
          class="space-y-2 rounded-lg border border-primary-200 bg-primary-50/40 p-3 dark:border-primary-900/40 dark:bg-primary-900/10"
          data-test="group-model-mapping-split-row"
        >
          <div class="grid grid-cols-[minmax(0,1fr)_auto_auto] items-center gap-2">
            <Select
              :model-value="row.from"
              :options="modelOptions"
              searchable
              creatable
              :creatable-prefix="t('admin.groups.modelMapping.useCustom')"
              :placeholder="t('admin.groups.modelMapping.source')"
              :search-placeholder="t('admin.groups.modelMapping.searchSource')"
              :aria-label="t('admin.groups.modelMapping.source')"
              @update:model-value="updateRow(index, 'from', $event)"
            />
            <button
              type="button"
              class="rounded-lg px-2 py-1 text-xs text-primary-600 transition-colors hover:bg-primary-100 dark:text-primary-300 dark:hover:bg-primary-900/30"
              :aria-label="t('admin.groups.modelMapping.toSingle')"
              @click="convertToSingle(index)"
            >
              {{ t('admin.groups.modelMapping.single') }}
            </button>
            <button
              type="button"
              class="rounded-lg p-2 text-gray-400 transition-colors hover:bg-red-50 hover:text-red-500 focus:outline-none focus:ring-2 focus:ring-red-500/30 dark:hover:bg-red-900/20"
              :aria-label="t('admin.groups.modelMapping.remove')"
              @click="removeRow(index)"
            >
              <Icon name="trash" size="sm" />
            </button>
          </div>

          <div
            v-for="(target, targetIndex) in row.targets ?? []"
            :key="targetIndex"
            class="grid grid-cols-[auto_minmax(0,1fr)_auto_auto] items-center gap-2 rounded-md border border-gray-200 bg-white p-2 dark:border-dark-600 dark:bg-dark-800"
          >
            <span
              class="inline-block size-2.5 shrink-0 rounded-full"
              :class="healthDotClass(row.from, target.model)"
              :title="healthTitle(row.from, target.model)"
            />
            <Select
              :model-value="target.model"
              :options="modelOptions"
              searchable
              creatable
              :creatable-prefix="t('admin.groups.modelMapping.useCustom')"
              :placeholder="t('admin.groups.modelMapping.target')"
              :search-placeholder="t('admin.groups.modelMapping.searchTarget')"
              :aria-label="t('admin.groups.modelMapping.target')"
              @update:model-value="updateTarget(index, targetIndex, 'model', $event)"
            />
            <div class="flex items-center gap-1">
              <button
                type="button"
                class="rounded p-1 text-gray-400 hover:bg-gray-100 hover:text-gray-700 dark:hover:bg-dark-600"
                :aria-label="t('admin.groups.modelMapping.weightDown')"
                @click="adjustTargetWeight(index, targetIndex, -10)"
              >
                <Icon name="arrowDown" size="sm" class="rotate-[-90deg]" />
              </button>
              <div class="w-14 text-center">
                <input
                  :value="target.weight"
                  type="number"
                  min="1"
                  max="100"
                  class="input px-1 py-0.5 text-center text-xs"
                  :aria-label="t('admin.groups.modelMapping.weight')"
                  @change="updateTargetWeightInput(index, targetIndex, $event)"
                />
              </div>
              <button
                type="button"
                class="rounded p-1 text-gray-400 hover:bg-gray-100 hover:text-gray-700 dark:hover:bg-dark-600"
                :aria-label="t('admin.groups.modelMapping.weightUp')"
                @click="adjustTargetWeight(index, targetIndex, 10)"
              >
                <Icon name="arrowUp" size="sm" class="rotate-[-90deg]" />
              </button>
              <span class="w-12 text-right text-xs text-gray-500 dark:text-gray-400">{{ targetPercent(row, targetIndex) }}%</span>
            </div>
            <div class="flex items-center gap-1">
              <button
                v-if="targetHealth(row.from, target.model)?.state === 'cooling' && groupId"
                type="button"
                class="rounded p-1.5 text-amber-500 transition-colors hover:bg-amber-50 dark:hover:bg-amber-900/20"
                :aria-label="t('admin.groups.modelMapping.resetHealth')"
                :title="t('admin.groups.modelMapping.resetHealth')"
                @click="emit('reset-route-health', row.from.trim(), target.model.trim())"
              >
                <Icon name="refresh" size="sm" />
              </button>
              <button
                type="button"
                class="rounded-lg p-1.5 text-gray-400 transition-colors hover:bg-red-50 hover:text-red-500 dark:hover:bg-red-900/20"
                :aria-label="t('admin.groups.modelMapping.removeTarget')"
                @click="removeTarget(index, targetIndex)"
              >
                <Icon name="trash" size="sm" />
              </button>
            </div>
          </div>

          <div class="flex items-center justify-between gap-2">
            <button
              type="button"
              class="btn btn-secondary btn-sm inline-flex items-center gap-1.5"
              :disabled="(row.targets?.length ?? 0) >= 8"
              @click="addTarget(index)"
            >
              <Icon name="plus" size="sm" />
              {{ t('admin.groups.modelMapping.addTarget') }}
            </button>
            <span
              v-if="coolingCount(row) > 0"
              class="text-xs text-amber-600 dark:text-amber-400"
            >{{ t('admin.groups.modelMapping.coolingCount', { count: coolingCount(row) }) }}</span>
          </div>
        </div>
      </template>
    </div>

    <p v-if="loading" class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.groups.modelMapping.loading') }}</p>

    <div v-if="previewRows.length > 0" class="rounded-lg bg-primary-50/70 p-3 dark:bg-primary-900/10">
      <p class="mb-2 text-xs font-medium text-primary-700 dark:text-primary-300">{{ t('admin.groups.modelMapping.preview') }}</p>
      <div class="flex flex-wrap gap-2">
        <code
          v-for="row in previewRows"
          :key="row.from + row.to"
          class="rounded-md border border-primary-100 bg-white px-2 py-1 text-xs text-gray-700 dark:border-primary-900/40 dark:bg-dark-800 dark:text-gray-200"
        >{{ row.from }} → {{ row.to }}</code>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import type { GroupModelMappingRow } from '@/views/admin/groupsModelMapping'
import type { GroupModelRouteHealthView } from '@/types'

const props = defineProps<{
  modelValue: GroupModelMappingRow[]
  candidates: string[]
  loading?: boolean
  // 仅 openai 分组允许智能分流；其他平台隐藏分流入口。
  platform?: string
  // 编辑已有分组时传入，用于目标健康徽标与手动重置。
  groupId?: number | null
  health?: GroupModelRouteHealthView[]
}>()

const emit = defineEmits<{
  (event: 'update:modelValue', value: GroupModelMappingRow[]): void
  (event: 'reset-route-health', publicModel: string, targetModel: string): void
}>()

const { t } = useI18n()

const splitAvailable = computed(() => (props.platform ?? '').trim().toLowerCase() === 'openai')

const modelOptions = computed(() => [...new Set(props.candidates.map((model) => model.trim()).filter(Boolean))]
  .map((model) => ({ value: model, label: model })))

const previewRows = computed(() => {
  const rows: { from: string; to: string }[] = []
  for (const row of props.modelValue) {
    const from = row.from.trim()
    if (!from) continue
    if (row.split) {
      const total = (row.targets ?? []).reduce((sum, target) => sum + (target.weight > 0 ? target.weight : 0), 0)
      if (total <= 0) continue
      for (const target of row.targets ?? []) {
        if (!target.model.trim()) continue
        rows.push({ from, to: `${target.model.trim()} ${Math.round((target.weight / total) * 100)}%` })
      }
    } else if (row.to.trim()) {
      rows.push({ from, to: row.to.trim() })
    }
  }
  return rows
})

function healthKey(publicModel: string, targetModel: string) {
  return `${publicModel.trim().toLowerCase()}\u0000${targetModel.trim().toLowerCase()}`
}

function targetHealth(publicModel: string, targetModel: string): GroupModelRouteHealthView | undefined {
  if (!props.health?.length) return undefined
  const key = healthKey(publicModel, targetModel)
  return props.health.find((view) => healthKey(view.public_model, view.target_model) === key)
}

function healthDotClass(publicModel: string, targetModel: string) {
  switch (targetHealth(publicModel, targetModel)?.state) {
    case 'cooling': return 'bg-red-500'
    case 'recovering': return 'bg-amber-400'
    case 'failing': return 'bg-amber-400'
    default: return 'bg-emerald-500'
  }
}

function healthTitle(publicModel: string, targetModel: string) {
  const view = targetHealth(publicModel, targetModel)
  if (!view) return t('admin.groups.modelMapping.healthHealthy')
  const seconds = Math.ceil((view.cooldown_remaining_ms ?? 0) / 1000)
  switch (view.state) {
    case 'cooling': return t('admin.groups.modelMapping.healthCooling', { seconds })
    case 'recovering': return t('admin.groups.modelMapping.healthRecovering')
    case 'failing': return t('admin.groups.modelMapping.healthFailing', { count: view.consecutive_failures })
    default: return t('admin.groups.modelMapping.healthHealthy')
  }
}

function coolingCount(row: GroupModelMappingRow) {
  return (row.targets ?? []).filter((target) => targetHealth(row.from, target.model)?.state === 'cooling').length
}

function targetPercent(row: GroupModelMappingRow, targetIndex: number) {
  const targets = row.targets ?? []
  const total = targets.reduce((sum, target) => sum + (target.weight > 0 ? target.weight : 0), 0)
  if (total <= 0) return 0
  return Math.round((targets[targetIndex].weight / total) * 100)
}

function addRow() {
  emit('update:modelValue', [...props.modelValue, { from: '', to: '' }])
}

function removeRow(index: number) {
  emit('update:modelValue', props.modelValue.filter((_, rowIndex) => rowIndex !== index))
}

function updateRow(index: number, field: keyof GroupModelMappingRow, value: unknown) {
  const rows = props.modelValue.map((row, rowIndex) => rowIndex === index
    ? { ...row, [field]: typeof value === 'string' ? value : '' }
    : row)
  emit('update:modelValue', rows)
}

function convertToSplit(index: number) {
  const rows = props.modelValue.map((row, rowIndex) => {
    if (rowIndex !== index) return row
    const first = row.to.trim()
    const targets = first
      ? [{ model: first, weight: 70 }, { model: '', weight: 30 }]
      : [{ model: '', weight: 50 }, { model: '', weight: 50 }]
    return { from: row.from, to: '', split: true, targets }
  })
  emit('update:modelValue', rows)
}

function convertToSingle(index: number) {
  const rows = props.modelValue.map((row, rowIndex) => rowIndex === index
    ? { from: row.from, to: (row.targets?.[0]?.model ?? '').trim() }
    : row)
  emit('update:modelValue', rows)
}

function updateTarget(rowIndex: number, targetIndex: number, field: 'model', value: unknown) {
  const rows = props.modelValue.map((row, index) => {
    if (index !== rowIndex || !row.targets) return row
    const targets = row.targets.map((target, position) => position === targetIndex
      ? { ...target, [field]: typeof value === 'string' ? value : '' }
      : target)
    return { ...row, targets }
  })
  emit('update:modelValue', rows)
}

function updateTargetWeightInput(rowIndex: number, targetIndex: number, event: Event) {
  const parsed = Number.parseInt((event.target as HTMLInputElement).value, 10)
  const clamped = Number.isNaN(parsed) ? 1 : Math.min(100, Math.max(1, parsed))
  const rows = props.modelValue.map((row, index) => {
    if (index !== rowIndex || !row.targets) return row
    const targets = row.targets.map((target, position) => position === targetIndex
      ? { ...target, weight: clamped }
      : target)
    return { ...row, targets }
  })
  emit('update:modelValue', rows)
}

function adjustTargetWeight(rowIndex: number, targetIndex: number, delta: number) {
  const rows = props.modelValue.map((row, index) => {
    if (index !== rowIndex || !row.targets) return row
    const targets = row.targets.map((target, position) => position === targetIndex
      ? { ...target, weight: Math.min(100, Math.max(1, target.weight + delta)) }
      : target)
    return { ...row, targets }
  })
  emit('update:modelValue', rows)
}

function addTarget(rowIndex: number) {
  const rows = props.modelValue.map((row, index) => {
    if (index !== rowIndex) return row
    const targets = [...(row.targets ?? []), { model: '', weight: 10 }]
    return { ...row, targets }
  })
  emit('update:modelValue', rows)
}

function removeTarget(rowIndex: number, targetIndex: number) {
  const rows = props.modelValue.map((row, index) => {
    if (index !== rowIndex || !row.targets) return row
    return { ...row, targets: row.targets.filter((_, position) => position !== targetIndex) }
  })
  emit('update:modelValue', rows)
}
</script>
