<template>
  <section class="overflow-hidden rounded-xl border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900">
    <div class="flex flex-col gap-3 border-b border-gray-100 px-4 py-4 dark:border-dark-800 sm:flex-row sm:items-start sm:justify-between">
      <div class="flex min-w-0 items-start gap-3">
        <span class="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-primary-50 text-primary-600 dark:bg-primary-900/20 dark:text-primary-400">
          <Icon name="cpu" size="sm" />
        </span>
        <div>
          <div class="flex flex-wrap items-center gap-2">
            <h2 class="font-semibold text-gray-900 dark:text-white">{{ localText('高级调度运行策略', 'Advanced scheduling runtime policy') }}</h2>
            <span v-if="policy" class="badge badge-gray">rev {{ policy.revision }}</span>
          </div>
          <p class="mt-1 max-w-3xl text-xs leading-5 text-gray-500 dark:text-gray-400">
            {{ localText('保存后只影响下一次请求；正在进行的 HTTP、流式和 WebSocket 请求继续使用开始时冻结的策略。', 'Changes affect only the next request. In-flight HTTP, streaming, and WebSocket requests keep their start-time snapshot.') }}
          </p>
        </div>
      </div>
      <button type="button" class="btn btn-secondary btn-sm shrink-0" :disabled="loading || saving" @click="loadPolicy">
        <Icon name="refresh" size="xs" :class="loading ? 'animate-spin' : ''" />
        {{ localText('刷新策略', 'Refresh policy') }}
      </button>
    </div>

    <div v-if="loading && !policy" class="grid gap-3 p-4 sm:grid-cols-2">
      <div v-for="index in 4" :key="index" class="h-24 animate-pulse rounded-xl bg-gray-100 dark:bg-dark-800" />
    </div>

    <div v-else-if="policy" class="space-y-5 p-4">
      <div class="grid gap-3 lg:grid-cols-3">
        <div class="rounded-xl border border-emerald-200 bg-emerald-50 p-3 dark:border-emerald-900 dark:bg-emerald-950/25">
          <div class="text-sm font-semibold text-emerald-800 dark:text-emerald-300">previous_response_id</div>
          <p class="mt-1 text-xs leading-5 text-emerald-700 dark:text-emerald-400">{{ localText('固定硬绑定，不能被权重覆盖。', 'Always a hard binding and never overridden by weights.') }}</p>
        </div>
        <div class="rounded-xl border border-emerald-200 bg-emerald-50 p-3 dark:border-emerald-900 dark:bg-emerald-950/25">
          <div class="text-sm font-semibold text-emerald-800 dark:text-emerald-300">{{ localText('健康与容量门禁', 'Health and capacity gates') }}</div>
          <p class="mt-1 text-xs leading-5 text-emerald-700 dark:text-emerald-400">{{ localText('禁用、限流、错误、模型不符和满并发优先逃逸。', 'Disabled, limited, unhealthy, incompatible, or full accounts are escaped first.') }}</p>
        </div>
        <div class="rounded-xl border border-emerald-200 bg-emerald-50 p-3 dark:border-emerald-900 dark:bg-emerald-950/25">
          <div class="text-sm font-semibold text-emerald-800 dark:text-emerald-300">{{ localText('销售计费隔离', 'Sales billing isolation') }}</div>
          <p class="mt-1 text-xs leading-5 text-emerald-700 dark:text-emerald-400">{{ localText('采购成本只选账号，不改用户倍率和扣费。', 'Purchase cost selects accounts only; it never changes customer billing.') }}</p>
        </div>
      </div>

      <div class="flex flex-wrap gap-2">
        <button v-for="preset in presets" :key="preset.key" type="button" :class="['btn btn-sm', form.preset === preset.key ? 'btn-primary' : 'btn-secondary']" @click="applyPreset(preset.key)">
          {{ preset.label }}
        </button>
      </div>

      <div class="grid gap-3 md:grid-cols-2 xl:grid-cols-4">
        <label class="flex items-center justify-between gap-3 rounded-xl border border-gray-200 p-3 dark:border-dark-700">
          <span>
            <strong class="block text-sm text-gray-900 dark:text-white">{{ localText('高级调度', 'Advanced scheduler') }}</strong>
            <small class="text-gray-500 dark:text-gray-400">{{ localText('关闭时沿用稳定旧逻辑', 'Off keeps the stable legacy path') }}</small>
          </span>
          <Toggle v-model="form.advanced_enabled" @update:model-value="markCustom" />
        </label>
        <label class="flex items-center justify-between gap-3 rounded-xl border border-gray-200 p-3 dark:border-dark-700">
          <span>
            <strong class="block text-sm text-gray-900 dark:text-white">{{ localText('优先订阅账号池', 'Prefer subscription pool') }}</strong>
            <small class="text-gray-500 dark:text-gray-400">{{ localText('同等健康下轻微优先', 'A small bonus among healthy peers') }}</small>
          </span>
          <Toggle v-model="form.prefer_subscription_pool" @update:model-value="markCustom" />
        </label>
        <label class="flex items-center justify-between gap-3 rounded-xl border border-amber-200 bg-amber-50/60 p-3 dark:border-amber-900 dark:bg-amber-950/20">
          <span>
            <strong class="block text-sm text-amber-900 dark:text-amber-300">{{ localText('粘性软打分实验', 'Sticky soft-scoring experiment') }}</strong>
            <small class="text-amber-700 dark:text-amber-400">{{ localText('关闭时仍是确定性粘性', 'Off keeps deterministic stickiness') }}</small>
          </span>
          <Toggle v-model="form.sticky_soft_scoring_enabled" @update:model-value="markCustom" />
        </label>
        <label class="flex items-center justify-between gap-3 rounded-xl border border-gray-200 p-3 dark:border-dark-700">
          <span>
            <strong class="block text-sm text-gray-900 dark:text-white">{{ localText('采购成本排序', 'Purchase-cost ordering') }}</strong>
            <small class="text-gray-500 dark:text-gray-400">{{ localText('不进入用户计费', 'Excluded from customer billing') }}</small>
          </span>
          <Toggle v-model="form.upstream_cost_enabled" @update:model-value="markCustom" />
        </label>
      </div>

      <div class="rounded-xl border border-gray-200 p-4 dark:border-dark-700">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <div>
            <strong class="text-sm text-gray-900 dark:text-white">Top-K</strong>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">{{ localText('只在得分最高的少量合格账号中做平滑选择。', 'Smoothly select only among the highest-scoring eligible accounts.') }}</p>
          </div>
          <div class="flex items-center gap-2">
            <button type="button" class="btn btn-secondary btn-sm !px-3" :disabled="form.top_k <= 1" @click="changeTopK(-1)">−</button>
            <strong class="min-w-10 text-center text-lg text-gray-900 dark:text-white">{{ form.top_k }}</strong>
            <button type="button" class="btn btn-secondary btn-sm !px-3" :disabled="form.top_k >= 1000" @click="changeTopK(1)">+</button>
          </div>
        </div>
        <div class="mt-3 flex flex-wrap gap-2">
          <button v-for="value in [3, 7, 12, 20]" :key="value" type="button" :class="['btn btn-sm', form.top_k === value ? 'btn-primary' : 'btn-secondary']" @click="setTopK(value)">{{ value }}</button>
        </div>
      </div>

      <div class="grid gap-x-6 gap-y-3 lg:grid-cols-2">
        <label v-for="item in weightFields" :key="item.key" class="grid grid-cols-[minmax(7rem,1fr)_minmax(8rem,2fr)_3rem] items-center gap-3 rounded-lg border border-gray-100 px-3 py-2 dark:border-dark-800" :class="item.disabled ? 'opacity-45' : ''">
          <span class="text-sm text-gray-600 dark:text-gray-300">{{ item.label }}</span>
          <input v-model.number="form[item.key]" type="range" min="0" max="2" step="0.1" :disabled="item.disabled" class="w-full accent-primary-600" @input="markCustom" />
          <strong class="text-right text-sm tabular-nums text-gray-900 dark:text-white">{{ Number(form[item.key]).toFixed(1) }}</strong>
        </label>
      </div>

      <div v-if="message" :class="['rounded-lg border p-3 text-sm', messageTone === 'success' ? 'border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-900 dark:bg-emerald-950/25 dark:text-emerald-300' : 'border-red-200 bg-red-50 text-red-700 dark:border-red-900 dark:bg-red-950/25 dark:text-red-300']">
        {{ message }}
      </div>

      <div class="flex flex-col-reverse gap-2 border-t border-gray-100 pt-4 dark:border-dark-800 sm:flex-row sm:items-center sm:justify-between">
        <button type="button" class="btn btn-secondary" :disabled="saving" @click="resetPolicy">
          {{ localText('恢复安全默认', 'Restore safe defaults') }}
        </button>
        <button type="button" class="btn btn-primary" :disabled="saving" @click="savePolicy">
          <Icon v-if="saving" name="refresh" size="xs" class="animate-spin" />
          {{ saving ? localText('保存中', 'Saving') : localText('保存并热更新', 'Save and hot update') }}
        </button>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import Toggle from '@/components/common/Toggle.vue'
