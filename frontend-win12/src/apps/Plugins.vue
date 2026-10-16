<script setup>
/* 插件系统管理页（官方 v0.2.5 对齐）
 * 契约来源：internal/handler/admin/plugin_handler.go + 官方 frontend/src/views/admin/PluginsView.vue
 * - GET    /admin/plugins                      → PluginInstallation[]
 * - POST   /admin/plugins/upload               (multipart: plugin=.s2plugin)
 * - POST   /admin/plugins/:id/enable           { accept_untested, rollout_percent }
 * - POST   /admin/plugins/:id/disable
 * - DELETE /admin/plugins/:id
 * - GET/PUT /admin/plugins/:id/config          (JSON 裸响应)
 * - POST   /admin/plugins/:id/test             → { success, message }
 * - GET    /admin/plugins/:id/status           → { healthy, message, status_json }（只读，不消耗 step-up）
 * - POST   /admin/plugins/:id/ui-session       → { url, bridge_token, ui_bridge_version, expires_at }
 * 配置弹窗走官方 Bridge postMessage 协议（sub2api-plugin-ui ↔ sub2api-plugin-host）。
 */
import { ref, computed, onMounted, onBeforeUnmount } from 'vue'
import { message, Modal } from 'ant-design-vue'
import { request, getToken } from '../api'
import {
  ReloadOutlined, UploadOutlined, SettingOutlined, PlayCircleOutlined,
  PauseCircleOutlined, DeleteOutlined, ExperimentOutlined,
} from '@ant-design/icons-vue'

const plugins = ref([])
const loading = ref(false)
const uploading = ref(false)
const busyID = ref(null)
const fileInput = ref(null)

/* 启用弹窗状态 */
const enableOpen = ref(false)
const enableTarget = ref(null)
const enableRollout = ref(100)
const acceptUntested = ref(false)

/* 配置弹窗（Bridge iframe）状态 */
const configPlugin = ref(null)
const configOpen = ref(false)
const uiSession = ref(null)
const uiLoading = ref(false)
const uiError = ref('')
const iframeHeight = ref(640)
const pluginFrameEl = ref(null)
let frameLoaded = false
const pendingBridge = new Map()

const columns = [
  { title: '插件', key: 'name', width: 200 },
  { title: '版本', dataIndex: 'version', key: 'version', width: 90 },
  { title: '状态', key: 'state', width: 90 },
  { title: '兼容性', key: 'compat', width: 200 },
  { title: '运行时', key: 'runtime', width: 90 },
  { title: '签名', key: 'signature', width: 110 },
  { title: '灰度', key: 'rollout', width: 80 },
  { title: '安装时间', key: 'installed', width: 160 },
  { title: '操作', key: 'actions', width: 250 },
]

/* 统一错误文案提取（与官方 errorMessage 等价） */
const errMsg = (e) => (e && e.message ? String(e.message) : '未知错误')

const loadPlugins = async () => {
  loading.value = true
  try {
    const data = await request('/admin/plugins', { method: 'GET' })
    plugins.value = Array.isArray(data) ? data : (data?.items ?? [])
  } catch (e) { message.error(`加载插件列表失败：${errMsg(e)}`) }
  finally { loading.value = false }
}

/* 上传走原生 fetch：multipart 不能带 JSON Content-Type */
const handleFileSelected = async (event) => {
  const file = event.target.files?.[0]
  event.target.value = ''
  if (!file || !file.name.toLowerCase().endsWith('.s2plugin')) {
    message.error('请选择 .s2plugin 插件包文件')
    return
  }
  uploading.value = true
  try {
    const form = new FormData()
    form.append('plugin', file)
    const res = await fetch('/api/v1/admin/plugins/upload', {
      method: 'POST',
      headers: { Authorization: `Bearer ${getToken()}` },
      body: form,
    })
    const payload = await res.json().catch(() => null)
    if (!res.ok || (payload && typeof payload.code !== 'undefined' && payload.code !== 0)) {
      throw new Error(payload?.message || `上传失败（HTTP ${res.status}）`)
    }
    message.success('插件安装成功')
    await loadPlugins()
  } catch (e) { message.error(errMsg(e)) }
  finally { uploading.value = false }
}

