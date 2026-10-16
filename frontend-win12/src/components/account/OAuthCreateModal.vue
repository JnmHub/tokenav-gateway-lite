<script setup>
/* OAuth 账号创建弹窗（多平台）——旧实现契约：CreateAccountModal.vue + useOpenAIOAuth /
 * useAccountOAuth / useGeminiOAuth / useAntigravityOAuth composables（已逐个核实）。
 * 平台路由（生成 → 交换 → POST /admin/accounts 创建）：
 *   openai      /admin/openai/generate-auth-url      → /admin/openai/exchange-code（需 state）
 *               或直接粘贴 refresh_token：POST /admin/openai/refresh-token 验证并换取凭据
 *               （官方 useOpenAIOAuth 同款入口，此前本方缺少该入口）
 *   grok        /admin/grok/oauth/auth-url → /admin/grok/oauth/exchange-code（需 state）
 *               或直接粘贴 refresh_token：POST /admin/grok/oauth/refresh-token 验证并换取凭据
 *               （官方 useGrokOAuth 同款入口，此前本方缺少该入口）
 *   anthropic   /admin/accounts/generate-auth-url（oauth）
 *               /admin/accounts/generate-setup-token-url（setup-token）
 *               → /admin/accounts/exchange-code | /admin/accounts/exchange-setup-token-code
 *               另支持 Cookie 方式：/admin/accounts/cookie-auth | setup-token-cookie-auth
 *               {session_id:'', code: sessionKey}，多行批量（旧版 handleCookieAuth 同款）
 *   gemini      /admin/gemini/oauth/auth-url {oauth_type?, project_id?} → /admin/gemini/oauth/exchange-code
 *               {session_id, state, code, oauth_type?}
 *   antigravity /admin/antigravity/oauth/auth-url → /admin/antigravity/oauth/exchange-code（需 state）
 *               或直接粘贴 refresh_token：POST /admin/antigravity/oauth/refresh-token 验证并换取凭据
 * credentials 组装对照各平台 buildCredentials；授权需真实浏览器操作，本环境只能验证「生成链接」
 * 与接口连通性，交换/创建需真实授权凭据才能验证。 */
import { ref, computed, watch } from 'vue'
import { request } from '../../api'
import { message } from 'ant-design-vue'

const open = defineModel('open', { type: Boolean, default: false })
const emit = defineEmits(['created'])

const PLATFORMS = [
  { value: 'openai', label: 'OpenAI' },
  { value: 'anthropic', label: 'Anthropic（Claude）' },
  { value: 'gemini', label: 'Gemini' },
  { value: 'antigravity', label: 'Antigravity' },
  { value: 'grok', label: 'Grok（OAuth 授权）' },
  { value: 'grok_sso', label: 'Grok（SSO Cookie 直转）' },
  { value: 'codex_import', label: 'OpenAI Codex（凭据导入）' },
]
const step = ref(1)
const loading = ref(false)
const platform = ref('openai')
const accountName = ref('')

// grok SSO 直转：多行 SSO token 一次提交（POST /admin/grok/sso-to-oauth）
const grokSSOText = ref('')
const isGrokSSO = computed(() => platform.value === 'grok_sso')

// OpenAI Codex 凭据导入（旧 handleOpenAIImportCodexSession/AgentIdentity/PAT 等价）
const codexImportKind = ref('session') // session | identity | pat
const codexImportText = ref('')
const isCodexImport = computed(() => platform.value === 'codex_import')

// anthropic 专有：授权方式 + 输入方式
const addMethod = ref('oauth') // oauth | setup-token
const anthropicInput = ref('code') // code=授权码流 | cookie=SessionKey 粘贴
// gemini 专有
const geminiOauthType = ref('code_assist')
const geminiProjectId = ref('')
// 通用授权码流状态
const authUrl = ref('')
const sessionId = ref('')
const genState = ref('')
const callbackInput = ref('')
const tokenInfo = ref(null)
// antigravity refresh_token 直连路径
const antigravityRT = ref('')
// openai / grok 的 refresh_token 直连路径（官方 useOpenAIOAuth / useGrokOAuth 同款入口）
const rtMode = ref('code') // code=浏览器授权 | cookie=粘贴 refresh_token
const rtText = ref('')
// gemini 能力缓存：决定 AI Studio 选项是否可选（来自 GET /admin/gemini/oauth/capabilities）
const geminiCaps = ref(null)