import {
  getOpenAISchedulerRuntimePolicy,
  resetOpenAISchedulerRuntimePolicy,
  updateOpenAISchedulerRuntimePolicy,
  type OpenAISchedulerPolicyPreset,
  type OpenAISchedulerRuntimePolicy,
  type UpdateOpenAISchedulerRuntimePolicy,
} from '@/api/admin/runtimeSummary'
import { extractApiErrorMessage } from '@/utils/apiError'

type WeightKey = 'priority_weight' | 'load_weight' | 'queue_weight' | 'error_rate_weight' | 'ttft_weight' | 'prefer_soonest_reset_weight' | 'quota_headroom_weight' | 'upstream_cost_weight' | 'session_sticky_weight'

const { locale } = useI18n()
const localText = (zh: string, en: string) => locale.value.toLowerCase().startsWith('zh') ? zh : en
const policy = ref<OpenAISchedulerRuntimePolicy | null>(null)
const loading = ref(false)
const saving = ref(false)
const message = ref('')
const messageTone = ref<'success' | 'error'>('success')

const form = reactive<UpdateOpenAISchedulerRuntimePolicy>({
  expected_revision: 0,
  preset: 'safe_default',
  advanced_enabled: false,
  sticky_soft_scoring_enabled: false,
  top_k: 7,
  prefer_subscription_pool: false,
  priority_weight: 1,
  load_weight: 1,
  queue_weight: 0.7,
  error_rate_weight: 0.8,
  ttft_weight: 0.5,
  prefer_soonest_reset_weight: 0,
  quota_headroom_weight: 0,
  upstream_cost_enabled: false,
  upstream_cost_weight: 0,
  session_sticky_weight: 0,
})

