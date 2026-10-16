<script setup>
/* 行内用量：周额度是主信息（进度 + 按 1% 档反推总额），5h 作辅条。
 * 金额取服务端 WindowStats.cost（账号采购），不在前端用当前倍率倒算。 */
import { ref, computed, watch } from 'vue'
import { ReloadOutlined, UndoOutlined } from '@ant-design/icons-vue'
import { fmtRelative, fmtNum, fmtMoney, fmtMoneyCompact, fmtTokens } from './format'
import { message, Modal } from 'ant-design-vue'
import { enqueueUsage, getAccountUsage, readUsageCache, writeUsageCache, readUsageInflight, trackUsageInflight, refreshOpenAIQuota, resetOpenAIQuota } from './accountsApi'
import { getSessionEpoch, isCancellation } from '../../api/index.js'
import { useRequestLifetime } from '../../composables/useRequestLifetime.js'
import { estimateWeekQuota } from './weekQuotaEstimate'

const props = defineProps({
  account: { type: Object, required: true },
  todayStats: { type: Object, default: null },
  todayLoading: { type: Boolean, default: false },
  refreshToken: { type: Number, default: 0 },
  embedToday: { type: Boolean, default: false },
})
const emit = defineEmits(['detail'])

const info = ref(null)
const loading = ref(false)
const probing = ref(false)
const error = ref('')

// OpenAI OAuth 重置券：先从账号 extra 的安全快照恢复，列表首屏不主动请求上游。
// 用户点击刷新才访问 /quota/refresh；真正消耗前必须二次确认。
const resetCredits = ref(null)
const resetCreditLoading = ref(false)
const resetCreditBusy = ref(false)
const isOpenAIOAuth = computed(() => props.account?.platform === 'openai' && props.account?.type === 'oauth')
const normalizeResetCredits = (raw) => {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null
  const count = Number(raw.available_count)
  if (!Number.isFinite(count) || count < 0) return null
  const now = Date.now()
  const credits = Array.isArray(raw.credits) ? raw.credits.filter((x) => {
    if (!x || typeof x !== 'object') return false
    if (!x.expires_at) return true
    const at = new Date(x.expires_at).getTime()
    return Number.isFinite(at) && at > now
  }) : []
  // 带明细的快照必须以未过期明细为上限；不能把过期券继续显示为可重置。
  // 兼容极早期仅有 count 的快照时保留该数值，交由下一次主动刷新校准。
  if (Array.isArray(raw.credits)) return credits.length ? Math.min(Math.floor(count), credits.length) : (count > 0 ? null : 0)
  return Math.floor(count)
}
const seedResetCredits = (account) => normalizeResetCredits(account?.extra?.codex_reset_credit_snapshot)
const refreshResetCredits = async (ev) => {
  ev?.stopPropagation?.()
  if (!isOpenAIOAuth.value || resetCreditLoading.value || resetCreditBusy.value) return
  resetCreditLoading.value = true
  try {
    const data = await refreshOpenAIQuota(props.account.id)
    resetCredits.value = normalizeResetCredits(data?.rate_limit_reset_credits)
    message.success(`重置次数已刷新：${resetCredits.value ?? 0}`)
  } catch (e) { message.error(`刷新重置次数失败：${e?.message || '未知错误'}`) }
  finally { resetCreditLoading.value = false }
}
const consumeResetCredit = (ev) => {
  ev?.stopPropagation?.()
  const count = Number(resetCredits.value) || 0
  if (!isOpenAIOAuth.value || resetCreditBusy.value) return
  if (count <= 0) { message.warning('没有可用重置次数，请先刷新确认'); return }
  Modal.confirm({
    title: '确认消耗一次 OpenAI 重置次数？',
    content: `当前可用 ${count} 次。确认后将立即请求上游重置该账号额度，操作不可撤销。`,
    okText: '确认重置', cancelText: '取消', okType: 'danger',
    onOk: async () => {
      resetCreditBusy.value = true
      try {
        const result = await resetOpenAIQuota(props.account.id)
        resetCredits.value = normalizeResetCredits(result?.quota?.rate_limit_reset_credits)
        if (resetCredits.value == null) resetCredits.value = Math.max(0, count - 1)
        message.success(`额度已重置${Number(result?.windows_reset) ? `（${result.windows_reset} 个窗口）` : ''}`)
        await load(true)
      } catch (e) { message.error(`额度重置失败：${e?.message || '未知错误'}`) }
      finally { resetCreditBusy.value = false }
    },
  })
}
// 请求代次：账号切换或新请求发起后，旧响应不得再写回，避免串号与旧数据覆盖新的强制查询。
let requestSeq = 0
const lifetime = useRequestLifetime(() => { requestSeq += 1; info.value = null; resetCredits.value = null; error.value = '' })

