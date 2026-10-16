<script setup>
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { request as apiRequest, isCancellation } from '../../api/index.js'
import { readRiskLog, aiCallLabel } from '../../api/riskControl.js'
import { isAILog, riskLogResult, riskLogCategories, formatRiskTime, formatRiskPercent, formatRiskMS } from '../../api/riskLogPresentation.js'
import { useWindowContext } from '../../core/windowContext.js'
import { message, Modal } from 'ant-design-vue'
import { createLatestRequest, runLatest } from '../../api/latestRequest.js'
import { useRequestLifetime } from '../../composables/useRequestLifetime.js'
import RiskLogDetail from './RiskLogDetail.vue'
import W12Button from '../design/W12Button.vue'
import W12Input from '../design/W12Input.vue'
import W12Select from '../design/W12Select.vue'
import W12Table from '../design/W12Table.vue'
import W12Dialog from '../design/W12Dialog.vue'
const emit = defineEmits(['fingerprints-changed'])
const lane = createLatestRequest(), detailLane = createLatestRequest()
const windowContext = useWindowContext()
const logs = ref([]), logsLoading = ref(false), logsErr = ref('')
const page = ref(1), pageSize = ref(20), total = ref(0), groups = ref([])
const detailOpen = ref(false), detailRow = ref(null), detailId = ref(null), detailLoading = ref(false), detailError = ref('')
const detailHashDeleting = ref(false), unbanning = ref(null)
const lifetime = useRequestLifetime(() => {
  lane.cancel(); detailLane.cancel(); logs.value = []; total.value = 0; groups.value = []
  detailRow.value = null; detailId.value = null; detailError.value = ''; detailLoading.value = false; detailOpen.value = false
})
const request = (path, options) => apiRequest(path, { ...lifetime, ...options })
const emptyFilters = () => ({ result: '', group_id: 0, content_source: '', detected_language: '', route_reason: '', endpoint: '', search: '', from: '', to: '' })
const filters = ref(emptyFilters())
const RESULT_OPTIONS = [
  { value: '', label: '全部结果' }, { value: 'hit', label: '已命中' }, { value: 'blocked', label: '已拦截' },
  { value: 'pass', label: '未命中' }, { value: 'error', label: '异常 / 未判定' },
]
const SOURCE_OPTIONS = [
  { value: '', label: '全部来源' }, { value: 'user', label: '用户输入' }, { value: 'instructions', label: '系统指令' },
  { value: 'skill', label: 'Skill（技能）内容' }, { value: 'tool', label: '工具输出' }, { value: 'mixed', label: '混合' },
]
const ROUTE_OPTIONS = [
  { value: '', label: '全部路由' }, { value: 'allow_original', label: '原组放行' }, { value: 'rerouted', label: '已切组' },
  { value: 'no_candidate', label: '无候选组' }, { value: 'unsupported_language', label: '语种不支持' },
]
const ENDPOINT_OPTIONS = [
  { value: '', label: '全部端点' },
  ...['/v1/messages', '/v1/responses', '/v1/chat/completions', '/v1beta/models', '/v1/images/generations', '/v1/images/edits'].map((value) => ({ value, label: value })),
]
const LANGUAGE_OPTIONS = [
  { value: '', label: '全部语言' },
  ...[['zh', '中文'], ['en', '英语'], ['ja', '日语'], ['ko', '韩语'], ['es', '西班牙语'], ['fr', '法语'],
    ['de', '德语'], ['pt', '葡萄牙语'], ['it', '意大利语'], ['ru', '俄语'], ['ar', '阿拉伯语'], ['hi', '印地语'],
    ['th', '泰语'], ['tr', '土耳其语'], ['vi', '越南语'], ['id', '印尼语'], ['he', '希伯来语'], ['el', '希腊语']].map(([value, label]) => ({ value, label })),
]
const SOURCE_LABELS = { user: '用户输入', instructions: '系统指令', skill: 'Skill（技能）内容', tool: '工具输出', mixed: '混合' }
const groupOptions = computed(() => [{ value: 0, label: '全部分组' }, ...groups.value.map((g) => ({ value: g.id, label: `${g.name} (${g.platform})` }))])
const columns = [
  { title: '时间', key: 'time', width: 150 }, { title: '分组', key: 'group', width: 170 },
  { title: '用户', key: 'user', width: 140 }, { title: '端点 / 模型', key: 'endpoint', width: 200 },
  { title: '结果 / 模型调用', key: 'result', width: 210 }, { title: '分类标签 / 本地得分', key: 'highest', width: 190 },
  { title: '违规', key: 'violation', width: 80 }, { title: '耗时', key: 'latency', width: 170 }, { title: '', key: 'detail', width: 64 },
]
const violationText = (row) => {
  if (isAILog(row) && row.ai_review_detail?.verdict === 'error') return '未计违规'
  if (!row.flagged) return '—'
  if (row.violation_count === 0) return '未计入封号'
  return `${row.violation_count || 1} 次`
}
const canUnbanRow = (row) => Boolean(row.auto_banned && row.user_id && row.user_status === 'disabled')
const buildQuery = () => {
  const f = filters.value, query = { page: page.value, page_size: pageSize.value }
  for (const key of ['result', 'group_id', 'endpoint', 'content_source', 'detected_language', 'route_reason']) if (f[key]) query[key] = f[key]
  if (f.search.trim()) query.search = f.search.trim()
  if (f.from) query.from = new Date(f.from).toISOString()
  if (f.to) query.to = new Date(f.to).toISOString()
  return query
}
async function loadLogs() {
  logsLoading.value = true
  await runLatest(lane, (signal) => request('/admin/risk-control/logs', { method: 'GET', query: buildQuery(), signal }), {
    apply: (data) => {
      logs.value = data.items || []; total.value = data.total || 0
      page.value = data.page || page.value; pageSize.value = data.page_size || pageSize.value; logsErr.value = ''
    },
    error: (e) => { logsErr.value = e.message },
    finish: () => { logsLoading.value = false },
  })
}
const reload = () => { page.value = 1; return loadLogs() }
const resetFilters = () => { filters.value = emptyFilters(); return reload() }
const onTableChange = (pagination) => { page.value = pagination.current; pageSize.value = pagination.pageSize; return loadLogs() }
const pagination = computed(() => ({ current: page.value, pageSize: pageSize.value, total: total.value, showSizeChanger: true, pageSizeOptions: ['10', '20', '50', '100'], showTotal: (n) => `共 ${n} 条` }))

