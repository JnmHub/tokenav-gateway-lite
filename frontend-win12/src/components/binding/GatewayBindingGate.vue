<script setup>
import { onMounted, onBeforeUnmount, ref } from 'vue'
import W12Button from '../design/W12Button.vue'
import GatewayBindingWizard from './GatewayBindingWizard.vue'
import { bindingApi, bindingRequiresGate, bindingErrorText } from '../../api/gatewayBinding.js'
import { useRequestLifetime } from '../../composables/useRequestLifetime.js'
const status = ref(null), error = ref(''), loading = ref(true)
const lifetime = useRequestLifetime(() => { status.value = null })
async function load() {
  loading.value = true; error.value = ''
  try { status.value = await bindingApi.status(lifetime) }
  catch (e) { if (lifetime.isAlive()) error.value = bindingErrorText(e) }
  finally { if (lifetime.isAlive()) loading.value = false }
}
onMounted(() => { load(); window.addEventListener('w12-binding-changed', load) })
onBeforeUnmount(() => window.removeEventListener('w12-binding-changed', load))
</script>
<template>
  <div v-if="bindingRequiresGate(status)" class="binding-gate" role="dialog" aria-modal="true" aria-label="首次主站绑定">
    <div class="gate-window">
      <div v-if="loading" class="gate-loading" role="status" aria-live="polite">
        <span class="gate-loading-spinner" aria-hidden="true" />
        <span class="gate-loading-text">正在读取主站绑定状态</span>
      </div>
      <div v-else-if="error" class="gate-error"><a-alert :message="error" type="error" show-icon /><W12Button @click="load">重新读取</W12Button></div>
      <GatewayBindingWizard v-else :status="status" @status="status = $event" @bound="status = $event" />
    </div>
  </div>
</template>
<style scoped>
.binding-gate { position: fixed; inset: 0; z-index: 20000; display: grid; place-items: center; padding: 16px; background: rgba(24,24,24,.24); backdrop-filter: blur(14px); }
.gate-window { width: min(540px,100%); max-height: calc(100dvh - 32px); overflow: auto; border-radius: 12px; background: var(--surface); box-shadow: 0 4px 20px rgba(0,0,0,.08); }
/* 加载态用统一的 Win12 表单行高与毛玻璃容器，避免 antd 默认 spin 塌缩成细长横条 */
.gate-loading { display: flex; align-items: center; justify-content: center; gap: 12px; min-height: 120px; padding: 24px 16px; }
.gate-loading-spinner { width: 20px; height: 20px; border-radius: 50%; border: 2px solid var(--primary,#2563eb); border-top-color: transparent; animation: gate-spin .8s linear infinite; }
.gate-loading-text { font-size: 14px; color: var(--text-secondary,#5c5c5c); }
@keyframes gate-spin { to { transform: rotate(360deg); } }
.gate-error { display: grid; gap: 16px; padding: 16px; }
@media(max-width:600px) { .binding-gate { padding: 0; }.gate-window { width:100%;height:100dvh;max-height:100dvh;border-radius:0; } }
</style>
