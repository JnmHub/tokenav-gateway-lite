<script setup>
/* OpenAI OAuth（开放授权）接入弹窗——对照旧 useOpenAIOAuth.ts + ReAuthAccountModal.vue
 * ① POST /admin/openai/generate-auth-url {} → {auth_url, session_id}（auth_url 的 state 参数留作交换校验）
 * ② 用户浏览器完成登录后，把回调链接或授权码粘回来（自动从 code= 提取）
 * ③ POST /admin/openai/exchange-code {session_id, code, state} → tokenInfo
 *    → buildCredentials（access/refresh/id_token/expires_at/账号标识…）
 *    → POST /admin/accounts 创建 oauth 型账号（名称默认取邮箱）
 * 仅覆盖 openai 平台；anthropic/gemini 端点不同，待后续接入 */
import { ref, computed } from 'vue'
import { request } from '../../api'
import { message } from 'ant-design-vue'

const open = defineModel('open', { type: Boolean, default: false })
const emit = defineEmits(['created'])

const step = ref(1)
const loading = ref(false)
const authUrl = ref('')
const sessionId = ref('')
const oauthState = ref('')
const callbackInput = ref('')
const tokenInfo = ref(null)
const accountName = ref('')

// 从粘贴内容里提取 code 与 state（支持完整回调 URL 或裸授权码）
const parsedCode = computed(() => {
  const raw = callbackInput.value.trim()
  if (!raw) return ''
  if (!raw.includes(' ') && !raw.includes('=')) return raw
  try {
    const u = new URL(raw)
    return u.searchParams.get('code') || ''
  } catch {
    const m = raw.match(/code=([^&\s]+)/)
    return m ? m[1] : ''
  }
})
const parsedState = computed(() => {
  const raw = callbackInput.value.trim()
  try {
    const u = new URL(raw)
    return u.searchParams.get('state') || oauthState.value || ''
  } catch {
    const m = raw.match(/state=([^&\s]+)/)
    return m ? m[1] : oauthState.value || ''
  }
})

const genUrl = async () => {
  loading.value = true
  try {
    const res = await request('/admin/openai/generate-auth-url', { method: 'POST', body: {} })
    authUrl.value = res.auth_url
    sessionId.value = res.session_id
    try { oauthState.value = new URL(res.auth_url).searchParams.get('state') || '' } catch { /* 忽略解析失败 */ }
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

const exchange = async () => {
  if (!parsedCode.value) { message.error('请先粘贴回调链接或授权码'); return }
  if (!parsedState.value) { message.error('缺少 state 校验参数（请粘贴完整回调链接）'); return }
  loading.value = true
  try {
    const info = await request('/admin/openai/exchange-code', {
      method: 'POST',
      body: { session_id: sessionId.value, code: parsedCode.value, state: parsedState.value },
    })
    tokenInfo.value = info
    accountName.value = info?.email || info?.name || `oauth-${new Date().toISOString().slice(5, 16)}`
    step.value = 3
  } catch (e) {
    message.error(`交换令牌失败：${e.message}`)
  } finally {
    loading.value = false
  }
}

const createAccount = async () => {
  const t = tokenInfo.value
  if (!t) return
  if (!accountName.value.trim()) { message.error('请填写账号名称'); return }
  // credentials 组装规则与旧 buildCredentials 一致：只写非空字段，防止空值覆盖
  const creds = { access_token: t.access_token, expires_at: t.expires_at }
  for (const k of ['refresh_token', 'id_token', 'email', 'chatgpt_account_id', 'chatgpt_user_id', 'organization_id', 'account_id']) {
    if (t[k]) creds[k] = t[k]
  }
  const extra = {}
  if (t.email) extra.email = t.email
  if (t.name) extra.name = t.name
  loading.value = true
  try {
    await request('/admin/accounts', {
      method: 'POST',
      body: {
        name: accountName.value.trim(),
        platform: 'openai',
        type: 'oauth',
        credentials: creds,
        extra: Object.keys(extra).length ? extra : undefined,
      },
    })
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

const reset = () => {
  step.value = 1
  authUrl.value = ''
  sessionId.value = ''
  oauthState.value = ''
  callbackInput.value = ''
  tokenInfo.value = null
  accountName.value = ''
}
</script>

<template>
  <a-modal v-model:open="open" title="OpenAI OAuth 授权接入" width="560px" :footer="null">
    <a-steps :current="step - 1" size="small" style="margin-bottom: 16px" :items="[{ title: '生成链接' }, { title: '授权' }, { title: '完成' }]" />

    <!-- ① 生成授权链接 -->
    <div v-if="step === 1">
      <p class="hint">点击生成授权链接，然后在浏览器中登录 OpenAI 完成授权。</p>
      <a-button type="primary" block :loading="loading" @click="genUrl">① 生成授权链接</a-button>
    </div>

    <!-- ② 打开授权并回填 -->
    <div v-else-if="step === 2">
      <div class="urlbox">
        <span class="url">{{ authUrl }}</span>
        <a-space style="margin-top: 8px">
          <a-button size="small" type="primary" @click="openAuthPage">打开授权页</a-button>
          <a-button size="small" @click="copyUrl">复制链接</a-button>
        </a-space>
      </div>
      <p class="hint" style="margin-top: 12px">授权完成后，把浏览器跳转的<b>完整回调链接</b>（或裸授权码）粘贴到这里：</p>
      <a-textarea v-model:value="callbackInput" :rows="3" placeholder="http://localhost:1455/auth/callback?code=...&state=..." />
      <a-space style="margin-top: 12px">
        <a-button type="primary" :loading="loading" :disabled="!parsedCode" @click="exchange">③ 完成授权</a-button>
        <a-button @click="reset">重新开始</a-button>
      </a-space>
    </div>

    <!-- ③ 确认创建 -->
    <div v-else-if="step === 3">
      <a-alert type="success" show-icon message="令牌交换成功，确认账号名称后创建。" style="margin-bottom: 12px" />
      <a-form layout="vertical" size="small">
        <a-form-item label="账号名称" required>
          <a-input v-model:value="accountName" />
        </a-form-item>
        <div v-if="tokenInfo?.email" class="kv"><span>邮箱</span><b>{{ tokenInfo.email }}</b></div>
        <div v-if="tokenInfo?.plan_type" class="kv"><span>套餐</span><b>{{ tokenInfo.plan_type }}</b></div>
      </a-form>
      <a-space style="margin-top: 8px">
        <a-button type="primary" :loading="loading" @click="createAccount">创建账号</a-button>
        <a-button @click="reset">重新开始</a-button>
      </a-space>
    </div>
  </a-modal>
</template>

<style scoped>
.hint { font-size: 12px; color: var(--text2); }
.urlbox { padding: 10px 12px; border-radius: 8px; background: var(--card); border: 1px solid var(--hr); word-break: break-all; }
.url { font-size: 12px; font-family: Consolas, monospace; }
.kv { display: flex; justify-content: space-between; padding: 6px 0; border-bottom: 1px solid var(--hr); font-size: 13px; }
.kv span { color: var(--text2); }
</style>