const isAnthropic = computed(() => platform.value === 'anthropic')
const needState = computed(() => ['openai', 'gemini', 'antigravity'].includes(platform.value))
const isOAuthRTPlatform = computed(() => platform.value === 'openai' || platform.value === 'grok')
const useRT = computed(() => isOAuthRTPlatform.value && rtMode.value === 'cookie')
// 是否需要走浏览器授权码流；false 时走「粘贴凭据直接验证」
const canBrowser = computed(() => !useRT.value
  && !(platform.value === 'antigravity' && anthropicInput.value === 'cookie')
  && !(isAnthropic.value && anthropicInput.value === 'cookie'))

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

const genEndpoint = () => {
  if (isAnthropic.value) return addMethod.value === 'oauth' ? '/admin/accounts/generate-auth-url' : '/admin/accounts/generate-setup-token-url'
  if (platform.value === 'openai') return '/admin/openai/generate-auth-url'
  if (platform.value === 'gemini') return '/admin/gemini/oauth/auth-url'
  if (platform.value === 'grok') return '/admin/grok/oauth/auth-url'
  return '/admin/antigravity/oauth/auth-url'
}

const genUrl = async () => {
  loading.value = true
  try {
    const body = {}
    if (platform.value === 'gemini') {
      body.oauth_type = geminiOauthType.value
      if (geminiProjectId.value.trim() && geminiOauthType.value === 'code_assist') body.project_id = geminiProjectId.value.trim()
    }
    const res = await request(genEndpoint(), { method: 'POST', body })
    authUrl.value = res.auth_url || ''
    sessionId.value = res.session_id || ''
    genState.value = res.state || ''
    if (!genState.value) {
      try { genState.value = new URL(res.auth_url).searchParams.get('state') || '' } catch { /* 忽略解析失败 */ }
    }
    step.value = 2
  } catch (e) {
    message.error(`生成授权链接失败：${e.message}`)
  } finally {
    loading.value = false
  }
}

const openAuthPage = () => { if (authUrl.value) window.open(authUrl.value, '_blank') }
const copyUrl = async () => {
  try { await navigator.clipboard.writeText(authUrl.value); message.success('已复制') } catch { message.warning('复制失败，请手动选择') }
}

// ===== credentials 组装（逐平台对照旧 buildCredentials；字段绝不一概而论） =====
const buildCreds = (t) => {
  if (platform.value === 'openai') {
    const c = { access_token: t.access_token, expires_at: t.expires_at }
    for (const k of ['refresh_token', 'id_token', 'email', 'chatgpt_account_id', 'chatgpt_user_id', 'organization_id', 'plan_type', 'client_id']) {
      if (t[k]) c[k] = t[k]
    }
    return { creds: c, extra: buildOpenAIExtra(t) }
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
        access_token: t.access_token, refresh_token: t.refresh_token || antigravityRT.value.trim() || undefined,
        token_type: t.token_type, expires_at: expiresAt, project_id: t.project_id, email: t.email,
      },
      extra: undefined,
    }
  }
  if (platform.value === 'grok') {
    // 字段对照官方 useGrokOAuth.buildCredentials：只保留 OAuth 凭据，绝不落 SSO cookie / 密码
    const c = {
      access_token: t.access_token, token_type: t.token_type, expires_at: t.expires_at,
      client_id: t.client_id, scope: t.scope, email: t.email, sub: t.sub,
      team_id: t.team_id, subscription_tier: t.subscription_tier,
      entitlement_status: t.entitlement_status, refresh_token: t.refresh_token, id_token: t.id_token,
    }
    for (const k of Object.keys(c)) if (c[k] === undefined || c[k] === '') delete c[k]
    const x = {}
    for (const k of ['email', 'subscription_tier', 'entitlement_status']) if (t[k]) x[k] = t[k]
    return { creds: c, extra: Object.keys(x).length ? x : undefined }
  }
  // anthropic：credentials = tokenInfo 全量；extra 只取三个标识字段（旧 useAccountOAuth.buildExtraInfo）
  const extra = {}
  for (const k of ['org_uuid', 'account_uuid', 'email_address']) if (t[k]) extra[k] = t[k]
  return { creds: { ...t }, extra }
}
const buildOpenAIExtra = (t) => {
  const extra = {}
  for (const k of ['email', 'name', 'privacy_mode']) if (t[k]) extra[k] = t[k]
  return Object.keys(extra).length ? extra : undefined
}

