<script setup>
/* ============================================================
 * 管理员 TOTP 启用/禁用弹窗（对照旧 TotpSetupModal / TotpDisableDialog）
 * 接口（与旧 totpAPI 完全一致）：
 *  - GET  /user/totp/verification-method → { method: 'email' | 'password' }
 *  - POST /user/totp/send-code           （email 方式先发验证码）
 *  - POST /user/totp/setup    { password | email_code } → { secret, qr_code_url, setup_token }
 *  - POST /user/totp/enable   { totp_code, setup_token }
 *  - POST /user/totp/disable  { password | email_code }
 * 启停都会撤销全部旧会话：成功后由父组件登出回到锁屏。
 * ============================================================ */
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { request as apiRequest, isCancellation } from '../../api'
import { useRequestLifetime } from '../../composables/useRequestLifetime'
import { useDiscardGuard } from '../../core/windowContext'
import W12Button from '../design/W12Button.vue'
import W12Input from '../design/W12Input.vue'
import W12Dialog from '../design/W12Dialog.vue'
import { message } from 'ant-design-vue'

const props = defineProps({
  mode: { type: String, required: true }, // 'setup' | 'disable'
})
const emit = defineEmits(['close', 'success'])

const method = ref('password') // 身份二次验证方式
const verifyPassword = ref('')
const emailCode = ref('')
const sending = ref(false)
const cooldown = ref(0)
let timer = null

// setup 专属状态
const step = ref(0) // 0=身份验证 1=扫码 2=输入动态码
const setupData = ref(null) // { secret, qr_code_url, setup_token }
const totpCode = ref('')
const busy = ref(false)
const lifetime = useRequestLifetime(() => {
  verifyPassword.value = ''; emailCode.value = ''; totpCode.value = ''; setupData.value = null
  if (timer) clearInterval(timer); timer = null; cooldown.value = 0
})
const request = (path, options) => apiRequest(path, { ...options, ...lifetime })

const canClose = useDiscardGuard(() => !!(verifyPassword.value || emailCode.value || totpCode.value || setupData.value), () => busy.value || sending.value)
const close = async () => { if (await canClose() && lifetime.isAlive()) emit('close') }
const isSetup = computed(() => props.mode === 'setup')
const title = computed(() => (isSetup.value ? '启用双因素认证（TOTP）' : '禁用双因素认证（TOTP）'))

const startCooldown = () => {
  cooldown.value = 60
  timer = setInterval(() => {
    cooldown.value -= 1
    if (cooldown.value <= 0) { clearInterval(timer); timer = null }
  }, 1000)
}

const sendCode = async () => {
  if (sending.value || cooldown.value || !lifetime.isAlive()) return
  sending.value = true
  try {
    await request('/user/totp/send-code', { method: 'POST', body: {} })
    message.success('验证码已发送')
    startCooldown()
  } catch (e) {
    if (!isCancellation(e)) message.error(`发送验证码失败：${e.message}`)
  } finally {
    sending.value = false
  }
}

const verifyPayload = () => (method.value === 'email' ? { email_code: emailCode.value.trim() } : { password: verifyPassword.value })

/* ── setup：身份验证 → 拿 secret/QR → 提交动态码 ── */
const doSetup = async () => {
  if (busy.value || !lifetime.isAlive()) return
  busy.value = true
  try {
    setupData.value = await request('/user/totp/setup', { method: 'POST', body: verifyPayload() })
    step.value = 1
  } catch (e) {
    if (!isCancellation(e)) message.error(`身份验证失败：${e.message}`)
  } finally {
    busy.value = false
  }
}

const doEnable = async () => {
  if (busy.value || !lifetime.isAlive()) return
  const code = totpCode.value.replace(/\D/g, '')
  if (code.length !== 6) { message.error('请输入 6 位动态验证码'); return }
  busy.value = true
  try {
    await request('/user/totp/enable', { method: 'POST', body: { totp_code: code, setup_token: setupData.value.setup_token } })
    message.success('双因素认证已启用')
    emit('success')
  } catch (e) {
    if (!isCancellation(e)) message.error(`启用失败：${e.message}`)
    totpCode.value = ''
  } finally {
    busy.value = false
  }
}

/* ── disable：身份验证即完成 ── */
const doDisable = async () => {
  if (busy.value || !lifetime.isAlive()) return
  busy.value = true
  try {
    await request('/user/totp/disable', { method: 'POST', body: verifyPayload() })
    message.success('双因素认证已禁用')
    emit('success')
  } catch (e) {
    if (!isCancellation(e)) message.error(`禁用失败：${e.message}`)
  } finally {
    busy.value = false
  }
}

const copySecret = async () => {
  try {
    await navigator.clipboard.writeText(setupData.value.secret)
    if (lifetime.isAlive()) message.success('已复制密钥')
  } catch { if (lifetime.isAlive()) message.error('复制失败') }
}

