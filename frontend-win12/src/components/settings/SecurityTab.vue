<script setup>
import W12Input from '../design/W12Input.vue'
import W12Button from '../design/W12Button.vue'
/* ============================================================
 * 安全 tab（对照旧 SettingsView.vue「安全」tab）
 * - 修改管理员密码：PUT /user/password（成功后全部旧会话失效 → 登出回锁屏）
 * - 管理员 TOTP 卡：GET /user/totp/status；启用/禁用走 TotpModal（同样登出）
 * - 管理员 API Key：只读状态卡（揭示/重生成在「系统配置」模块）
 * - 访问安全：PATCH /admin/settings 带当前修订局部更新
 *    totp_enabled / turnstile_enabled / turnstile_site_key
 *    turnstile_secret_key（留空不改，payload 仅在有值时携带）
 *    api_key_acl_trust_forwarded_ip + forwarded_client_ip_headers
 *    （4 预设 chips + 自定义 + 最多 16 个 + HTTP 头名格式校验）
 * ============================================================ */
import { ref, reactive, onMounted } from 'vue'
import { request as apiRequest, isCancellation } from '../../api'
import { settingsErrorMessage } from '../../api/settings'
import { useSettingsContract } from '../../composables/useSettingsContract'
import { createLatestRequest } from '../../api/latestRequest'
import { useDraftBaseline, requireFields } from '../../composables/useDraftBaseline'
import { message } from 'ant-design-vue'
import { useAuthStore } from '../../core/stores'
import TotpModal from './TotpModal.vue'
import ComplianceCard from './ComplianceCard.vue'

const HEADER_RE = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/
const HEADER_PRESETS = ['CF-Connecting-IP', 'X-Real-IP', 'True-Client-IP', 'X-Forwarded-For']

const authStore = useAuthStore()

const loading = ref(true)
const err = ref('')

// ── 修改密码 ──
const pwd = reactive({ old: '', next: '', confirm: '' })
const pwdChanging = ref(false)

const changePassword = async () => {
  if (pwdChanging.value) return
  if (pwd.next.length < 8) { message.error('新密码至少需要 8 个字符'); return }
  if (pwd.next !== pwd.confirm) { message.error('两次输入的新密码不一致'); return }
  pwdChanging.value = true
  try {
    await request('/user/password', { method: 'PUT', body: { old_password: pwd.old, new_password: pwd.next } })
    message.success('密码已修改，所有旧登录令牌已失效，请重新登录')
    authStore.resetSession('password-changed')
  } catch (e) {
    if (!isCancellation(e)) message.error(`修改密码失败：${e.message}`)
  } finally {
    pwdChanging.value = false
  }
}

// ── TOTP 卡 ──
const totp = reactive({ loaded: false, enabled: false, featureEnabled: true, enabledAt: 0, busy: false })
const totpError = ref('')
const totpModal = ref('') // '' | 'setup' | 'disable'

const loadTotp = async () => {
  totpError.value = ''
  try {
    const s = await request('/user/totp/status', { method: 'GET' })
    totp.enabled = Boolean(s.enabled)
    totp.featureEnabled = s.feature_enabled !== false
    totp.enabledAt = s.enabled_at || 0
    totp.loaded = true
  } catch (e) { if (!isCancellation(e)) { totp.loaded = false; totpError.value = e.message } }
}

const onTotpSuccess = () => {
  // 启停 TOTP 会撤销全部旧会话：清理凭据回锁屏
  totpModal.value = ''
  authStore.resetSession('totp-changed')
}

// ── 管理 API Key（只读） ──
const ak = ref(null)

// ── 访问安全 ──
const sec = ref(null)
const savingSec = ref(false)
const customHeader = ref('')
const loadLane = createLatestRequest()
const contract = useSettingsContract(() => { loadLane.cancel(); draft.clear();
  sec.value = null; ak.value = null; customHeader.value = ''; totpModal.value = ''; totp.loaded = false; totp.enabled = false; totpError.value = ''
  Object.assign(pwd, { old: '', next: '', confirm: '' })
})
const draft = useDraftBaseline(() => sec.value, () => savingSec.value || pwdChanging.value)
useDraftBaseline(() => ({ ...pwd, customHeader: customHeader.value }), () => pwdChanging.value).accept()
const { conflict } = contract
const request = (path, options) => apiRequest(path, { ...options, ...contract.lifetime, signal: options?.signal || contract.lifetime.signal })
const applySec = (settings) => {
  requireFields(settings, ["totp_enabled","turnstile_enabled","turnstile_site_key","turnstile_secret_key_configured","api_key_acl_trust_forwarded_ip","forwarded_client_ip_headers"])
  sec.value = {
    totp_enabled: Boolean(settings.totp_enabled),
    turnstile_enabled: Boolean(settings.turnstile_enabled),
    turnstile_site_key: settings.turnstile_site_key || '',
    turnstile_secret_key: '',
    turnstile_secret_key_configured: Boolean(settings.turnstile_secret_key_configured),
    api_key_acl_trust_forwarded_ip: Boolean(settings.api_key_acl_trust_forwarded_ip),
    forwarded_client_ip_headers: Array.from(new Set((settings.forwarded_client_ip_headers || []).map((i) => String(i).trim()).filter(Boolean))),
  }
}

