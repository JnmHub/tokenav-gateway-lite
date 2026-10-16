<script setup>
import W12Button from '../components/design/W12Button.vue'
import W12Input from '../components/design/W12Input.vue'
import W12Select from '../components/design/W12Select.vue'
import W12Table from '../components/design/W12Table.vue'
import W12Dialog from '../components/design/W12Dialog.vue'
/* 审计日志 v3（对照旧 AuditLogView.vue 审计清单补齐）：
 * - 筛选 6 项：操作者邮箱/操作类型/资源类型/资源编号/结果/时间范围（RFC3339 提交）
 * - result 枚举修正：pending/success/failure/denied（后端 operation_audit.go 枚举；
 *   旧 v2 里的 error 是无效值，永远筛空——审计发现的缺陷）
 * - 表格补操作者列（邮箱+角色+鉴权方式）、资源列、request_id（悬停全文）
 * - 详情弹窗（GET /admin/audit-logs/:id，12 字段）
 * - 上一页（游标栈回退）+ 常态刷新按钮
 * 清理流程不变：preview → TOTP step-up → clear */
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { createLatestRequest, runLatest } from '../api/latestRequest'
import { message } from 'ant-design-vue'
import { request as apiRequest, isCancellation } from '../api'
import { clearAuditLogs } from '../api/audit'
import { useRequestLifetime } from '../composables/useRequestLifetime'

const loading = ref(false)
const rows = ref([])
const nextCursor = ref(null)
const err = ref('')
// 游标栈：每翻一页 push 当前游标，「上一页」pop 回退（旧版语义）
const cursorStack = ref([])
const currentCursor = ref(null)
const listLane = createLatestRequest()
const detailLane = createLatestRequest()

const filters = ref({ actor_email: '', action: '', resource_type: '', resource_id: '', result: '', start_time: '', end_time: '' })

const columns = [
  { title: '时间', key: 'time', width: 150 },
  { title: '操作者', key: 'actor', width: 200 },
  { title: '操作', dataIndex: 'action', key: 'action', width: 160 },
  { title: '资源', key: 'resource', width: 150 },
  { title: '结果', key: 'result', width: 80 },
  { title: '请求', key: 'req', ellipsis: true },
  { title: '详情', key: 'detail', width: 60 },
]

const RESULT_OPTIONS = [
  { value: '', label: '全部结果' },
  { value: 'pending', label: '待确认' },
  { value: 'success', label: '成功' },
  { value: 'failure', label: '失败' },
  { value: 'denied', label: '拒绝' },
]

const fmtTime = (t) => (t ? String(t).replace('T', ' ').slice(0, 19) : '-')

const buildQuery = (cursor) => {
  const q = { limit: 50 }
  const f = filters.value
  if (cursor) q.before_id = cursor
  if (f.actor_email.trim()) q.actor_email = f.actor_email.trim()
  if (f.action.trim()) q.action = f.action.trim()
  if (f.resource_type.trim()) q.resource_type = f.resource_type.trim()
  if (f.resource_id.trim()) q.resource_id = f.resource_id.trim()
  if (f.result) q.result = f.result
  // datetime-local → RFC3339（后端契约）
  if (f.start_time) q.start_time = new Date(f.start_time).toISOString()
  if (f.end_time) q.end_time = new Date(f.end_time).toISOString()
  return q
}

const load = async (dir = 'first') => {
  loading.value = true
  let cursor = null
  if (dir === 'more') cursor = nextCursor.value
  else if (dir === 'prev') cursor = cursorStack.value[cursorStack.value.length - 1] ?? null
  await runLatest(listLane, (signal) => request('/admin/audit-logs', { method: 'GET', query: buildQuery(cursor), signal }), {
    apply: (data) => {
      rows.value = data.items || []
      if (dir === 'more') cursorStack.value.push(currentCursor.value)
      else if (dir === 'first') cursorStack.value = []
      else if (dir === 'prev') cursorStack.value.pop()
      currentCursor.value = cursor; nextCursor.value = data.next_cursor ?? null; err.value = ''
    },
    error: (e) => { err.value = e.message },
    finish: () => { loading.value = false },
  })
}