const shouldFetch = computed(() => {
  const a = props.account || {}
  if (a.platform === 'anthropic') return ['oauth', 'setup-token'].includes(a.type)
  if (a.platform === 'openai' || a.platform === 'grok' || a.platform === 'antigravity') return a.type === 'oauth'
  if (a.platform === 'gemini') return true
  // Kimi/智谱/DeepSeek 的行内数据来自账号快照，无需额外请求上游。
  return false
})
const usageSource = computed(() => (props.account?.platform === 'anthropic' ? 'passive' : 'active'))

// 国产平台：5 小时 / 周 用量来自账号快照（extra），与旧前端的 CNProviderQuotaCell 等价。
const cnRows = computed(() => {
  const p = props.account?.platform
  if (!['kimi', 'zhipu', 'deepseek'].includes(p)) return []
  const extra = props.account?.extra || {}
  return [
    ['5小时', extra[`${p}_5h_used_percent`], extra[`${p}_5h_reset_at`]],
    ['周', extra[`${p}_weekly_used_percent`], extra[`${p}_weekly_reset_at`]],
  ]
    .filter(([, value]) => Number.isFinite(Number(value)))
    .map(([label, utilization, resets_at]) => ({ label, utilization: Number(utilization), resets_at }))
})

const seedFromAccount = (a) => {
  if (!a) return {}
  const extra = a.extra || {}
  const health = a.codex_quota_health || {}
  const num = (v) => { const n = Number(v); return Number.isFinite(n) ? n : null }
  const win = (util, reset) => (util == null ? null : { utilization: util, resets_at: reset || null })
  const five = health.five_hour
  const seven = health.seven_day
  return {
    five_hour: win(
      five?.used_percent ?? (num(extra.codex_5h_used_percent) ?? (num(extra.session_window_utilization) != null ? extra.session_window_utilization * 100 : null)),
      five?.reset_at || extra.codex_5h_reset_at || a.session_window_end,
    ),
    seven_day: win(
      seven?.used_percent ?? (num(extra.codex_7d_used_percent) ?? (num(extra.passive_usage_7d_utilization) != null ? extra.passive_usage_7d_utilization * 100 : null)),
      seven?.reset_at || extra.codex_7d_reset_at || extra.passive_usage_7d_reset,
    ),
  }
}

const display = computed(() => {
  const api = info.value || {}
  const seed = seedFromAccount(props.account)
  return {
    ...seed,
    ...api,
    five_hour: api.five_hour || seed.five_hour,
    seven_day: api.seven_day || seed.seven_day,
  }
})

