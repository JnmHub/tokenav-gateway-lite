<script setup>
/* 系统日志：10 项筛选 + 按当前筛选清理 + 摄入健康 + 运行时日志配置
 * 契约来源：internal/handler/admin/ops_system_log_handler.go（已核对）：
 * - GET /admin/ops/system-logs：page/page_size/time_range(5m~30d)/start_time/end_time/
 *   level/component/request_id/client_request_id/user_id/account_id/platform/model/q
 *   （后端 OpsSystemLogFilter 不支持 api_key_id，旧版传参为假筛选，已移除）
 * - POST /admin/ops/system-logs/cleanup：同名筛选参数（不含 page/time_range），返回 { deleted }
 * - GET /admin/ops/system-logs/health：{ queue_depth, queue_capacity, dropped_count,
 *   write_failed_count, written_count, avg_write_delay_ms, last_error }
 * - 运行时配置 GET/PUT /admin/ops/runtime/logging、POST .../reset（OpsRuntimeLogConfig）
 * - 起止时间以 datetime-local 输入，按本地时区转 RFC3339 后上送 */
import { ref, reactive, onMounted } from 'vue'
import { message, Modal } from 'ant-design-vue'
import { ReloadOutlined } from '@ant-design/icons-vue'
import { request } from '../../api'