const presets = computed<Array<{ key: OpenAISchedulerPolicyPreset; label: string }>>(() => [
  { key: 'safe_default', label: localText('安全默认', 'Safe default') },
  { key: 'conservative', label: localText('保守', 'Conservative') },
  { key: 'balanced', label: localText('均衡', 'Balanced') },
  { key: 'low_latency', label: localText('低延迟', 'Low latency') },
])

const weightFields = computed<Array<{ key: WeightKey; label: string; disabled: boolean }>>(() => [
  { key: 'priority_weight', label: localText('账号优先级', 'Account priority'), disabled: !form.advanced_enabled },
  { key: 'load_weight', label: localText('当前负载', 'Current load'), disabled: !form.advanced_enabled },
  { key: 'queue_weight', label: localText('排队长度', 'Queue depth'), disabled: !form.advanced_enabled },
  { key: 'error_rate_weight', label: localText('错误率', 'Error rate'), disabled: !form.advanced_enabled },
  { key: 'ttft_weight', label: localText('首字速度', 'First-token speed'), disabled: !form.advanced_enabled },
  { key: 'prefer_soonest_reset_weight', label: localText('即将重置', 'Soonest reset'), disabled: !form.advanced_enabled },
  { key: 'quota_headroom_weight', label: localText('额度余量', 'Quota headroom'), disabled: !form.advanced_enabled },
  { key: 'upstream_cost_weight', label: localText('采购成本', 'Purchase cost'), disabled: !form.advanced_enabled || !form.upstream_cost_enabled },
  { key: 'session_sticky_weight', label: localText('会话粘性', 'Session stickiness'), disabled: !form.advanced_enabled || !form.sticky_soft_scoring_enabled },
])

function hydrate(value: OpenAISchedulerRuntimePolicy) {
  policy.value = value
  Object.assign(form, {
    expected_revision: value.revision,
    preset: value.preset,
    advanced_enabled: value.advanced_enabled,
    sticky_soft_scoring_enabled: value.sticky_soft_scoring_enabled,
    top_k: value.top_k,
    prefer_subscription_pool: value.prefer_subscription_pool,
    priority_weight: value.priority_weight,
    load_weight: value.load_weight,
    queue_weight: value.queue_weight,
    error_rate_weight: value.error_rate_weight,
    ttft_weight: value.ttft_weight,
    prefer_soonest_reset_weight: value.prefer_soonest_reset_weight,
    quota_headroom_weight: value.quota_headroom_weight,
    upstream_cost_enabled: value.upstream_cost_enabled,
    upstream_cost_weight: value.upstream_cost_weight,
    session_sticky_weight: value.session_sticky_weight,
  })
}

