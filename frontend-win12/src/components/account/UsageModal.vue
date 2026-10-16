<script setup>
/* 用量窗口弹窗：GET /admin/accounts/:id/usage
 * Claude 默认 passive；其余平台 active。force=true 才打上游。
 * 窗口统计三口径：cost=账号采购、user_cost=用户实扣、standard_cost=标准价。 */
import { ref, computed, watch } from 'vue'
import { fmtDateTime, fmtNum, fmtMoney, fmtMoneyCompact, fmtTokens } from './format'
import { getAccountUsage } from './accountsApi'
import { estimateWeekQuota } from './weekQuotaEstimate'

const open = defineModel('open', { type: Boolean, default: false })
const props = defineProps({ account: { type: Object, default: null } })

const loading = ref(false)
const probing = ref(false)
const info = ref(null)
const errMsg = ref('')

const sourceOf = (a) => (a?.platform === 'anthropic' ? 'passive' : 'active')

const load = async (force = false) => {
  if (!props.account?.id) return
  loading.value = !force
  probing.value = force
  errMsg.value = ''
  try {
    info.value = await getAccountUsage(props.account.id, { source: sourceOf(props.account), force })
  } catch (e) {
    info.value = null
    errMsg.value = e.message
  } finally {
    loading.value = false
    probing.value = false
  }
}
watch(open, (o) => { if (o) load() })

const pct = (v) => `${Math.min(200, Math.round(Number(v) || 0))}%`
const barCls = (u) => (u >= 100 ? 'b-red' : u >= 80 ? 'b-orange' : u >= 50 ? 'b-yellow' : 'b-green')

const gq = (w) => {
  if (!w || (w.limit == null && w.remaining == null)) return null
  const used = (Number(w.limit) || 0) - (Number(w.remaining) || 0)
  const pctv = w.limit > 0 ? Math.round((used / w.limit) * 100) : 0
  return { used, limit: w.limit, pct: pctv, reset: w.reset_at ? fmtDateTime(w.reset_at) : (w.reset_unix ? fmtDateTime(w.reset_unix) : '-') }
}
const costLine = (s) => s
  ? `${fmtNum(s.requests)} 次 / ${fmtTokens(s.tokens)} tok · 账号 ${fmtMoney(s.cost)} · 用户 ${fmtMoney(s.user_cost)} · 标准 ${fmtMoney(s.standard_cost)}`
  : null
const weekEst = ref(null)
watch(
  () => [props.account?.id, info.value?.seven_day?.utilization, info.value?.seven_day?.window_stats?.cost, info.value?.seven_day?.resets_at],
  () => {
    const d = info.value?.seven_day
    if (!d || !props.account?.id) { weekEst.value = null; return }
    weekEst.value = estimateWeekQuota(props.account.id, d.utilization, d.window_stats?.cost, d.resets_at)
  },
)
const hasWindow = (x) => x?.five_hour || x?.seven_day || x?.seven_day_sonnet || x?.seven_day_fable || x?.thirty_day
  || x?.gemini_shared_daily || x?.gemini_pro_daily || x?.gemini_flash_daily
  || x?.grok_request_quota || x?.grok_token_quota || x?.grok_billing
  || antigravityRows.value.length

// Antigravity 多模型配额（后端 antigravity_quota：{ 模型名: { utilization, reset_time } }）
const antigravityRows = computed(() => Object.entries(info.value?.antigravity_quota || {}).map(([model, d]) => ({
  label: String(model).replace(/^gemini-/, '').replace(/^claude-/, 'Claude '),
  utilization: Number(d?.utilization) || 0,
})))
</script>