const resetFilters = () => {
  filters.value = { actor_email: '', action: '', resource_type: '', resource_id: '', result: '', start_time: '', end_time: '' }
  cursorStack.value = []
  load('first')
}
onMounted(() => load('first'))

/* ── 详情弹窗：GET /admin/audit-logs/:id（12 字段，对照旧 detailRows） ── */
const detailOpen = ref(false)
const detailLoading = ref(false)
const detail = ref(null)

const openDetail = async (id) => {
  detailOpen.value = true; detailLoading.value = true; detail.value = null
  await runLatest(detailLane, (signal) => request(`/admin/audit-logs/${id}`, { method: 'GET', signal }), {
    apply: (data) => { if (detailOpen.value) detail.value = data },
    error: (e) => { message.error(`加载详情失败：${e.message}`); detailOpen.value = false },
    finish: () => { detailLoading.value = false },
  })
}

const detailRows = computed(() => {
  const it = detail.value
  if (!it) return []
  return [
    ['ID', String(it.id)],
    ['时间', fmtTime(it.created_at)],
    ['操作者', `${it.actor_email || '-'}（${it.actor_role || '-'}）`],
    ['鉴权方式', it.auth_method || '-'],
    ['操作', it.action || '-'],
    ['资源', `${it.resource_type || '-'}${it.resource_id ? ` #${it.resource_id}` : ''}`],
    ['结果', `${it.result} / HTTP ${it.status_code}`],
    ['请求', `${it.method || '-'} ${it.path || '-'}`],
    ['request_id', it.request_id || '-'],
    ['来源 IP', it.client_ip || '-'],
    ['UA hash', it.ua_hash || '-'],
    ['耗时', `${it.latency_ms}ms`],
  ]
})

/* ── summary / extra：后端 OperationAuditLog 契约字段（旧实现读不存在的 changes，
 *    永不显示——已修正为 summary=脱敏摘要 + extra=附加字段，均为扁平 key-value 对象） ── */
const fmtKvValue = (v) => {
  if (v === null || v === undefined || v === '') return '-'
  if (Array.isArray(v)) return v.length ? v.join('、') : '-'
  if (typeof v === 'object') return JSON.stringify(v)
  return String(v)
}
const toKvRows = (obj) => Object.entries(obj || {}).map(([k, v]) => ({ k, v: fmtKvValue(v) }))
const summaryKv = computed(() => toKvRows(detail.value?.summary))
const extraKv = computed(() => toKvRows(detail.value?.extra))

/* ── 清理日志：预览 → TOTP（若启用）→ 确认（与 v2 相同） ── */
const clearOpen = ref(false)
const clearLoading = ref(false)
const clearPreview = ref(null)
const stepUpCode = ref('')
const clearError = ref('')
const lifetime = useRequestLifetime(() => {
  listLane.cancel(); detailLane.cancel(); cursorStack.value = []; currentCursor.value = null
  rows.value = []; detail.value = null; detailOpen.value = false
  clearPreview.value = null; stepUpCode.value = ''; clearOpen.value = false; clearError.value = ''
})
const request = (path, options) => apiRequest(path, { ...lifetime, ...options })

const openClear = async () => {
  clearLoading.value = true
  clearPreview.value = null
  stepUpCode.value = ''; clearError.value = ''
  clearOpen.value = true
  try {
    clearPreview.value = await request('/admin/audit-logs/clear/preview', { method: 'GET' })
  } catch (e) {
    if (!isCancellation(e)) message.error(`获取清理预览失败：${e.message}`)
    clearOpen.value = false
  } finally {
    clearLoading.value = false
  }
}

const doClear = async () => {
  if (clearLoading.value || !clearPreview.value) return
  clearLoading.value = true
  clearError.value = ''
  try {
    const result = await clearAuditLogs(stepUpCode.value, lifetime)
    message.success(`已清理 ${Number(result.deleted).toLocaleString('zh-CN')} 条审计日志`)
    clearOpen.value = false
    await load('first')
  } catch (e) {
    if (!isCancellation(e)) {
      clearError.value = e.message
      message.error(`清理失败：${e.message}`)
    }
  } finally {
    stepUpCode.value = ''
    clearLoading.value = false
  }
}
onUnmounted(() => { listLane.dispose(); detailLane.dispose() })
</script>

<template>
  <div class="audit w12-design">
    <!-- 筛选区：6 项 + 筛选/重置（对照旧版） -->
    <div class="filters">
      <W12Input v-model:value="filters.actor_email" placeholder="操作者邮箱（完整）" style="width: 180px" @pressEnter="load('first')" />
      <W12Input v-model:value="filters.action" placeholder="操作类型" style="width: 150px" @pressEnter="load('first')" />
      <W12Input v-model:value="filters.resource_type" placeholder="资源类型" style="width: 130px" @pressEnter="load('first')" />
      <W12Input v-model:value="filters.resource_id" placeholder="资源编号" style="width: 110px" @pressEnter="load('first')" />
      <W12Select v-model:value="filters.result" style="width: 110px" :options="RESULT_OPTIONS" @change="() => load('first')" />
      <W12Input v-model:value="filters.start_time" type="datetime-local" style="width: 190px" />
      <span class="total">~</span>
      <W12Input v-model:value="filters.end_time" type="datetime-local" style="width: 190px" />
      <W12Button variant="primary" compact @click="load('first')">筛选</W12Button>
      <W12Button compact @click="resetFilters">重置</W12Button>
      <span class="flex1"></span>
      <W12Button compact @click="load('first')">刷新</W12Button>
      <W12Button compact @click="openClear" variant="danger">清理日志…</W12Button>
    </div>
    <div v-if="err" class="total err" style="margin-bottom: 8px">{{ err }}（点击刷新重试）</div>
    <W12Table
      :columns="columns" :data-source="rows" :loading="loading" row-key="id" size="small" :scroll="{ x: 'max-content' }"
      :pagination="false"
    >
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'time'">{{ fmtTime(record.created_at) }}</template>
        <template v-else-if="column.key === 'actor'">
          <div class="actor">
            <div class="actor-mail">{{ record.actor_email || '-' }}</div>
            <div class="actor-sub">{{ record.actor_role || '-' }} · {{ record.auth_method || '-' }}</div>
          </div>
        </template>
        <template v-else-if="column.key === 'resource'">
          <span class="mono">{{ record.resource_type }}{{ record.resource_id ? ` #${record.resource_id}` : '' }}</span>
        </template>
        <template v-else-if="column.key === 'result'">
          <a-tag :color="record.result === 'success' ? 'green' : record.result === 'denied' ? 'orange' : record.result === 'pending' ? 'blue' : 'red'">{{ record.result }}</a-tag>
        </template>
        <template v-else-if="column.key === 'req'">
          <a-tooltip :title="record.request_id"><span class="mono">{{ record.method }} {{ record.path }}</span></a-tooltip>
        </template>
        <template v-else-if="column.key === 'detail'">
          <W12Button compact @click="openDetail(record.id)">查看</W12Button>
        </template>
      </template>
    </W12Table>
    <div class="more-bar">
      <W12Button v-if="cursorStack.length" :disabled="loading" @click="load('prev')">上一页</W12Button>
      <W12Button v-if="nextCursor" :loading="loading" @click="load('more')">加载更多</W12Button>
    </div>

    <!-- 详情弹窗：12 字段网格（对照旧 detailRows） -->
    <W12Dialog v-model:open="detailOpen" title="操作日志详情" :footer="null" width="560px">
      <a-spin :spinning="detailLoading">
        <div v-if="detail" class="detail-grid">
          <div v-for="[label, value] in detailRows" :key="label" class="d-row">
            <span class="d-k">{{ label }}</span>
            <span class="d-v mono">{{ value }}</span>
          </div>
          <!-- 变更摘要（脱敏）：summary 字段（fields/states/body/mode/binding/count 等固定词表） -->
          <div v-if="summaryKv.length" class="d-kv">
            <div class="d-kv-title">变更摘要（脱敏）</div>
            <div class="d-kv-grid">
              <div v-for="item in summaryKv" :key="item.k" class="d-kv-item">
                <div class="d-kv-k">{{ item.k }}</div>
                <div class="d-kv-v mono">{{ item.v }}</div>
              </div>
            </div>
          </div>
          <!-- 附加字段：extra 字段（扁平 key-value 小网格） -->
          <div v-if="extraKv.length" class="d-kv">
            <div class="d-kv-title">附加字段</div>
            <div class="d-kv-grid">
              <div v-for="item in extraKv" :key="item.k" class="d-kv-item">
                <div class="d-kv-k">{{ item.k }}</div>
                <div class="d-kv-v mono">{{ item.v }}</div>
              </div>
            </div>
          </div>
        </div>
      </a-spin>
    </W12Dialog>

    <!-- 清理确认弹窗：预览 → TOTP → 执行（不变） -->
    <W12Dialog v-model:open="clearOpen" title="清理审计日志" :confirm-loading="clearLoading" :busy="clearLoading" @cancel="stepUpCode = ''; clearPreview = null" ok-text="确认清理" ok-type="danger" cancel-text="取消" @ok="doClear">
      <a-alert v-if="clearError" :message="clearError" type="error" show-icon />
      <a-spin :spinning="clearLoading">
        <template v-if="clearPreview">
          <p style="margin: 0 0 8px">将清理 <b>{{ clearPreview.rows?.toLocaleString('zh-CN') }}</b> 条审计日志</p>
          <p style="margin: 0 0 12px; color: var(--text2); font-size: 12px">
            时间范围：{{ fmtTime(clearPreview.oldest_at) }} ~ {{ fmtTime(clearPreview.newest_at) }}
          </p>
          <p style="margin: 0 0 12px; color: var(--text2); font-size: 12px">清理后仅保留一条清理记录。此操作立即生效且不可恢复。</p>
          <div class="frow-stepup">
            <span>TOTP（动态验证码；高风险验证关闭时可留空）</span>
            <W12Input v-model:value="stepUpCode" placeholder="6 位验证码" style="width: 160px" :maxlength="6" />
          </div>
        </template>
      </a-spin>
    </W12Dialog>
  </div>
</template>

<style scoped>
.audit { height: 100%; overflow-y: auto; }
.filters { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-bottom: 10px; }
.toolbar { display: flex; align-items: center; gap: 12px; margin-bottom: 10px; }
.total { font-size: 12px; color: var(--text2); }
.total.err { color: #d13438; }
.more-bar { margin-top: 10px; display: flex; gap: 8px; justify-content: center; }
.frow-stepup { display: flex; align-items: center; justify-content: space-between; gap: 12px; font-size: 13px; }
.actor-mail { font-size: 13px; }
.actor-sub { font-size: 11px; color: var(--text2); }
.mono { font-family: Consolas, monospace; font-size: 12px; }
.flex1 { flex: 1; }
.detail-grid { display: grid; grid-template-columns: 1fr; gap: 8px; }
.d-row { display: flex; gap: 12px; padding: 7px 10px; border: 1px solid var(--hr); border-radius: 8px; font-size: 13px; }
.d-k { flex: none; width: 90px; color: var(--text2); }
.d-v { word-break: break-all; }
.d-kv { border: 1px solid var(--hr); border-radius: 8px; padding: 8px 10px; }
.d-kv-title { font-size: 12px; color: var(--text2); margin-bottom: 6px; }
.d-kv-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(150px, 1fr)); gap: 6px; }
.d-kv-item { border: 1px solid var(--hr); border-radius: 6px; padding: 4px 8px; min-width: 0; }
.d-kv-k { font-size: 11px; color: var(--text2); word-break: break-all; }
.d-kv-v { font-size: 12px; word-break: break-all; }
</style>