async function loadDetail() {
  if (detailId.value === null || !detailOpen.value) return
  const id = detailId.value
  detailLoading.value = true; detailError.value = ''; detailRow.value = null
  // 详情有独立代际；切换、关闭弹窗或会话结束后，迟到的全文不得写回其他记录。
  await runLatest(detailLane, (signal) => readRiskLog(id, { ...lifetime, signal }), {
    apply: (row) => {
      if (!row || String(row.id) !== String(id)) { detailError.value = '后台未返回对应日志详情，请重新读取'; return }
      detailRow.value = row
    },
    error: () => { detailError.value = '日志详情读取失败，请重试；完整文本尚未加载。' },
    finish: () => { detailLoading.value = false },
  })
}
const openDetail = (row) => { detailId.value = row.id; detailOpen.value = true; return loadDetail() }
watch(detailOpen, (open) => {
  if (!open) { detailLane.cancel(); detailRow.value = null; detailId.value = null; detailError.value = ''; detailLoading.value = false }
}, { flush: 'sync' })
const deleteDetailFingerprint = () => {
  const fingerprint = detailRow.value?.content_fingerprint
  if (!fingerprint) return
  Modal.confirm({
    getContainer: windowContext?.getContainer, title: '删除该内容指纹', content: '删除该历史指纹记录？纯 AI 仍按当前策略审查对应内容。', okText: '确认删除', okType: 'danger', cancelText: '取消',
    onOk: async () => {
      detailHashDeleting.value = true
      try {
        const response = await request('/admin/risk-control/hashes', { method: 'DELETE', body: { input_hash: fingerprint } })
        if (typeof response?.deleted !== 'boolean') throw new Error('后台未返回有效删除结果，请刷新后核对')
        if (response.deleted) message.success('已删除该历史指纹记录')
        else message.info('该历史指纹已不存在，无需删除')
        emit('fingerprints-changed')
      } catch (e) { if (!isCancellation(e)) message.error(`删除失败：${e.message}`) }
      finally { detailHashDeleting.value = false }
    },
  })
}
const unban = (row) => {
  Modal.confirm({
    getContainer: windowContext?.getContainer, title: '解封用户', content: `确认解封 ${row.user_email || `UID ${row.user_id}`}？解封后该用户立即恢复调用。`, okText: '确认解封', okType: 'danger', cancelText: '取消',
    onOk: async () => {
      unbanning.value = row.user_id
      try {
        const response = await request(`/admin/risk-control/users/${row.user_id}/unban`, { method: 'POST' })
        logs.value = logs.value.map((item) => item.user_id === row.user_id ? { ...item, user_status: response.status } : item)
        message.success('已解封')
      } catch (e) { if (!isCancellation(e)) message.error(`解封失败：${e.message}`) }
      finally { unbanning.value = null }
    },
  })
}
async function loadGroups() {
  try { groups.value = await request('/admin/groups/all', { method: 'GET' }) } catch { /* 分组筛选不阻塞主流程。 */ }
}
onMounted(() => { loadGroups(); loadLogs() })
onUnmounted(() => { lane.dispose(); detailLane.dispose() })
</script>

