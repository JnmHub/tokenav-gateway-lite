<script setup>
/* ============================================================
 * 错误请求日志面板（旧 UsageView「错误」tab / OpsErrorLogTable 等价，Win12 重构版）
 * 接口：GET /admin/ops/errors（OpsErrorLogFilter）、详情 GET /admin/ops/errors/:id、
 *       标记处理 PUT /admin/ops/errors/:id/resolve
 * 筛选参数契约（internal/handler/admin/ops_handler.go 实测）：
 *  - 后端不解析 severity：旧实现传 P0~P3 属假筛选（静默不过滤），已改为真实生效的
 *    category 分类参数（applyOpsErrorCategoryFilter → service.CategoryToFilter，
 *    分类码：auth/rate_limit/quota/invalid_request/cyber/upstream/service_unavailable/internal）；
 *  - 行数据自身的 severity（P0~P3）照常展示，只是不再作为筛选参数传给后端。
 * ============================================================ */
import { ref, onMounted } from 'vue'
import { ReloadOutlined } from '@ant-design/icons-vue'
import { message } from 'ant-design-vue'
import { request } from '../../api'
import { fmtTime } from './usageFormat'

const errRows = ref([])
const errLoading = ref(false)
const errPage = ref(1)
const errPageSize = 20
const errTotal = ref(0)
const errCategory = ref('')
const errOwner = ref('')
const errResolved = ref('')
const errColumns = [
  { title: '时间', key: 'time', width: 150 },
  { title: '级别', key: 'severity', width: 70 },
  { title: '状态码', key: 'status_code', width: 80 },
  { title: '类型', dataIndex: 'type', width: 120, ellipsis: true },
  { title: '平台 / 模型', key: 'model', width: 150, ellipsis: true },
  { title: '账号 / 用户', key: 'who', width: 170, ellipsis: true },
  { title: '错误信息', key: 'message', ellipsis: true },
  { title: '状态', key: 'resolved', width: 80 },
  { title: '操作', key: 'action', width: 140 },
]
/* 分类选项与后端 CategoryToFilter 一一对应；空值 = 不过滤，
 * 未知分类后端等价不过滤（CategoryToFilter default 分支），不会整表查不出数据 */
const CATEGORY_OPTS = [
  { value: '', label: '全部分类' },
  { value: 'auth', label: '鉴权失败' },
  { value: 'rate_limit', label: '速率限制' },
  { value: 'quota', label: '配额 / 订阅' },
  { value: 'invalid_request', label: '无效请求' },
  { value: 'cyber', label: 'Cyber 拦截' },
  { value: 'upstream', label: '上游错误' },
  { value: 'service_unavailable', label: '服务不可用' },
  { value: 'internal', label: '平台内部' },
]
const loadErrors = async () => {
  errLoading.value = true
  try {
    const params = { page: errPage.value, page_size: errPageSize }
    // 真实生效的分类筛选（后端 applyOpsErrorCategoryFilter）；severity 已废弃——后端不解析
    if (errCategory.value) params.category = errCategory.value
    if (errOwner.value) params.error_owner = errOwner.value
    if (errResolved.value !== '') params.resolved = errResolved.value
    const res = await request('/admin/ops/errors', { method: 'GET', query: params })
    errRows.value = res?.items || []
    errTotal.value = Number(res?.total || 0)
  } catch (e) { message.error(`错误日志加载失败：${e.message}`) } finally { errLoading.value = false }
}
const onErrPage = (p) => { errPage.value = p; loadErrors() }
const errDetailOpen = ref(false)
const errDetail = ref(null)
const openErrDetail = async (row) => {
  errDetail.value = row
  errDetailOpen.value = true
  try {
    // 详情接口取全量字段（列表行缺 resolved_by 等时以详情回填）
    const full = await request(`/admin/ops/errors/${row.id}`, { method: 'GET' })
    if (full) errDetail.value = full
  } catch { /* 列表行数据兜底展示 */ }
}
const resolveErr = async (row) => {
  try {
    await request(`/admin/ops/errors/${row.id}/resolve`, { method: 'PUT', body: { resolved: true } })
    message.success('已标记为处理')
    row.resolved = true
    if (errDetail.value?.id === row.id) errDetail.value.resolved = true
    await loadErrors()
  } catch (e) { message.error(`标记失败：${e.message}`) }
}
onMounted(loadErrors)
</script>

