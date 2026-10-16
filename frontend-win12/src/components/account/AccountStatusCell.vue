<script setup>
/* 状态列单元格（三期）——旧实现契约：frontend/src/components/account/AccountStatusIndicator.vue
 * 徽章优先级与旧版一致：
 * 1. 限流中（rate_limit_reset_at > now）：「限流」徽章 + 自动恢复倒计时 + 429 角标
 * 2. 过载中（overload_until > now）：「过载」徽章 + 倒计时 + 529 角标
 * 3. 其余：主徽章——临时停调（可点开详情）/ error / inactive / 暂停调度 / 正常
 * 4. error_message 悬浮提示；extra.model_rate_limits 模型级限流徽章（AICredits=积分耗尽） */
import { computed } from 'vue'
import { WarningOutlined } from '@ant-design/icons-vue'
import { fmtCountdown, fmtDateTime } from './format'

const props = defineProps({ account: { type: Object, required: true } })
const emit = defineEmits(['show-temp-unsched'])

const isFuture = (t) => t && new Date(t).getTime() > Date.now()
const isRateLimited = computed(() => isFuture(props.account.rate_limit_reset_at))
const isOverloaded = computed(() => isFuture(props.account.overload_until))
const isTempUnsched = computed(() => isFuture(props.account.temp_unschedulable_until))
const hasError = computed(() => props.account.status === 'error')

const mainBadge = computed(() => {
  const a = props.account
  if (hasError.value) return { text: '错误', cls: 'st-err' }
  if (isTempUnsched.value) return { text: '临时停调', cls: 'st-warn', click: true }
  if (a.status !== 'active') return { text: '已停用', cls: 'st-gray' }
  if (!a.schedulable) return { text: '已暂停', cls: 'st-gray' }
  return { text: '正常', cls: 'st-ok' }
})

// 模型级限流（extra.model_rate_limits），AICredits 键代表积分耗尽（旧 activeModelStatuses）
const modelLimits = computed(() => {
  const limits = props.account.extra?.model_rate_limits
  if (!limits || typeof limits !== 'object') return []
  const now = Date.now()
  const allowOverages = props.account.extra?.allow_overages === true
  const creditsEntry = limits.AICredits
  const creditsActive = creditsEntry && new Date(creditsEntry.rate_limit_reset_at).getTime() > now
  const items = []
  for (const [model, info] of Object.entries(limits)) {
    if (!info?.rate_limit_reset_at || new Date(info.rate_limit_reset_at).getTime() <= now) continue
    if (model === 'AICredits') items.push({ model, kind: 'exhausted', reset: info.rate_limit_reset_at })
    else if (allowOverages && !creditsActive) items.push({ model, kind: 'credits', reset: info.rate_limit_reset_at })
    else items.push({ model, kind: 'limited', reset: info.rate_limit_reset_at })
  }
  return items.slice(0, 4)
})
const moreLimits = computed(() => {
  const limits = props.account.extra?.model_rate_limits
  if (!limits) return 0
  const now = Date.now()
  return Object.values(limits).filter((i) => i?.rate_limit_reset_at && new Date(i.rate_limit_reset_at).getTime() > now).length - 4
})
</script>

<template>
  <div class="stcell">
    <!-- 限流（429）双行展示 -->
    <template v-if="isRateLimited">
      <div class="row"><span class="badge st-warn">限流</span><span class="dim">{{ fmtCountdown(account.rate_limit_reset_at) }}后自动恢复</span></div>
      <div class="row"><a-tag color="warning" class="mini"><WarningOutlined /> 429</a-tag><span class="dim">{{ fmtDateTime(account.rate_limit_reset_at) }}</span></div>
    </template>
    <!-- 过载（529）双行展示 -->
    <template v-else-if="isOverloaded">
      <div class="row"><span class="badge st-err">过载</span><span class="dim">{{ fmtCountdown(account.overload_until) }}</span></div>
      <div class="row"><a-tag color="error" class="mini"><WarningOutlined /> 529</a-tag></div>
    </template>
    <!-- 主徽章 -->
    <div v-else class="row">
      <span
        :class="['badge', mainBadge.cls]"
        :style="mainBadge.click ? 'cursor: pointer' : ''"
        :title="mainBadge.click ? '点击查看临时停调详情' : ''"
        @click="mainBadge.click && emit('show-temp-unsched', account)"
      >{{ mainBadge.text }}</span>
      <a-tooltip v-if="hasError && account.error_message" :title="account.error_message">
        <span class="errinfo">ⓘ</span>
      </a-tooltip>
    </div>
    <!-- 模型级限流徽章（最多 4 个） -->
    <div v-for="m in modelLimits" :key="m.model" class="row">
      <span :class="['badge', m.kind === 'exhausted' ? 'st-err' : m.kind === 'credits' ? 'st-warn' : 'st-model']">
        {{ m.kind === 'exhausted' ? '积分耗尽' : m.model }}
        <i class="rs">{{ fmtCountdown(m.reset) }}</i>
      </span>
    </div>
    <div v-if="moreLimits > 0" class="dim">+{{ moreLimits }} 个模型限流</div>
  </div>
</template>

<style scoped>
.stcell { display: flex; flex-direction: column; gap: 2px; }
.row { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.badge { display: inline-flex; align-items: center; gap: 4px; height: 20px; padding: 0 6px; border-radius: 4px; font-size: 11px; white-space: nowrap; }
.badge i.rs { font-style: normal; opacity: 0.7; font-size: 10px; }
.st-ok { background: rgba(82, 196, 26, 0.14); color: #389e0d; }
.st-err { background: rgba(207, 48, 48, 0.13); color: #cf3030; }
.st-warn { background: rgba(212, 140, 20, 0.14); color: #a86307; }
.st-gray { background: rgba(128, 128, 128, 0.14); color: var(--text2); }
.st-model { background: rgba(130, 90, 200, 0.14); color: #7048b6; }
.mini { font-size: 10px; line-height: 16px; height: 16px; padding: 0 4px; margin: 0; }
.dim { font-size: 11px; color: var(--text2); }
.errinfo { cursor: help; color: #cf3030; font-size: 12px; }
</style>