const load = async (force = false) => {
  if (!shouldFetch.value) return
  const id = String(props.account?.id || '')
  if (!id) return
  const source = usageSource.value
  const cacheKey = `${id}:${source}`
  const seq = ++requestSeq
  const epoch = getSessionEpoch()

  // TTL 内直接复用，不再排队打上游（列表刷新/窗口重挂载不应重复请求）。
  if (!force) {
    const cached = readUsageCache(cacheKey)
    if (cached) {
      info.value = cached
      error.value = ''
      loading.value = false
      return
    }
  }

  loading.value = !info.value
  error.value = ''
  try {
    // 非强制查询复用进行中的同一请求，避免同账号并发重复打上游。
    let request = force ? null : readUsageInflight(cacheKey)
    if (!request) {
      request = enqueueUsage(() => getAccountUsage(id, { source, force, sessionEpoch: epoch }))
      if (!force) trackUsageInflight(cacheKey, request)
    }
    const data = (await request) || {}
    // 代次/账号双校验：旧请求晚到也不能写回。
    if (!lifetime.isAlive() || epoch !== getSessionEpoch() || seq !== requestSeq || String(props.account?.id || '') !== id) return
    info.value = data
    writeUsageCache(cacheKey, data, epoch)
  } catch (e) {
    if (!isCancellation(e) && lifetime.isAlive() && seq === requestSeq && !info.value) error.value = e?.message || '用量读取失败'
  } finally {
    if (seq === requestSeq) loading.value = false
  }
}
const probe = async (ev) => {
  ev?.stopPropagation?.()
  probing.value = true
  // Claude 的被动采样分支 force 会被后端忽略，此时按钮语义是"刷新本地采样"。
  try { await load(usageSource.value !== 'passive') } finally { probing.value = false }
}
watch(() => props.account, (account) => {
  requestSeq += 1
  info.value = null
  error.value = ''
  resetCredits.value = seedResetCredits(account)
  load()
}, { immediate: true })
watch(() => props.refreshToken, (n, o) => { if (o !== undefined && n !== o) load() })

const weekWin = computed(() => {
  const x = display.value
  // Anthropic 周窗口的占比与窗口内采购成本同源，可安全反推总额。
  if (x.seven_day) return { label: '周额度', d: x.seven_day, estimateable: true }
  const b = x.grok_billing
  // Grok 官方周占比与本地 7d 采购成本口径不同，只并列展示，不反推总额。
  if (b?.period_type === 'weekly' && b.usage_percent != null) {
    return { label: 'Grok 周配额', d: { utilization: b.usage_percent, resets_at: b.period_end, window_stats: x.grok_local_usage_7d }, estimateable: false }
  }
  return null
})

// Antigravity 多模型配额（后端 antigravity_quota），最多展示 4 条避免撑高行。
const antigravityRows = computed(() => {
  const quota = display.value.antigravity_quota || {}
  return Object.entries(quota).slice(0, 4).map(([model, d]) => ({
    label: String(model).replace(/^gemini-/, '').replace(/^claude-/, 'Claude '),
    utilization: Number(d?.utilization) || 0,
  }))
})

// 后端降级状态：用户必须能看出账号为什么不可用，而不是只看到空白。
const stateHint = computed(() => {
  const x = display.value
  if (x.needs_reauth) return { tone: 'warn', text: '需要重新授权' }
  if (x.is_forbidden) return { tone: 'bad', text: x.forbidden_type === 'validation' ? '需要完成验证' : '上游已禁止访问' }
  if (x.error_code === 'rate_limited') return { tone: 'warn', text: '上游限流中' }
  if (x.error) return { tone: 'warn', text: String(x.error) }
  return null
})

const extras = computed(() => {
  const x = display.value
  const list = []
  if (x.five_hour) list.push({ label: '5小时', d: x.five_hour })
  if (x.seven_day_sonnet) list.push({ label: 'Sonnet', d: x.seven_day_sonnet })
  if (x.seven_day_fable) list.push({ label: 'Fable', d: x.seven_day_fable })
  if (x.thirty_day) list.push({ label: '30天', d: x.thirty_day })
  const daily = x.gemini_shared_daily || x.gemini_pro_daily || x.gemini_flash_daily
  if (daily) list.push({ label: '日额度', d: daily })
  return list
})