// 第 3 步的凭据摘要展示（按平台取关键标识字段）
const previewKv = computed(() => {
  const t = tokenInfo.value || {}
  const out = []
  const push = (label, val) => { if (val !== undefined && val !== null && val !== '') out.push([label, String(val)]) }
  if (isAnthropic.value) {
    push('邮箱', t.email_address)
    push('组织 UUID', t.org_uuid)
    push('账号 UUID', t.account_uuid)
  } else {
    push('邮箱', t.email)
    push('套餐', t.plan_type)
    push('项目 ID', t.project_id)
  }
  return out
})

const exchangeEndpoint = () => {
  if (isAnthropic.value) return addMethod.value === 'oauth' ? '/admin/accounts/exchange-code' : '/admin/accounts/exchange-setup-token-code'
  if (platform.value === 'openai') return '/admin/openai/exchange-code'
  if (platform.value === 'gemini') return '/admin/gemini/oauth/exchange-code'
  if (platform.value === 'grok') return '/admin/grok/oauth/exchange-code'
  return '/admin/antigravity/oauth/exchange-code'
}

const exchange = async () => {
  if (!parsedCode.value) { message.error('请先粘贴回调链接或授权码'); return }
  if (needState.value && !parsedState.value) { message.error('缺少 state 校验参数（请粘贴完整回调链接）'); return }
  loading.value = true
  try {
    const body = { session_id: sessionId.value, code: parsedCode.value }
    if (needState.value) body.state = parsedState.value
    if (platform.value === 'gemini') body.oauth_type = geminiOauthType.value
    tokenInfo.value = await request(exchangeEndpoint(), { method: 'POST', body })
    if (!accountName.value.trim()) {
      const t = tokenInfo.value
      accountName.value = t?.email || t?.email_address || t?.name || `oauth-${platform.value}-${new Date().toISOString().slice(5, 16)}`
    }
    step.value = 3
  } catch (e) {
    message.error(`交换令牌失败：${e.message}`)
  } finally {
    loading.value = false
  }
}

const createAccount = async () => {
  if (!accountName.value.trim()) { message.error('请填写账号名称'); return }
  const t = tokenInfo.value
  if (!t) return
  const { creds, extra } = buildCreds(t)
  const type = isAnthropic.value ? addMethod.value : 'oauth'
  loading.value = true
  try {
    const body = {
      name: accountName.value.trim(), platform: platform.value, type,
      credentials: creds,
    }
    if (extra && Object.keys(extra).length) body.extra = extra
    await request('/admin/accounts', { method: 'POST', body })
    message.success('OAuth 账号已创建')
    open.value = false
    reset()
    emit('created')
  } catch (e) {
    message.error(`创建失败：${e.message}`)
  } finally {
    loading.value = false
  }
}

// anthropic Cookie 方式：多行 SessionKey，一行一个账号（旧 handleCookieAuth 批量语义）
const anthropicCookie = async () => {
  const keys = anthropicRTText.value.split('\n').map((k) => k.trim()).filter(Boolean)
  if (!keys.length) { message.error('请粘贴至少一行 SessionKey'); return }
  const base = accountName.value.trim() || `claude-cookie-${new Date().toISOString().slice(5, 16)}`
  const endpoint = addMethod.value === 'oauth' ? '/admin/accounts/cookie-auth' : '/admin/accounts/setup-token-cookie-auth'
  loading.value = true
  let ok = 0
  let fail = 0
  try {
    for (let i = 0; i < keys.length; i++) {
      try {
        const t = await request(endpoint, { method: 'POST', body: { session_id: '', code: keys[i] } })
        const extra = {}
        for (const k of ['org_uuid', 'account_uuid', 'email_address']) if (t?.[k]) extra[k] = t[k]
        const body = {
          name: keys.length > 1 ? `${base} #${i + 1}` : base,
          platform: 'anthropic', type: addMethod.value, credentials: { ...(t || {}) },
        }
        if (Object.keys(extra).length) body.extra = extra
        await request('/admin/accounts', { method: 'POST', body })
        ok++
      } catch { fail++ }
    }
    if (ok > 0) {
      message.success(fail ? `创建完成：成功 ${ok}，失败 ${fail}` : `已创建 ${ok} 个账号`)
      open.value = false
      reset()
      emit('created')
    } else {
      message.error('全部创建失败，请检查 SessionKey')
    }
  } finally {
    loading.value = false
  }
}
const anthropicRTText = ref('')

