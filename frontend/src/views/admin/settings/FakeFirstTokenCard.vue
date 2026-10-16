<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { apiClient } from '@/api/client'
import Icon from '@/components/icons/Icon.vue'

// 伪造流式首字总开关：开启后流式请求在 [min, max] 秒随机延迟先给客户端发送
// 一条 SSE 注释行（对 SDK 无害），上游真实返回前客户端即收到字节（伪装 TTFT）。
// 保存即热生效，无需重启；请求日志同时记录上游首字与伪造首字两个时刻。
const loading = ref(false)
const saving = ref(false)
const error = ref('')

const draft = reactive({ enabled: false, min_seconds: 0.3, max_seconds: 1.2 })

async function load() {
  loading.value = true
  error.value = ''
  try {
    const { data } = await apiClient.get('/admin/ops/runtime/fake-first-token')
    Object.assign(draft, data || {})
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

async function save() {
  saving.value = true
  error.value = ''
  try {
    const { data } = await apiClient.put('/admin/ops/runtime/fake-first-token', { ...draft })
    Object.assign(draft, data || {})
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="rounded-xl border border-gray-200 bg-white p-5 dark:border-dark-700 dark:bg-dark-800">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div class="flex items-center gap-3">
        <span class="flex h-10 w-10 items-center justify-center rounded-xl bg-cyan-100 text-cyan-700 dark:bg-cyan-900/30 dark:text-cyan-300">
          <Icon name="clock" size="md" />
        </span>
        <div>
          <div class="flex items-center gap-2">
            <h3 class="text-sm font-semibold text-gray-900 dark:text-white">伪造流式首字</h3>
            <span v-if="draft.enabled" class="badge badge-success">已启用</span>
            <span v-else class="badge">未启用</span>
          </div>
          <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
            流式请求在随机延迟内先向客户端发送一条无害 SSE 注释行，伪装首字节奏；保存即热生效。
          </p>
        </div>
      </div>
      <div class="flex gap-2">
        <button class="btn btn-secondary btn-sm" :disabled="loading" @click="load">
          <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
        </button>
        <button class="btn btn-primary btn-sm" :disabled="saving" @click="save">
          {{ saving ? '保存中…' : '保存并应用' }}
        </button>
      </div>
    </div>

    <p v-if="error" class="mt-3 rounded-lg bg-rose-50 px-3 py-2 text-xs text-rose-600 dark:bg-rose-900/20 dark:text-rose-300">
      {{ error }}
    </p>

    <div class="mt-4 grid gap-3 sm:grid-cols-3">
      <label class="text-xs font-medium text-gray-500 dark:text-gray-400">
        启用
        <div class="mt-1.5">
          <button
            type="button"
            class="relative inline-flex h-6 w-11 items-center rounded-full transition-colors"
            :class="draft.enabled ? 'bg-cyan-500' : 'bg-gray-300 dark:bg-dark-600'"
            @click="draft.enabled = !draft.enabled"
          >
            <span class="inline-block h-4 w-4 transform rounded-full bg-white transition-transform" :class="draft.enabled ? 'translate-x-6' : 'translate-x-1'" />
          </button>
        </div>
      </label>
      <label class="text-xs font-medium text-gray-500 dark:text-gray-400">
        最小延迟（秒）
        <input v-model.number="draft.min_seconds" type="number" min="0.01" max="30" step="0.01" class="input mt-1.5 w-full" />
      </label>
      <label class="text-xs font-medium text-gray-500 dark:text-gray-400">
        最大延迟（秒）
        <input v-model.number="draft.max_seconds" type="number" min="0.01" max="30" step="0.01" class="input mt-1.5 w-full" />
      </label>
    </div>
    <p class="mt-2 text-[11px] text-gray-400">
      支持小数（0.01-30 秒）；每次请求在区间内随机。请求日志将并列展示上游首字与伪造首字两个时刻。
    </p>
  </div>
</template>