<template>
  <div class="w12-design">
    <div class="filters">
      <W12Select v-model:value="filters.result" style="width: 130px" :options="RESULT_OPTIONS" @change="reload" />
      <W12Select v-model:value="filters.group_id" style="width: 170px" :options="groupOptions" @change="reload" />
      <W12Select v-model:value="filters.content_source" style="width: 150px" :options="SOURCE_OPTIONS" @change="reload" />
      <W12Select v-model:value="filters.detected_language" style="width: 130px" :options="LANGUAGE_OPTIONS" @change="reload" />
      <W12Select v-model:value="filters.route_reason" style="width: 130px" :options="ROUTE_OPTIONS" @change="reload" />
      <W12Select v-model:value="filters.endpoint" style="width: 180px" :options="ENDPOINT_OPTIONS" @change="reload" />
      <W12Input v-model:value="filters.search" placeholder="搜索内容 / AI 文本 / 错误" style="width: 220px" allow-clear @pressEnter="reload" />
      <W12Input v-model:value="filters.from" type="datetime-local" style="width: 185px" @change="reload" />
      <span class="total">~</span><W12Input v-model:value="filters.to" type="datetime-local" style="width: 185px" @change="reload" />
      <W12Button size="small" @click="resetFilters">重置</W12Button><span class="flex1"></span>
      <W12Button size="small" :loading="logsLoading" @click="loadLogs">刷新</W12Button>
    </div>
    <div v-if="logsErr" class="logs-err">{{ logsErr }}（点击刷新重试）</div>
    <W12Table :columns="columns" :data-source="logs" :loading="logsLoading" row-key="id" size="small" :scroll="{ x: 'max-content' }" :pagination="pagination" @change="onTableChange">
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'time'">{{ formatRiskTime(record.created_at) }}</template>
        <template v-else-if="column.key === 'group'">
          <div>{{ record.effective_group_name || record.group_name || '—' }}</div>
          <div v-if="record.keyword_policy_rerouted" class="sub warn">{{ record.original_group_name || '—' }} → {{ record.effective_group_name || record.group_name || '—' }}</div>
          <div v-if="record.content_source || record.detected_languages?.length" class="sub">{{ SOURCE_LABELS[record.content_source] || record.content_source || '' }}<template v-if="record.detected_languages?.length"> · {{ record.detected_languages.join(', ') }}</template></div>
        </template>
        <template v-else-if="column.key === 'user'"><div>{{ record.user_email || '—' }}</div><div v-if="record.user_id" class="sub">UID {{ record.user_id }}</div></template>
        <template v-else-if="column.key === 'endpoint'"><div>{{ record.endpoint || '—' }}</div><div class="sub">{{ record.provider || '—' }} / {{ record.model || '—' }}</div></template>
        <template v-else-if="column.key === 'result'">
          <a-tag :color="riskLogResult(record).color" class="result-tag">{{ riskLogResult(record).label }}</a-tag>
          <div v-if="isAILog(record)" class="sub">{{ aiCallLabel(record.ai_review_detail?.provider_called) }}</div>
        </template>
        <template v-else-if="column.key === 'highest'">
          <template v-if="isAILog(record)">
            <div class="category-tags"><a-tag v-for="category in riskLogCategories(record)" :key="category">{{ category }}</a-tag></div>
            <span v-if="!riskLogCategories(record).length" class="sub">{{ !record.ai_review_detail || record.ai_review_detail.verdict === 'error' ? '未判定' : '无分类标签' }}</span>
          </template>
          <template v-else><div>{{ record.highest_category || '—' }}</div><div class="sub">{{ formatRiskPercent(record.highest_score) }}</div><div v-if="record.matched_keyword" class="sub warn kw">命中：{{ record.matched_keyword }}</div></template>
        </template>
        <template v-else-if="column.key === 'violation'">
          <div>{{ violationText(record) }}</div><div v-if="record.auto_banned" class="sub warn">已封禁</div>
          <W12Button v-if="canUnbanRow(record)" size="small" variant="danger" :loading="unbanning === record.user_id" @click="unban(record)">解封</W12Button>
        </template>
        <template v-else-if="column.key === 'latency'">
          <template v-if="isAILog(record)"><div>模型 {{ record.ai_review_detail?.provider_called === false ? '未调用' : formatRiskMS(record.ai_review_detail?.provider_latency_ms) }}</div><div class="sub">AI 总耗时 {{ formatRiskMS(record.ai_review_detail?.latency_ms) }}</div></template>
          <div v-else>上游 {{ formatRiskMS(record.upstream_latency_ms) }}</div>
          <div v-if="record.queue_delay_ms !== null && record.queue_delay_ms !== undefined" class="sub">日志落库 {{ formatRiskMS(record.queue_delay_ms) }}</div>
        </template>
        <template v-else-if="column.key === 'detail'"><W12Button size="small" @click="openDetail(record)">查看</W12Button></template>
      </template>
    </W12Table>
    <W12Dialog v-model:open="detailOpen" title="审查日志详情" :footer="null" width="800px">
      <RiskLogDetail :row="detailRow" :loading="detailLoading" :error="detailError" :deleting="detailHashDeleting" @retry="loadDetail" @delete-fingerprint="deleteDetailFingerprint" />
    </W12Dialog>
  </div>
</template>

<style scoped>
.filters { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-bottom: 16px; padding-bottom: 16px; border-bottom: 1px solid var(--hr); }
.logs-err { color: #d13438; margin-bottom: 8px; }
.flex1 { flex: 1; }
.sub { font-size: 12px; color: var(--text2); }
.warn { color: #d97706; }
.kw { max-width: 200px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.result-tag { margin-inline-end: 0; }
.category-tags { display: flex; gap: 4px; flex-wrap: wrap; max-width: 240px; }
.category-tags :deep(.ant-tag) { margin: 0; }
@media (max-width: 600px) {
  .filters > :deep(.ant-select), .filters > :deep(.ant-input), .filters > :deep(.ant-input-affix-wrapper) { width: 100% !important; }
}
</style>