const load = async () => {
  if (savingSec.value || pwdChanging.value) return
  const turn = loadLane.begin()
  loading.value = true
  try {
    const [settings, akRes] = await Promise.all([
      contract.read({ signal: turn.signal }),
      request('/admin/settings/admin-api-key', { method: 'GET', signal: AbortSignal.any([turn.signal, contract.lifetime.signal]) }).catch(() => null),
    ])
    ak.value = akRes
    if (!turn.isCurrent() || !contract.lifetime.isAlive()) return
    const edits = draft.edits()
    applySec(settings)
    draft.accept()
    Object.assign(sec.value, edits)
    err.value = ''
  } catch (e) {
    if (turn.isCurrent() && !isCancellation(e)) err.value = e.message
  } finally {
    if (turn.isCurrent() && contract.lifetime.isAlive()) loading.value = false
  }
}

/* ── 转发 IP 头 chips 逻辑（对照旧 has/add/remove/toggle） ── */
const hasHeader = (header) => sec.value.forwarded_client_ip_headers.some((i) => i.toLowerCase() === header.toLowerCase())

const addHeader = (header) => {
  const normalized = String(header || '').trim()
  if (!normalized || hasHeader(normalized)) return false
  if (sec.value.forwarded_client_ip_headers.length >= 16) {
    message.error('客户端 IP 请求头最多 16 个')
    return false
  }
  if (!HEADER_RE.test(normalized)) {
    message.error('请求头名称格式不正确')
    return false
  }
  sec.value.forwarded_client_ip_headers = [...sec.value.forwarded_client_ip_headers, normalized]
  return true
}

const removeHeader = (header) => {
  const wanted = header.toLowerCase()
  sec.value.forwarded_client_ip_headers = sec.value.forwarded_client_ip_headers.filter((i) => i.toLowerCase() !== wanted)
}

const toggleHeader = (header) => { hasHeader(header) ? removeHeader(header) : addHeader(header) }

const addCustomHeader = () => {
  if (addHeader(customHeader.value)) customHeader.value = ''
}

/* ── 访问安全保存：PATCH /admin/settings 带当前修订局部更新 ── */
const saveSec = async () => {
  if (savingSec.value || !sec.value) return
  savingSec.value = true
  loadLane.cancel(); loading.value = false
  try {
    const v = sec.value
    const body = {
      totp_enabled: v.totp_enabled,
      turnstile_enabled: v.turnstile_enabled,
      turnstile_site_key: String(v.turnstile_site_key || '').trim(),
      api_key_acl_trust_forwarded_ip: v.api_key_acl_trust_forwarded_ip,
      forwarded_client_ip_headers: [...v.forwarded_client_ip_headers],
    }
    // Turnstile Secret Key 留空 = 不修改
    if (String(v.turnstile_secret_key || '').trim()) {
      body.turnstile_secret_key = String(v.turnstile_secret_key).trim()
    }
    const submitted = JSON.parse(JSON.stringify(sec.value))
    const updated = await contract.save(body)
    const current = sec.value
    applySec(updated)
    draft.accept()
    for (const [key, value] of Object.entries(current)) if (JSON.stringify(value) !== JSON.stringify(submitted[key])) sec.value[key] = value
    await loadTotp()
    if (contract.lifetime.isAlive()) message.success('访问安全设置已保存')
  } catch (e) {
    if (!isCancellation(e)) message.error(`保存失败：${settingsErrorMessage(e)}`)
  } finally {
    savingSec.value = false
  }
}

onMounted(async () => {
  await Promise.allSettled([load(), loadTotp()])
})
</script>

