<script setup>
/* 重新授权弹窗——旧实现契约：frontend/src/components/account/ReAuthAccountModal.vue + 各 OAuth composable
 * 显示条件（旧 AccountActionMenu）：type 为 oauth/setup-token 且非 Spark 影子（parent_account_id 为空）。
 * 流程均为「弹窗内生成授权链接 → 用户浏览器授权 → 回粘贴 code（+state）→ 交换令牌 → 覆盖凭据」，
 * 不是直接跳转。按平台路由（全部核实自旧 composables / api 层）：
 *   anthropic  oauth:        POST /admin/accounts/generate-auth-url     → exchange-code            （type 保持/切换 oauth）
 *   anthropic  setup-token:  POST /admin/accounts/generate-setup-token-url → exchange-setup-token-code（type=setup-token）
 *   openai:                  POST /admin/openai/generate-auth-url       → /admin/openai/exchange-code {session_id,code,state}
 *   gemini:                  POST /admin/gemini/oauth/auth-url          → /admin/gemini/oauth/exchange-code {session_id,state,code,oauth_type}
 *   antigravity:             POST /admin/antigravity/oauth/auth-url     → /admin/antigravity/oauth/exchange-code {session_id,state,code}
 *   grok:                    POST /admin/grok/oauth/auth-url            → /admin/grok/oauth/exchange-code {session_id,state,code}
 * 凭据组装对照旧 buildCredentials（各平台字段不同，不能混用）。保存用 PUT /admin/accounts/:id
 * （旧版同款全量 PUT），随后 POST :id/clear-error 清错误态。
 * 注意：真实授权需要人工在浏览器完成，本环境只能验证到「生成链接」一步。 */
import { ref, computed, watch } from 'vue'
import { request } from '../../api'
import { message } from 'ant-design-vue'

const open = defineModel('open', { type: Boolean, default: false })
const props = defineProps({
  account: { type: Object, default: null },
})
const emit = defineEmits(['reauthorized'])

const loading = ref(false)
const authUrl = ref('')
const sessionId = ref('')
const genState = ref('') // 生成链接时保存的 state（openai/gemini/antigravity/grok 交换必填）
const callbackInput = ref('')
const done = ref(false)

const platform = computed(() => props.account?.platform || '')
const isAnthropic = computed(() => platform.value === 'anthropic')
const addMethod = ref('oauth') // 仅 anthropic：oauth | setup-token
const geminiOauthType = ref('code_assist')

const proxyBody = () => (props.account?.proxy_id ? { proxy_id: props.account.proxy_id } : {})

const genEndpoint = () => {
  if (isAnthropic.value) return addMethod.value === 'oauth' ? '/admin/accounts/generate-auth-url' : '/admin/accounts/generate-setup-token-url'
  if (platform.value === 'openai') return '/admin/openai/generate-auth-url'
  if (platform.value === 'gemini') return '/admin/gemini/oauth/auth-url'
  if (platform.value === 'antigravity') return '/admin/antigravity/oauth/auth-url'
  return '/admin/grok/oauth/auth-url'
}

const genUrl = async () => {
  loading.value = true
  try {
    const body = proxyBody()
    if (platform.value === 'gemini') {
      // gemini 的 oauth_type 跟随账号现有凭据（旧 ReAuthAccountModal 初始化逻辑）
      body.oauth_type = geminiOauthType.value
    }
    const res = await request(genEndpoint(), { method: 'POST', body })
    authUrl.value = res.auth_url || ''
    sessionId.value = res.session_id || ''
    genState.value = res.state || ''
    if (!genState.value) {
      try { genState.value = new URL(res.auth_url).searchParams.get('state') || '' } catch { /* 裸链接解析失败忽略 */ }
    }
  } catch (e) {
    message.error(`生成授权链接失败：${e.message}`)
  } finally {
    loading.value = false
  }
}

// 从粘贴内容提取 code / state（完整回调 URL 或裸授权码，同旧 OAuthAuthorizationFlow 放宽处理）
const parsedCode = computed(() => {
  const raw = callbackInput.value.trim()
  if (!raw) return ''
  if (!raw.includes(' ') && !raw.includes('=')) return raw
  try { return new URL(raw).searchParams.get('code') || '' } catch { const m = raw.match(/code=([^&\s]+)/); return m ? m[1] : '' }
})
const parsedState = computed(() => {
  const raw = callbackInput.value.trim()
  try {
    const s = new URL(raw).searchParams.get('state')
    if (s) return s
  } catch { const m = raw.match(/state=([^&\s]+)/); if (m) return m[1] }
  return genState.value
})
const needState = computed(() => ['openai', 'gemini', 'antigravity', 'grok'].includes(platform.value))