const keyQuota = computed(() => {
  const a = props.account || {}
  return [
    ['日', a.quota_daily_used, a.quota_daily_limit],
    ['周', a.quota_weekly_used, a.quota_weekly_limit],
    ['总', a.quota_used, a.quota_limit],
  ].filter(([, , limit]) => Number(limit) > 0).map(([label, used, limit]) => ({
    label,
    utilization: Math.min(100, ((Number(used) || 0) / Number(limit)) * 100),
    text: `${fmtNum(used)} / ${fmtNum(limit)}`,
  }))
})

const weekEst = ref(null)
watch(
  () => [props.account?.id, weekWin.value?.estimateable, weekWin.value?.d?.utilization, weekWin.value?.d?.window_stats?.cost, weekWin.value?.d?.resets_at],
  () => {
    const w = weekWin.value
    if (!w || !w.estimateable) { weekEst.value = null; return }
    weekEst.value = estimateWeekQuota(props.account.id, w.d.utilization, w.d.window_stats?.cost, w.d.resets_at)
  },
  { immediate: true },
)

const pctNum = (v) => Math.max(0, Math.min(100, Number(v) || 0))
const barCls = (u) => (u >= 100 ? 'full' : u >= 80 ? 'hot' : u >= 1 ? 'ok' : 'idle')
const resetHint = (d) => {
  if (!d?.resets_at) return ''
  const rel = fmtRelative(d.resets_at)
  if (rel === '-' || rel === '刚刚') return ''
  return rel.endsWith('后') ? `${rel}重置` : rel
}
const hasMain = computed(() => weekWin.value || extras.value.length || keyQuota.value.length || cnRows.value.length || antigravityRows.value.length || stateHint.value || (isOpenAIOAuth.value && resetCredits.value != null))
const showToday = computed(() => props.embedToday && (props.todayStats || props.todayLoading))
</script>