const hasEnabledBinding = (p) => (p.bindings || []).some((b) => b.enabled)
const currentRollout = (p) =>
  (p.bindings || []).find((b) => b.capability === 'openai.oauth.outbound_transport.v1')?.rollout_percent || 100

/* 状态/兼容性标签：低饱和半透明底色（Win12 规范） */
const stateText = { enabled: '已启用', starting: '启动中', disabled: '已停用', error: '错误', incompatible: '不兼容' }
const stateClass = (s) =>
  s === 'enabled' ? 'tag ok' : (s === 'error' || s === 'incompatible') ? 'tag bad' : s === 'starting' ? 'tag warn' : 'tag idle'
const compatText = (c) => ({ compatible: '兼容', untested: '未验证', incompatible: '不兼容' }[c?.status] || c?.status || '-')
const compatClass = (c) =>
  c?.status === 'compatible' ? 'tag ok' : c?.status === 'untested' ? 'tag warn' : 'tag bad'

const openEnable = (p) => {
  enableTarget.value = p
  enableRollout.value = currentRollout(p)
  acceptUntested.value = false
  enableOpen.value = true
}

const doEnable = async () => {
  const p = enableTarget.value
  if (!p) return
  busyID.value = p.id
  try {
    await request(`/admin/plugins/${p.id}/enable`, {
      method: 'POST',
      body: { accept_untested: acceptUntested.value, rollout_percent: enableRollout.value },
    })
    message.success('插件已启用')
    enableOpen.value = false
    await loadPlugins()
  } catch (e) { message.error(errMsg(e)) }
  finally { busyID.value = null }
}

const doDisable = async (p) => {
  busyID.value = p.id
  try {
    await request(`/admin/plugins/${p.id}/disable`, { method: 'POST' })
    message.success('插件已停用')
    await loadPlugins()
  } catch (e) { message.error(errMsg(e)) }
  finally { busyID.value = null }
}

const doUninstall = (p) => {
  // 卸载确认：ant Modal.confirm 危险操作弹窗（danger 红色确认键，风格与启用流程弹窗统一），
  // onOk 返回 Promise 时弹窗自动进入 loading，确认后才发删除请求（busyID 同步防抖表格按钮）
  Modal.confirm({
    title: '卸载插件',
    content: `确认卸载插件「${p.name}」？该操作会删除插件文件与配置。`,
    okText: '卸载',
    okType: 'danger',
    cancelText: '取消',
    onOk: () => {
      busyID.value = p.id
      return request(`/admin/plugins/${p.id}`, { method: 'DELETE' })
        .then(async () => { message.success('插件已卸载'); await loadPlugins() })
        .catch((e) => message.error(errMsg(e)))
        .finally(() => { busyID.value = null })
    },
  })
}

const doTest = async (p) => {
  busyID.value = p.id
  try {
    const result = await request(`/admin/plugins/${p.id}/test`, { method: 'POST' })
    if (result?.success) message.success(result.message || '配置测试通过')
    else message.error(result?.message || '配置测试失败')
  } catch (e) { message.error(errMsg(e)) }
  finally { busyID.value = null }
}

/* ---------- 配置弹窗：创建 UI 会话 + Bridge postMessage 协议 ---------- */
const openConfiguration = async (p) => {
  configPlugin.value = p
  configOpen.value = true
  uiSession.value = null
  uiError.value = ''
  uiLoading.value = true
  iframeHeight.value = 640
  frameLoaded = false
  try {
    uiSession.value = await request(`/admin/plugins/${p.id}/ui-session`, { method: 'POST' })
  } catch (e) {
    uiLoading.value = false
    uiError.value = errMsg(e)
  }
}

