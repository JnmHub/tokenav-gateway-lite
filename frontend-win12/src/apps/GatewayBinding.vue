<script setup>
import { onMounted, ref } from 'vue'
import { DisconnectOutlined, LinkOutlined, ReloadOutlined } from '@ant-design/icons-vue'
import { Modal } from 'ant-design-vue'
import W12Button from '../components/design/W12Button.vue'
import GatewayBindingWizard from '../components/binding/GatewayBindingWizard.vue'
import { bindingApi, bindingErrorText, bindingPhaseText } from '../api/gatewayBinding.js'
import { useRequestLifetime } from '../composables/useRequestLifetime.js'
const status = ref(null), busy = ref(false), error = ref(''), drain = ref(null)
const lifetime = useRequestLifetime(() => { status.value = null; drain.value = null })
const options = () => ({ signal: lifetime.signal, sessionEpoch: lifetime.sessionEpoch })
async function load() { try { status.value = await bindingApi.status(options()) } catch (e) { if (lifetime.isAlive()) error.value = bindingErrorText(e) } }
async function act(kind) {
  busy.value = true; error.value = ''
  try {
    if (kind === 'cancel') { status.value = await bindingApi.cancel(options()); drain.value = null }
    else if (kind === 'check') drain.value = await bindingApi.check(options())
    else { status.value = await bindingApi.disconnect(options()); drain.value = null; window.dispatchEvent(new Event('w12-binding-changed')) }
  } catch (e) { if (lifetime.isAlive()) { error.value = bindingErrorText(e); drain.value = e.details?.counts ? e.details : drain.value } }
  finally { if (lifetime.isAlive()) { busy.value = false; await load() } }
}
function disconnect() {
  Modal.confirm({ title: '断开当前主站？', content: '将停止新增付费请求，并把在途请求和待回传用量结算到原主站。只有全部检查通过才会解绑；未完成时可重试或取消。账号、上游配置和请求历史会保留。', okText: '确认断开', cancelText: '暂不操作', onOk: () => act('disconnect') })
}
const countNames = { inflight: '在途请求', sql_retry: '数据库用量补偿', oauth_outbox: '委托恢复任务', penalty_outbox: '待回传处罚事件', image_tasks: '未结算图片任务', grok_video: '未结算视频任务', batch_image: '未结算批量图片', redis_stream: '待回传用量', redis_dead: '用量死信', redis_pending: '待确认用量', reserved_requests: '请求资金预留', reserved_leases: '额度租约预留', pending_funding: '未结算资金', seedance_registry: '待结算 Seedance 视频', control_plane_funding: '主站未结算资金', control_plane_oauth_reservations: '主站未结算委托' }
onMounted(load)
</script>
<template>
  <main class="binding-page w12-design">
    <div class="binding-toolbar"><h2><LinkOutlined /> 主站绑定</h2><W12Button :disabled="busy" @click="load"><template #icon><ReloadOutlined /></template>刷新状态</W12Button></div>
    <a-alert v-if="error" :message="error" type="error" show-icon />
    <a-spin v-if="!status" tip="读取绑定状态" />
    <template v-else-if="status.first_binding_required"><GatewayBindingWizard :status="status" @status="status = $event" @bound="status = $event" /></template>
    <template v-else>
      <section class="binding-card">
        <h3>{{ bindingPhaseText(status.phase) }}</h3>
        <dl><dt>主站</dt><dd>{{ status.url }}</dd><dt>节点名称</dt><dd>{{ status.name || status.gateway_code }}</dd><dt>公开地址</dt><dd>{{ status.public_url || '由原节点配置提供' }}</dd><dt>区域</dt><dd>{{ status.region }}</dd><dt>最近确认</dt><dd>{{ status.snapshot_at ? new Date(status.snapshot_at).toLocaleString() : '尚未完成主站握手与配置同步' }}</dd></dl>
        <p v-if="status.disconnect_pending">断开已进入远端确认阶段；请点击重试断开完成状态核验，此阶段不能直接恢复原连接。</p>
        <p v-if="status.last_error">{{ status.last_error }}</p>
        <div class="binding-actions"><W12Button :loading="busy" @click="act('check')">检查断开条件</W12Button><W12Button variant="danger" :disabled="busy" @click="disconnect"><template #icon><DisconnectOutlined /></template>{{ ['draining', 'blocked'].includes(status.phase) ? '重试断开' : '断开并绑定其他主站' }}</W12Button><W12Button v-if="status.can_cancel" :disabled="busy" @click="act('cancel')">取消断开</W12Button></div>
      </section>
      <section v-if="drain" class="binding-card"><h3>{{ drain.ready ? '当前结算检查通过' : '尚未满足断开条件' }}</h3><p>原主站连接：{{ drain.reachable ? '已确认' : '未确认' }}</p><dl><template v-for="(count, key) in drain.counts" :key="key"><dt>{{ countNames[key] || key }}</dt><dd>{{ count }}</dd></template></dl><a-alert v-for="reason in drain.unknown || []" :key="reason" :message="reason" type="warning" show-icon /></section>
    </template>
  </main>
</template>
<style scoped>
.binding-page { display:flex;flex-direction:column;gap:16px;height:100%;overflow:auto;padding:16px; }.binding-toolbar { display:flex;align-items:center;justify-content:space-between;gap:16px; }.binding-toolbar h2 {font-size:20px;font-weight:500;margin:0;}.binding-card {padding:16px;border-radius:8px;background:color-mix(in srgb,var(--surface) 60%,transparent);backdrop-filter:blur(16px);}.binding-card h3 {margin:0 0 16px;font-size:16px;font-weight:500;}.binding-card p {color:var(--text2);font-size:13px;line-height:1.7;}dl {display:grid;grid-template-columns:120px minmax(0,1fr);gap:8px 12px;font-size:13px;}dt {color:var(--text2);text-align:right;}dd {margin:0;overflow-wrap:anywhere;}.binding-actions {display:flex;gap:8px;justify-content:flex-end;flex-wrap:wrap;margin-top:16px;}
@media(max-width:600px){.binding-toolbar{align-items:flex-start;}.binding-toolbar h2{font-size:18px;}dl{grid-template-columns:90px minmax(0,1fr);}.binding-actions{justify-content:stretch;}.binding-actions>*{flex:1;}}
</style>