const loading = ref(false)
const logs = ref([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)

const TR_OPTS = ['5m', '30m', '1h', '6h', '24h', '7d', '30d'].map((v) => ({ value: v, label: v }))
const LEVEL_OPTS = [
  { value: '', label: '全部级别' },
  { value: 'debug', label: 'debug' },
  { value: 'info', label: 'info' },
  { value: 'warn', label: 'warn' },
  { value: 'error', label: 'error' },
]

/* 10 项筛选（参数名与后端 OpsSystemLogFilter 完全一致；无 api_key_id） */
const filters = reactive({
  time_range: '1h',
  start_time: '', end_time: '',
  level: '', component: '',
  request_id: '', client_request_id: '',
  user_id: '', account_id: '',
  platform: '', model: '', q: '',
})

const toRFC3339 = (v) => {
  // datetime-local 值按本地时区解释（与旧 parseDefaultTimeZoneDateTimeInput 等价）
  if (!v) return undefined
  const d = new Date(v)
  return Number.isNaN(d.getTime()) ? undefined : d.toISOString()
}
const posInt = (s) => {
  const n = Number.parseInt(String(s).trim(), 10)
  return Number.isFinite(n) && n > 0 ? n : undefined
}

const buildQuery = () => {
  const query = { page: page.value, page_size: pageSize.value, time_range: filters.time_range }
  if (filters.start_time) query.start_time = toRFC3339(filters.start_time)
  if (filters.end_time) query.end_time = toRFC3339(filters.end_time)
  if (filters.level.trim()) query.level = filters.level.trim()
  if (filters.component.trim()) query.component = filters.component.trim()
  if (filters.request_id.trim()) query.request_id = filters.request_id.trim()
  if (filters.client_request_id.trim()) query.client_request_id = filters.client_request_id.trim()
  const uid = posInt(filters.user_id); if (uid) query.user_id = uid
  const aid = posInt(filters.account_id); if (aid) query.account_id = aid
  if (filters.platform.trim()) query.platform = filters.platform.trim()
  if (filters.model.trim()) query.model = filters.model.trim()
  if (filters.q.trim()) query.q = filters.q.trim()
  return query
}

const fetchLogs = async () => {
  loading.value = true
  try {
    const data = await request('/admin/ops/system-logs', { method: 'GET', query: buildQuery() })
    logs.value = data.items || []
    total.value = data.total || 0
  } catch { logs.value = []; total.value = 0 } finally { loading.value = false }
}
const applyFilters = () => { page.value = 1; fetchLogs() }
const resetFilters = () => {
  Object.assign(filters, {
    time_range: '1h', start_time: '', end_time: '', level: '', component: '',
    request_id: '', client_request_id: '', user_id: '', account_id: '',
    platform: '', model: '', q: '',
  })
  page.value = 1; fetchLogs()
}

/* 摄入健康 */
const health = ref(null)
const fetchHealth = async () => {
  try { health.value = await request('/admin/ops/system-logs/health', { method: 'GET' }) } catch { health.value = null }
}

/* 按当前筛选清理（危险操作，二次确认；参数与旧 cleanupCurrentFilter 一致） */
const cleanupByFilter = () => {
  Modal.confirm({
    title: '按当前筛选清理系统日志',
    content: '将删除命中当前筛选条件的全部系统日志，此操作不可撤销。确定继续？',
    okText: '确认清理', okType: 'danger', cancelText: '取消',
    onOk: async () => {
      try {
        const payload = {}
        if (filters.start_time) payload.start_time = toRFC3339(filters.start_time)
        if (filters.end_time) payload.end_time = toRFC3339(filters.end_time)
        if (filters.level.trim()) payload.level = filters.level.trim()
        if (filters.component.trim()) payload.component = filters.component.trim()
        if (filters.request_id.trim()) payload.request_id = filters.request_id.trim()
        if (filters.client_request_id.trim()) payload.client_request_id = filters.client_request_id.trim()
        const uid = posInt(filters.user_id); if (uid) payload.user_id = uid
        const aid = posInt(filters.account_id); if (aid) payload.account_id = aid
        if (filters.platform.trim()) payload.platform = filters.platform.trim()
        if (filters.model.trim()) payload.model = filters.model.trim()
        if (filters.q.trim()) payload.q = filters.q.trim()
        const res = await request('/admin/ops/system-logs/cleanup', { method: 'POST', body: payload })
        message.success(`清理完成，删除 ${(res?.deleted ?? res?.rows ?? 0).toLocaleString('zh-CN')} 条日志`)
        page.value = 1
        fetchLogs(); fetchHealth()
      } catch (e) { message.error(`清理失败：${e.message}`) }
    },
  })
}

/* 按保留天数清理（承接旧版 cleanDays 弹窗逻辑） */
const cleanOpen = ref(false)
const cleanDays = ref(7)
const cleanLoading = ref(false)
const doCleanByDays = async () => {
  cleanLoading.value = true
  try {
    const start = new Date(Date.now() - (cleanDays.value || 0) * 86400000).toISOString()
    const res = await request('/admin/ops/system-logs/cleanup', { method: 'POST', body: { start_time: start } })
    message.success(`已清理 ${(res?.deleted ?? res?.rows ?? 0).toLocaleString('zh-CN')} 条系统日志`)
    cleanOpen.value = false
    page.value = 1
    fetchLogs(); fetchHealth()
  } catch (e) { message.error(`清理失败：${e.message}`) } finally { cleanLoading.value = false }
}

/* 运行时日志配置（实时生效；功能承接旧 OpsSystemLogTable 上半区） */
const rt = reactive({ level: 'info', enable_sampling: false, sampling_initial: 100, sampling_thereafter: 100, caller: true, stacktrace_level: 'error', retention_days: 30 })
const rtSaving = ref(false)
const loadRt = async () => {
  try { Object.assign(rt, await request('/admin/ops/runtime/logging', { method: 'GET' })) } catch { /* 配置读取失败不阻塞日志表 */ }
}
const saveRt = async () => {
  rtSaving.value = true
  try {
    Object.assign(rt, await request('/admin/ops/runtime/logging', { method: 'PUT', body: { ...rt } }))
    message.success('日志运行时配置已生效')
  } catch (e) { message.error(`保存失败：${e.message}`) } finally { rtSaving.value = false }
}
const resetRt = () => {
  Modal.confirm({
    title: '回滚日志配置', content: '将回滚为启动配置（env/yaml）并立即生效，确定继续？',
    okText: '确定', cancelText: '取消',
    onOk: async () => {
      try { Object.assign(rt, await request('/admin/ops/runtime/logging/reset', { method: 'POST' })); message.success('已回滚到启动配置') }
      catch (e) { message.error(`回滚失败：${e.message}`) }
    },
  })
}

/* 日志行详情：message + extra 关键字段拼平（旧 formatSystemLogDetail 同语义） */
const extraStr = (extra, key) => {
  const v = extra?.[key]
  if (v == null) return ''
  return typeof v === 'object' ? '' : String(v).trim()
}
const fmtDetail = (row) => {
  const parts = [String(row.message || '').trim()]
  const ex = row.extra || {}
  const access = [['status_code', 'status'], ['latency_ms', 'latency_ms'], ['method', 'method'], ['path', 'path'], ['client_ip', 'ip'], ['protocol', 'proto']]
    .map(([k, tag]) => { const v = extraStr(ex, k); return v ? `${tag}=${v}` : '' }).filter(Boolean)
  if (access.length) parts.push(access.join(' '))
  const corr = []
  if (row.request_id) corr.push(`req=${row.request_id}`)
  if (row.client_request_id) corr.push(`client_req=${row.client_request_id}`)
  if (row.user_id != null) corr.push(`user=${row.user_id}`)
  if (row.account_id != null) corr.push(`acc=${row.account_id}`)
  if (row.platform) corr.push(`platform=${row.platform}`)
  if (row.model) corr.push(`model=${row.model}`)
  if (corr.length) parts.push(corr.join(' '))
  const err = extraStr(ex, 'errors') || extraStr(ex, 'err') || extraStr(ex, 'error')
  if (err) parts.push(`error=${err}`)
  return parts.filter(Boolean).join('  ')
}

const fmtTime = (t) => (t ? String(t).replace('T', ' ').slice(5, 19) : '-')
const levelColor = (l) => (l === 'error' || l === 'fatal' ? 'red' : l === 'warn' || l === 'warning' ? 'orange' : l === 'debug' ? 'default' : 'blue')

const columns = [
  { title: '时间', key: 'time', width: 110 },
  { title: '级别', key: 'level', width: 74 },
  { title: '组件', dataIndex: 'component', key: 'component', width: 120, ellipsis: true },
  { title: '日志详情', key: 'detail', ellipsis: false },
]

onMounted(() => { fetchLogs(); fetchHealth(); loadRt() })
defineExpose({ refresh: () => { fetchLogs(); fetchHealth() } })
</script>

<template>
  <div>
    <!-- 摄入健康 + 工具按钮 -->
    <div class="lg-bar">
      <span class="sec-t">系统日志</span>
      <span v-if="health" class="hpill">
        摄入{{ (health.dropped_count > 0 || health.write_failed_count > 0) ? '异常' : '正常' }}
        · 队列 {{ health.queue_depth ?? '-' }}/{{ health.queue_capacity ?? '-' }}
        · 已写 {{ (health.written_count ?? 0).toLocaleString('zh-CN') }}
        <template v-if="health.dropped_count > 0"> · 丢弃 {{ health.dropped_count }}</template>
        <template v-if="health.write_failed_count > 0"> · 失败 {{ health.write_failed_count }}</template>
      </span>
      <div style="flex: 1"></div>
      <a-button size="small" :loading="loading" @click="fetchLogs"><template #icon><ReloadOutlined /></template></a-button>
      <a-button size="small" danger @click="cleanupByFilter">按当前筛选清理</a-button>
      <!-- 承接旧版“按保留天数清理”：start_time = 现在 - N 天，仅传时间参数 -->
      <a-button size="small" @click="cleanOpen = true">按保留天数清理…</a-button>
    </div>

    <!-- 10 项筛选（api_key_id 后端不支持，已移除） -->
    <div class="flt-grid">
      <a-select v-model:value="filters.time_range" :options="TR_OPTS" size="small" style="width: 78px" @change="applyFilters" />
      <a-input v-model:value="filters.start_time" type="datetime-local" size="small" style="width: 178px" placeholder="开始时间（可选）" />
      <a-input v-model:value="filters.end_time" type="datetime-local" size="small" style="width: 178px" placeholder="结束时间（可选）" />
      <a-select v-model:value="filters.level" :options="LEVEL_OPTS" size="small" style="width: 110px" @change="applyFilters" />
      <a-input v-model:value="filters.component" size="small" style="width: 130px" placeholder="组件 如 http.access" allow-clear @press-enter="applyFilters" />
      <a-input v-model:value="filters.request_id" size="small" style="width: 190px" placeholder="request_id" allow-clear @press-enter="applyFilters" />
      <a-input v-model:value="filters.client_request_id" size="small" style="width: 190px" placeholder="client_request_id" allow-clear @press-enter="applyFilters" />
      <a-input v-model:value="filters.user_id" size="small" style="width: 90px" placeholder="user_id" allow-clear @press-enter="applyFilters" />
      <a-input v-model:value="filters.account_id" size="small" style="width: 100px" placeholder="account_id" allow-clear @press-enter="applyFilters" />
      <a-input v-model:value="filters.platform" size="small" style="width: 110px" placeholder="平台" allow-clear @press-enter="applyFilters" />
      <a-input v-model:value="filters.model" size="small" style="width: 140px" placeholder="模型" allow-clear @press-enter="applyFilters" />
      <a-input v-model:value="filters.q" size="small" style="width: 170px" placeholder="关键词（消息/ID）" allow-clear @press-enter="applyFilters" />
      <a-button size="small" type="primary" @click="applyFilters">查询</a-button>
      <a-button size="small" @click="resetFilters">重置</a-button>
    </div>

    <a-table
      :columns="columns" :data-source="logs" row-key="id" size="small" :loading="loading"
      :scroll="{ x: 'max-content' }"
      :expandable="{ rowExpandable: (r) => Boolean(r.extra && Object.keys(r.extra).length) }"
      :pagination="{
        current: page, pageSize, total, showSizeChanger: false, showTotal: (t) => `共 ${t.toLocaleString('zh-CN')} 条`,
        onChange: (p) => { page = p; fetchLogs() },
      }"
    >
      <!-- 展开行：完整 extra JSON（行内只拼平固定 key，这里看全量附加字段） -->
      <template #expandedRowRender="{ record }">
        <pre class="extra-json">{{ JSON.stringify(record.extra, null, 2) }}</pre>
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'time'">{{ fmtTime(record.created_at) }}</template>
        <template v-else-if="column.key === 'level'">
          <a-tag :color="levelColor(record.level)" style="margin: 0">{{ record.level }}</a-tag>
        </template>
        <template v-else-if="column.key === 'detail'"><span class="detail-txt">{{ fmtDetail(record) }}</span></template>
      </template>
    </a-table>
    <div v-if="health?.last_error" class="sink-err">最近写入错误：{{ health.last_error }}</div>

    <!-- 按保留天数清理弹窗（旧版能力保留） -->
    <a-modal v-model:open="cleanOpen" title="清理系统日志" :confirm-loading="cleanLoading" ok-text="确认清理" ok-type="danger" cancel-text="取消" @ok="doCleanByDays">
      <p style="margin: 0 0 12px">清理 <b>{{ cleanDays }}</b> 天之前的全部系统日志，此操作不可恢复。</p>
      <div style="display: flex; align-items: center; gap: 12px; font-size: 13px">
        <span>保留最近</span>
        <a-input-number v-model:value="cleanDays" :min="1" :max="365" style="width: 100px" />
        <span>天</span>
      </div>
    </a-modal>

    <!-- 运行时日志配置（实时生效） -->
    <a-collapse ghost size="small" style="margin-top: 8px">
      <a-collapse-panel key="rt" header="运行时日志配置（实时生效）">
        <div class="rt-grid">
          <label>级别
            <a-select v-model:value="rt.level" size="small" :options="['debug', 'info', 'warn', 'error'].map((v) => ({ value: v, label: v }))" style="width: 100%" />
          </label>
          <label>堆栈阈值
            <a-select v-model:value="rt.stacktrace_level" size="small" :options="['none', 'error', 'fatal'].map((v) => ({ value: v, label: v }))" style="width: 100%" />
          </label>
          <label>采样初始
            <a-input-number v-model:value="rt.sampling_initial" size="small" :min="1" style="width: 100%" />
          </label>
          <label>采样后续
            <a-input-number v-model:value="rt.sampling_thereafter" size="small" :min="1" style="width: 100%" />
          </label>
          <label>保留天数
            <a-input-number v-model:value="rt.retention_days" size="small" :min="1" :max="3650" style="width: 100%" />
          </label>
          <div class="rt-checks">
            <a-checkbox v-model:checked="rt.caller">caller</a-checkbox>
            <a-checkbox v-model:checked="rt.enable_sampling">sampling</a-checkbox>
          </div>
          <div class="rt-btns">
            <a-button size="small" type="primary" :loading="rtSaving" @click="saveRt">保存并生效</a-button>
            <a-button size="small" @click="resetRt">回滚默认值</a-button>
          </div>
        </div>
      </a-collapse-panel>
    </a-collapse>
  </div>
</template>

<style scoped>
.lg-bar { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; margin: 16px 0 8px; }
.sec-t { font-size: 13px; font-weight: 500; }
.hpill { font-size: 12px; color: var(--text2); }
.flt-grid { display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 10px; }
.detail-txt { white-space: normal; word-break: break-all; font-size: 12px; line-height: 1.55; }
.extra-json {
  margin: 4px 0;
  font-family: Consolas, monospace;
  font-size: 12px;
  line-height: 1.6;
  background: rgba(127, 127, 127, 0.08);
  border-radius: 6px;
  padding: 8px 10px;
  max-height: 280px;
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-all;
}
.sink-err { margin-top: 6px; font-size: 12px; color: #dc2626; }
.rt-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(140px, 1fr)); gap: 10px; align-items: end; }
.rt-grid label { display: flex; flex-direction: column; gap: 4px; font-size: 12px; color: var(--text2); }
.rt-checks { display: flex; gap: 12px; align-items: center; padding-bottom: 2px; }
.rt-btns { display: flex; gap: 8px; }
</style>
