<template>
  <fieldset class="space-y-3">
    <legend class="input-label">{{ t('admin.groups.displayIdentity.title') }}</legend>
    <p class="input-hint">{{ t('admin.groups.displayIdentity.hint') }}</p>

    <div>
      <label class="input-label" :for="brandInputId">{{ t('admin.groups.displayIdentity.brand') }}</label>
      <input
        :id="brandInputId"
        :value="effectiveBrand"
        data-testid="group-display-brand-input"
        class="input"
        type="text"
        maxlength="40"
        :placeholder="defaultIdentity.brand"
        @input="updateBrand"
      />
    </div>

    <div class="grid grid-cols-2 gap-2 sm:grid-cols-3">
      <button
        v-for="option in groupDisplayIconOptions"
        :key="option.key"
        type="button"
        :data-testid="`group-display-identity-${option.key}`"
        class="group flex min-h-14 items-center gap-2 rounded-lg border px-3 py-2 text-left text-xs font-medium transition duration-200 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 focus-visible:ring-offset-2 dark:focus-visible:ring-offset-dark-900"
        :class="effectiveIcon === option.key
          ? 'border-primary-500 bg-primary-50 text-primary-700 shadow-sm dark:border-primary-400 dark:bg-primary-900/20 dark:text-primary-300'
          : 'border-gray-200 bg-white text-gray-600 hover:-translate-y-px hover:border-gray-300 hover:bg-gray-50 active:translate-y-0 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-300 dark:hover:border-dark-500 dark:hover:bg-dark-700'"
        :aria-pressed="effectiveIcon === option.key"
        @click="selectIcon(option)"
      >
        <span class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-gray-100 text-sm font-semibold text-gray-600 dark:bg-dark-700 dark:text-gray-200">
          <img v-if="option.src" :src="option.src" :alt="option.label" class="h-5 w-5 object-contain" />
          <span v-else>{{ option.label.slice(0, 1) }}</span>
        </span>
        <span>{{ option.label }}</span>
      </button>
    </div>
  </fieldset>
</template>

<script setup lang="ts">
import { computed, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import type { GroupPlatform } from '@/types'
import {
  defaultGroupDisplayIdentity,
  groupDisplayIconOptions,
  type GroupDisplayIconOption
} from '@/constants/groupDisplay'

const props = defineProps<{
  displayBrand: string
  displayIcon: string
  platform: GroupPlatform
}>()

const emit = defineEmits<{
  'update:displayBrand': [value: string]
  'update:displayIcon': [value: string]
}>()

const { t } = useI18n()
const brandInputId = `group-display-brand-${useId()}`
const defaultIdentity = computed(() => defaultGroupDisplayIdentity(props.platform))
const effectiveBrand = computed(() => props.displayBrand || defaultIdentity.value.brand)
const effectiveIcon = computed(() => props.displayIcon || defaultIdentity.value.icon)

function updateBrand(event: Event) {
  emit('update:displayBrand', (event.target as HTMLInputElement).value)
}

function selectIcon(option: GroupDisplayIconOption) {
  emit('update:displayIcon', option.key)
  emit('update:displayBrand', option.label)
}
</script>
