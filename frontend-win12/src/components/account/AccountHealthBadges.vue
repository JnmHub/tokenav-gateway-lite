<script setup>
/* 列表只读健康徽章（对照旧 AccountUsageCell 的 codex 配额徽章 + dto.Account 只读字段）
 * - session_window_status：Claude 会话窗口状态（allowed/allowed_warning/rejected，
 *   来自 anthropic-ratelimit-unified-* 响应头），非空才显示；title 放窗口 start/end
 * - codex_quota_health：OpenAI OAuth 配额健康快照（x-codex-* 头，只读摘要不参与调度，
 *   旧 AccountUsageCell codexQuotaHealth 徽章等价：status 徽章 + 5h/7d 用量摘要）
 * - grok_media_eligibility：Grok 新媒体资格，effective_eligible=false 时警示，title 放 reason
 *   （旧 GrokQuotaProbeCell 语义：覆写与自动探测证据分离，这里只做只读展示） */
import { computed } from 'vue'
import { fmtDateTime } from './format'

const props = defineProps({ account: { type: Object, required: true } })

// Claude 会话窗口：非空才显示（used_percent 语义对齐后端：rejected≈100%、warning≈80%）
const SW_LABEL = { allowed: ['窗口内', 'hb-ok'], allowed_warning: ['窗口将满', 'hb-warn'], rejected: ['窗口已满', 'hb-err'] }
const sessionWindow = computed(() => {
  const a = props.account || {}
  const m = SW_LABEL[a.session_window_status]
  if (!m) return null
  const title = ['Claude 会话窗口']
  if (a.session_window_start) title.push(`开始 ${fmtDateTime(a.session_window_start)}`)
  if (a.session_window_end) title.push(`结束 ${fmtDateTime(a.session_window_end)}`)
  return { text: m[0], cls: m[1], title: title.join(' · ') }
})

// Codex 配额健康（used_percent 为 0-100 数值；状态取最严窗口，见 service/codex_quota_health.go）
const codexHealth = computed(() => {
  const a = props.account || {}
  const h = a.codex_quota_health
  if (!h || a.platform !== 'openai' || a.type !== 'oauth') return null
  const LABEL = {
    healthy: ['额度健康', 'hb-ok'],
    near_limit: ['接近上限', 'hb-warn'],
    exhausted: ['已耗尽', 'hb-err'],
    recovered_pending_refresh: ['待刷新恢复', 'hb-info'],
    unknown: ['状态未知', 'hb-gray'],
  }
  const m = LABEL[h.status] || LABEL.unknown
  const parts = [m[0]]
  const win = (label, w) => { if (w && w.used_percent != null) parts.push(`${label} ${Math.round(w.used_percent)}%`) }
  win('5h', h.five_hour)
  win('7d', h.seven_day)
  const title = [m[0]]
  if (h.trigger_window) title.push(`触发窗口 ${h.trigger_window}`)
  if (h.reason) title.push(h.reason)
  if (h.five_hour?.reset_at) title.push(`5h 重置 ${fmtDateTime(h.five_hour.reset_at)}`)
  if (h.seven_day?.reset_at) title.push(`7d 重置 ${fmtDateTime(h.seven_day.reset_at)}`)
  if (h.next_reset_at) title.push(`下次重置 ${fmtDateTime(h.next_reset_at)}`)
  if (h.observed_at) title.push(`观测于 ${fmtDateTime(h.observed_at)}`)
  if (h.stale) title.push('快照已过期')
  return { text: parts.join(' · '), cls: m[1], title: title.join(' · ') }
})

// Grok 新媒体资格：仅 effective_eligible=false 时警示（true/unknown 不打扰）
const grokEligibility = computed(() => {
  const a = props.account || {}
  const g = a.grok_media_eligibility
  if (!g || a.platform !== 'grok' || g.effective_eligible !== false) return null
  const title = ['Grok 新媒体生成不可用（effective_eligible=false）']
  if (g.reason) title.push(`原因：${g.reason}`)
  if (g.state) title.push(`状态：${g.state}`)
  if (g.source === 'override') title.push('管理员覆写生效中')
  if (g.stale) title.push('探测结果已过期')
  return { text: '媒体不可用', cls: 'hb-warn', title: title.join(' · ') }
})