<template>
  <div class="panel err-panel">
    <div class="err-head">
      <h3>错误请求日志</h3>
      <div class="err-filters">
        <a-select v-model:value="errCategory" style="width: 130px" size="small" :options="CATEGORY_OPTS" />
        <a-select v-model:value="errOwner" style="width: 120px" size="small"
          :options="[{ value: '', label: '全部归属' }, { value: 'client', label: '客户端' }, { value: 'provider', label: '上游' }, { value: 'platform', label: '平台' }]" />
        <a-select v-model:value="errResolved" style="width: 110px" size="small"
          :options="[{ value: '', label: '全部状态' }, { value: 'false', label: '未处理' }, { value: 'true', label: '已处理' }]" />
        <a-button size="small" :loading="errLoading" @click="loadErrors"><ReloadOutlined /> 刷新</a-button>
      </div>
    </div>
    <a-table
      :columns="errColumns" :data-source="errRows" :loading="errLoading" row-key="id" size="small" :scroll="{ x: 'max-content' }"
      :pagination="{ current: errPage, pageSize: errPageSize, total: errTotal, showSizeChanger: false, onChange: onErrPage }"
    >
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'time'">{{ fmtTime(record.created_at) }}</template>
        <template v-else-if="column.key === 'severity'">
          <a-tag :color="{ P0: 'red', P1: 'orange', P2: 'blue', P3: 'default' }[record.severity] || 'default'">{{ record.severity }}</a-tag>
        </template>
        <template v-else-if="column.key === 'status_code'">
          <a-tag :color="(record.status_code || 0) >= 500 ? 'red' : (record.status_code || 0) >= 400 ? 'orange' : 'default'">{{ record.status_code || '-' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'model'">{{ record.model || record.platform || '-' }}</template>
        <template v-else-if="column.key === 'who'">{{ record.account_name || record.user_email || '-' }}</template>
        <template v-else-if="column.key === 'message'">
          <span class="err-msg" :title="record.message">{{ record.message }}</span>
        </template>
        <template v-else-if="column.key === 'resolved'">
          <a-tag :color="record.resolved ? 'green' : 'red'">{{ record.resolved ? '已处理' : '未处理' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'action'">
          <a-button size="small" type="text" @click="openErrDetail(record)">详情</a-button>
          <a-button v-if="!record.resolved" size="small" type="text" @click="resolveErr(record)">标记处理</a-button>
        </template>
      </template>
      <template #empty><span class="empty">暂无错误记录</span></template>
    </a-table>

    <!-- 错误详情弹窗 -->
    <a-modal v-model:open="errDetailOpen" title="错误详情" :footer="null" width="640px">
      <div v-if="errDetail" class="err-detail">
        <div class="drow"><span>时间</span><span>{{ fmtTime(errDetail.created_at) }}</span></div>
        <div class="drow"><span>级别 / 状态码</span><span>{{ errDetail.severity }} · {{ errDetail.status_code || '-' }}</span></div>
        <div class="drow"><span>类型 / 归属</span><span>{{ errDetail.type }} · {{ errDetail.error_owner }} · {{ errDetail.error_source }}</span></div>
        <div class="drow"><span>平台 / 模型</span><span>{{ errDetail.platform || '-' }} / {{ errDetail.model || '-' }}</span></div>
        <div class="drow"><span>账号 / 用户</span><span>{{ errDetail.account_name || '-' }} / {{ errDetail.user_email || '-' }}</span></div>
        <div class="drow"><span>API Key</span><span>{{ errDetail.api_key_name || '-' }}{{ errDetail.api_key_deleted ? '（已删除）' : '' }}</span></div>
        <div class="drow"><span>请求 ID</span><span class="mono">{{ errDetail.request_id || '-' }}</span></div>
        <div class="drow"><span>客户端请求 ID</span><span class="mono">{{ errDetail.client_request_id || '-' }}</span></div>
        <div class="drow" style="align-items: flex-start"><span style="flex: none; padding-top: 2px">错误信息</span><span class="mono" style="word-break: break-all">{{ errDetail.message }}</span></div>
        <div class="drow"><span>处理状态</span>
          <span>
            <a-tag :color="errDetail.resolved ? 'green' : 'red'">{{ errDetail.resolved ? '已处理' : '未处理' }}</a-tag>
            <a-button v-if="!errDetail.resolved" size="small" @click="resolveErr(errDetail)">标记为已处理</a-button>
          </span>
        </div>
      </div>
    </a-modal>
  </div>
</template>

<style scoped>
.err-panel { margin-top: 12px; }
.err-head { display: flex; align-items: center; justify-content: space-between; gap: 10px; flex-wrap: wrap; }
.err-filters { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.err-msg { display: inline-block; max-width: 420px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; vertical-align: bottom; }
.err-detail { display: flex; flex-direction: column; gap: 8px; }
.err-detail .drow { display: flex; gap: 12px; font-size: 13px; }
.err-detail .drow > span:first-child { flex: none; width: 110px; color: var(--text2); }
.mono { font-family: ui-monospace, monospace; font-size: 12px; }
.empty { font-size: 12px; color: var(--text2); }
</style>
