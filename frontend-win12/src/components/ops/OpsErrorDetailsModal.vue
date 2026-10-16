<script setup>
/* 错误列表弹窗（三级下钻·第一级）：请求错误 / 上游错误 两个 tab
 * 契约来源：旧 OpsErrorDetailsModal.vue + OpsErrorLogTable.vue：
 * - GET /admin/ops/request-errors、GET /admin/ops/upstream-errors
 *   参数：page/page_size/time_range/view/sort/platform/group_id/q/
 *         status_codes（单码）/ status_codes_other=1（非枚举码）/ phase / error_owner
 * - 响应 { items: OpsErrorLog[], total, page, page_size, pages }
 * - 搜索 350ms 防抖；view: errors|excluded|all；sort: created_at_desc 等
 * - 表列：时间/类型/分类(.phase)/端点/平台/模型/分组/用户/API Key/账号/状态/消息
 * - 列表补齐：API Key 列并入用户列（key 名称 + api_key_deleted「已删除」标记，
 *   参照旧 OpsErrorLogTable 的 keyDeletedBadge 语义；详情字段在 OpsErrorDetailModal） */
import { ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { ReloadOutlined, CheckCircleOutlined, RollbackOutlined } from '@ant-design/icons-vue'
import { request } from '../../api'
import { confirmAction } from '../../utils/confirmAction'

const props = defineProps({
  open: { type: Boolean, default: false },
  errorType: { type: String, default: 'request' }, // 'request' | 'upstream'
  timeRange: { type: String, default: '1h' },
  platform: { type: String, default: '' },
  groupId: { type: Number, default: null },
})
const emit = defineEmits(['update:open', 'openDetail', 'switchType'])

const loading = ref(false)
const rows = ref([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(10)

const q = ref('')
const statusCode = ref(null) // number | 'other' | null
const phase = ref('')
const errorOwner = ref('')
const viewMode = ref('errors')

const switchTab = (k) => emit('switchType', k)

const STATUS_CODES = [400, 401, 403, 404, 409, 422, 429, 500, 502, 503, 504, 529]
const statusOpts = [{ value: null, label: '全部状态码' }, ...STATUS_CODES.map((c) => ({ value: c, label: String(c) })), { value: 'other', label: '其他' }]
const phaseOpts = [
  { value: '', label: '全部阶段' },
  { value: 'request', label: 'request' },
  { value: 'auth', label: 'auth' },
  { value: 'routing', label: 'routing' },
  { value: 'upstream', label: 'upstream' },
  { value: 'network', label: 'network' },
  { value: 'internal', label: 'internal' },
]
const ownerOpts = [
  { value: '', label: '全部归属' },
  { value: 'provider', label: 'provider' },
  { value: 'client', label: 'client' },
  { value: 'platform', label: 'platform' },
]
const viewOpts = [
  { value: 'errors', label: '计入错误' },
  { value: 'excluded', label: '已排除' },
  { value: 'all', label: '全部' },
]

/* 上游 tab 默认按旧实现锁定 phase=upstream */
const fetchRows = async () => {
  if (!props.open) return
  loading.value = true
  try {
    const query = {
      page: page.value, page_size: pageSize.value,
      time_range: props.timeRange, view: viewMode.value, sort: 'created_at_desc',
    }
    if (props.platform) query.platform = props.platform
    if (props.groupId) query.group_id = props.groupId
    if (q.value.trim()) query.q = q.value.trim()
    if (statusCode.value === 'other') query.status_codes_other = '1'
    else if (typeof statusCode.value === 'number') query.status_codes = String(statusCode.value)
    const pv = phase.value.trim()
    if (pv) query.phase = pv
    else if (props.errorType === 'upstream') query.phase = 'upstream'
    if (errorOwner.value) query.error_owner = errorOwner.value
    const path = props.errorType === 'upstream' ? '/admin/ops/upstream-errors' : '/admin/ops/request-errors'
    const data = await request(path, { method: 'GET', query })
    rows.value = data.items || []
    total.value = data.total || 0
  } catch (e) {
    rows.value = []; total.value = 0
    message.error(`错误列表加载失败：${e.message}`)
  } finally { loading.value = false }
}

const resetFilters = () => {
  q.value = ''; statusCode.value = null; phase.value = ''
  errorOwner.value = ''; viewMode.value = 'errors'; page.value = 1
  fetchRows()
}

watch(() => props.open, (v) => { if (v) { page.value = 1; resetFilters() } })
watch(() => props.errorType, () => { if (props.open) { page.value = 1; fetchRows() } })
watch(() => [props.timeRange, props.platform, props.groupId], () => { if (props.open) { page.value = 1; fetchRows() } })
watch(() => [statusCode.value, phase.value, errorOwner.value, viewMode.value], () => { if (props.open) { page.value = 1; fetchRows() } })

/* 搜索防抖 350ms（与旧实现一致） */
let searchTimer = null
watch(q, () => {
  if (!props.open) return
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => { page.value = 1; fetchRows() }, 350)
})

const fmtTime = (t) => (t ? String(t).replace('T', ' ').slice(5, 19) : '-')
const displayModel = (r) => r.upstream_model || r.requested_model || r.model || '-'

/* Key 前缀脱敏缩写：剥掉 sk- 后缩写尾 4 位（sk-***abcd），无值回退 '-'（参照旧列表 keyName） */
const fmtPrefix = (p) => {
  const s = String(p || '').trim()
  if (!s) return '-'
  return s.startsWith('sk-') ? `sk-***${s.slice(-4)}` : s
}
/* 列表「用户/API Key」单元格：优先 Key 名称（带被删标记），其次用户邮箱 / #id（旧 OpsErrorLogTable 列表语义） */
const apiTag = (r) => {
  if (r.api_key_id != null || r.api_key_name) return `${r.api_key_name || `#${r.api_key_id}`}${r.api_key_deleted ? '（已删除）' : ''}`
  return r.user_email || (r.user_id != null ? `#${r.user_id}` : '-')
}

const codeColor = (c) => (c >= 500 ? 'red' : c === 429 ? 'purple' : c >= 400 ? 'orange' : 'default')
const openRow = (r) => emit('openDetail', r.id, props.errorType)

/* 标记已解决 / 取消已解决：PUT /admin/ops/{request|upstream}-errors/:id/resolve
 * 危险语义（取消已解决会让错误重新计入统计）走统一确认弹窗 confirmAction */
const resolvingId = ref(null)
const toggleResolve = async (r) => {
  if (resolvingId.value === r.id) return
  const next = !r.resolved
  const ok = await confirmAction({
    title: next ? '标记为已解决' : '取消已解决',
    content: next
      ? `确认把错误 #${r.id} 标记为已解决？`
      : `取消已解决后错误 #${r.id} 将重新计入错误统计，确认继续？`,
    okText: next ? '标记已解决' : '取消已解决',
    danger: !next,
  })
  if (!ok) return
  resolvingId.value = r.id
  try {
    const base = props.errorType === 'upstream' ? '/admin/ops/upstream-errors' : '/admin/ops/request-errors'
    await request(`${base}/${r.id}/resolve`, { method: 'PUT', body: { resolved: next } })
    r.resolved = next // 本地即时更新，避免整页重拉丢失滚动位置
    message.success(next ? '已标记为已解决' : '已取消已解决')
  } catch (e) {
    message.error(`更新解决状态失败：${e.message}`)
  } finally { resolvingId.value = null }
}

const columns = [
  { title: '时间', key: 'time', width: 118 },
  { title: '状态', key: 'code', width: 80 },
  { title: '阶段', dataIndex: 'phase', key: 'phase', width: 84 },
  { title: '类型', dataIndex: 'type', key: 'type', width: 110, ellipsis: true },
  { title: '平台', key: 'platform', width: 84 },
  { title: '模型', key: 'model', width: 150, ellipsis: true },
  { title: '分组', dataIndex: 'group_name', key: 'group', width: 110, ellipsis: true },
  { title: '用户/API Key', key: 'user', width: 180, ellipsis: true },
  { title: '账号', dataIndex: 'account_name', key: 'account', width: 130, ellipsis: true },
  { title: '端点', dataIndex: 'inbound_endpoint', key: 'endpoint', width: 150, ellipsis: true },
  { title: '消息', key: 'msg', ellipsis: true },
  { title: '操作', key: 'act', width: 104, fixed: 'right' },
]
</script>

<template>
  <a-modal
    :open="open" width="1100px" style="top: 40px" :footer="null"
    :title="errorType === 'upstream' ? '上游错误明细' : '请求错误明细'"
    @cancel="emit('update:open', false)"
  >
    <a-tabs :active-key="errorType" size="small" @tab-click="switchTab">
      <a-tab-pane key="request" tab="请求错误" />
      <a-tab-pane key="upstream" tab="上游错误" />
    </a-tabs>

    <div class="flt">
      <a-input v-model:value="q" size="small" placeholder="搜索消息 / request_id / 模型…" style="width: 220px" allow-clear />
      <a-select v-model:value="statusCode" :options="statusOpts" size="small" style="width: 110px" />
      <a-select v-model:value="phase" :options="phaseOpts" size="small" style="width: 110px" />
      <a-select v-model:value="errorOwner" :options="ownerOpts" size="small" style="width: 104px" />
      <a-select v-model:value="viewMode" :options="viewOpts" size="small" style="width: 96px" />
      <span class="cnt">共 {{ total.toLocaleString('zh-CN') }} 条</span>
      <div style="flex: 1"></div>
      <a-button size="small" @click="resetFilters">重置</a-button>
      <a-button size="small" :loading="loading" @click="fetchRows"><template #icon><ReloadOutlined /></template></a-button>
    </div>

    <a-table
      :columns="columns" :data-source="rows" row-key="id" size="small" :loading="loading"
      :scroll="{ x: 'max-content' }" :pagination="{
        current: page, pageSize, total, showSizeChanger: false, showTotal: (t) => `共 ${t} 条`,
        onChange: (p) => { page = p; fetchRows() },
      }"
      :custom-row="(r) => ({ style: 'cursor: pointer', onClick: () => openRow(r) })"
    >
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'time'">{{ fmtTime(record.created_at) }}</template>
        <template v-else-if="column.key === 'code'">
          <a-tag :color="codeColor(record.status_code)">{{ record.status_code ?? '-' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'platform'">{{ record.platform || '-' }}</template>
        <template v-else-if="column.key === 'model'">{{ displayModel(record) }}</template>
        <template v-else-if="column.key === 'user'">{{ apiTag(record) }}</template>
        <template v-else-if="column.key === 'account'">{{ record.account_name || (record.account_id != null ? `#${record.account_id}` : '-') }}</template>
        <template v-else-if="column.key === 'msg'">{{ record.message || '-' }}</template>
        <template v-else-if="column.key === 'act'">
          <a-button
            size="small" :loading="resolvingId === record.id"
            @click.stop="toggleResolve(record)"
          >
            <template #icon><RollbackOutlined v-if="record.resolved" /><CheckCircleOutlined v-else /></template>
            {{ record.resolved ? '取消已解决' : '标记已解决' }}
          </a-button>
        </template>
      </template>
    </a-table>
  </a-modal>
</template>

<style scoped>
.flt { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; margin-bottom: 10px; }
.cnt { font-size: 12px; color: var(--text2); margin-left: 4px; }
</style>
