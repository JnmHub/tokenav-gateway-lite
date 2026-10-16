<script setup>
/* 入口拒绝（ingress reject）统计卡：官方 v0.2.5 对齐
 * 契约来源：internal/handler/admin/ops_ingress_reject_handler.go + internal/service/ops_ingress_reject.go
 * - GET /admin/ops/ingress-rejects?start_time&end_time&page&page_size&reason&route_family&protocol
 *   响应：{ items[{ bucket_start, reject_reason, route_family, protocol, client_ip,
 *          user_id, api_key_id, request_count, first_seen, last_seen }], total, page, page_size }
 * - GET /admin/ops/ingress-rejects/health
 *   响应：{ cardinality, capacity, pending_batches, pending_rows, overflowed_count,
 *          dropped_count, flushed_request_count, flush_failure_count, accepting, last_error }
 * 时间参数为 RFC3339；不传时后端默认近 1 小时。
 */
import { ref, computed, watch } from 'vue'
import { message } from 'ant-design-vue'
import { ReloadOutlined } from '@ant-design/icons-vue'
import { request } from '../../api'

const loading = ref(false)
const list = ref([])
const total = ref(0)
const health = ref(null)

const page = ref(1)
const pageSize = ref(20)
const timeRange = ref('1h')
const reason = ref('')

const TR_OPTS = [
  { value: '1h', label: '近1小时' },
  { value: '6h', label: '近6小时' },
  { value: '24h', label: '近24小时' },
  { value: '7d', label: '近7天' },
]
const TR_MS = { '1h': 3600e3, '6h': 6 * 3600e3, '24h': 24 * 3600e3, '7d': 7 * 24 * 3600e3 }
// 拒绝原因筛选：与后端 ingressRejectReasons 白名单一致
const REASON_OPTS = [
  { value: '', label: '全部原因' },
  { value: 'invalid_api_key', label: '无效 API Key' },
  { value: 'api_key_required', label: '缺少 API Key' },
  { value: 'api_key_disabled', label: 'API Key 已禁用' },
  { value: 'invalid_auth_rate_limited', label: '鉴权限流' },
  { value: 'api_key_auth_overloaded', label: '鉴权过载' },
  { value: 'ip_restricted', label: 'IP 受限' },
  { value: 'user_inactive', label: '用户未激活' },
  { value: 'group_deleted', label: '分组已删除' },
  { value: 'group_disabled', label: '分组已停用' },
  { value: 'group_not_allowed', label: '分组不允许' },
  { value: 'group_unassigned', label: '未分配分组' },
  { value: 'query_api_key_deprecated', label: '查询串 Key 已弃用' },
  { value: 'other', label: '其他' },
]
const totalPages = computed(() => Math.max(1, Math.ceil(total.value / (pageSize.value || 20))))

const buildParams = () => {
  const end = new Date()
  const start = new Date(end.getTime() - (TR_MS[timeRange.value] || TR_MS['1h']))
  const params = { start_time: start.toISOString(), end_time: end.toISOString(), page: page.value, page_size: pageSize.value }
  if (reason.value) params.reason = reason.value
  return params
}

const loadHealth = async () => {
  try { health.value = await request('/admin/ops/ingress-rejects/health', { method: 'GET' }) }
  catch (e) { health.value = null }
}

const loadData = async () => {
  loading.value = true
  try {
    const data = await request('/admin/ops/ingress-rejects', { method: 'GET', query: buildParams() })
    list.value = data?.items ?? []
    total.value = data?.total ?? 0
    loadHealth()
  } catch (e) { list.value = []; total.value = 0; message.error(`加载入口拒绝统计失败：${e.message}`) }
  finally { loading.value = false }
}

watch(
  () => ({ tr: timeRange.value, reason: reason.value }),
  () => { if (page.value !== 1) { page.value = 1 } else { loadData() } },
  { immediate: true }, // 挂载即拉取首屏数据
)
watch(() => page.value, loadData)

const fmtInt = (v) => (typeof v === 'number' && Number.isFinite(v) ? Math.round(v).toLocaleString('zh-CN') : '-')
const fmtTime = (v) => {
  if (!v) return '-'
  const d = new Date(v)
  return Number.isNaN(d.getTime()) ? '-' : d.toLocaleString('zh-CN', { hour12: false })
}
const fmtIp = (v) => (v ? String(v).replace(/\/\d+$/, '') : '-')
const reasonText = (v) => REASON_OPTS.find((o) => o.value === v)?.label || v || '-'

/* 行 key：接口 items 无 id 字段，用业务字段组合生成稳定 key
 * （时间片+原因+路由族+协议+IP+用户+Key 即后端聚合的唯一维度） */
const rowKey = (r) =>
  [r.bucket_start, r.reject_reason, r.route_family, r.protocol, r.client_ip, r.user_id ?? '', r.api_key_id ?? ''].join('|')