// antigravity refresh_token 直连：验证 → 换取凭据 → 创建（旧 handleAntigravityValidateRT）
const antigravityByRT = async () => {
  const rt = antigravityRT.value.trim()
  if (!rt) { message.error('请粘贴 refresh_token'); return }
  loading.value = true
  try {
    const t = await request('/admin/antigravity/oauth/refresh-token', { method: 'POST', body: { refresh_token: rt } })
    tokenInfo.value = t
    if (!accountName.value.trim()) accountName.value = t?.email || `antigravity-${new Date().toISOString().slice(5, 16)}`
    step.value = 3
  } catch (e) {
    message.error(`refresh_token 验证失败：${e.message}`)
  } finally {
    loading.value = false
  }
}
// OpenAI refresh_token 直连：验证 → 换取凭据 → 创建（官方 useOpenAIOAuth.refreshToken 同款入口）
const openaiByRT = async () => {
  const rt = rtText.value.trim()
  if (!rt) { message.error('请粘贴 refresh_token'); return }
  loading.value = true
  try {
    const t = await request('/admin/openai/refresh-token', { method: 'POST', body: { refresh_token: rt } })
    tokenInfo.value = t
    if (!accountName.value.trim()) accountName.value = t?.email || `openai-${new Date().toISOString().slice(5, 16)}`
    step.value = 3
  } catch (e) {
    message.error(`refresh_token 验证失败：${e.message}`)
  } finally {
    loading.value = false
  }
}

// Grok refresh_token 直连（官方 useGrokOAuth.validateRefreshToken 同款入口）
const grokByRT = async () => {
  const rt = rtText.value.trim()
  if (!rt) { message.error('请粘贴 refresh_token'); return }
  loading.value = true
  try {
    const t = await request('/admin/grok/oauth/refresh-token', { method: 'POST', body: { refresh_token: rt } })
    tokenInfo.value = t
    if (!accountName.value.trim()) accountName.value = t?.email || `grok-${new Date().toISOString().slice(5, 16)}`
    step.value = 3
  } catch (e) {
    message.error(`refresh_token 验证失败：${e.message}`)
  } finally {
    loading.value = false
  }
}

// 「② 验证并创建」按钮统一分发：不同平台走不同「粘贴凭据」端点
const runByRT = () => {
  if (isAnthropic.value) return anthropicCookie()
  if (platform.value === 'antigravity') return antigravityByRT()
  if (platform.value === 'grok') return grokByRT()
  return openaiByRT()
}

// gemini 能力（GET /admin/gemini/oauth/capabilities）：AI Studio 是否可用由后端配置决定
const loadGeminiCaps = async () => {
  try { geminiCaps.value = await request('/admin/gemini/oauth/capabilities', { method: 'GET' }) }
  catch { geminiCaps.value = null }
}
watch(platform, (p) => { if (p === 'gemini' && !geminiCaps.value) loadGeminiCaps() }, { immediate: true })

// Grok SSO 直转：批量提交 SSO token，后端逐个换 OAuth 凭据并建号（旧 handleGrokSSOImport 等价）
const grokSSOImport = async () => {
  const tokens = grokSSOText.value.split('\n').map((s) => s.trim()).filter(Boolean)
  if (!tokens.length) { message.error('请粘贴至少一行 SSO token'); return }
  loading.value = true
  try {
    // 后端逐个转换，返回 created/failed 明细；token 多时耗时较长
    const r = await request('/admin/grok/sso-to-oauth', {
      method: 'POST',
      body: { sso_tokens: tokens, name: accountName.value.trim() || undefined },
    })
    const ok = (r?.created || []).length
    const fail = (r?.failed || []).length
    if (ok > 0) {
      message.success(fail ? `转换完成：成功 ${ok}，失败 ${fail}` : `已创建 ${ok} 个 Grok OAuth 账号`)
      open.value = false
      reset()
      emit('created')
    } else {
      const firstErr = r?.failed?.[0]?.error
      message.error(`全部转换失败${firstErr ? `：${firstErr}` : ''}`)
    }
  } catch (e) {
    message.error(`SSO 转换失败：${e.message}`)
  } finally {
    loading.value = false
  }
}