<template>
  <div class="ucard" :class="{ clickable: shouldFetch, bare: !hasMain && !loading && !error }" @click="shouldFetch && emit('detail')">
    <div v-if="loading && !hasMain" class="usk">
      <i /><i /><i />
    </div>
    <span v-else-if="error && !hasMain" class="uerr" :title="error">{{ error }}</span>
    <template v-else>
      <div v-if="weekWin" class="week">
        <div class="hd">
          <span class="k">{{ weekWin.label }}</span>
          <span class="p">{{ Math.round(pctNum(weekWin.d.utilization)) }}%</span>
          <button v-if="shouldFetch" type="button" class="ref" :disabled="probing" :title="usageSource === 'passive' ? '刷新本地采样' : '主动查询上游'" @click="probe">
            <ReloadOutlined :spin="probing" />
          </button>
        </div>
        <div class="track"><b :class="barCls(weekWin.d.utilization)" :style="{ width: pctNum(weekWin.d.utilization) + '%' }" /></div>
        <div v-if="weekEst?.ready && weekWin.estimateable" class="est" :title="`采购成本口径：按周占比 ${weekEst.pct}% 与窗口内账号采购成本 ${fmtMoney(weekEst.used)} 反推，占比每变化 1% 重算一次`">
          <span class="est-total">{{ fmtMoneyCompact(weekEst.total) }}</span>
          <span class="est-sub">预估总额 · 剩 {{ fmtMoneyCompact(weekEst.remaining) }}</span>
        </div>
        <div v-else-if="weekWin.estimateable && weekEst?.reason === 'lt1'" class="hint">占比不足 1%，暂不预估总额</div>
        <div v-else-if="weekWin.estimateable && weekEst?.reason === 'nocost'" class="hint">等待窗口计费后预估总额</div>
        <div v-if="weekWin.d.window_stats" class="chips">
          <span title="账号采购成本：实际上游模型价格 × 账号倍率">账 {{ fmtMoney(weekWin.d.window_stats.cost) }}</span>
          <span title="用户实扣：actual_cost，受分组倍率影响">户 {{ fmtMoney(weekWin.d.window_stats.user_cost) }}</span>
          <span title="标准价：total_cost，不含倍率">标 {{ fmtMoney(weekWin.d.window_stats.standard_cost) }}</span>
        </div>
        <div v-if="resetHint(weekWin.d)" class="hint">{{ resetHint(weekWin.d) }}</div>
      </div>

      <div v-for="w in cnRows" :key="`cn-${w.label}`" class="side">
        <span class="k">{{ w.label }}</span>
        <div class="track slim"><b :class="barCls(w.utilization)" :style="{ width: pctNum(w.utilization) + '%' }" /></div>
        <span class="p">{{ Math.round(pctNum(w.utilization)) }}%</span>
      </div>

      <div v-for="w in antigravityRows" :key="`ag-${w.label}`" class="side">
        <span class="k ellip" :title="w.label">{{ w.label }}</span>
        <div class="track slim"><b :class="barCls(w.utilization)" :style="{ width: pctNum(w.utilization) + '%' }" /></div>
        <span class="p">{{ Math.round(pctNum(w.utilization)) }}%</span>
      </div>

      <!-- OpenAI OAuth 重置次数：快照值首屏可见；刷新才请求上游；消耗必须确认。 -->
      <div v-if="isOpenAIOAuth" class="reset-credit" @click.stop>
        <span class="k">重置次数</span>
        <span class="reset-count" :class="{ unknown: resetCredits == null }">{{ resetCredits == null ? '未读取' : `${resetCredits} 次` }}</span>
        <button type="button" class="credit-btn" :disabled="resetCreditLoading || resetCreditBusy" title="主动查询上游并更新重置次数" @click="refreshResetCredits">
          <ReloadOutlined :spin="resetCreditLoading" /> 刷新
        </button>
        <button type="button" class="credit-btn danger" :disabled="resetCreditLoading || resetCreditBusy || !(Number(resetCredits) > 0)" :title="Number(resetCredits) > 0 ? '消耗一次重置次数并重置额度' : '没有可用重置次数'" @click="consumeResetCredit">
          <UndoOutlined :spin="resetCreditBusy" /> 重置
        </button>
      </div>

      <div v-if="stateHint" :class="['hint', stateHint.tone]" :title="display.error || ''">{{ stateHint.text }}</div>

      <div v-for="w in extras" :key="w.label" class="side">
        <span class="k">{{ w.label }}</span>
        <div class="track slim"><b :class="barCls(w.d.utilization)" :style="{ width: pctNum(w.d.utilization) + '%' }" /></div>
        <span class="p">{{ Math.round(pctNum(w.d.utilization)) }}%</span>
        <span v-if="w.d.window_stats" class="side-cost">{{ fmtMoney(w.d.window_stats.cost) }}</span>
      </div>

      <div v-for="q in keyQuota" :key="q.label" class="side">
        <span class="k">{{ q.label }}</span>
        <div class="track slim"><b :class="barCls(q.utilization)" :style="{ width: pctNum(q.utilization) + '%' }" /></div>
        <span class="p">{{ q.text }}</span>
      </div>

      <div v-if="showToday && todayStats" class="today">
        今日 {{ fmtNum(todayStats.requests) }} 次 · {{ fmtTokens(todayStats.tokens) }}
        <div>账 {{ fmtMoney(todayStats.cost) }} · 户 {{ fmtMoney(todayStats.user_cost) }} · 标 {{ fmtMoney(todayStats.standard_cost) }}</div>
      </div>
      <span v-else-if="showToday && todayLoading" class="hint">正在读取今日计费</span>
      <span v-else-if="!hasMain && !showToday" class="empty">—</span>
    </template>
  </div>
</template>

