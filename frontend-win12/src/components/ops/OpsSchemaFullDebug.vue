<script setup>
/* 完整调试 Tab（三期 Schema 分析器）：本地密钥完整内容捕获（内存存储、自动过期）
 * 契约来源：旧 RequestSchemaAnalyzerView.vue 完整调试区块 + ops.ts + 后端 ops_request_debug_handler.go（已核实真实响应）
 * - GET  /admin/ops/runtime/request-debug/full-config
 *        → { enabled, key_id, key_ref, max_records, max_body_bytes, max_total_bytes,
 *            expires_at, ttl_seconds, count, stored_bytes }（无 preset 字段，按 ttl_seconds 回推导出）
 * - PUT  /admin/ops/runtime/request-debug/full-config { preset, api_key_id, max_records, max_body_bytes }
 * - POST /admin/ops/runtime/request-debug/full-config/disable（立即停止）
 * - GET  /admin/ops/runtime/request-debug/full-records → { items|records }
 * - DELETE /admin/ops/runtime/request-debug/full-records（清空）
 * - 本地密钥下拉：GET /admin/gateway-lite/local-api-keys（容器传入）
 * 变更性操作（开启/停止/清空）先弹确认（铁律：不真开启长时采集），确认后才调用真实接口 */
import { ref, computed, onMounted } from 'vue'
import dayjs from 'dayjs'
import { message, Modal } from 'ant-design-vue'
import { ReloadOutlined, DownloadOutlined, DeleteOutlined, StopOutlined } from '@ant-design/icons-vue'
import { request } from '../../api'

const props = defineProps({ keys: { type: Array, default: () => [] } })

const loading = ref(false)
const saving = ref(false)
const config = ref(null)
const records = ref([])
const expandedId = ref(null)

/* ---------- 表单草稿 ---------- */
const selectedKey = ref('')
const preset = ref('15m')
const maxRecords = ref(50)
const maxBodyBytes = ref(1024 * 1024)
const PRESET_OPTS = [
  { value: '5m', label: '5 分钟' },
  { value: '15m', label: '15 分钟' },
  { value: '30m', label: '30 分钟' },
]

const loadConfig = async () => {
  try { config.value = await request('/admin/ops/runtime/request-debug/full-config', { method: 'GET' }) }
  catch (e) { config.value = null; message.error(`加载完整调试配置失败：${e.message}`); return }
  const c = config.value
  selectedKey.value = c?.key_id ? String(c.key_id) : ''
  const ttl = Number(c?.ttl_seconds) || 0
  preset.value = ttl <= 0 ? '15m' : ttl <= 300 ? '5m' : ttl <= 900 ? '15m' : '30m'
  maxRecords.value = c?.max_records || 50
  maxBodyBytes.value = c?.max_body_bytes || 1024 * 1024
}
const loadRecords = async () => {
  loading.value = true
  try {
    const r = await request('/admin/ops/runtime/request-debug/full-records', { method: 'GET' })
    records.value = r?.items || r?.records || []
  } catch (e) { records.value = [] }
  finally { loading.value = false }
}
onMounted(() => { loadConfig(); loadRecords() })

/* ---------- 变更性操作（确认层 → 真实接口） ---------- */
const enable = () => {
  if (!selectedKey.value) { message.warning('请先选择本地密钥'); return }
  Modal.confirm({
    title: '开启完整调试',
    content: `将捕获本地密钥「${keyLabel(selectedKey.value)}」的完整请求/响应正文（内存存储，${preset.value.replace('m', ' 分钟')} 后自动关闭，敏感头自动脱敏）。确定开启？`,
    okText: '开启', cancelText: '取消',
    onOk: async () => {
      saving.value = true
      try {
        config.value = await request('/admin/ops/runtime/request-debug/full-config', {
          method: 'PUT',
          body: {
            enabled: true,
            api_key_id: Number(selectedKey.value),
            preset: preset.value,
            max_records: Number(maxRecords.value) || 50,
            max_body_bytes: Number(maxBodyBytes.value) || 1024 * 1024,
          },
        })
        await loadRecords()
        message.success('完整调试已开启')
      } catch (e) { message.error(`开启失败：${e.message}`) }
      finally { saving.value = false }
    },
  })
}
const stop = () => {
  Modal.confirm({
    title: '停止完整调试',
    content: '将立即停止该本地密钥的完整内容捕获。确定停止？',
    okText: '停止', okType: 'danger', cancelText: '取消',
    onOk: async () => {
      saving.value = true
      try {
        config.value = await request('/admin/ops/runtime/request-debug/full-config/disable', { method: 'POST' })
        records.value = []
        message.success('完整调试已停止')
      } catch (e) { message.error(`停止失败：${e.message}`) }
      finally { saving.value = false }
    },
  })
}
const clearRecords = () => {
  if (!records.value.length) return
  Modal.confirm({
    title: '清空完整调试记录',
    content: `将删除当前 ${records.value.length} 条完整内容记录。确定清空？`,
    okText: '清空', okType: 'danger', cancelText: '取消',
    onOk: async () => {
      try {
        await request('/admin/ops/runtime/request-debug/full-records', { method: 'DELETE' })
        records.value = []
        message.success('已清空完整调试记录')
      } catch (e) { message.error(`清空失败：${e.message}`) }
    },
  })
}