// Codex 凭据导入：会话文件 / Agent 身份 JSON / PAT 三种（旧三 handler 合并等价）
const codexImport = async () => {
  const content = codexImportText.value.trim()
  if (!content) {
    message.error(codexImportKind.value === 'pat' ? '请粘贴 Access Token' : '请粘贴凭据文件内容')
    return
  }
  loading.value = true
  try {
    const name = accountName.value.trim() || undefined
    let msg
    if (codexImportKind.value === 'session') {
      // 会话文件支持多份 JSON 拼接提交（空行分隔），后端按 update_existing 合并去重
      const contents = content.split(/\n\s*\n/).map((s) => s.trim()).filter(Boolean)
      const r = await request('/admin/accounts/import/codex-session', {
        method: 'POST',
        body: contents.length > 1 ? { contents, name, update_existing: true } : { content, name, update_existing: true },
      })
      const ok = (r?.created || 0) + (r?.updated || 0)
      msg = `导入完成：新增 ${r?.created ?? 0}，更新 ${r?.updated ?? 0}，跳过 ${r?.skipped ?? 0}，失败 ${r?.failed ?? 0}`
      if (ok === 0 && (r?.failed || 0) === 0) { message.warning(msg); return }
      if ((r?.errors || []).length) { message.error(String(r.errors[0]?.message || '存在导入错误')); return }
    } else {
      const endpoint = codexImportKind.value === 'identity'
        ? '/admin/accounts/import/agent-identity'
        : '/admin/accounts/import/personal-access-token'
      const body = codexImportKind.value === 'identity' ? { content, name } : { access_token: content, name }
      const r = await request(endpoint, { method: 'POST', body })
      msg = r?.action === 'updated' ? '凭据已更新到既有账号' : '凭据导入成功，账号已创建'
    }
    message.success(msg)
    open.value = false
    reset()
    emit('created')
  } catch (e) {
    message.error(`导入失败：${e.message}`)
  } finally {
    loading.value = false
  }
}

const reset = () => {
  step.value = 1
  authUrl.value = ''
  sessionId.value = ''
  genState.value = ''
  callbackInput.value = ''
  tokenInfo.value = null
  accountName.value = ''
  antigravityRT.value = ''
  anthropicRTText.value = ''
  grokSSOText.value = ''
  codexImportText.value = ''
  codexImportKind.value = 'session'
}
watch(open, (o) => { if (o) reset() })

const stepsItems = computed(() => (isAnthropic.value && anthropicInput.value === 'cookie'
  ? [{ title: '选择方式' }, { title: '粘贴凭据' }, { title: '完成' }]
  : [{ title: '生成链接' }, { title: '授权' }, { title: '完成' }]))
</script>

