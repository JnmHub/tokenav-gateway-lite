<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import ipv6EgressAPI from '@/api/admin/ipv6Egress'
import type { IPv6EgressStatus } from '@/api/admin/ipv6Egress'
import Icon from '@/components/icons/Icon.vue'

// IPv6 出口自动管理卡片：识别本机全局 IPv6 → 源绑定测通 → 注册本地 CONNECT 代理。
// 配置保存即热生效（后端持久化 data/ipv6_egress.json，无需重启服务）。
const loading = ref(false)
const saving = ref(false)
const rescanning = ref(false)
const error = ref('')
const status = ref<IPv6EgressStatus | null>(null)

const draft = reactive({
  enabled: false,
  listen_host: '127.0.0.1',
  listen_port: 32600,
  probe_target: 'chatgpt.com:443',
  sync_interval_seconds: 300,
  max_addresses: 512,
})

function applyStatus(next: IPv6EgressStatus) {
  status.value = next
  draft.enabled = next.config.enabled
  draft.listen_host = next.config.listen_host
  draft.listen_port = next.config.listen_port
  draft.probe_target = next.config.probe_target
  draft.sync_interval_seconds = next.config.sync_interval_seconds
  draft.max_addresses = next.config.max_addresses
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    applyStatus(await ipv6EgressAPI.getStatus())
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
    applyStatus(await ipv6EgressAPI.updateConfig({ ...draft }))
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    saving.value = false
  }
}

async function rescan() {
  rescanning.value = true
  error.value = ''
  try {
    applyStatus(await ipv6EgressAPI.rescan())
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    rescanning.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="mb-4 rounded-xl border border-gray-200 bg-white p-5 dark:border-dark-700 dark:bg-dark-800">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div class="flex items-center gap-3">
        <span class="flex h-10 w-10 items-center justify-center rounded-xl bg-cyan-100 text-cyan-700 dark:bg-cyan-900/30 dark:text-cyan-300">
          <Icon name="shield" size="md" />
        </span>
        <div>
          <div class="flex items-center gap-2">
            <h3 class="text-sm font-semibold text-gray-900 dark:text-white">IPv6 出口自动管理</h3>
            <span v-if="status?.running" class="badge badge-success">运行中</span>
            <span v-else-if="draft.enabled" class="badge badge-warning">已启用未运行</span>
            <span v-else class="badge">未启用</span>
          </div>
          <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
            自动识别本机全局 IPv6 → 源绑定测通 → 注册为本地代理；保存即热生效，无需重启服务。
          </p>
        </div>
      </div>
      <div class="flex gap-2">
        <button class="btn btn-secondary btn-sm" :disabled="loading" @click="load">
          <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
        </button>
        <button class="btn btn-secondary btn-sm" :disabled="!draft.enabled || rescanning" @click="rescan">
          {{ rescanning ? '扫描中…' : '立即重扫' }}
        </button>
        <button class="btn btn-primary btn-sm" :disabled="saving" @click="save">
          {{ saving ? '保存中…' : '保存并应用' }}
        </button>
      </div>
    </div>

    <p v-if="error" class="mt-3 rounded-lg bg-rose-50 px-3 py-2 text-xs text-rose-600 dark:bg-rose-900/20 dark:text-rose-300">
      {{ error }}
    </p>

    <div class="mt-4 grid gap-3 sm:grid-cols-2 lg:grid-cols-6">
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
        监听地址
        <input v-model="draft.listen_host" type="text" class="input mt-1.5 w-full" placeholder="127.0.0.1" />
      </label>
      <label class="text-xs font-medium text-gray-500 dark:text-gray-400">
        监听端口
        <input v-model.number="draft.listen_port" type="number" min="1" max="65535" class="input mt-1.5 w-full" />
      </label>
      <label class="text-xs font-medium text-gray-500 dark:text-gray-400">
        测通目标
        <input v-model="draft.probe_target" type="text" class="input mt-1.5 w-full" placeholder="chatgpt.com:443" />
      </label>
      <label class="text-xs font-medium text-gray-500 dark:text-gray-400">
        重扫间隔（秒）
        <input v-model.number="draft.sync_interval_seconds" type="number" min="30" class="input mt-1.5 w-full" />
      </label>
      <label class="text-xs font-medium text-gray-500 dark:text-gray-400">
        地址数上限
        <input v-model.number="draft.max_addresses" type="number" min="1" max="4096" class="input mt-1.5 w-full" />
      </label>
    </div>

    <div v-if="status && status.entries.length" class="mt-4">
      <div class="mb-2 flex items-center justify-between text-xs text-gray-500 dark:text-gray-400">
        <span>
          共 {{ status.entries.length }} 个全局 IPv6 · 测通 {{ status.ok_count }} 个
          <template v-if="status.last_scan">· 上次扫描 {{ status.last_scan }}</template>
        </span>
        <span>测通地址已自动注册为代理（名称 {{ status.config.name_prefix }}-*），可在账号代理池中选用</span>
      </div>
      <div class="max-h-40 overflow-y-auto rounded-lg border border-gray-100 dark:border-dark-700">
        <div
          v-for="entry in status.entries"
          :key="entry.address"
          class="flex items-center gap-3 border-b border-gray-50 px-3 py-1.5 text-xs last:border-0 dark:border-dark-700/50"
        >
          <span class="inline-block h-2 w-2 flex-none rounded-full" :class="entry.status === 'ok' ? 'bg-emerald-500' : 'bg-rose-500'" />
          <code class="min-w-0 flex-1 truncate text-gray-800 dark:text-gray-200">{{ entry.address }}</code>
          <span v-if="entry.status === 'ok'" class="flex-none text-emerald-600 dark:text-emerald-300">{{ entry.latency_ms }}ms</span>
          <span v-else class="flex-none max-w-[40%] truncate text-rose-500" :title="entry.error">{{ entry.error || '测通失败' }}</span>
          <span v-if="entry.proxy_id" class="flex-none text-gray-400">#{{ entry.proxy_id }}</span>
        </div>
      </div>
    </div>
    <p v-else-if="status" class="mt-4 text-xs text-gray-400">
      {{ status.config.enabled ? '尚无扫描结果，点击"立即重扫"或等待自动扫描。' : '未启用：开启开关并保存后自动开始识别与测通。' }}
    </p>
  </div>
</template>