const keyLabel = (id) => {
  const k = props.keys.find((x) => String(x.id) === String(id))
  return k ? `${k.name} · ${k.key_masked}` : `#${id}`
}
const enabled = computed(() => !!config.value?.enabled)

/* ---------- 记录展示 ---------- */
const fmtTime = (t) => (t ? dayjs(t).format('MM-DD HH:mm:ss') : '-')
const isSensitive = (name) => /authorization|cookie|api[-_]?key|token|secret/i.test(name)
const headerEntries = (headers) => {
  if (Array.isArray(headers)) return headers.map((h) => [h, '[已脱敏]'])
  return Object.entries(headers || {}).map(([k, v]) => [k, isSensitive(k) ? '[已脱敏]' : Array.isArray(v) ? v.join(', ') : String(v)])
}
const bodyData = (body) => (body != null && typeof body === 'object' && 'data' in body ? body.data : body)
const prettyBody = (body) => {
  const data = bodyData(body)
  if (data == null || data === '') return ''
  if (typeof data === 'string') { try { return JSON.stringify(JSON.parse(data), null, 2) } catch { return data } }
  try { return JSON.stringify(data, null, 2) } catch { return String(data) }
}
const bodyTruncated = (body, top) => top || (typeof body === 'object' && body !== null && 'truncated' in body && !!body.truncated)
const bodyEncoding = (body) => (typeof body === 'object' && body !== null && 'encoding' in body ? String(body.encoding || '') : '')

