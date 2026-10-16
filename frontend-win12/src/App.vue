<script setup>
import { computed, watch, ref, onMounted, onBeforeUnmount, nextTick } from 'vue'
import { ConfigProvider, Modal, message } from 'ant-design-vue'
import zhCN from 'ant-design-vue/es/locale/zh_CN'
import { useThemeStore, useAuthStore } from './core/stores'
import { getToken } from './api'
import { buildAntdTheme } from './core/antdTheme'
import BootScreen from './components/BootScreen.vue'
import LockScreen from './components/LockScreen.vue'
import Desktop from './components/Desktop.vue'
import GatewayBindingGate from './components/binding/GatewayBindingGate.vue'
const phase = ref('boot'), ready = ref(false)
const theme = useThemeStore(), auth = useAuthStore()
auth.initSession(); theme._apply()
const antdTheme = computed(() => buildAntdTheme({ dark: theme.dark, accent: theme.accent }))
const authenticated = computed(() => !!auth.user && !!getToken())
const bootDest = () => authenticated.value ? 'desktop' : 'lock'
watch(() => auth.sessionEpoch, () => { Modal.destroyAll(); message.destroy() })
watch(() => auth.user, (user) => { if (!user && phase.value === 'desktop') phase.value = 'lock' })
const unauthorized = () => auth.resetSession('unauthorized')
const storage = (event) => {
  if (!['w12-token', 'w12-user'].includes(event.key)) return
  // 其它标签页换身份时本页清内存；storage 回调不写新 token，不能形成广播回环。
  auth.invalidateLocalSession()
}
onMounted(async () => {
  window.addEventListener('w12-unauthorized', unauthorized); window.addEventListener('storage', storage)
  await nextTick(); ready.value = true
})
onBeforeUnmount(() => { auth.disposeSession(); window.removeEventListener('w12-unauthorized', unauthorized); window.removeEventListener('storage', storage) })
</script>
<template>
  <ConfigProvider :locale="zhCN" :theme="antdTheme">
    <!-- 退出/锁定卸载整个业务子树，不能只盖住含明文与草稿的桌面。 -->
    <Desktop v-if="phase === 'desktop' && authenticated" :key="auth.sessionEpoch" />
    <GatewayBindingGate v-if="phase === 'desktop' && authenticated" :key="`binding-${auth.sessionEpoch}`" />
    <LockScreen v-if="phase !== 'desktop' || !authenticated" @unlock="phase = bootDest()" />
    <BootScreen v-if="phase === 'boot'" :ready="ready" @done="phase = bootDest()" />
  </ConfigProvider>
</template>
