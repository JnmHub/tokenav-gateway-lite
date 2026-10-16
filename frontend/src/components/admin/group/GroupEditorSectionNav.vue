<template>
  <nav
    class="rounded-xl border border-gray-200 bg-gray-50/70 p-2 dark:border-dark-600 dark:bg-dark-800/60 lg:sticky lg:top-0 lg:w-52 lg:flex-none lg:self-start"
    :aria-label="t('admin.groups.editorNavigation.label')"
  >
    <div
      class="flex gap-1 overflow-x-auto pb-0.5 lg:block lg:space-y-1 lg:overflow-visible"
      role="tablist"
    >
      <button
        v-for="section in sections"
        :key="section.id"
        type="button"
        role="tab"
        :id="`${editorId}-${section.id}-tab`"
        :aria-controls="`${editorId}-${section.id}`"
        :aria-selected="modelValue === section.id"
        :class="[
          'group-editor-section-nav__item min-w-[9.75rem] rounded-lg border px-3 py-2.5 text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/40 lg:min-w-0 lg:w-full',
          modelValue === section.id
            ? 'border-primary-300 bg-white text-primary-800 shadow-sm dark:border-primary-700 dark:bg-dark-700 dark:text-primary-200'
            : 'border-transparent text-gray-600 hover:border-gray-200 hover:bg-white/80 dark:text-gray-400 dark:hover:border-dark-600 dark:hover:bg-dark-700/70',
        ]"
        @click="emit('update:modelValue', section.id)"
      >
        <span class="flex items-center gap-2">
          <span
            :class="[
              'flex h-7 w-7 flex-none items-center justify-center rounded-md',
              modelValue === section.id ? section.activeTone : section.idleTone,
            ]"
          >
            <Icon :name="section.icon" size="sm" />
          </span>
          <span class="min-w-0">
            <span class="block text-sm font-semibold">{{ section.label }}</span>
            <span class="mt-0.5 block truncate text-[11px] font-normal leading-4 opacity-75">
              {{ section.description }}
            </span>
          </span>
        </span>
      </button>
    </div>
    <p class="hidden border-t border-gray-200 px-2 pt-3 text-[11px] leading-5 text-gray-500 dark:border-dark-600 dark:text-gray-400 lg:block">
      {{ t('admin.groups.editorNavigation.hint') }}
    </p>
  </nav>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'

type GroupEditorSectionIcon = 'cog' | 'swap' | 'calculator' | 'shield'

export interface GroupEditorSection {
  id: string
  label: string
  description: string
  icon: GroupEditorSectionIcon
  activeTone: string
  idleTone: string
}

defineProps<{
  modelValue: string
  sections: readonly GroupEditorSection[]
  editorId: string
}>()

const emit = defineEmits<{
  'update:modelValue': [sectionId: string]
}>()

const { t } = useI18n()
</script>
