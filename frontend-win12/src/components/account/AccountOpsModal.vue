<script setup>
/* 账号运维弹窗（补全官方 v0.2.13 后台缺失入口）
 * 契约（均已在本机轻量网关 18080 实测）：
 * - GET  /admin/accounts/:id/today-stats            → { requests, tokens, cost, standard_cost, user_cost }
 * - POST /admin/accounts/:id/refresh-tier           → 刷新 Google One / 上游套餐档位
 * - POST /admin/accounts/:id/apply-oauth-credentials → { type: 'oauth'|'setup-token', credentials: {...}, extra: {...} }
 *   旧版 ReAuth 走「全量 PUT /admin/accounts/:id」，这里是官方新增的「增量落库」通道，
 *   用于把重新授权拿到的凭据原子写入而不覆盖其他字段。
 */
import { ref, computed, watch } from 'vue'
import { request } from '../../api'
import { message } from 'ant-design-vue'
import { confirmAction } from '../../utils/confirmAction'

const open = defineModel('open', { type: Boolean, default: false })
const props = defineProps({
  account: { type: Object, default: null },
})

const today = ref(null)
const loadingToday = ref(false)
const refreshingTier = ref(false)
const applying = ref(false)

const credType = ref('oauth')
const credJson = ref('')
const extraJson = ref('')

const platformLabel = computed(() => props.account?.platform || '-')

const fmtNum = (n) => new Intl.NumberFormat('zh-CN').format(Math.round(Number(n) || 0))
const fmtTokens = (n) => {
  const v = Number(n) || 0
  if (v >= 1e9) return `${(v / 1e9).toFixed(2)}B`
  if (v >= 1e6) return `${(v / 1e6).toFixed(2)}M`
  if (v >= 1e3) return `${(v / 1e3).toFixed(1)}K`
  return String(Math.round(v))
}
const fmtCost = (n) => `$${(Number(n) || 0).toFixed(4).replace(/\.?0+$/, '')}`

const loadToday = async () => {
  if (!props.account?.id) return
  loadingToday.value = true
  today.value = null
  try {
    today.value = await request(`/admin/accounts/${props.account.id}/today-stats`, { method: 'GET' })
  } catch (e) {
    message.error(`读取今日统计失败：${e.message}`)
  } finally {
    loadingToday.value = false
  }
}

const refreshTier = async () => {
  if (!props.account?.id) return
  refreshingTier.value = true
  try {
    const res = await request(`/admin/accounts/${props.account.id}/refresh-tier`, { method: 'POST' })
    const tier = res?.tier ?? res?.subscription_tier ?? res?.plan
    message.success(tier ? `档位已刷新：${tier}` : '档位已刷新')
  } catch (e) {
    message.error(`刷新档位失败：${e.message}`)
  } finally {
    refreshingTier.value = false
  }
}

const parseJson = (text, label) => {
  const t = String(text || '').trim()
  if (!t) return null
  try {
    const v = JSON.parse(t)
    if (v === null || typeof v !== 'object' || Array.isArray(v)) throw new Error('必须是 JSON 对象')
    return v
  } catch (e) {
    message.error(`${label}不是合法 JSON 对象：${e.message}`)
    return undefined
  }
}

const applyCredentials = async () => {
  if (!props.account?.id) return
  const credentials = parseJson(credJson.value, '凭据')
  if (credentials === undefined) return
  if (!credentials) {
    message.warning('请先粘贴新授权得到凭据 JSON')
    return
  }
  const extra = parseJson(extraJson.value, 'extras')
  if (extra === undefined) return
  const ok = await confirmAction({
    title: '应用 OAuth 凭据',
    content: `确认把这份凭据增量写入账号「${props.account?.name || props.account?.id}」？只覆盖凭据字段，其它配置保持不变。`,
    okText: '写入',
    danger: true,
  })
  if (!ok) return
  applying.value = true
  try {
    await request(`/admin/accounts/${props.account.id}/apply-oauth-credentials`, {
      method: 'POST',
      body: { type: credType.value, credentials, ...(extra ? { extra } : {}) },
    })
    message.success('凭据已写入')
    credJson.value = ''
    extraJson.value = ''
  } catch (e) {
    message.error(`写入失败：${e.message}`)
  } finally {
    applying.value = false
  }
}