const openAuthPage = () => { if (authUrl.value) window.open(authUrl.value, '_blank') }
const copyUrl = async () => {
  try { await navigator.clipboard.writeText(authUrl.value); message.success('已复制') } catch { message.warning('复制失败，请手动选择') }
}

// ===== 凭据组装（逐平台对照旧 buildCredentials / buildExtraInfo） =====
const buildCreds = (t) => {
  if (platform.value === 'openai') {
    const c = { access_token: t.access_token, expires_at: t.expires_at }
    for (const k of ['refresh_token', 'id_token', 'email', 'chatgpt_account_id', 'chatgpt_user_id', 'organization_id', 'plan_type', 'client_id']) {
      if (t[k]) c[k] = t[k] // 仅写非空字段，防止空值覆盖（旧注释同款）
    }
    const extra = {}
    for (const k of ['email', 'name', 'privacy_mode']) if (t[k]) extra[k] = t[k]
    return { creds: c, extra }
  }
  if (platform.value === 'gemini') {
    let expiresAt
    if (typeof t.expires_at === 'number' && Number.isFinite(t.expires_at)) expiresAt = Math.floor(t.expires_at).toString()
    else if (typeof t.expires_at === 'string' && t.expires_at.trim()) expiresAt = t.expires_at.trim()
    return {
      creds: {
        access_token: t.access_token, refresh_token: t.refresh_token, token_type: t.token_type,
        expires_at: expiresAt, scope: t.scope, project_id: t.project_id,
        oauth_type: t.oauth_type || geminiOauthType.value, tier_id: t.tier_id,
      },
      extra: t.extra && typeof t.extra === 'object' ? t.extra : undefined,
    }
  }
  if (platform.value === 'antigravity') {
    let expiresAt
    if (typeof t.expires_at === 'number' && Number.isFinite(t.expires_at)) expiresAt = Math.floor(t.expires_at).toString()
    else if (typeof t.expires_at === 'string' && t.expires_at.trim()) expiresAt = t.expires_at.trim()
    return {
      creds: {
        access_token: t.access_token, refresh_token: t.refresh_token, token_type: t.token_type,
        expires_at: expiresAt, project_id: t.project_id, email: t.email,
      },
      extra: undefined,
    }
  }
  if (platform.value === 'grok') {
    const c = {
      access_token: t.access_token, token_type: t.token_type, expires_at: t.expires_at,
      client_id: t.client_id, scope: t.scope, email: t.email,
      subscription_tier: t.subscription_tier, entitlement_status: t.entitlement_status,
    }
    if (t.refresh_token) c.refresh_token = t.refresh_token
    if (t.id_token) c.id_token = t.id_token
    const extra = {}
    for (const k of ['email', 'subscription_tier', 'entitlement_status']) if (t[k]) extra[k] = t[k]
    return { creds: c, extra }
  }
  // anthropic：credentials = tokenInfo 全量（旧版 { ...tokenInfo }），extra 取三个标识字段
  const extra = {}
  for (const k of ['org_uuid', 'account_uuid', 'email_address']) if (t[k]) extra[k] = t[k]
  return { creds: { ...t }, extra }
}

const exchangeEndpoint = () => {
  if (isAnthropic.value) return addMethod.value === 'oauth' ? '/admin/accounts/exchange-code' : '/admin/accounts/exchange-setup-token-code'
  if (platform.value === 'openai') return '/admin/openai/exchange-code'
  if (platform.value === 'gemini') return '/admin/gemini/oauth/exchange-code'
  if (platform.value === 'antigravity') return '/admin/antigravity/oauth/exchange-code'
  return '/admin/grok/oauth/exchange-code'
}

