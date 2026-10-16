<template>
  <section class="rounded-xl border border-gray-200 bg-gray-50/70 p-4 dark:border-dark-600 dark:bg-dark-800/45">
    <div class="mb-3">
      <p class="text-sm font-medium text-gray-900 dark:text-gray-100">{{ t('admin.groups.scheduling.title') }}</p>
      <p class="mt-1 text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('admin.groups.scheduling.hint') }}</p>
    </div>
    <div class="grid gap-2 sm:grid-cols-2 lg:grid-cols-3" role="radiogroup" :aria-label="t('admin.groups.scheduling.title')">
      <button
        v-for="option in options"
        :key="option.value"
        type="button"
        role="radio"
        :aria-checked="modelValue === option.value"
        class="rounded-lg border px-3.5 py-3 text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/40"
        :class="modelValue === option.value
          ? 'border-primary-500 bg-primary-50 text-primary-800 dark:border-primary-500 dark:bg-primary-500/10 dark:text-primary-200'
          : 'border-gray-200 bg-white text-gray-700 hover:border-gray-300 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-300 dark:hover:border-dark-500'"
        @click="emit('update:modelValue', option.value)"
      >
        <span class="block text-sm font-semibold">{{ option.label }}</span>
        <span class="mt-1 block text-xs leading-5 opacity-75">{{ option.description }}</span>
      </button>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { GroupPlatform, GroupSchedulingMode } from '@/types'

const props = defineProps<{ modelValue: GroupSchedulingMode; platform?: GroupPlatform }>()
const emit = defineEmits<{ 'update:modelValue': [value: GroupSchedulingMode] }>()
const { t } = useI18n()
const options = computed(() => {
  const standardOptions = [
    { value: 'default' as const, label: t('admin.groups.scheduling.default'), description: t('admin.groups.scheduling.defaultHint') },
    { value: 'random' as const, label: t('admin.groups.scheduling.random'), description: t('admin.groups.scheduling.randomHint') },
  ]
  if (props.platform !== 'openai' && props.platform !== 'grok') {
    return standardOptions
  }
  return [
    standardOptions[0],
    { value: 'strict_priority' as const, label: t('admin.groups.scheduling.strictPriority'), description: t('admin.groups.scheduling.strictPriorityHint') },
    standardOptions[1],
  ]
})

watch(
  () => props.platform,
  (platform) => {
    if (props.modelValue === 'strict_priority' && platform !== 'openai' && platform !== 'grok') {
      // 切换到没有 OpenAI-compatible scheduler（OpenAI 兼容调度器）的平台时，
      // 立即回退默认值，不能隐藏选项后把无效值留在待保存表单中。
      emit('update:modelValue', 'default')
    }
  },
  { immediate: true },
)
</script>
