<script setup>
/* IPv6 出口自动管理卡（对照旧 views/admin/proxies/IPv6EgressCard.vue）
 * 能力：本机网卡全局 IPv6 自动识别 → 源绑定测通 → 注册为本地 CONNECT 代理；
 * 保存即热生效（后端持久化 data/ipv6_egress.json，无需重启）。
 * 接口：GET/PUT /admin/ipv6-egress、POST /admin/ipv6-egress/rescan
 * 测通地址自动注册为代理（name_prefix-*），可在账号代理池中选用。 */
import { onMounted, reactive, ref } from 'vue'
import { message } from 'ant-design-vue'
import { ReloadOutlined, ThunderboltOutlined, SaveOutlined } from '@ant-design/icons-vue'
import { request } from '../../api'

const loading = ref(false)
const saving = ref(false)
const rescanning = ref(false)
const status = ref(null)

const draft = reactive({
  enabled: false,
  listen_host: '127.0.0.1',
  listen_port: 32600,
  probe_target: 'chatgpt.com:443',
  sync_interval_seconds: 300,
  max_addresses: 512,
})

const load = async () => {
  loading.value = true
  try {
    status.value = await request('/admin/ipv6-egress', { method: 'GET' })
    const cfg = status.value?.config || {}
    Object.assign(draft, {
      enabled: !!cfg.enabled,
      listen_host: cfg.listen_host || '127.0.0.1',
      listen_port: Number(cfg.listen_port) || 32600,
      probe_target: cfg.probe_target || 'chatgpt.com:443',
      sync_interval_seconds: Number(cfg.sync_interval_seconds) || 300,
      max_addresses: Number(cfg.max_addresses) || 512,
    })
  } catch (e) { message.error(`读取失败：${e.message}`) } finally { loading.value = false }
}

const save = async () => {
  saving.value = true
  try {
    status.value = await request('/admin/ipv6-egress', { method: 'PUT', body: { ...draft } })
    message.success('已保存并热应用')
  } catch (e) { message.error(`保存失败：${e.message}`) } finally { saving.value = false }
}

const rescan = async () => {
  rescanning.value = true
  try {
    status.value = await request('/admin/ipv6-egress/rescan', { method: 'POST' })
    message.success(`扫描完成：测通 ${status.value?.ok_count ?? 0} 个`)
  } catch (e) { message.error(`扫描失败：${e.message}`) } finally { rescanning.value = false }
}

onMounted(load)
</script>

<template>
  <div class="ipv6-card">
    <div class="ipv6-head">
      <div class="ipv6-head-main">
        <div class="ipv6-title">IPv6 出口自动管理
          <a-tag v-if="status" :color="status.running ? 'green' : 'default'" style="margin-left: 8px">
            {{ status.running ? '运行中' : '未运行' }}
          </a-tag>
        </div>
        <div class="ipv6-hint">自动识别本机全局 IPv6 → 源绑定测通 → 注册为本地代理；保存即热生效，无需重启服务。</div>
      </div>
      <div class="ipv6-actions">
        <a-button size="small" :loading="loading" @click="load"><ReloadOutlined /></a-button>
        <a-button size="small" :disabled="!draft.enabled || rescanning" :loading="rescanning" @click="rescan">
          <ThunderboltOutlined /> 立即重扫
        </a-button>
        <a-button size="small" type="primary" :loading="saving" @click="save"><SaveOutlined /> 保存并应用</a-button>
      </div>
    </div>

    <!-- 启用行：开关自然宽度 + 说明并排（Win11 列表行：文字左、控件右） -->
    <div class="ipv6-enable">
      <a-switch v-model:checked="draft.enabled" />
      <div class="ipv6-enable-text">
        <span class="et">启用自动识别</span>
        <span class="ipv6-hint">{{ draft.enabled ? '已启用：保存后自动开始识别与测通。' : '未启用：开启并保存后自动开始识别与测通。' }}</span>
      </div>
    </div>

    <div class="ipv6-form">
      <div class="fitem"><span class="flabel">监听地址</span><a-input v-model:value="draft.listen_host" placeholder="127.0.0.1" /></div>
      <div class="fitem"><span class="flabel">监听端口</span><a-input-number v-model:value="draft.listen_port" :min="1" :max="65535" style="width: 100%" /></div>
      <div class="fitem"><span class="flabel">测通目标</span><a-input v-model:value="draft.probe_target" placeholder="chatgpt.com:443" /></div>
      <div class="fitem"><span class="flabel">重扫间隔（秒）</span><a-input-number v-model:value="draft.sync_interval_seconds" :min="30" style="width: 100%" /></div>
      <div class="fitem"><span class="flabel">地址数上限</span><a-input-number v-model:value="draft.max_addresses" :min="1" :max="4096" style="width: 100%" /></div>
    </div>

    <div v-if="status && status.entries?.length" class="ipv6-list">
      <div class="ipv6-meta">
        共 {{ status.entries.length }} 个全局 IPv6 · 测通 {{ status.ok_count }} 个
        <template v-if="status.last_scan">· 上次扫描 {{ status.last_scan }}</template>
      </div>
      <div class="ipv6-rows">
        <div v-for="entry in status.entries" :key="entry.address" class="ipv6-row">
          <span class="dot" :class="entry.status === 'ok' ? 'ok' : 'bad'" />
          <code class="addr" :title="entry.address">{{ entry.address }}</code>
          <span v-if="entry.status === 'ok'" class="latency">{{ entry.latency_ms }}ms</span>
          <span v-else class="errmsg" :title="entry.error">{{ entry.error || '测通失败' }}</span>
          <span v-if="entry.proxy_id" class="pid">#{{ entry.proxy_id }}</span>
        </div>
      </div>
      <div class="ipv6-hint" style="margin-top: 6px">
        测通地址已自动注册为代理（名称 {{ status.config?.name_prefix || 'ipv6' }}-*），可在账号代理池中选用。
      </div>
    </div>
    <p v-else-if="status && status.config?.enabled" class="ipv6-hint" style="margin-top: 8px">
      尚无扫描结果，点击「立即重扫」或等待自动扫描。
    </p>
  </div>