function markCustom() {
  form.preset = 'custom'
}

function applyPreset(preset: OpenAISchedulerPolicyPreset) {
  if (preset === 'safe_default') {
    Object.assign(form, { preset, advanced_enabled: false, sticky_soft_scoring_enabled: false, top_k: 7, prefer_subscription_pool: false, priority_weight: 1, load_weight: 1, queue_weight: 0.7, error_rate_weight: 0.8, ttft_weight: 0.5, prefer_soonest_reset_weight: 0, quota_headroom_weight: 0, upstream_cost_enabled: false, upstream_cost_weight: 0, session_sticky_weight: 0 })
  } else if (preset === 'conservative') {
    Object.assign(form, { preset, advanced_enabled: true, sticky_soft_scoring_enabled: false, top_k: 7, prefer_subscription_pool: true, priority_weight: 1, load_weight: 1, queue_weight: 0.7, error_rate_weight: 0.9, ttft_weight: 0.6, prefer_soonest_reset_weight: 0.2, quota_headroom_weight: 0.4, upstream_cost_enabled: true, upstream_cost_weight: 0.3, session_sticky_weight: 0 })
  } else if (preset === 'balanced') {
    Object.assign(form, { preset, advanced_enabled: true, sticky_soft_scoring_enabled: false, top_k: 10, prefer_subscription_pool: true, priority_weight: 0.8, load_weight: 1.2, queue_weight: 1, error_rate_weight: 1, ttft_weight: 0.8, prefer_soonest_reset_weight: 0.4, quota_headroom_weight: 0.7, upstream_cost_enabled: true, upstream_cost_weight: 0.6, session_sticky_weight: 0.5 })
  } else if (preset === 'low_latency') {
    Object.assign(form, { preset, advanced_enabled: true, sticky_soft_scoring_enabled: false, top_k: 5, prefer_subscription_pool: false, priority_weight: 0.4, load_weight: 1.4, queue_weight: 1.4, error_rate_weight: 1.2, ttft_weight: 1.5, prefer_soonest_reset_weight: 0, quota_headroom_weight: 0.3, upstream_cost_enabled: false, upstream_cost_weight: 0, session_sticky_weight: 0 })
  }
}

function setTopK(value: number) {
  form.top_k = value
  markCustom()
}

function changeTopK(delta: number) {
  setTopK(Math.min(1000, Math.max(1, form.top_k + delta)))
}

async function loadPolicy() {
  loading.value = true
  message.value = ''
  try {
    hydrate(await getOpenAISchedulerRuntimePolicy())
  } catch (error) {
    messageTone.value = 'error'
    message.value = extractApiErrorMessage(error, localText('调度策略读取失败。', 'Failed to load scheduler policy.'))
  } finally {
    loading.value = false
  }
}

async function savePolicy() {
  saving.value = true
  message.value = ''
  try {
    hydrate(await updateOpenAISchedulerRuntimePolicy({ ...form, expected_revision: policy.value?.revision ?? form.expected_revision }))
    messageTone.value = 'success'
    message.value = localText('策略已原子保存，只影响之后开始的请求。', 'Policy saved atomically and applies only to subsequent requests.')
  } catch (error) {
    messageTone.value = 'error'
    message.value = extractApiErrorMessage(error, localText('保存失败；若其他管理员已修改，请刷新后重试。', 'Save failed. Refresh and retry if another administrator changed it.'))
  } finally {
    saving.value = false
  }
}

async function resetPolicy() {
  if (!policy.value || !window.confirm(localText('恢复安全默认并关闭实验模式？', 'Restore safe defaults and disable experiments?'))) return
  saving.value = true
  message.value = ''
  try {
    hydrate(await resetOpenAISchedulerRuntimePolicy(policy.value.revision))
    messageTone.value = 'success'
    message.value = localText('已恢复安全默认。', 'Safe defaults restored.')
  } catch (error) {
    messageTone.value = 'error'
    message.value = extractApiErrorMessage(error, localText('恢复失败，请刷新后重试。', 'Reset failed. Refresh and retry.'))
  } finally {
    saving.value = false
  }
}

onMounted(loadPolicy)
</script>