<template>
  <a-modal v-model:open="open" :title="`用量窗口 · ${account?.name || ''}`" width="640px">
    <template #footer>
      <a-button :loading="probing" @click="load(true)">主动查询上游</a-button>
    </template>
    <a-spin :spinning="loading">
      <a-alert v-if="errMsg" type="warning" show-icon :message="`用量读取失败：${errMsg}（上游探测可能暂不可用）`" />
      <template v-if="info">
        <div v-for="w in [
          { label: '5 小时窗口', d: info.five_hour },
          { label: '7 天窗口', d: info.seven_day },
          { label: '7 天 Sonnet 窗口', d: info.seven_day_sonnet },
          { label: '7 天 Fable 窗口', d: info.seven_day_fable },
          { label: '30 天窗口', d: info.thirty_day },
        ]" :key="w.label">
          <template v-if="w.d">
            <div class="wrow">
              <span class="wlabel">{{ w.label }}</span>
              <div class="bar"><i :class="barCls(w.d.utilization)" :style="{ width: pct(w.d.utilization) }" /></div>
              <span class="wpct">{{ Math.round(w.d.utilization) }}%</span>
              <span class="dim" :title="w.d.resets_at || ''">{{ w.d.resets_at ? fmtDateTime(w.d.resets_at) + ' 重置' : '' }}</span>
            </div>
            <div v-if="w.d.window_stats" class="wsub">{{ costLine(w.d.window_stats) }}</div>
            <div v-if="w.label === '7 天窗口' && weekEst?.ready" class="west">
              周额度预估总额 {{ fmtMoneyCompact(weekEst.total) }}（采购成本口径）
              <div class="west-sub">占比 {{ weekEst.pct }}% · 已用 {{ fmtMoneyCompact(weekEst.used) }} · 剩余 {{ fmtMoneyCompact(weekEst.remaining) }}，占比每变化 1% 重算</div>
            </div>
          </template>
        </div>
        <div v-for="w in [
          { label: 'Gemini 共享日窗口', d: info.gemini_shared_daily },
          { label: 'Gemini Pro 日窗口', d: info.gemini_pro_daily },
          { label: 'Gemini Flash 日窗口', d: info.gemini_flash_daily },
        ]" :key="w.label">
          <div v-if="w.d" class="wrow">
            <span class="wlabel">{{ w.label }}</span>
            <div class="bar"><i :class="barCls(w.d.utilization)" :style="{ width: pct(w.d.utilization) }" /></div>
            <span class="wpct">{{ Math.round(w.d.utilization) }}%</span>
          </div>
        </div>
        <template v-if="antigravityRows.length">
          <div class="sub">Antigravity 多模型配额</div>
          <div v-for="w in antigravityRows" :key="w.label" class="wrow">
            <span class="wlabel ellip" :title="w.label">{{ w.label }}</span>
            <div class="bar"><i :class="barCls(w.utilization)" :style="{ width: pct(w.utilization) }" /></div>
            <span class="wpct">{{ Math.round(w.utilization) }}%</span>
          </div>
        </template>
        <template v-if="gq(info.grok_request_quota) || gq(info.grok_token_quota) || costLine(info.grok_local_usage_24h)">
          <div class="sub">Grok</div>
          <div v-if="gq(info.grok_request_quota)" class="wrow">
            <span class="wlabel">请求配额</span>
            <div class="bar"><i :class="barCls(gq(info.grok_request_quota).pct)" :style="{ width: pct(gq(info.grok_request_quota).pct) }" /></div>
            <span class="wpct">{{ gq(info.grok_request_quota).used }}/{{ gq(info.grok_request_quota).limit }}</span>
            <span class="dim">{{ gq(info.grok_request_quota).reset }} 重置</span>
          </div>
          <div v-if="gq(info.grok_token_quota)" class="wrow">
            <span class="wlabel">Token 配额</span>
            <div class="bar"><i :class="barCls(gq(info.grok_token_quota).pct)" :style="{ width: pct(gq(info.grok_token_quota).pct) }" /></div>
            <span class="wpct">{{ gq(info.grok_token_quota).used }}/{{ gq(info.grok_token_quota).limit }}</span>
            <span class="dim">{{ gq(info.grok_token_quota).reset }} 重置</span>
          </div>
          <div v-if="costLine(info.grok_local_usage_24h)" class="wsub">24h 本地用量：{{ costLine(info.grok_local_usage_24h) }}</div>
          <div v-if="costLine(info.grok_local_usage_7d)" class="wsub">7d 本地用量：{{ costLine(info.grok_local_usage_7d) }}</div>
          <div v-if="costLine(info.grok_local_usage_monthly)" class="wsub">月本地用量：{{ costLine(info.grok_local_usage_monthly) }}</div>
        </template>
        <template v-if="(info.ai_credits || []).length">
          <div class="sub">AI 积分</div>
          <div v-for="(c, i) in info.ai_credits" :key="i" class="wsub">{{ c.credit_type || 'credit' }}：{{ c.amount ?? '-' }}（最低保留 {{ c.minimum_balance ?? 0 }}）</div>
        </template>
        <div v-if="info.updated_at" class="wsub dim">更新于 {{ fmtDateTime(info.updated_at) }}（{{ info.source || sourceOf(account) }}）</div>
        <div v-if="!hasWindow(info)" class="empty">该账号暂无可用窗口快照</div>
      </template>
      <div v-else-if="!loading && !errMsg" class="empty">暂无数据</div>
    </a-spin>
  </a-modal>
</template>

<style scoped>
.wrow { display: flex; align-items: center; gap: 10px; padding: 6px 0; }
.wlabel { width: 120px; flex: none; font-size: 13px; color: var(--text2); }
.bar { flex: 1; height: 8px; border-radius: 4px; background: var(--hr); overflow: hidden; }
.bar i { display: block; height: 100%; border-radius: 4px; }
.b-green { background: #52c41a; } .b-yellow { background: #d48c14; } .b-orange { background: #f56c1e; } .b-red { background: #cf3030; }
.wpct { width: 46px; text-align: right; font-size: 12px; font-family: Consolas, monospace; }
.wsub { font-size: 12px; color: var(--text2); padding: 2px 0 2px 130px; }
.west { font-size: 13px; font-weight: 500; color: var(--text); padding: 4px 0 8px 130px; }
.west-sub { font-weight: 400; font-size: 11px; color: var(--text2); margin-top: 2px; }
.ellip { max-width: 120px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.sub { font-weight: 500; font-size: 13px; margin: 12px 0 6px; padding-top: 10px; border-top: 1px solid var(--hr); }
.empty { color: var(--text2); font-size: 13px; padding: 24px 0; text-align: center; }
</style>
