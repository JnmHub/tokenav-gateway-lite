<template>
  <section class="space-y-3 border-t border-gray-200 pt-4 dark:border-dark-600">
    <div>
      <h4 class="text-sm font-semibold text-gray-900 dark:text-white">{{ title }}</h4>
      <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ description }}</p>
    </div>
    <div class="grid gap-2 sm:grid-cols-3">
      <button
        v-for="option in options"
        :key="option.value"
        type="button"
        class="rounded-xl border p-3 text-left transition-colors"
        :class="modelValue === option.value
          ? 'border-primary-500 bg-primary-50 ring-1 ring-primary-500/20 dark:bg-primary-500/10'
          : 'border-gray-200 bg-white hover:border-gray-300 dark:border-dark-600 dark:bg-dark-800 dark:hover:border-dark-500'"
        @click="$emit('update:modelValue', option.value)"
      >
        <span class="flex items-center gap-2 text-sm font-medium text-gray-900 dark:text-white">
          {{ option.label }}
          <span v-if="option.recommended" class="rounded-full bg-primary-100 px-2 py-0.5 text-[10px] text-primary-700 dark:bg-primary-500/15 dark:text-primary-300">推荐</span>
        </span>
        <span class="mt-1 block text-xs leading-5 text-gray-500 dark:text-gray-400">{{ option.hint }}</span>
      </button>
    </div>
    <div v-if="modelValue === 'custom'">
      <label class="mb-1 block text-xs font-medium text-gray-700 dark:text-gray-300">自定义模型名称</label>
      <input
        :value="customValue"
        maxlength="200"
        class="form-input w-full"
        placeholder="例如：gpt-5.6-sol"
        @input="$emit('update:customValue', ($event.target as HTMLInputElement).value)"
      />
    </div>
  </section>
</template>

<script setup lang="ts">
type ResponseMode = 'inherit' | 'request' | 'upstream' | 'custom'

const props = withDefaults(defineProps<{
  modelValue: ResponseMode
  customValue?: string
  allowInherit?: boolean
}>(), { customValue: '', allowInherit: false })

defineEmits<{
  'update:modelValue': [value: ResponseMode]
  'update:customValue': [value: string]
}>()

const title = props.allowInherit ? '响应声明模型（账号覆盖）' : '响应声明模型'
const description = '只改变客户端响应中的 model 声明，不影响路由、计费和上游审计。'
const options = [
  ...(props.allowInherit ? [{ value: 'inherit' as const, label: '继承分组', hint: '使用所在分组的设置。', recommended: true }] : []),
  { value: 'request' as const, label: '跟随请求', hint: '返回用户最初请求的模型名。', recommended: !props.allowInherit },
  { value: 'upstream' as const, label: '上游原样', hint: '保留上游真实返回的模型名。', recommended: false },
  { value: 'custom' as const, label: '自定义', hint: '统一返回管理员设置的模型名。', recommended: false },
]
</script>
