<template>
  <section class="rounded-xl border border-gray-200 bg-gray-50/70 p-4 dark:border-dark-600 dark:bg-dark-800/45">
    <div>
      <p class="text-sm font-medium text-gray-900 dark:text-gray-100">每用户并发上限</p>
      <p class="mt-1 text-xs leading-5 text-gray-500 dark:text-gray-400">同一主站用户的所有 API Key 共享此上限，不受 T0/T1/T2 等级影响；仍会同时执行用户等级并发。</p>
    </div>
    <div class="mt-3 flex flex-wrap gap-2">
      <button
        v-for="option in options"
        :key="option.value"
        type="button"
        class="rounded-lg border px-3 py-2 text-sm font-medium transition-colors"
        :class="modelValue === option.value ? 'border-primary-500 bg-primary-50 text-primary-700 dark:bg-primary-500/10 dark:text-primary-300' : 'border-gray-200 bg-white text-gray-600 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-300'"
        @click="emit('update:modelValue', option.value)"
      >
        {{ option.label }}
      </button>
    </div>
    <label class="mt-3 block text-xs font-medium text-gray-600 dark:text-gray-300">精确数值</label>
    <input
      :value="modelValue"
      type="number"
      min="0"
      max="100000"
      class="input mt-1"
      @input="emit('update:modelValue', Math.max(0, Number(($event.target as HTMLInputElement).value) || 0))"
    />
    <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">0 表示不限；例如设为 3，同一用户在本分组最多同时运行 3 个请求。</p>
  </section>
</template>

<script setup lang="ts">
defineProps<{ modelValue: number }>()
const emit = defineEmits<{ 'update:modelValue': [value: number] }>()
const options = [
  { label: '不限', value: 0 },
  { label: '1', value: 1 },
  { label: '2', value: 2 },
  { label: '3', value: 3 },
  { label: '5', value: 5 },
  { label: '10', value: 10 },
]
</script>