const columns = [
  { title: '时间片', key: 'bucket', width: 160 },
  { title: '拒绝原因', key: 'reason', width: 140 },
  { title: '路由族', dataIndex: 'route_family', key: 'family', width: 130 },
  { title: '协议', dataIndex: 'protocol', key: 'protocol', width: 100 },
  { title: '客户端 IP', key: 'ip', width: 140 },
  { title: '用户 ID', key: 'uid', width: 90 },
  { title: 'API Key ID', key: 'kid', width: 100 },
  { title: '请求数', key: 'count', width: 90 },
]
</script>

<template>
  <div class="card">
    <div class="card-h">
      <div>
        <div class="sec-t">入口拒绝统计</div>
        <div class="sub-t">鉴权与入口侧被拒绝请求的聚合视图（安全审计用，不含请求内容）</div>
      </div>
      <div class="h-btns">
        <a-select v-model:value="timeRange" :options="TR_OPTS" size="small" style="width: 104px" />
        <a-select v-model:value="reason" :options="REASON_OPTS" size="small" style="width: 150px" />
        <a-button size="small" :loading="loading" @click="loadData"><ReloadOutlined />刷新</a-button>
      </div>
    </div>

    <!-- 采集健康状态：低饱和状态标签 + 关键计数 -->
    <div class="health-bar">
      <span class="tag" :class="health?.accepting ? 'ok' : 'bad'">
        {{ health ? (health.accepting ? '采集中' : '采集未启用') : '健康状态未知' }}
      </span>
      <template v-if="health">
        <span class="dim">条目 {{ health.cardinality }}/{{ health.capacity }}</span>
        <span class="dim">待落库 {{ health.pending_rows }}</span>
        <span class="dim">已落库 {{ fmtInt(health.flushed_request_count) }}</span>
        <span class="dim" :class="{ warn: health.dropped_count > 0 || health.overflowed_count > 0 }">
          丢弃 {{ fmtInt(health.dropped_count) }} / 溢出 {{ fmtInt(health.overflowed_count) }}
        </span>
        <span v-if="health.last_error" class="dim warn" :title="health.last_error">最近错误</span>
      </template>
    </div>

    <a-spin :spinning="loading">
      <div v-if="!loading && list.length === 0" class="empty">当前筛选条件下暂无入口拒绝记录</div>
      <a-table
        v-else :columns="columns" :data-source="list" :row-key="rowKey" size="small"
        :scroll="{ x: 'max-content' }" :pagination="false"
      >
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'bucket'">{{ fmtTime(record.bucket_start) }}</template>
          <template v-else-if="column.key === 'reason'">
            <span class="tag warn" style="height: 20px">{{ reasonText(record.reject_reason) }}</span>
          </template>
          <template v-else-if="column.key === 'ip'">{{ fmtIp(record.client_ip) }}</template>
          <template v-else-if="column.key === 'uid'">{{ record.user_id ?? '-' }}</template>
          <template v-else-if="column.key === 'kid'">{{ record.api_key_id ?? '-' }}</template>
          <template v-else-if="column.key === 'count'">{{ fmtInt(record.request_count) }}</template>
        </template>
      </a-table>
      <div v-if="total > 0" class="pager">
        <a-button size="small" :disabled="loading || page <= 1" @click="page -= 1">上一页</a-button>
        <a-button size="small" :disabled="loading || page >= totalPages" @click="page += 1">下一页</a-button>
        <span class="dim">第 {{ page }}/{{ totalPages }} 页 · 共 {{ fmtInt(total) }} 条</span>
      </div>
    </a-spin>
  </div>
</template>

<style scoped>
.card { background: var(--card); border: 1px solid var(--hr); border-radius: 8px; padding: 12px 14px; min-width: 0; }
.card-h { display: flex; align-items: flex-start; justify-content: space-between; gap: 10px; margin-bottom: 10px; flex-wrap: wrap; }
.sec-t { font-size: 13px; font-weight: 500; }
.sub-t { font-size: 11px; color: var(--text2); margin-top: 2px; }
.h-btns { display: flex; gap: 8px; flex-wrap: wrap; align-items: center; }
.health-bar { display: flex; gap: 12px; align-items: center; flex-wrap: wrap; margin-bottom: 8px; }
.dim { font-size: 11px; color: var(--text2); }
.dim.warn { color: #b45309; }
.tag { display: inline-flex; align-items: center; padding: 0 8px; height: 20px; border-radius: 4px; font-size: 11px; }
.tag.ok { background: rgba(34, 197, 94, 0.14); color: #15803d; }
.tag.bad { background: rgba(239, 68, 68, 0.12); color: #b91c1c; }
.tag.warn { background: rgba(245, 158, 11, 0.14); color: #b45309; }
.empty { padding: 24px 0; text-align: center; font-size: 12px; color: var(--text2); }
.pager { display: flex; gap: 8px; align-items: center; margin-top: 8px; }
</style>