watch(open, (o) => { if (o) { loadToday(); credJson.value = ''; extraJson.value = '' } })
</script>

<template>
  <a-modal v-model:open="open" :title="`账号运维 · ${account?.name || ''}`" width="680px" :footer="null">
    <div class="kv">
      <div class="item"><span>平台</span><b>{{ platformLabel }}</b></div>
      <div class="item"><span>类型</span><b>{{ account?.type || '-' }}</b></div>
    </div>

    <div class="sub">今日统计</div>
    <a-spin :spinning="loadingToday">
      <div v-if="today" class="kv">
        <div class="item"><span>请求</span><b>{{ fmtNum(today.requests) }}</b></div>
        <div class="item"><span>Token</span><b>{{ fmtTokens(today.tokens) }}</b></div>
        <div class="item"><span>成本</span><b>{{ fmtCost(today.cost) }}</b></div>
        <div class="item"><span>标准价成本</span><b>{{ fmtCost(today.standard_cost) }}</b></div>
        <div class="item"><span>用户售价</span><b>{{ fmtCost(today.user_cost) }}</b></div>
      </div>
      <div v-else-if="!loadingToday" class="empty">暂无数据</div>
    </a-spin>
    <div class="row-actions">
      <a-button size="small" @click="loadToday">刷新统计</a-button>
      <a-button size="small" :loading="refreshingTier" @click="refreshTier">刷新套餐档位</a-button>
    </div>

    <div class="sub">应用 OAuth 凭据（增量写入）</div>
    <div class="hint">
      重新授权后把新凭据 JSON 粘贴到这里，只会覆盖凭据与 extras，不会清空该账号的分组、并发、优先级等配置。
    </div>
    <a-form layout="vertical" style="margin-top: 8px">
      <a-form-item label="凭据类型">
        <a-radio-group v-model:value="credType" :options="[{ label: 'oauth', value: 'oauth' }, { label: 'setup-token', value: 'setup-token' }]" />
      </a-form-item>
      <a-form-item label="凭据 JSON">
        <a-textarea v-model:value="credJson" class="mono" :rows="5" placeholder='{"refresh_token":"…","access_token":"…"}' />
      </a-form-item>
      <a-form-item label="extras JSON（可选）">
        <a-textarea v-model:value="extraJson" class="mono" :rows="3" placeholder='{"codex_fingerprint_mode":"off"}' />
      </a-form-item>
      <div class="row-actions">
        <a-button type="primary" :loading="applying" @click="applyCredentials">写入凭据</a-button>
      </div>
    </a-form>
  </a-modal>
</template>

<style scoped>
.kv { display: grid; grid-template-columns: 1fr 1fr; gap: 8px 16px; }
.item { display: flex; justify-content: space-between; align-items: center; gap: 8px; padding: 7px 10px; border-radius: 8px; background: var(--card); font-size: 13px; }
.item span { color: var(--text2); flex: none; }
.item b { font-weight: 500; word-break: break-all; text-align: right; }
.sub { font-weight: 500; font-size: 13px; margin: 16px 0 8px; padding-top: 10px; border-top: 1px solid var(--hr); }
.sub:first-of-type { margin-top: 12px; }
.hint { color: var(--text2); font-size: 12px; line-height: 1.6; }
.row-actions { display: flex; gap: 8px; margin-top: 10px; flex-wrap: wrap; }
.empty { color: var(--text2); font-size: 13px; padding: 16px 0; text-align: center; }
.mono :deep(textarea) { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; }
@media (max-width: 640px) {
  .kv { grid-template-columns: 1fr; }
}
</style>
