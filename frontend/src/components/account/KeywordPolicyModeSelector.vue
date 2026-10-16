<template>
  <section class="space-y-2" :data-testid="`${testIdPrefix}-keyword-policy`">
    <div v-if="showHeading">
      <label class="input-label mb-0">{{ t('admin.accounts.keywordPolicy.title') }}</label>
      <p class="mt-1 text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('admin.accounts.keywordPolicy.hint') }}</p>
    </div>
    <div class="grid grid-cols-1 gap-2 sm:grid-cols-3" role="radiogroup" :aria-label="t('admin.accounts.keywordPolicy.title')">
      <button
        v-for="option in options"
        :key="option.value"
        type="button"
        role="radio"
        :aria-checked="modelValue === option.value"
        class="rounded-lg border p-3 text-left transition-colors disabled:cursor-not-allowed disabled:opacity-50"
        :class="modelValue === option.value
          ? 'border-primary-300 bg-primary-50 text-primary-900 shadow-sm dark:border-primary-700 dark:bg-primary-900/20 dark:text-primary-100'
          : 'border-gray-200 bg-white text-gray-600 hover:border-gray-300 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-300'"
        :disabled="disabled"
        :data-testid="`${testIdPrefix}-keyword-policy-${option.value}`"
        @click="emit('update:modelValue', option.value)"
      >
        <span class="flex items-center justify-between gap-2">
          <span class="text-sm font-semibold">{{ option.label }}</span>
          <Icon v-if="modelValue === option.value" name="check" size="xs" class="text-primary-500" />
        </span>
        <span class="mt-1 block text-xs leading-5 text-gray-500 dark:text-gray-400">{{ option.description }}</span>
      </button>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'

export type KeywordPolicyMode = 'inherit' | 'protect' | 'allow'

withDefaults(defineProps<{
  modelValue: KeywordPolicyMode
  disabled?: boolean
  testIdPrefix?: string
  showHeading?: boolean
}>(), {
  disabled: false,
  testIdPrefix: 'account',
  showHeading: true,
})

const emit = defineEmits<{
  (event: 'update:modelValue', value: KeywordPolicyMode): void
}>()

const { t } = useI18n()
const options = computed<Array<{ value: KeywordPolicyMode; label: string; description: string }>>(() => [
  { value: 'inherit', label: t('admin.accounts.keywordPolicy.inherit'), description: t('admin.accounts.keywordPolicy.inheritDesc') },
  { value: 'protect', label: t('admin.accounts.keywordPolicy.protect'), description: t('admin.accounts.keywordPolicy.protectDesc') },
  { value: 'allow', label: t('admin.accounts.keywordPolicy.allow'), description: t('admin.accounts.keywordPolicy.allowDesc') },
])
</script>
