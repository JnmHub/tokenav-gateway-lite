<script setup>
import { computed, ref } from 'vue'
import { message } from 'ant-design-vue'
import { UserOutlined, LockOutlined, SafetyOutlined } from '@ant-design/icons-vue'
import { useAuthStore, useThemeStore } from '../core/stores'
import LockClock from './LockClock.vue'
import { wallpaperBackground } from '../core/wallpaper'

const emit = defineEmits(['unlock'])

const auth = useAuthStore()
const theme = useThemeStore()

// lock: 点击解锁等待页；login: 登录表单；twofa: 2FA 验证码
const mode = ref(auth.user ? 'lock' : 'login')
const gone = ref(false)
const loading = ref(false)
const formState = ref({ email: '', password: '' })
// 2FA 中间态：第一步密码验证通过后由 auth.login 抛出（见 stores.js）
const twoFA = ref({ tempToken: '', emailMasked: '' })
const totpCode = ref('')

const unlock = () => {
  gone.value = true
  setTimeout(() => emit('unlock'), 420)
}

const onClickScreen = () => {
  if (mode.value === 'lock') mode.value = 'login'
}

const onFinish = async (values) => {
  loading.value = true
  try {
    await auth.login(values.email, values.password)
    message.success(`欢迎回来，${auth.user.nickname ?? auth.user.username ?? auth.user.email}`)
    unlock()
  } catch (e) {
    if (e.twoFactor) {
      // 密码正确但需要 2FA：切换到验证码界面
      twoFA.value = e.twoFactor
      mode.value = 'twofa'
    } else {
      message.error(e.message)
    }
  } finally {
    loading.value = false
  }
}

const onFinish2FA = async () => {
  const code = String(totpCode.value || '').trim()
  if (!/^\d{6}$/.test(code)) { message.error('请输入 6 位验证码'); return }
  loading.value = true
  try {
    await auth.login2FA(twoFA.value.tempToken, code)
    message.success(`欢迎回来，${auth.user.nickname ?? auth.user.username ?? auth.user.email}`)
    unlock()
  } catch (e) {
    message.error(e.message)
  } finally {
    loading.value = false
  }
}

const backToLogin = () => {
  mode.value = 'login'
  totpCode.value = ''
}

// 锁屏与桌面共用同一张壁纸（同一主题风格 + 明暗），解锁过渡不会出现两种底色跳变
const bgStyle = computed(() => ({ background: wallpaperBackground(theme.style, theme.dark) }))
</script>

<template>
  <div
    class="w12-lock"
    :class="{ 'login-mode': mode !== 'lock', hide: gone }"
    :style="bgStyle"
    @click="onClickScreen"
  >
    <LockClock v-if="mode === 'lock'" />
    <div v-else-if="mode === 'twofa'" class="w12-login-card" @click.stop>
      <div class="w12-login-avatar"><LockOutlined /></div>
      <div class="w12-login-title">两步验证</div>
      <div class="w12-login-sub">请输入 {{ twoFA.emailMasked }} 的 6 位动态验证码</div>
      <a-form layout="vertical" size="large" @finish="onFinish2FA">
        <a-form-item>
          <a-input v-model:value="totpCode" placeholder="000000" :maxlength="6" inputmode="numeric" class="w12-totp" @pressEnter="onFinish2FA">
            <template #prefix><SafetyOutlined /></template>
          </a-input>
        </a-form-item>
        <button class="w12-login-btn" type="submit" :disabled="loading">
          {{ loading ? '验证中…' : '验证并登录' }}
        </button>
      </a-form>
      <a-button type="link" size="small" class="w12-totp-back" @click="backToLogin">返回重新登录</a-button>
    </div>
    <div v-else class="w12-login-card" @click.stop>
      <!-- tokenav 品牌标记：CSS mask 蒙版上色（#10B981） -->
      <div class="w12-login-brand" aria-hidden="true" />
      <div class="w12-login-avatar">
        <UserOutlined />
      </div>
      <div class="w12-login-title">登录到 tokenav gateway</div>
      <a-form layout="vertical" :model="formState" size="large" @finish="onFinish">
        <a-form-item name="email" :rules="[{ required: true, message: '请输入邮箱' }]">
          <a-input v-model:value="formState.email" placeholder="邮箱">
            <template #prefix><UserOutlined /></template>
          </a-input>
        </a-form-item>
        <a-form-item name="password" :rules="[{ required: true, message: '请输入密码' }]">
          <a-input-password v-model:value="formState.password" placeholder="密码">
            <template #prefix><LockOutlined /></template>
          </a-input-password>
        </a-form-item>
        <button class="w12-login-btn" type="submit" :disabled="loading">
          {{ loading ? '登录中…' : '登录' }}
        </button>
      </a-form>
    </div>
  </div>
</template>