const closeConfiguration = () => {
  clearPendingBridge()
  frameLoaded = false
  configOpen.value = false
  configPlugin.value = null
  uiSession.value = null
  uiLoading.value = false
}

function clearPendingBridge() {
  for (const timeout of pendingBridge.values()) window.clearTimeout(timeout)
  pendingBridge.clear()
}

function onFrameLoad() {
  // iframe 二次加载可能是插件内部导航：丢弃未决请求，避免配置响应发到新文档
  if (frameLoaded) clearPendingBridge()
  frameLoaded = true
  uiLoading.value = false
}

function registerBridgeRequest(requestID) {
  pendingBridge.set(requestID, window.setTimeout(() => pendingBridge.delete(requestID), 30000))
}

function postBridgeResult(req, payload) {
  const frame = pluginFrameEl?.value
  if (!frame?.contentWindow || !uiSession.value) return
  const requestID = typeof req.request_id === 'string' ? req.request_id.trim() : ''
  if (!requestID || !pendingBridge.has(requestID)) return
  window.clearTimeout(pendingBridge.get(requestID))
  pendingBridge.delete(requestID)
  frame.contentWindow.postMessage(
    { source: 'sub2api-plugin-host', bridge_token: uiSession.value.bridge_token, type: `${req.type}.result`, request_id: requestID, ...payload },
    '*', // sandbox iframe 是不透明来源，无固定 targetOrigin（官方同策略）
  )
}

async function handleBridgeMessage(event) {
  if (!uiSession.value || !configPlugin.value) return
  if (event.source !== pluginFrameEl?.value?.contentWindow || event.origin !== 'null') return
  const msg = event.data
  if (!msg || msg.source !== 'sub2api-plugin-ui' || msg.bridge_token !== uiSession.value.bridge_token) return
  const requestID = typeof msg.request_id === 'string' ? msg.request_id.trim() : ''
  const expectsResponse = ['config.load', 'config.save', 'config.test', 'plugin.status'].includes(msg.type)
  if (expectsResponse) {
    if (!requestID || pendingBridge.has(requestID)) return
    registerBridgeRequest(requestID)
  }
  try {
    switch (msg.type) {
      case 'sub2api.plugin.ready':
        uiLoading.value = false
        break
      case 'config.load': {
        const config = await request(`/admin/plugins/${configPlugin.value.id}/config`, { method: 'GET' })
        postBridgeResult(msg, { ok: true, config })
        break
      }
      case 'config.save': {
        if (!msg.config || typeof msg.config !== 'object' || Array.isArray(msg.config)) throw new Error('插件配置格式无效')
        const config = await request(`/admin/plugins/${configPlugin.value.id}/config`, { method: 'PUT', body: msg.config })
        postBridgeResult(msg, { ok: true, config })
        message.success('配置已保存')
        break
      }
      case 'config.test': {
        const result = await request(`/admin/plugins/${configPlugin.value.id}/test`, { method: 'POST' })
        postBridgeResult(msg, { ok: !!result?.success, result })
        if (result?.success) message.success(result.message || '配置测试通过')
        else message.error(result?.message || '配置测试失败')
        break
      }
      case 'plugin.status': {
        // 官方 UI Bridge：被动读取运行状态，不保存配置、不探测上游。
        const status = await request(`/admin/plugins/${configPlugin.value.id}/status`, { method: 'GET' })
        postBridgeResult(msg, { ok: true, status })
        break
      }
      case 'ui.resize': {
        const height = Number(msg.height)
        if (Number.isFinite(height)) iframeHeight.value = Math.min(960, Math.max(520, Math.round(height)))
        break
      }
      case 'ui.notify': {
        const text = typeof msg.message === 'string' ? msg.message.slice(0, 500) : ''
        if (!text) break
        if (msg.level === 'error') message.error(text)
        else if (msg.level === 'success') message.success(text)
        else message.info(text)
        break
      }
    }
  } catch (e) {
    postBridgeResult(msg, { ok: false, error: errMsg(e) })
  }
}