onMounted(async () => {
  try {
    const m = await request('/user/totp/verification-method', { method: 'GET' })
    method.value = m.method === 'email' ? 'email' : 'password'
  } catch (e) {
    if (!isCancellation(e)) message.error(`读取验证方式失败：${e.message}`)
    if (!isCancellation(e) && lifetime.isAlive()) emit('close')
  }
})

onUnmounted(() => { if (timer) clearInterval(timer) })
</script>

<template>
  <W12Dialog :open="true" :busy="busy || sending" :title="title" :footer="null" :width="440" @cancel="close">
    <!-- 禁用：一步身份验证 -->
    <div v-if="!isSetup" class="mbody">
      <p class="hint" style="margin: 0 0 12px">
        {{ method === 'email' ? '输入邮箱收到的 6 位验证码以确认操作。' : '输入当前登录密码以确认操作。' }}
      </p>
      <a-input-password v-if="method === 'password'" v-model:value="verifyPassword" placeholder="当前密码" autocomplete="current-password" />
      <div v-else style="display: flex; gap: 8px">
        <W12Input v-model:value="emailCode" placeholder="6 位邮箱验证码" :maxlength="6" />
        <W12Button :loading="sending" :disabled="cooldown > 0" @click="sendCode">{{ cooldown > 0 ? `${cooldown}s` : '发送验证码' }}</W12Button>
      </div>
      <div class="mbtns">
        <W12Button @click="close">取消</W12Button>
        <W12Button variant="danger" :loading="busy" :disabled="method === 'email' ? emailCode.trim().length !== 6 : !verifyPassword" @click="doDisable">确认禁用</W12Button>
      </div>
    </div>

    <!-- 启用：三步流 -->
    <div v-else class="mbody">
      <!-- 步骤 0：身份验证 -->
      <template v-if="step === 0">
        <p class="hint" style="margin: 0 0 12px">
          {{ method === 'email' ? '先输入邮箱验证码验证身份。' : '先输入当前登录密码验证身份。' }}
        </p>
        <a-input-password v-if="method === 'password'" v-model:value="verifyPassword" placeholder="当前密码" autocomplete="current-password" @pressEnter="doSetup" />
        <div v-else style="display: flex; gap: 8px">
          <W12Input v-model:value="emailCode" placeholder="6 位邮箱验证码" :maxlength="6" />
          <W12Button :loading="sending" :disabled="cooldown > 0" @click="sendCode">{{ cooldown > 0 ? `${cooldown}s` : '发送验证码' }}</W12Button>
        </div>
        <div class="mbtns">
          <W12Button @click="close">取消</W12Button>
          <W12Button variant="primary" :loading="busy" :disabled="method === 'email' ? emailCode.trim().length !== 6 : !verifyPassword" @click="doSetup">下一步</W12Button>
        </div>
      </template>
      <!-- 步骤 1：扫码 / 密钥 -->
      <template v-else-if="step === 1">
        <p class="hint" style="margin: 0 0 12px">用验证器 App（如 Google Authenticator）扫码，或手动输入密钥。</p>
        <div class="qrbox">
          <img v-if="setupData?.qr_code_url" :src="setupData.qr_code_url" alt="TOTP QR" />
          <div v-else class="hint">未返回二维码图片，请使用下方密钥手动添加。</div>
        </div>
        <div class="secretrow">
          <span class="mono">{{ setupData?.secret }}</span>
          <W12Button compact @click="copySecret">复制</W12Button>
        </div>
        <div class="mbtns">
          <W12Button @click="step = 0">上一步</W12Button>
          <W12Button variant="primary" @click="step = 2">下一步</W12Button>
        </div>
      </template>
      <!-- 步骤 2：输入动态码 -->
      <template v-else>
        <p class="hint" style="margin: 0 0 12px">输入验证器显示的 6 位动态码完成启用。</p>
        <W12Input v-model:value="totpCode" placeholder="6 位动态验证码" :maxlength="6" inputmode="numeric" @pressEnter="doEnable" />
        <div class="mbtns">
          <W12Button @click="step = 1">上一步</W12Button>
          <W12Button variant="primary" :loading="busy" @click="doEnable">验证并启用</W12Button>
        </div>
      </template>
    </div>
  </W12Dialog>
</template>

<style scoped>
.mbody { padding-top: 4px; }
.mbtns { display: flex; justify-content: flex-end; gap: 8px; margin-top: 16px; }
.qrbox { display: flex; justify-content: center; padding: 12px; border-radius: 8px; background: #fff; }
.qrbox img { width: 180px; height: 180px; }
.secretrow { display: flex; align-items: center; justify-content: space-between; gap: 8px; margin-top: 10px; padding: 8px 10px; border-radius: 8px; background: var(--card); border: 1px solid var(--hr); word-break: break-all; }
</style>