const finish = async () => {
  if (!parsedCode.value) { message.error('请先粘贴回调链接或授权码'); return }
  if (needState.value && !parsedState.value) { message.error('缺少 state 校验参数（请粘贴完整回调链接）'); return }
  loading.value = true
  try {
    const body = { session_id: sessionId.value, code: parsedCode.value, ...proxyBody() }
    if (needState.value) body.state = parsedState.value
    if (platform.value === 'gemini') body.oauth_type = geminiOauthType.value
    const tokenInfo = await request(exchangeEndpoint(), { method: 'POST', body })
    const { creds, extra } = buildCreds(tokenInfo || {})
    // 旧版语义：全量 PUT 更新凭据与类型，再清错误态（apply-oauth-credentials 是另一条增量接口，旧 ReAuth 未使用）
    const putBody = { type: isAnthropic.value ? addMethod.value : 'oauth', credentials: creds }
    if (extra && Object.keys(extra).length) putBody.extra = extra
    await request(`/admin/accounts/${props.account.id}`, { method: 'PUT', body: putBody })
    await request(`/admin/accounts/${props.account.id}/clear-error`, { method: 'POST' })
    done.value = true
    message.success('重新授权完成')
    emit('reauthorized')
    open.value = false
  } catch (e) {
    message.error(`交换令牌失败：${e.message}`)
  } finally {
    loading.value = false
  }
}

const reset = () => {
  authUrl.value = ''
  sessionId.value = ''
  genState.value = ''
  callbackInput.value = ''
  done.value = false
}

watch(open, (o) => {
  if (o) {
    reset()
    const t = props.account?.type
    if (isAnthropic.value && (t === 'oauth' || t === 'setup-token')) addMethod.value = t
    const creds = props.account?.credentials || {}
    if (platform.value === 'gemini') {
      geminiOauthType.value = ['google_one', 'ai_studio', 'code_assist'].includes(creds.oauth_type) ? creds.oauth_type : 'code_assist'
    }
    if (!isAnthropic.value) addMethod.value = 'oauth'
  }
})
</script>

<template>
  <a-modal v-model:open="open" :title="`重新授权 · ${account?.name || ''}`" width="560px" :footer="null">
    <div class="acc-info">
      <span class="acc-name">{{ account?.name }}</span>
      <a-tag>{{ platform }}</a-tag>
      <a-tag>{{ account?.type }}</a-tag>
    </div>

    <template v-if="isAnthropic">
      <div class="flab">授权方式</div>
      <a-radio-group v-model:value="addMethod">
        <a-radio value="oauth">OAuth（授权码流）</a-radio>
        <a-radio value="setup-token">Setup Token（长期令牌）</a-radio>
      </a-radio-group>
    </template>
    <div v-if="platform === 'gemini'" class="flab">
      OAuth 类型：{{ geminiOauthType === 'google_one' ? 'Google One（个人账号）' : geminiOauthType === 'ai_studio' ? 'AI Studio' : 'Code Assist（内置）' }}（跟随账号现有凭据）
    </div>

    <template v-if="!authUrl">
      <p class="hint">点击生成授权链接，在浏览器中登录并完成授权后，把回调链接粘回来。真实授权需人工操作，本环境无法自动验证。</p>
      <a-button type="primary" block :loading="loading" @click="genUrl">① 生成授权链接</a-button>
    </template>
    <template v-else>
      <div class="urlbox">
        <span class="url">{{ authUrl }}</span>
        <a-space style="margin-top: 8px">
          <a-button size="small" type="primary" @click="openAuthPage">打开授权页</a-button>
          <a-button size="small" @click="copyUrl">复制链接</a-button>
        </a-space>
      </div>
      <p class="hint" style="margin-top: 12px">授权完成后，把浏览器跳转的<b>完整回调链接</b>（或裸授权码）粘贴到这里：</p>
      <a-textarea v-model:value="callbackInput" :rows="3" placeholder="https://...?code=...&state=..." />
      <a-space style="margin-top: 12px">
        <a-button type="primary" :loading="loading" :disabled="!parsedCode" @click="finish">② 完成重新授权</a-button>
        <a-button @click="reset">重新开始</a-button>
      </a-space>
    </template>
  </a-modal>
</template>

<style scoped>
.acc-info { display: flex; align-items: center; gap: 8px; padding: 8px 10px; border-radius: 8px; background: var(--card); margin-bottom: 12px; }
.acc-name { font-weight: 500; }
.flab { font-size: 13px; color: var(--text2); margin: 10px 0 6px; }
.hint { font-size: 12px; color: var(--text2); margin: 8px 0; }
.urlbox { padding: 10px 12px; border-radius: 8px; background: var(--card); border: 1px solid var(--hr); word-break: break-all; }
.url { font-size: 12px; font-family: Consolas, monospace; }
</style>