const fmtTime = (v) => {
  if (!v) return '-'
  const d = new Date(v)
  return Number.isNaN(d.getTime()) ? '-' : d.toLocaleString('zh-CN', { hour12: false })
}

onMounted(() => {
  window.addEventListener('message', handleBridgeMessage)
  loadPlugins()
})
onBeforeUnmount(() => {
  window.removeEventListener('message', handleBridgeMessage)
  clearPendingBridge()
})
</script>

<template>
  <div class="page">
    <div class="page-h">
      <div>
        <div class="page-t">插件管理</div>
        <div class="page-s">安装 .s2plugin 插件包，管理插件生命周期、灰度与配置（当前作用于 OpenAI OAuth 出站通道）</div>
      </div>
      <div class="h-btns">
        <input ref="fileInput" type="file" accept=".s2plugin,application/zip" style="display: none" @change="handleFileSelected" />
        <a-button :loading="uploading" @click="fileInput?.click()"><UploadOutlined />安装插件</a-button>
        <a-button :loading="loading" @click="loadPlugins"><ReloadOutlined />刷新</a-button>
      </div>
    </div>

    <div class="notice">
      仅支持 .s2plugin 签名插件包；插件以独立进程运行并提供隔离 UI，配置保存在插件自己的加密存储中。
    </div>

    <a-table
      :columns="columns" :data-source="plugins" row-key="id" size="small"
      :scroll="{ x: 'max-content' }" :pagination="false" :loading="loading"
    >
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'name'">
          <div class="p-name">{{ record.name }}</div>
          <div class="p-key">{{ record.plugin_key }}</div>
        </template>
        <template v-else-if="column.key === 'state'">
          <span :class="stateClass(record.state)">{{ stateText[record.state] || record.state }}</span>
        </template>
        <template v-else-if="column.key === 'compat'">
          <span :class="compatClass(record.compatibility)">{{ compatText(record.compatibility) }}</span>
          <div v-if="record.compatibility?.message" class="p-sub">{{ record.compatibility.message }}</div>
        </template>
        <template v-else-if="column.key === 'runtime'">
          <span :class="record.runtime_healthy ? 'tag ok' : 'tag idle'">{{ record.runtime_healthy ? '健康' : '未运行' }}</span>
          <div v-if="record.last_error" class="p-sub err">{{ record.last_error }}</div>
          <div v-else-if="record.runtime_message" class="p-sub">{{ record.runtime_message }}</div>
        </template>
        <template v-else-if="column.key === 'signature'">
          <span class="tag idle">{{ { verified: '已验证', unsigned: '未签名', invalid: '签名无效' }[record.signature_status] || record.signature_status }}</span>
        </template>
        <template v-else-if="column.key === 'rollout'">{{ currentRollout(record) }}%</template>
        <template v-else-if="column.key === 'installed'">{{ fmtTime(record.installed_at) }}</template>
        <template v-else-if="column.key === 'actions'">
          <div class="acts">
            <a-button size="small" @click="openConfiguration(record)"><SettingOutlined />配置</a-button>
            <a-button size="small" :disabled="busyID === record.id" @click="doTest(record)"><ExperimentOutlined />测试</a-button>
            <a-button
              v-if="hasEnabledBinding(record)" size="small" :disabled="busyID === record.id"
              @click="doDisable(record)"
            ><PauseCircleOutlined />停用</a-button>
            <a-button
              v-else type="primary" size="small" :disabled="busyID === record.id || record.state === 'starting' || !record.compatibility?.compatible"
              @click="openEnable(record)"
            ><PlayCircleOutlined />启用</a-button>
            <a-button danger size="small" :disabled="busyID === record.id || hasEnabledBinding(record)" @click="doUninstall(record)"><DeleteOutlined />卸载</a-button>
          </div>
        </template>
      </template>
      <template #emptyText>
        <div class="empty">尚未安装任何插件。点击右上角「安装插件」上传 .s2plugin 插件包。</div>
      </template>
    </a-table>

    <!-- 启用弹窗：灰度比例 + 未验证版本确认 -->
    <a-modal v-model:open="enableOpen" title="启用插件" :width="420" @ok="doEnable" :confirm-loading="busyID === enableTarget?.id" ok-text="启用" cancel-text="取消">
      <div class="form-line">
        <span class="form-label">灰度比例</span>
        <a-slider v-model:value="enableRollout" :min="1" :max="100" style="flex: 1" />
        <span class="form-value">{{ enableRollout }}%</span>
      </div>
      <a-checkbox v-if="enableTarget && !enableTarget.compatibility?.tested" v-model:checked="acceptUntested">
        该插件版本未经官方验证，我确认接受风险
      </a-checkbox>
    </a-modal>

    <!-- 配置弹窗：隔离 iframe + Bridge 协议 -->
    <a-modal
      :open="configOpen" :title="`插件配置 - ${configPlugin?.name || ''}`" :width="860" :footer="null"
      :body-style="{ padding: '0' }" @cancel="closeConfiguration"
    >
      <div class="ui-wrap" :style="{ height: `${iframeHeight}px` }">
        <div v-if="uiLoading" class="ui-mask">插件 UI 加载中…</div>
        <div v-if="uiError" class="ui-mask err">
          <div class="ui-err-t">插件 UI 不可用</div>
          <div class="ui-err-m">{{ uiError }}</div>
        </div>
        <iframe
          v-if="uiSession" ref="pluginFrameEl" :src="uiSession.url" sandbox="allow-scripts"
          referrerpolicy="no-referrer" class="ui-frame" :title="`插件配置 - ${configPlugin?.name || ''}`" @load="onFrameLoad"
        />
      </div>
    </a-modal>
  </div>
