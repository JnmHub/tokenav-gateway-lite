<template>
  <nav
    class="rounded-xl border border-gray-200 bg-gray-50/80 p-2 dark:border-dark-600 dark:bg-dark-700/70 lg:sticky lg:top-0"
    :aria-label="label"
    data-testid="account-editor-section-nav"
  >
    <div class="mb-2 hidden items-start gap-2 px-2 pt-1 lg:flex">
      <span class="mt-0.5 rounded-lg bg-primary-100 p-1.5 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300">
        <Icon name="cog" size="sm" :stroke-width="2" />
      </span>
      <div class="min-w-0">
        <p class="text-xs font-semibold text-gray-900 dark:text-white">{{ label }}</p>
        <p v-if="hint" class="mt-0.5 text-[11px] leading-4 text-gray-500 dark:text-gray-400">{{ hint }}</p>
      </div>
    </div>

    <div class="flex gap-1 overflow-x-auto pb-1 lg:flex-col lg:overflow-visible lg:pb-0">
      <button
        v-for="section in sections"
        :key="section.id"
        type="button"
        :data-testid="`account-editor-section-${section.id}`"
        :aria-current="modelValue === section.id ? 'page' : undefined"
        :class="[
          'group flex min-h-10 shrink-0 items-center gap-2 rounded-lg px-2.5 py-2 text-left text-xs font-medium transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/40 lg:w-full',
          modelValue === section.id
            ? 'bg-white text-primary-700 shadow-sm ring-1 ring-primary-100 dark:bg-dark-600 dark:text-primary-300 dark:ring-primary-900/50'
            : 'text-gray-600 hover:bg-white hover:text-gray-900 dark:text-gray-400 dark:hover:bg-dark-600 dark:hover:text-gray-200'
        ]"
        @click="selectSection(section.id)"
      >
        <span
          :class="[
            'flex h-6 w-6 shrink-0 items-center justify-center rounded-md transition-colors',
            modelValue === section.id
              ? 'bg-primary-100 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300'
              : 'bg-gray-200/80 text-gray-500 group-hover:bg-gray-100 dark:bg-dark-600 dark:text-gray-400 dark:group-hover:bg-dark-500'
          ]"
        >
          <Icon :name="section.icon" size="xs" :stroke-width="2" />
        </span>
        <span class="whitespace-nowrap lg:min-w-0 lg:flex-1 lg:whitespace-normal">{{ section.label }}</span>
        <Icon
          name="chevronRight"
          size="xs"
          :class="[
            'hidden shrink-0 lg:block',
            modelValue === section.id ? 'text-primary-500' : 'text-gray-300 dark:text-dark-500'
          ]"
          :stroke-width="2"
        />
      </button>
    </div>
  </nav>
</template>

<script setup lang="ts">
import Icon from '@/components/icons/Icon.vue'

type SectionIcon = 'user' | 'key' | 'grid' | 'chartBar' | 'shield' | 'cog'

export interface AccountEditorSectionNavItem {
  id: string
  label: string
  icon: SectionIcon
}

defineProps<{
  sections: AccountEditorSectionNavItem[]
  modelValue: string
  label: string
  hint?: string
}>()

const emit = defineEmits<{
  'update:modelValue': [id: string]
  select: [id: string]
}>()

const selectSection = (id: string) => {
  emit('update:modelValue', id)
  emit('select', id)
}
</script>