// [v0.2.6] Codex 一小时票据（x-codex-turn-state）：有票据数据才显示，徽章文本 = 就绪数/总数
const codexTickets = computed(() => {
  const a = props.account || {}
  const list = a.codex_turn_tickets
  if (!Array.isArray(list) || !list.length) return null
  const ready = list.filter(t => t.ready).length
  const title = ['Codex 票据（x-codex-turn-state）']
  list.forEach(t => {
    title.push(`${t.model}：${t.ready ? '就绪' : (t.blocked ? 'blocked' : '未就绪')}${t.remaining_seconds ? `，剩余 ${Math.round(t.remaining_seconds / 60)} 分钟` : ''}`)
  })
  return {
    text: `Codex 票 ${ready}/${list.length}`,
    cls: ready > 0 ? 'hb-ok' : 'hb-warn',
    title: title.join(' · '),
  }
})
// [2026-09 上游规则变更] Codex 路由 Cookie 池（__cfblb/__oailb）：292 维持的
// 路由凭证，有池数据才显示；就绪数/总数徽章，悬停显示各 Cookie 状态。
const codexCookies = computed(() => {
  const a = props.account || {}
  const list = a.codex_route_cookies
  if (!Array.isArray(list) || !list.length) return null
  const ready = list.filter(c => c.ready).length
  const title = ['Codex 路由 Cookie（__cfblb / __oailb）']
  list.forEach(c => {
    const rem = c.remaining_seconds ? `，剩余 ${Math.round(c.remaining_seconds / 60)} 分钟` : ''
    title.push(`${c.name}：${c.ready ? '就绪' : (c.stale ? '疑似过期' : '无值')}${rem}`)
  })
  return {
    text: `Cookie 池 ${ready}/${list.length}`,
    cls: ready > 0 ? 'hb-ok' : 'hb-warn',
    title: title.join(' · '),
  }
})
</script>
<template>
  <div v-if="sessionWindow || codexHealth || grokEligibility || codexTickets || codexCookies" class="hbadges">
    <span v-if="sessionWindow" :class="['hb', sessionWindow.cls]" :title="sessionWindow.title">{{ sessionWindow.text }}</span>
    <span v-if="codexHealth" :class="['hb', codexHealth.cls]" :title="codexHealth.title">{{ codexHealth.text }}</span>
    <span v-if="grokEligibility" :class="['hb', grokEligibility.cls]" :title="grokEligibility.title">{{ grokEligibility.text }}</span>
    <span v-if="codexTickets" :class="['hb', codexTickets.cls]" :title="codexTickets.title">{{ codexTickets.text }}</span>
    <span v-if="codexCookies" :class="['hb', codexCookies.cls]" :title="codexCookies.title">{{ codexCookies.text }}</span>
  </div>
</template>

<style scoped>
/* 状态标签：高 20px、圆角 4px、低饱和半透明底（Win12 规范） */
.hbadges { display: flex; align-items: center; gap: 4px; flex-wrap: wrap; }
.hb { display: inline-block; height: 20px; line-height: 20px; padding: 0 6px; border-radius: 4px; font-size: 11px; white-space: nowrap; cursor: default; }
.hb-ok { background: rgba(82, 196, 26, 0.14); color: #389e0d; }
.hb-warn { background: rgba(212, 140, 20, 0.14); color: #a86307; }
.hb-err { background: rgba(207, 48, 48, 0.13); color: #cf3030; }
.hb-info { background: rgba(22, 104, 220, 0.12); color: #1668dc; }
.hb-gray { background: rgba(128, 128, 128, 0.14); color: var(--text2); }
</style>