</template>

<style scoped>
.page { padding: 16px; height: 100%; overflow-y: auto; }
.page-h { display: flex; align-items: flex-end; justify-content: space-between; gap: 12px; flex-wrap: wrap; margin-bottom: 12px; }
.page-t { font-size: 18px; font-weight: 600; }
.page-s { font-size: 12px; color: var(--text2); margin-top: 4px; }
.h-btns { display: flex; gap: 8px; }
.notice { font-size: 12px; color: var(--text2); background: var(--card); border: 1px solid var(--hr); border-radius: 8px; padding: 8px 12px; margin-bottom: 12px; }
.p-name { font-weight: 500; }
.p-key { font-size: 11px; color: var(--text2); font-family: Consolas, monospace; }
.p-sub { font-size: 11px; color: var(--text2); margin-top: 2px; max-width: 220px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.p-sub.err { color: #b91c1c; }
.tag { display: inline-flex; align-items: center; padding: 0 8px; height: 20px; border-radius: 4px; font-size: 11px; }
.tag.ok { background: rgba(34, 197, 94, 0.14); color: #15803d; }
.tag.bad { background: rgba(239, 68, 68, 0.12); color: #b91c1c; }
.tag.warn { background: rgba(245, 158, 11, 0.14); color: #b45309; }
.tag.idle { background: rgba(148, 163, 184, 0.18); color: var(--text2); }
.acts { display: flex; gap: 6px; flex-wrap: wrap; }
.empty { padding: 32px 0; font-size: 12px; color: var(--text2); }
.form-line { display: flex; align-items: center; gap: 12px; margin-bottom: 16px; }
.form-label { font-size: 12px; color: var(--text2); }
.form-value { font-size: 12px; min-width: 42px; text-align: right; }
.ui-wrap { position: relative; overflow: hidden; }
.ui-mask { position: absolute; inset: 0; z-index: 10; display: flex; flex-direction: column; align-items: center; justify-content: center; font-size: 12px; color: var(--text2); background: var(--card); gap: 8px; }
.ui-err-t { font-size: 14px; font-weight: 500; color: #b45309; }
.ui-err-m { max-width: 480px; text-align: center; }
.ui-frame { width: 100%; height: 100%; border: 0; }
</style>
