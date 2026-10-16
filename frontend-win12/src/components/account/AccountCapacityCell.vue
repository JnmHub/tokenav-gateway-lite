<script setup>
/* 并发/负载列单元格（三期）——旧实现契约：frontend/src/components/account/AccountCapacityCell.vue
 * 展示规则与旧版一致：
 * - 并发槽位：current_concurrency / concurrency（满=红，占用=黄，空闲=灰）
 * - 5h 窗口费用（仅 anthropic oauth/setup-token 且 window_cost_limit>0）
 * - 会话数（同上平台且 max_sessions>0）
 * - RPM（同上平台且 base_rpm>0，[T]=tiered / [S]=sticky_exempt）
 * - API Key 账号配额（quota_daily/weekly/total） */
import { computed } from 'vue'
import { fmtCost } from './format'

const props = defineProps({ account: { type: Object, required: true } })

const isAnthropicAuth = computed(() =>
  props.account.platform === 'anthropic' && ['oauth', 'setup-token'].includes(props.account.type))

const conc = computed(() => ({
  cur: props.account.current_concurrency || 0,
  max: props.account.concurrency,
  cls: (props.account.current_concurrency || 0) >= props.account.concurrency ? 'c-red'
    : (props.account.current_concurrency || 0) > 0 ? 'c-yellow' : 'c-gray',
}))

const windowCost = computed(() => {
  const a = props.account
  if (!isAnthropicAuth.value || a.window_cost_limit == null || a.window_cost_limit <= 0) return null
  const cur = a.current_window_cost ?? 0
  const limit = a.window_cost_limit
  const reserve = a.window_cost_sticky_reserve || 10
  const cls = cur >= limit + reserve ? 'c-red' : cur >= limit ? 'c-orange' : cur >= limit * 0.8 ? 'c-yellow' : 'c-green'
  const tip = cur >= limit + reserve ? '已触顶（仅 sticky 会话可用）' : cur >= limit ? '已到限额（sticky 保留区）' : '正常'
  return { text: `$${fmtCost(cur)} / $${fmtCost(limit)}`, cls, tip }
})

const sessions = computed(() => {
  const a = props.account
  if (!isAnthropicAuth.value || a.max_sessions == null || a.max_sessions <= 0) return null
  const cur = a.active_sessions ?? 0
  const max = a.max_sessions
  const cls = cur >= max ? 'c-red' : cur >= max * 0.8 ? 'c-yellow' : 'c-green'
  return { text: `${cur}/${max} 会话`, cls, tip: `会话空闲超时 ${a.session_idle_timeout_minutes || 5} 分钟` }
})

const rpm = computed(() => {
  const a = props.account
  if (!isAnthropicAuth.value || a.base_rpm == null || a.base_rpm <= 0) return null
  const cur = a.current_rpm ?? 0
  const base = a.base_rpm
  const strategy = a.rpm_strategy || 'tiered'
  const buffer = a.rpm_sticky_buffer ?? Math.max(1, Math.floor(base / 5))
  let cls = 'c-green'
  if (strategy === 'tiered') cls = cur >= base + buffer ? 'c-red' : cur >= base ? 'c-orange' : cur >= base * 0.8 ? 'c-yellow' : 'c-green'
  else cls = cur >= base ? 'c-orange' : cur >= base * 0.8 ? 'c-yellow' : 'c-green'
  return { text: `${cur}/${base} RPM ${strategy === 'sticky_exempt' ? '[S]' : '[T]'}`, cls, tip: `缓冲 ${buffer}` }
})

const quota = computed(() => {
  const a = props.account
  if (!['apikey', 'bedrock'].includes(a.type)) return []
  const list = []
  if (a.quota_daily_limit > 0) list.push({ label: `D ${a.quota_daily_used ?? 0}/${a.quota_daily_limit}`, cls: (a.quota_daily_used ?? 0) >= a.quota_daily_limit ? 'c-red' : 'c-gray' })
  if (a.quota_weekly_limit > 0) list.push({ label: `W ${a.quota_weekly_used ?? 0}/${a.quota_weekly_limit}`, cls: (a.quota_weekly_used ?? 0) >= a.quota_weekly_limit ? 'c-red' : 'c-gray' })
  if (a.quota_limit > 0) list.push({ label: `总 ${a.quota_used ?? 0}/${a.quota_limit}`, cls: (a.quota_used ?? 0) >= a.quota_limit ? 'c-red' : 'c-gray' })
  return list
})
</script>

<template>
  <div class="capcell">
    <span :class="['pill', conc.cls]" :title="`并发占用 ${conc.cur}/${conc.max}`">{{ conc.cur }}/{{ conc.max }} 并发</span>
    <span v-if="windowCost" :class="['pill', windowCost.cls]" :title="windowCost.tip">{{ windowCost.text }} 5h</span>
    <span v-if="sessions" :class="['pill', sessions.cls]" :title="sessions.tip">{{ sessions.text }}</span>
    <span v-if="rpm" :class="['pill', rpm.cls]" :title="rpm.tip">{{ rpm.text }}</span>
    <span v-for="q in quota" :key="q.label" :class="['pill', q.cls]">{{ q.label }}</span>
  </div>
</template>

<style scoped>
.capcell { display: flex; flex-direction: column; gap: 3px; align-items: flex-start; }
.pill { display: inline-block; padding: 1px 6px; border-radius: 4px; font-size: 11px; line-height: 18px; white-space: nowrap; }
.c-green { background: rgba(82, 196, 26, 0.13); color: #389e0d; }
.c-yellow { background: rgba(212, 140, 20, 0.14); color: #a86307; }
.c-orange { background: rgba(245, 108, 30, 0.15); color: #c05614; }
.c-red { background: rgba(207, 48, 48, 0.13); color: #cf3030; }
.c-gray { background: rgba(128, 128, 128, 0.12); color: var(--text2); }
</style>