<template>
  <a-modal v-model:open="open" title="OAuth 授权接入" width="580px" :footer="null">
    <a-steps :current="step - 1" size="small" style="margin-bottom: 16px" :items="stepsItems" />

    <!-- ① 平台 / 方式选择 -->
    <div v-if="step === 1">
      <div class="flab">平台</div>
      <a-select v-model:value="platform" :options="PLATFORMS" style="width: 100%" />
      <template v-if="isAnthropic">
        <div class="flab">授权方式</div>
        <a-radio-group v-model:value="addMethod">
          <a-radio value="oauth">OAuth（授权码流）</a-radio>
          <a-radio value="setup-token">Setup Token（长期令牌）</a-radio>
        </a-radio-group>
        <div class="flab">凭据来源</div>
        <a-radio-group v-model:value="anthropicInput">
          <a-radio value="code">浏览器授权（推荐）</a-radio>
          <a-radio value="cookie">粘贴 SessionKey / Token（支持多行批量）</a-radio>
        </a-radio-group>
      </template>
      <template v-if="platform === 'gemini'">
        <div class="flab">OAuth 类型</div>
        <a-radio-group v-model:value="geminiOauthType">
          <a-radio value="code_assist">Code Assist</a-radio>
          <a-radio value="google_one">Google One</a-radio>
          <a-radio value="ai_studio" :disabled="!!geminiCaps && !geminiCaps.ai_studio_oauth_enabled">AI Studio</a-radio>
        </a-radio-group>
        <div v-if="geminiCaps && !geminiCaps.ai_studio_oauth_enabled" class="hint" style="margin-top: 6px">
          AI Studio 授权未启用（需配置自定义 Gemini OAuth 客户端）。启用时回调地址需包含 {{ (geminiCaps.required_redirect_uris || [])[0] || '—' }}
        </div>
        <a-input v-if="geminiOauthType === 'code_assist'" v-model:value="geminiProjectId" placeholder="项目 ID（可留空自动创建）" style="margin-top: 8px" />
      </template>
      <template v-if="platform === 'antigravity'">
        <div class="flab">接入方式</div>
        <a-radio-group v-model:value="anthropicInput">
          <a-radio value="code">浏览器授权（推荐）</a-radio>
          <a-radio value="cookie">粘贴 refresh_token</a-radio>
        </a-radio-group>
        <a-input-password v-if="anthropicInput === 'cookie'" v-model:value="antigravityRT" placeholder="粘贴 antigravity refresh_token" style="margin-top: 8px" />
      </template>
      <template v-if="isOAuthRTPlatform">
        <div class="flab">接入方式</div>
        <a-radio-group v-model:value="rtMode">
          <a-radio value="code">浏览器授权（推荐）</a-radio>
          <a-radio value="cookie">粘贴 refresh_token</a-radio>
        </a-radio-group>
        <a-input-password v-if="rtMode === 'cookie'" v-model:value="rtText" :placeholder="platform === 'grok' ? '粘贴 Grok refresh_token' : '粘贴 OpenAI refresh_token'" style="margin-top: 8px" />
      </template>
      <template v-if="isGrokSSO">
        <div class="flab">SSO Token（每行一个，支持批量）</div>
        <a-textarea v-model:value="grokSSOText" :rows="6" placeholder="粘贴 Grok SSO cookie 值，一行一个；后端将逐个转换为 OAuth 凭据并创建账号" />
        <div class="hint" style="margin-top: 6px">转换在服务端进行，token 较多时可能需要数分钟。</div>
      </template>
      <template v-if="isCodexImport">
        <div class="flab">凭据类型</div>
        <a-radio-group v-model:value="codexImportKind">
          <a-radio value="session">Codex 会话文件（auth.json）</a-radio>
          <a-radio value="identity">Agent 身份 JSON</a-radio>
          <a-radio value="pat">Personal Access Token</a-radio>
        </a-radio-group>
        <div class="flab" style="margin-top: 8px">{{ codexImportKind === 'pat' ? 'Access Token' : '凭据内容（JSON，多份用空行分隔）' }}</div>
        <a-textarea v-model:value="codexImportText" :rows="7" :placeholder="codexImportKind === 'pat' ? '粘贴 Access Token' : '粘贴 JSON 凭据文件内容'" />
      </template>
      <div class="flab">账号名称（留空自动生成）</div>
      <a-input v-model:value="accountName" placeholder="默认取邮箱或自动编号" />
      <a-button v-if="isGrokSSO" type="primary" block style="margin-top: 12px" :loading="loading" @click="grokSSOImport">② 转换并创建</a-button>
      <a-button v-else-if="isCodexImport" type="primary" block style="margin-top: 12px" :loading="loading" @click="codexImport">② 导入凭据</a-button>
      <a-button v-else-if="canBrowser" type="primary" block style="margin-top: 12px" :loading="loading" @click="genUrl">② 生成授权链接</a-button>
      <a-button v-else type="primary" block style="margin-top: 12px" :loading="loading" @click="runByRT">② 验证并创建</a-button>
    </div>

    <!-- ② 授权码流：打开链接 + 回粘贴 -->
    <div v-else-if="step === 2">
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
        <a-button type="primary" :loading="loading" :disabled="!parsedCode" @click="exchange">③ 完成授权</a-button>
        <a-button @click="reset">重新开始</a-button>
      </a-space>
    </div>

    <!-- ③ 确认创建 -->
    <div v-else-if="step === 3">
      <a-alert type="success" show-icon message="凭据获取成功，确认账号名称后创建。" style="margin-bottom: 12px" />
      <div class="flab">账号名称</div>
      <a-input v-model:value="accountName" />
      <div v-for="([k, val]) in previewKv" :key="k" class="kv"><span>{{ k }}</span><b>{{ val }}</b></div>
      <a-space style="margin-top: 12px">
        <a-button type="primary" :loading="loading" @click="createAccount">创建账号</a-button>
        <a-button @click="reset">重新开始</a-button>
      </a-space>
    </div>
  </a-modal>
</template>

<style scoped>
.flab { font-size: 13px; color: var(--text2); margin: 10px 0 6px; }
.hint { font-size: 12px; color: var(--text2); margin: 8px 0; }
.urlbox { padding: 10px 12px; border-radius: 8px; background: var(--card); border: 1px solid var(--hr); word-break: break-all; }
.url { font-size: 12px; font-family: Consolas, monospace; }
.kv { display: flex; justify-content: space-between; padding: 6px 0; border-bottom: 1px solid var(--hr); font-size: 13px; }
.kv span { color: var(--text2); }
.kv b { font-weight: 500; max-width: 70%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