</template>

<style scoped>
/* 卡片：半透毛玻璃底 + 8px 圆角 + 1px 浅描边（与全局卡片语言一致） */
.ipv6-card { background: var(--card); border: 1px solid var(--hr); border-radius: 8px; padding: 14px 16px; margin-bottom: 12px; }
.ipv6-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; flex-wrap: wrap; }
.ipv6-title { font-size: 13px; font-weight: 500; display: flex; align-items: center; }
.ipv6-hint { font-size: 12px; color: var(--text2); line-height: 1.6; }
.ipv6-head .ipv6-hint { margin-top: 2px; }
.ipv6-actions { display: flex; gap: 8px; flex-shrink: 0; }

/* 启用行：开关恢复自然宽度（flex column stretch 会把开关拉满格宽），说明文字右随 */
.ipv6-enable { display: flex; align-items: center; gap: 10px; margin-top: 12px; padding: 8px 12px; border: 1px solid var(--hr); border-radius: 8px; background: var(--surface); }
.ipv6-enable .ant-switch { flex: none; }
.ipv6-enable-text { display: flex; flex-direction: column; min-width: 0; }
.ipv6-enable-text .et { font-size: 13px; font-weight: 500; }

/* 参数网格：每列限宽 150–220px，避免宽屏下输入框被拉到 300px+ */
.ipv6-form { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 220px)); gap: 10px 14px; margin-top: 12px; }
.fitem { display: flex; flex-direction: column; gap: 4px; }
.flabel { font-size: 12px; color: var(--text2); }

.ipv6-meta { font-size: 12px; color: var(--text2); margin-bottom: 6px; }
.ipv6-list { margin-top: 12px; }
.ipv6-rows { max-height: 160px; overflow-y: auto; border: 1px solid var(--hr); border-radius: 8px; background: var(--surface); }
.ipv6-row { display: flex; align-items: center; gap: 10px; padding: 4px 10px; font-size: 12px; border-bottom: 1px solid var(--hr); }
.ipv6-row:last-child { border-bottom: none; }
.dot { width: 8px; height: 8px; border-radius: 50%; flex: none; }
.dot.ok { background: #2e7d32; }
.dot.bad { background: #c42b1c; }
.addr { min-width: 0; flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-family: ui-monospace, monospace; }
.latency { color: #2e7d32; flex: none; }
.errmsg { color: #c42b1c; max-width: 40%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: none; }
.pid { color: var(--text2); flex: none; }

@media (max-width: 640px) {
  .ipv6-enable { flex-wrap: wrap; }
  .ipv6-form { grid-template-columns: 1fr; }
}
</style>