<template>
  <a-spin :spinning="loading">
    <div class="sw w12-design">
      <div v-if="err" class="card"><W12Button @click="load" variant="danger">加载失败：{{ err }}（点击重试）</W12Button></div>
      <a-alert v-if="conflict" :message="conflict" type="warning" show-icon>
        <template #action><W12Button :disabled="savingSec" @click="load">重新读取</W12Button></template>
      </a-alert>
      <template v-if="sec">
        <!-- 修改管理员密码 -->
        <div class="card">
          <div class="card-title">修改管理员密码</div>
          <div class="card-sub">修改成功后所有旧登录令牌立即失效，需要重新登录。</div>
          <div class="form">
            <div class="frow"><label>当前密码</label><a-input-password v-model:value="pwd.old" class="fld" autocomplete="current-password" /></div>
            <div class="frow"><label>新密码</label><a-input-password v-model:value="pwd.next" class="fld" autocomplete="new-password" placeholder="至少 8 个字符" /></div>
            <div class="frow"><label>确认新密码</label><a-input-password v-model:value="pwd.confirm" class="fld" autocomplete="new-password" /></div>
          </div>
          <div class="savebar"><W12Button variant="primary" class="save" :loading="pwdChanging" @click="changePassword">修改密码</W12Button></div>
        </div>

        <!-- 管理员 TOTP 卡 -->
        <div class="card">
          <div class="card-title">管理员双因素认证（TOTP）</div>
          <W12Button v-if="totpError" @click="loadTotp">双因素状态读取失败：{{ totpError }}（重试）</W12Button>
          <a-spin :spinning="!totp.loaded && !totpError" size="small">
            <template v-if="totp.loaded">
              <div v-if="!totp.featureEnabled" class="hint" style="padding-left: 8px">
                双因素认证功能未开启。可在下方「访问安全」中打开 TOTP 功能开关后，再为管理员账号启用。
              </div>
              <div v-else class="frow">
                <label>当前状态</label>
                <div class="fmain">
                  <span class="static">{{ totp.enabled ? '已启用' : '未启用' }}</span>
                  <span v-if="totp.enabled && totp.enabledAt" class="static">启用时间：{{ new Date(totp.enabledAt * 1000).toLocaleString() }}</span>
                  <span style="flex: 1"></span>
                  <W12Button v-if="totp.enabled" @click="totpModal = 'disable'" variant="danger">禁用</W12Button>
                  <W12Button v-else variant="primary" @click="totpModal = 'setup'">启用</W12Button>
                </div>
              </div>
            </template>
          </a-spin>
        </div>

        <!-- 管理员 API Key（只读） -->
        <div class="card" v-if="ak">
          <div class="card-title">管理 API Key（只读）</div>
          <div class="form">
            <div class="frow"><label>已配置</label><span class="static">{{ ak.exists ? '是' : '否' }}</span></div>
            <div class="frow"><label>密钥掩码</label><span class="static mono">{{ ak.masked_key || '-' }}</span></div>
          </div>
          <a-alert style="margin-top: 8px" message="揭示 / 重生成 / 删除在「系统配置」模块管理。" type="info" show-icon />
        </div>

        <!-- 访问安全 -->
        <div class="card">
          <div class="card-title">访问安全</div>
          <div class="form">
            <div class="frow"><label>双因素认证功能</label><div class="fmain"><a-switch v-model:checked="sec.totp_enabled" /><span class="static">允许管理员启用 TOTP 并用于敏感操作的现场二次验证</span></div></div>
            <div class="frow"><label>Turnstile 人机验证</label><div class="fmain"><a-switch v-model:checked="sec.turnstile_enabled" /><span class="static">用于登录页的人机验证</span></div></div>
            <template v-if="sec.turnstile_enabled">
              <div class="frow"><label>Site Key（站点密钥）</label><W12Input v-model:value="sec.turnstile_site_key" class="fld" /></div>
              <div class="frow">
                <label>Secret Key（私密密钥）</label>
                <a-input-password v-model:value="sec.turnstile_secret_key" class="fld" autocomplete="new-password"
                  :placeholder="sec.turnstile_secret_key_configured ? '已配置，留空表示不修改' : '未配置'" />
              </div>
            </template>
            <div class="frow"><label>信任转发 IP</label><div class="fmain"><a-switch v-model:checked="sec.api_key_acl_trust_forwarded_ip" /><span class="static">网关前有可信反向代理时，允许 ACL、会话绑定和审计使用转发来源 IP</span></div></div>
            <div v-if="sec.api_key_acl_trust_forwarded_ip" class="subcard" style="margin: 4px 0 4px 8px">
              <div style="display: flex; align-items: center; justify-content: space-between; gap: 8px">
                <div>
                  <div style="font-size: 13px">客户端 IP 请求头</div>
                  <div class="hint">先点常用代理；只有直接连接来源命中 server.trusted_proxies 时才读取这些请求头。</div>
                </div>
                <span class="static mono">{{ sec.forwarded_client_ip_headers.length }}/16</span>
              </div>
              <div style="display: flex; flex-wrap: wrap; gap: 8px; margin-top: 10px">
                <button v-for="h in HEADER_PRESETS" :key="h" type="button" class="chip" :class="{ on: hasHeader(h) }" @click="toggleHeader(h)">{{ h }}</button>
              </div>
              <div v-if="sec.forwarded_client_ip_headers.length" style="display: flex; flex-wrap: wrap; gap: 6px; margin-top: 8px">
                <button v-for="h in sec.forwarded_client_ip_headers" :key="`sel-${h}`" type="button" class="chip-x" :title="`删除 ${h}`" @click="removeHeader(h)">{{ h }} ×</button>
              </div>
              <div style="display: flex; gap: 8px; margin-top: 10px; max-width: 460px">
                <W12Input v-model:value="customHeader" class="mono" placeholder="其他代理请求头（可选）" :maxlength="80" @pressEnter="addCustomHeader" />
                <W12Button @click="addCustomHeader">添加</W12Button>
              </div>
            </div>
          </div>
          <div class="savebar"><W12Button variant="primary" class="save" :loading="savingSec" @click="saveSec">保存访问安全</W12Button></div>
        </div>

        <!-- 管理员合规确认（GET /admin/compliance + POST /admin/compliance/accept） -->
        <ComplianceCard />
      </template>

      <TotpModal v-if="totpModal" :mode="totpModal" @close="totpModal = ''" @success="onTotpSuccess" />
    </div>
  </a-spin>
</template>

<style scoped>
@import './settings.css';
</style>

