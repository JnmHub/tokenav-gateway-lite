<template>
  <section class="space-y-3 border-t pt-4" data-test="group-billing-model-source">
    <div>
      <label class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.groups.billingModelSource.title') }}</label>
      <p class="mt-1 text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('admin.groups.billingModelSource.hint') }}</p>
    </div>
    <div class="grid gap-2 md:grid-cols-2 xl:grid-cols-4">
      <button
        v-for="option in options"
        :key="option.value"
        type="button"
        class="rounded-xl border p-3 text-left transition-colors"
        :class="modelValue === option.value
          ? 'border-primary-500 bg-primary-50 text-primary-800 ring-1 ring-primary-500/20 dark:bg-primary-900/15 dark:text-primary-200'
          : 'border-gray-200 bg-white text-gray-700 hover:border-primary-300 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-200'"
        @click="emit('update:modelValue', option.value)"
      >
        <span class="flex items-center justify-between gap-2">
          <span class="text-sm font-medium">{{ option.label }}</span>
          <span v-if="option.recommended" class="rounded-full bg-primary-100 px-2 py-0.5 text-[11px] text-primary-700 dark:bg-primary-900/30 dark:text-primary-300">
            {{ t('admin.groups.billingModelSource.recommended') }}
          </span>
        </span>
        <span class="mt-1.5 block text-xs leading-5 text-gray-500 dark:text-gray-400">{{ option.description }}</span>
      </button>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { BillingModelSource } from '@/types'

defineProps<{ modelValue: BillingModelSource }>()
const emit = defineEmits<{ 'update:modelValue': [value: BillingModelSource] }>()
const { t } = useI18n()

const options = computed(() => [
  { value: 'requested' as const, label: t('admin.groups.billingModelSource.requested'), description: t('admin.groups.billingModelSource.requestedHint'), recommended: true },
  { value: 'channel_mapped' as const, label: t('admin.groups.billingModelSource.channelMapped'), description: t('admin.groups.billingModelSource.channelMappedHint') },
  { value: 'upstream' as const, label: t('admin.groups.billingModelSource.upstream'), description: t('admin.groups.billingModelSource.upstreamHint') },
  { value: 'response_model' as const, label: t('admin.groups.billingModelSource.responseModel'), description: t('admin.groups.billingModelSource.responseModelHint') },
])
</script>
