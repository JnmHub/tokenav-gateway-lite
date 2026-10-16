<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { LinkOutlined, ReloadOutlined } from '@ant-design/icons-vue'
import W12Button from '../design/W12Button.vue'
import W12Input from '../design/W12Input.vue'
import { bindingApi, bindingErrorText, waitForBinding } from '../../api/gatewayBinding.js'
import { useRequestLifetime } from '../../composables/useRequestLifetime.js'

const props = defineProps({ status: { type: Object, default: null } })
const emit = defineEmits(['status', 'bound'])
const step = ref(0), busy = ref(false), error = ref('')
const current = ref(props.status)
const draft = reactive({ url: '', token: '', name: '区域网关', public_url: window.location.origin, region: 'default' })
const lifetime = useRequestLifetime(() => { draft.token = ''; error.value = '' })
const activating = computed(() => current.value?.phase === 'activating')
const setStatus = value => { current.value = value; emit('status', value) }
const options = () => ({ signal: lifetime.signal, sessionEpoch: lifetime.sessionEpoch })
async function poll(id) {
  const result = await waitForBinding(() => bindingApi.status(options()), id, { signal: lifetime.signal, onStatus: setStatus })
  if (lifetime.isAlive()) emit('bound', result)
}
async function next() {
  if (busy.value) return
  busy.value = true; error.value = ''
  try {
    if (step.value === 0) { await bindingApi.probe({ url: draft.url }, options()); step.value = 1 }
    else if (step.value === 1) { await bindingApi.verify({ url: draft.url, token: draft.token }, options()); step.value = 2 }
    else {
      const result = await bindingApi.register({ ...draft }, options())
      draft.token = ''
      if (result.status !== 202) throw new Error('节点登记未进入启用流程，请刷新检查。')
      setStatus(result.data)
      await poll(result.data.binding_id)
    }
  } catch (e) { if (lifetime.isAlive()) error.value = bindingErrorText(e) }
  finally { if (lifetime.isAlive()) busy.value = false }
}
async function retry() {
  busy.value = true; error.value = ''
  try { await bindingApi.retry(options()); await poll(current.value.binding_id) }
  catch (e) { if (lifetime.isAlive()) error.value = bindingErrorText(e) }
  finally { if (lifetime.isAlive()) busy.value = false }
}
onMounted(() => {
  if (current.value?.phase === 'registering') {
    Object.assign(draft, { url: current.value.url, name: current.value.name, public_url: current.value.public_url, region: current.value.region })
    step.value = 1
  }
  if (activating.value) { busy.value = true; poll(current.value.binding_id).catch(e => { if (lifetime.isAlive()) error.value = bindingErrorText(e) }).finally(() => { if (lifetime.isAlive()) busy.value = false }) }
})
</script>

<template>
  <section class="binding-wizard w12-design">
    <div class="binding-heading"><LinkOutlined /><h2>连接主站</h2></div>
    <p class="binding-help">完成主站绑定后，即可使用网关后台。账号、上游配置和请求记录会保留。</p>
    <template v-if="activating">
      <a-spin :spinning="busy"><h3>节点已登记，正在验证实际连接</h3><p>服务会有序重启。只有主站握手与配置同步完成，才会显示绑定成功。</p></a-spin>
      <p v-if="current.last_error" class="binding-help">{{ current.last_error }}</p>
      <W12Button :disabled="busy" @click="retry"><template #icon><ReloadOutlined /></template>重试连接</W12Button>
    </template>
    <template v-else>
      <ol class="binding-steps"><li v-for="(label, index) in ['主站地址', '对接密钥', '节点信息']" :key="label" :class="{ active: step === index, done: step > index }">{{ index + 1 }}. {{ label }}</li></ol>
      <form @submit.prevent="next">
        <div v-if="step === 0" class="binding-form">
          <label for="binding-url">主站 URL（网址）</label><W12Input id="binding-url" v-model:value="draft.url" placeholder="https://主站域名" :disabled="busy || current?.phase === 'registering'" autocomplete="url" required />
          <p class="binding-help">先确认该地址能提供网关绑定服务。</p>
        </div>
        <div v-else-if="step === 1" class="binding-form">
          <p class="binding-help">{{ draft.url }}</p><label for="binding-token">主站对接密钥</label><W12Input id="binding-token" v-model:value="draft.token" type="password" placeholder="从主站系统设置点击复制" autocomplete="new-password" :disabled="busy" required />
          <p class="binding-help">密钥仅用于登记此节点，不会保存到浏览器。</p>
        </div>
        <div v-else class="binding-form">
          <label for="binding-name">节点名称</label><W12Input id="binding-name" v-model:value="draft.name" :maxlength="100" :disabled="busy || current?.phase === 'registering'" required />
          <label for="binding-public">节点公开 URL（网址）</label><W12Input id="binding-public" v-model:value="draft.public_url" :disabled="busy || current?.phase === 'registering'" autocomplete="url" required />
          <label for="binding-region">区域</label><W12Input id="binding-region" v-model:value="draft.region" placeholder="例如香港、东京或 default" :maxlength="64" :disabled="busy || current?.phase === 'registering'" required />
          <p class="binding-help">节点代码、同步密钥和缓存隔离空间由服务自动生成。</p>
        </div>
        <div class="binding-actions"><W12Button v-if="step > 0" :disabled="busy" @click="step--">上一步</W12Button><W12Button variant="primary" html-type="submit" :loading="busy">{{ step === 0 ? '下一步' : step === 1 ? '验证密钥' : '绑定主站' }}</W12Button></div>
      </form>
    </template>
    <a-alert v-if="error" :message="error" type="error" show-icon />
  </section>
</template>

<style scoped>
.binding-wizard { padding: 16px; border-radius: 12px; background: color-mix(in srgb, var(--surface) 88%, transparent); backdrop-filter: blur(24px); color: var(--text); min-width: 0; }
.binding-heading { display: flex; gap: 12px; align-items: center; }.binding-heading h2 { margin: 0; font-size: 22px; font-weight: 500; }
.binding-help { color: var(--text2); font-size: 13px; line-height: 1.7; overflow-wrap: anywhere; }.binding-steps { display: flex; list-style: none; gap: 8px; padding: 0; margin: 24px 0; }.binding-steps li { flex: 1; padding: 8px; border-bottom: 2px solid var(--hr); color: var(--text2); font-size: 13px; }.binding-steps .active { border-color: var(--accent); color: var(--text); }
.binding-form { display: grid; gap: 8px; }.binding-form label { color: var(--text2); margin-top: 8px; font-size: 13px; }.binding-actions { display: flex; justify-content: flex-end; gap: 8px; margin: 16px 0; }
@media (max-width: 600px) { .binding-wizard { border-radius: 0; min-height: 100%; }.binding-steps li { padding: 8px 2px; } }
</style>