<style scoped>
.ucard {
  min-width: 240px; width: 100%; padding: 8px 10px; border-radius: 8px;
  background: color-mix(in srgb, var(--fill-subtle) 80%, transparent);
}
.ucard.bare { padding: 0; background: transparent; min-width: 0; }
.ucard.clickable { cursor: pointer; }
.ucard.clickable:hover { background: var(--fill-subtle); }
.ucard.bare.clickable:hover { background: transparent; }
.week { display: flex; flex-direction: column; gap: 6px; }
.hd { display: flex; align-items: center; gap: 8px; }
.k { font-size: 12px; color: var(--text2); white-space: nowrap; }
.p { margin-left: auto; font-size: 12px; font-variant-numeric: tabular-nums; color: var(--text); }
.ref {
  width: 22px; height: 22px; border: 0; border-radius: 6px; padding: 0; background: transparent;
  color: var(--text2); cursor: pointer; display: inline-flex; align-items: center; justify-content: center;
}
.ref:hover { background: var(--fill-subtle); color: var(--text); }
.ref:disabled { opacity: .5; cursor: default; }
.track { height: 10px; overflow: hidden; border-radius: 5px; background: color-mix(in srgb, var(--hr) 85%, transparent); }
.track.slim { height: 6px; }
.track b { display: block; height: 100%; border-radius: inherit; transition: width .25s ease; }
.track b.ok { background: #3b91d8; min-width: 8px; }
.track b.idle { background: color-mix(in srgb, #3b91d8 35%, var(--hr)); }
.track b.hot { background: #d9894b; }
.track b.full { background: #c75050; }
.est { display: flex; flex-direction: column; gap: 2px; }
.est-total { font-size: 20px; font-weight: 500; color: var(--text); letter-spacing: .01em; line-height: 1.2; }
.est-sub { font-size: 11px; color: var(--text2); }
.chips { display: flex; gap: 4px; flex-wrap: wrap; }
.chips span {
  height: 18px; line-height: 18px; padding: 0 5px; border-radius: 4px; font-size: 10px;
  background: color-mix(in srgb, var(--hr) 55%, transparent); color: var(--text2); white-space: nowrap;
}
.hint { font-size: 11px; color: var(--text2); }
.hint.warn { color: #a66b14; }
.reset-credit { display: flex; align-items: center; gap: 6px; margin-top: 7px; padding-top: 6px; border-top: 1px solid var(--hr); }
.reset-count { min-width: 42px; font-size: 12px; font-variant-numeric: tabular-nums; color: var(--text); }
.reset-count.unknown { color: var(--text2); }
.credit-btn { height: 22px; padding: 0 6px; border: 1px solid var(--hr); border-radius: 5px; background: transparent; color: var(--text2); font-size: 11px; cursor: pointer; display: inline-flex; align-items: center; gap: 3px; }
.credit-btn:hover:not(:disabled) { background: var(--fill-subtle); color: var(--text); }
.credit-btn.danger:hover:not(:disabled) { color: #b54747; border-color: color-mix(in srgb, #b54747 42%, var(--hr)); }
.credit-btn:disabled { opacity: .5; cursor: default; }
.hint.bad { color: #b54747; }
.ellip { max-width: 78px; overflow: hidden; text-overflow: ellipsis; }
.side { display: grid; grid-template-columns: 40px 1fr auto auto; gap: 6px; align-items: center; margin-top: 6px; }
.side-cost { font-size: 11px; color: var(--text2); font-variant-numeric: tabular-nums; }
.today { margin-top: 8px; padding-top: 6px; border-top: 1px solid var(--hr); font-size: 11px; color: var(--text2); line-height: 1.5; }
.empty, .uerr { font-size: 12px; color: var(--text2); }
.uerr { color: #b54747; max-width: 240px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.usk { display: flex; gap: 6px; padding: 6px 0; }
.usk i { flex: 1; height: 8px; border-radius: 4px; background: var(--hr); animation: pulse 1s ease-in-out infinite alternate; }
.usk i:nth-child(2) { animation-delay: .15s; } .usk i:nth-child(3) { animation-delay: .3s; }
@keyframes pulse { from { opacity: .35; } to { opacity: .9; } }
</style>