const downloadJSON = () => {
  const blob = new Blob([JSON.stringify(records.value, null, 2)], { type: 'application/json;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `local-key-debug-${new Date().toISOString().replace(/[:.]/g, '-')}.json`
  a.click()
  URL.revokeObjectURL(url)
}
</script>

<template>
  <div class="fd">
    <div class="fd-head">
      <div>
        <div class="fd-t">本地密钥完整调试</div>
        <div class="sub">仅捕获选中的本地密钥；正文只保存在网关内存中，自动过期后清除。敏感请求头（Authorization、Cookie、API Key）始终脱敏。</div>
      </div>
      <a-tag :color="enabled ? 'orange' : 'default'" style="margin: 0">{{ enabled ? '完整调试已开启' : '未开启' }}</a-tag>
    </div>

    <!-- 控制区：密钥 / 自动关闭 / 记录上限 / 正文上限 -->
    <div class="ctl">
      <div class="c-item">
        <label>本地 API Key（接口密钥）</label>
        <a-select v-model:value="selectedKey" size="small" style="width: 100%" placeholder="请选择本地密钥" :options="keys.map((k) => ({ value: String(k.id), label: `${k.name} · ${k.key_masked}` }))" />
      </div>
      <div class="c-item">
        <label>自动关闭</label>
        <a-select v-model:value="preset" :options="PRESET_OPTS" size="small" style="width: 100%" />
      </div>
      <div class="c-item">
        <label>最多记录数</label>
        <a-input-number v-model:value="maxRecords" :min="1" :max="500" size="small" style="width: 100%" />
      </div>
      <div class="c-item">
        <label>单条正文上限（字节）</label>
        <a-input-number v-model:value="maxBodyBytes" :min="4096" :max="5242880" :step="4096" size="small" style="width: 100%" />
        <span class="hint">超出会标记为截断</span>
      </div>
    </div>
    <div class="ops">
      <a-button size="small" type="primary" :loading="saving" :disabled="!selectedKey" @click="enable">开启并清空旧记录</a-button>
      <a-button size="small" :loading="saving" :disabled="!enabled" @click="stop"><template #icon><StopOutlined /></template>立即停止</a-button>
      <a-button size="small" :loading="loading" @click="loadRecords"><template #icon><ReloadOutlined /></template>刷新</a-button>
      <a-button size="small" :disabled="!records.length" @click="downloadJSON"><template #icon><DownloadOutlined /></template>下载 JSON</a-button>
      <a-button size="small" danger :disabled="!records.length" @click="clearRecords"><template #icon><DeleteOutlined /></template>清空记录</a-button>
      <span v-if="config?.expires_at" class="exp">预计 {{ fmtTime(config.expires_at) }} 自动关闭</span>
    </div>

    <!-- 记录列表 -->
    <div v-if="loading" class="empty">加载中…</div>
    <div v-else-if="!records.length" class="empty">暂无完整内容记录{{ enabled ? '，向该本地密钥发送一条请求后即可查看' : '。开启后向该本地密钥发送请求即可查看' }}</div>
    <div v-else class="list">
      <article v-for="r in records" :key="r.id" class="rec">
        <button class="rec-h" @click="expandedId = expandedId === r.id ? null : r.id">
          <span class="method">{{ r.method }}</span>
          <span class="path mono">{{ r.path }}</span>
          <span class="meta">{{ r.response_status }} · {{ r.duration_ms || 0 }}ms · {{ fmtTime(r.created_at) }}</span>
        </button>
        <div v-if="expandedId === r.id" class="rec-b two">
          <div class="subcard">
            <div class="sc-h">请求 <span v-if="bodyEncoding(r.request_body)" class="dim">{{ bodyEncoding(r.request_body) }}</span></div>
            <div class="hds">
              <div v-for="[k, v] in headerEntries(r.request_headers)" :key="k" class="hd"><code>{{ k }}</code><span class="dim">{{ v }}</span></div>
            </div>
            <pre class="body">{{ prettyBody(r.request_body) || '（无正文）' }}</pre>
            <p v-if="bodyTruncated(r.request_body, r.request_body_truncated)" class="trunc">正文已按单条上限截断</p>
          </div>
          <div class="subcard">
            <div class="sc-h">响应 <span v-if="bodyEncoding(r.response_body)" class="dim">{{ bodyEncoding(r.response_body) }}</span></div>
            <div class="hds">
              <div v-for="[k, v] in headerEntries(r.response_headers)" :key="k" class="hd"><code>{{ k }}</code><span class="dim">{{ v }}</span></div>
            </div>
            <pre class="body">{{ prettyBody(r.response_body) || '（无正文）' }}</pre>
            <p v-if="bodyTruncated(r.response_body, r.response_body_truncated)" class="trunc">正文已按单条上限截断</p>
          </div>
        </div>
      </article>
    </div>
  </div>
</template>

<style scoped>
.fd { padding: 12px 14px; }
.fd-head { display: flex; align-items: flex-start; gap: 10px; }
.fd-t { font-size: 13px; font-weight: 500; }
.sub { font-size: 11px; color: var(--text2); margin-top: 2px; }
.ctl { display: grid; grid-template-columns: repeat(auto-fit, minmax(170px, 1fr)); gap: 10px; margin-top: 12px; }
.c-item { display: flex; flex-direction: column; gap: 4px; }
.c-item label { font-size: 11px; color: var(--text2); }
.hint { font-size: 10px; color: var(--text2); }
.ops { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin: 10px 0 4px; }
.exp { font-size: 11px; color: var(--text2); }
.empty { padding: 26px 0; text-align: center; font-size: 12px; color: var(--text2); }
.list { margin-top: 8px; }
.rec { border: 1px solid var(--hr); border-radius: 8px; margin-bottom: 8px; overflow: hidden; }
.rec-h { display: flex; align-items: center; gap: 8px; width: 100%; border: 0; background: transparent; padding: 8px 12px; cursor: pointer; text-align: left; }
.rec-h:hover { background: rgba(127, 127, 127, 0.06); }
.method { flex: none; font-family: Consolas, monospace; font-size: 10px; background: rgba(59, 130, 246, 0.12); color: #3b82f6; border-radius: 4px; padding: 1px 6px; }
.path { min-width: 0; flex: 1; font-size: 12px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.meta { flex: none; font-size: 11px; color: var(--text2); }
.rec-b { padding: 10px 12px; border-top: 1px solid var(--hr); }
.two { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
.subcard { border: 1px solid var(--hr); border-radius: 8px; background: rgba(127, 127, 127, 0.04); padding: 10px 12px; min-width: 0; }
.sc-h { display: flex; align-items: center; justify-content: space-between; font-size: 11px; font-weight: 500; color: var(--text2); margin-bottom: 6px; }
.dim { font-size: 10px; color: var(--text2); font-weight: 400; }
.hds { margin-bottom: 8px; }
.hd { display: flex; gap: 8px; font-size: 11px; padding: 1px 0; }
.hd code { color: var(--text2); }
.hd .dim { overflow-wrap: anywhere; }
.body { max-height: 300px; overflow: auto; border-radius: 6px; background: rgba(127, 127, 127, 0.08); padding: 8px 10px; font-size: 11px; line-height: 1.5; font-family: Consolas, monospace; margin: 0; white-space: pre-wrap; overflow-wrap: anywhere; }
.trunc { font-size: 10px; color: #d97706; margin: 6px 0 0; }
@media (max-width: 900px) { .two { grid-template-columns: 1fr; } }
</style>
