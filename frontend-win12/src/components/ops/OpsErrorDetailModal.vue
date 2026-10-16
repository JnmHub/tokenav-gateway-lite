<script setup>
/* 错误详情弹窗（三级下钻·第二级）
 * 契约来源：旧 OpsErrorDetailModal.vue + utils/errorDetailResponse.ts：
 * - GET /admin/ops/request-errors/:id、GET /admin/ops/upstream-errors/:id → OpsErrorDetail（40 字段）
 * - 请求↔上游关联：GET /admin/ops/request-errors/:id/upstream-errors?include_detail=1&view=all&page_size=100
 * - 响应体取值：请求错误优先 error_body（若为通用网关包装则展示上游负载）；
 *   上游错误优先 upstream_error_detail > upstream_errors > upstream_error_message
 * - 字段摘要 15+ 项：request_id/client_request_id/时间/用户/账号/平台/分组/模型映射/
 *   入站端点/上游端点/状态码/请求类型/消息/Key 前缀/各阶段延迟/流式标记
 * - 字段补齐（internal/service/ops_models.go OpsErrorLogDetail 核实）：
 *   upstream_status_code（上游真实状态码，卡片与状态码并排）、
 *   api_key_prefix（在用 Key 前缀）/ attempted_key_prefix（尝试 Key 前缀）/
 *   deleted_key_owner_user_id/email + deleted_key_name（被删 Key 原主）/ resolved_by_user_name（解决人）/
 *   api_key_deleted（在用 Key 被删标记，OpsErrorLog）；
 *   attempted 与在用前缀不同、或存在被删 Key 原主 → 低饱和橙红警示卡（参照旧 keyDeletedBadge） */
import { ref, computed, watch } from 'vue'
import { message } from 'ant-design-vue'
import { request } from '../../api'

const props = defineProps({
  open: { type: Boolean, default: false },
  errorId: { type: Number, default: null },
  errorType: { type: String, default: 'request' },
})
const emit = defineEmits(['update:open'])

const loading = ref(false)
const detail = ref(null)
const upstreamList = ref([])
const upstreamLoading = ref(false)
const expandedUp = ref(new Set())

const fmtTime = (t) => (t ? String(t).replace('T', ' ').slice(0, 19) : '-')
const orDash = (v) => (v === null || v === undefined || v === '' ? '—' : v)

/* 后端以 0/1 int 落库，JSON 化后可能是布尔或数字：truthy 归一化，仅非空值参与判断 */
const truthy = (v) => v === true || v === 1 || v === '1'

const reqTypeLabel = (t) => ({ 1: '同步', 2: '流式', 3: 'WebSocket' }[t] || '未知')
const hasMapping = (d) => !!(String(d?.requested_model || '').trim() && String(d?.upstream_model || '').trim() && d.requested_model !== d.upstream_model)
const codeColor = (c) => (c >= 500 ? 'red' : c === 429 ? 'purple' : c >= 400 ? 'orange' : 'default')

/* Key 前缀脱敏缩写：剥掉 sk- 后缩写尾 4 位（sk-***abcd），无值回退 ''（参照旧 keyName） */
const fmtPrefix = (p) => {
  const s = String(p || '').trim()
  if (!s) return ''
  return s.startsWith('sk-') ? `sk-***${s.slice(-4)}` : s
}
/* 警示条件：尝试 Key 前缀与在用前缀不同，或存在被删 Key 原主（旧 keyDeletedBadge 语义） */
const keyPrefixMismatch = computed(() => {
  const d = detail.value
  if (!d) return false
  const bound = fmtPrefix(d.api_key_prefix)
  const attempted = fmtPrefix(d.attempted_key_prefix)
  return !!bound && !!attempted && bound !== attempted
})
const hasDeletedOwner = computed(() => {
  const d = detail.value
  return !!(d && (d.deleted_key_owner_user_id != null || String(d.deleted_key_owner_email || '').trim()))
})
/* 已删 Key 原主展示：邮箱 + (Key 名称 + #user_id)，纯文本避免插槽嵌套 */
const deletedOwnerText = computed(() => {
  const d = detail.value
  if (!d) return ''
  const email = String(d.deleted_key_owner_email || '').trim() || (d.deleted_key_owner_user_id != null ? `#${d.deleted_key_owner_user_id}` : '')
  if (!email) return ''
  const parts = [email]
  if (d.deleted_key_name) parts.push(`Key「${d.deleted_key_name}」`)
  if (d.deleted_key_owner_user_id != null) parts.push(`#${d.deleted_key_owner_user_id}`)
  return parts.join(' · ')
})

/* 与旧 errorDetailResponse.ts 同逻辑的响应体解析 */
const GENERIC_MSGS = new Set(['upstream request failed', 'upstream request failed after retries', 'upstream gateway error', 'upstream service temporarily unavailable'])
const upstreamPayload = (d) => {
  if (!d) return ''
  for (const c of [d.upstream_error_detail, d.upstream_errors, d.upstream_error_message]) {
    const s = String(c || '').trim()
    if (!s || s === '[]' || s === '{}' || s.toLowerCase() === 'null') continue
    return s
  }
  return ''
}
const isGenericWrapper = (raw) => {
  try {
    const err = JSON.parse(String(raw || ''))?.error
    return err?.type === 'upstream_error' && GENERIC_MSGS.has(String(err?.message || '').toLowerCase())
  } catch { return false }
}
const primaryBody = computed(() => {
  const d = detail.value
  if (!d) return ''
  const up = upstreamPayload(d)
  const body = String(d.error_body || '').trim()
  if (props.errorType === 'upstream') return up || body
  if (!body) return up
  if (up && isGenericWrapper(body)) return up
  return body
})
const prettyJSON = (raw) => {
  if (!raw) return 'N/A'
  try { return JSON.stringify(JSON.parse(raw), null, 2) } catch { return raw }
}

const load = async () => {
  if (!props.errorId) return
  loading.value = true
  expandedUp.value = new Set()
  try {
    const path = props.errorType === 'upstream'
      ? `/admin/ops/upstream-errors/${props.errorId}`
      : `/admin/ops/request-errors/${props.errorId}`
    detail.value = await request(path, { method: 'GET' })
  } catch (e) {
    detail.value = null
    message.error(`错误详情加载失败：${e.message}`)
  } finally { loading.value = false }
  // 请求错误才拉关联上游错误列表（与旧实现一致）
  if (props.errorType === 'request') {
    upstreamLoading.value = true
    try {
      const data = await request(`/admin/ops/request-errors/${props.errorId}/upstream-errors`, {
        method: 'GET', query: { page: 1, page_size: 100, view: 'all', include_detail: '1' },
      })
      upstreamList.value = data.items || []
    } catch { upstreamList.value = [] } finally { upstreamLoading.value = false }
  } else {
    upstreamList.value = []
  }
}

watch(() => [props.open, props.errorId], ([o]) => { if (o) load(); else { detail.value = null; upstreamList.value = [] } })

const toggleUp = (id) => {
  const next = new Set(expandedUp.value)
  next.has(id) ? next.delete(id) : next.add(id)
  expandedUp.value = next
}
const upPayload = (ev) => upstreamPayload(ev) || String(ev?.error_body || '').trim()

const SUMMARY = computed(() => {
  const d = detail.value
  if (!d) return []
  return [
    ['request_id', d.request_id, true],
    ['client_request_id', d.client_request_id, true],
    ['时间', fmtTime(d.created_at)],
    ['用户', d.user_email || (d.user_id != null ? `#${d.user_id}` : null)],
    ['账号', d.account_name || (d.account_id != null ? `#${d.account_id}` : null)],
    ['分组', d.group_name || (d.group_id != null ? `#${d.group_id}` : null)],
    ['API Key', d.api_key_name || (d.api_key_id != null ? `#${d.api_key_id}` : null), false, truthy(d.api_key_deleted) ? '（已删除）' : ''],
    ['在用 Key 前缀', fmtPrefix(d.api_key_prefix), true],
    ['尝试 Key 前缀', fmtPrefix(d.attempted_key_prefix), true],
    ['平台', d.platform],
    ['入站端点', d.inbound_endpoint, true],
    ['上游端点', d.upstream_endpoint, true],
    ['请求路径', d.request_path, true],
    ['severity', d.severity],
    ['error_owner', d.error_owner],
    ['error_source', d.error_source],
    ['来源 IP', (d.client_ip || '').replace('/32', '')],
    ['user_agent', d.user_agent],
    ['auth 延迟', d.auth_latency_ms != null ? `${d.auth_latency_ms} ms` : null],
    ['routing 延迟', d.routing_latency_ms != null ? `${d.routing_latency_ms} ms` : null],
    ['upstream 延迟', d.upstream_latency_ms != null ? `${d.upstream_latency_ms} ms` : null],
    ['response 延迟', d.response_latency_ms != null ? `${d.response_latency_ms} ms` : null],
    ['TTFT', d.time_to_first_token_ms != null ? `${d.time_to_first_token_ms} ms` : null],
    ['业务限流', typeof d.is_business_limited === 'boolean' ? (d.is_business_limited ? '是' : '否') : null],
    ['流式', typeof d.stream === 'boolean' ? (d.stream ? '是' : '否') : null],
    // 解决人与解决时间并排（旧 classificationKeys.resolvedBy「解决人」）
    ['已解决', typeof d.resolved === 'boolean' ? (d.resolved ? `是（${fmtTime(d.resolved_at)}${d.resolved_by_user_name ? ' · 解决人 ' + d.resolved_by_user_name : ''}）` : '否') : null],
  ].filter(([, v]) => v !== null && v !== undefined)
})
</script>

<template>
  <a-modal
    :open="open" width="920px" style="top: 40px" :footer="null"
    :title="`错误详情 #${errorId ?? ''}`" @cancel="emit('update:open', false)"
  >
    <a-spin :spinning="loading">
      <template v-if="detail">
        <!-- 核心摘要 -->
        <div class="sum-grid">
          <div class="sum-card"><div class="sk">request_id</div><div class="sv mono">{{ orDash(detail.request_id) }}</div></div>
          <div class="sum-card"><div class="sk">时间</div><div class="sv">{{ fmtTime(detail.created_at) }}</div></div>
          <div class="sum-card"><div class="sk">用户</div><div class="sv">{{ detail.user_email || orDash(detail.user_id) }}</div></div>
          <div class="sum-card"><div class="sk">平台</div><div class="sv">{{ orDash(detail.platform) }}</div></div>
          <div class="sum-card"><div class="sk">分组</div><div class="sv">{{ detail.group_name || orDash(detail.group_id) }}</div></div>
          <div class="sum-card">
            <div class="sk">模型</div>
            <div class="sv mono">
              <template v-if="hasMapping(detail)">{{ detail.requested_model }} → {{ detail.upstream_model }}</template>
              <template v-else>{{ detail.upstream_model || detail.requested_model || detail.model || '—' }}</template>
            </div>
          </div>
          <div class="sum-card"><div class="sk">入站端点</div><div class="sv mono">{{ orDash(detail.inbound_endpoint) }}</div></div>
          <div class="sum-card"><div class="sk">上游端点</div><div class="sv mono">{{ orDash(detail.upstream_endpoint) }}</div></div>
          <div class="sum-card">
            <div class="sk">状态码 / 类型</div>
            <div class="sv">
              <a-tag :color="codeColor(detail.status_code)">{{ detail.status_code }}</a-tag>
              <template v-if="detail.upstream_status_code != null">
                <span class="up-code">上游</span><a-tag :color="codeColor(detail.upstream_status_code)">{{ detail.upstream_status_code }}</a-tag>
              </template>
              <span>{{ reqTypeLabel(detail.request_type) }}</span>
              <a-tag v-if="detail.stream" style="margin-left: 6px">流式</a-tag>
              <a-tag v-if="detail.severity" style="margin-left: 6px">{{ detail.severity }}</a-tag>
            </div>
          </div>
          <div class="sum-card wide"><div class="sk">消息</div><div class="sv">{{ orDash(detail.message) }}<a-tag v-if="truthy(detail.api_key_deleted)" class="del-tag">Key 已删除</a-tag></div></div>

          <!-- Key 上下文：前缀不一致或存在被删 Key 原主 → 低饱和橙红警示（旧 keyDeletedBadge 语义，禁 emoji） -->
          <div v-if="keyPrefixMismatch || hasDeletedOwner" class="sum-card wide warn-card">
            <div class="sk warn-sk">Key 上下文警示</div>
            <div class="sv warn-lines">
              <span v-if="keyPrefixMismatch">尝试 Key 前缀（{{ fmtPrefix(detail.attempted_key_prefix) }}）与在用 Key 前缀（{{ fmtPrefix(detail.api_key_prefix) }}）不一致</span>
              <span v-if="hasDeletedOwner">已删除 Key 原主：{{ deletedOwnerText }}<a-tag class="del-tag">Key 已删除</a-tag></span>
            </div>
          </div>
        </div>

        <!-- 全字段摘要 -->
        <div class="sec">字段明细</div>
        <div class="kv">
          <div v-for="[k, v, mono] in SUMMARY" :key="k" class="kv-row">
            <span class="k">{{ k }}</span>
            <span class="v" :class="{ mono }">{{ v }}</span>
          </div>
        </div>

        <!-- 响应体 JSON 美化 -->
        <div class="sec">响应内容</div>
        <pre class="json">{{ prettyJSON(primaryBody) }}</pre>

        <!-- 请求错误 → 关联上游错误 -->
        <template v-if="errorType === 'request'">
          <div class="sec">关联上游错误（{{ upstreamList.length }}）</div>
          <div v-if="!upstreamLoading && !upstreamList.length" class="empty">无关联上游错误记录</div>
          <div v-for="(ev, i) in upstreamList" :key="ev.id" class="up-item">
            <div class="up-head">
              <span>#{{ i + 1 }}</span>
              <a-tag v-if="ev.type" style="margin: 0">{{ ev.type }}</a-tag>
              <a-tag :color="codeColor(ev.status_code)" style="margin: 0">{{ ev.status_code ?? '—' }}</a-tag>
              <span class="up-time">{{ fmtTime(ev.created_at) }}</span>
              <a style="margin-left: auto; font-size: 12px" @click="toggleUp(ev.id)">
                {{ expandedUp.has(ev.id) ? '收起响应' : '展开响应' }}
              </a>
            </div>
            <div class="up-msg">{{ orDash(ev.message) }}</div>
            <pre v-if="expandedUp.has(ev.id)" class="json small">{{ prettyJSON(upPayload(ev)) }}</pre>
          </div>
        </template>
      </template>
      <div v-else-if="!loading" class="empty">未选择错误</div>
    </a-spin>
  </a-modal>
</template>

<style scoped>
.sum-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 8px; }
.sum-card { padding: 8px 12px; border-radius: 8px; background: var(--card); border: 1px solid var(--hr); min-width: 0; }
.sum-card.wide { grid-column: 1 / -1; }
/* Key 上下文警示卡：低饱和橙红半透明底 + 同系描边（Win12 禁高饱和实色，深浅双套） */
.warn-card { background: rgba(217, 119, 6, 0.08); border-color: rgba(217, 119, 6, 0.3); }
:root.dark .warn-card { background: rgba(217, 119, 6, 0.14); border-color: rgba(217, 119, 6, 0.4); }
.warn-sk, .warn-lines span { color: #b45309; }
:root.dark .warn-sk, :root.dark .warn-lines span { color: #f59e0b; }
.warn-lines { display: flex; flex-direction: column; gap: 4px; }
/* 「Key 已删除」标记：状态标签 4px 圆角、低饱和红半透明底 */
.del-tag { margin-left: 8px; border-radius: 4px; font-size: 11px; color: #dc2626; background: rgba(220, 38, 38, 0.08); border-color: rgba(220, 38, 38, 0.3); }
:root.dark .del-tag { color: #f87171; background: rgba(220, 38, 38, 0.14); border-color: rgba(220, 38, 38, 0.4); }
.up-code { font-size: 11px; color: var(--text2); margin-right: 2px; }
.sk { font-size: 11px; color: var(--text2); }
.sv { margin-top: 2px; font-size: 13px; word-break: break-all; }
.mono { font-family: Consolas, monospace; font-size: 12px; }
.sec { margin: 14px 0 6px; font-size: 13px; font-weight: 500; }
.kv { border: 1px solid var(--hr); border-radius: 8px; overflow: hidden; }
.kv-row { display: flex; padding: 5px 12px; font-size: 12px; border-top: 1px solid var(--hr); }
.kv-row:first-child { border-top: none; }
.kv-row:nth-child(odd) { background: rgba(128,128,128,.04); }
.kv-row .k { width: 150px; flex-shrink: 0; color: var(--text2); }
.kv-row .v { flex: 1; word-break: break-all; }
.json { margin: 0; padding: 10px 12px; max-height: 320px; overflow: auto; border: 1px solid var(--hr); border-radius: 8px; background: rgba(128,128,128,.05); font-family: Consolas, monospace; font-size: 12px; line-height: 1.5; white-space: pre-wrap; word-break: break-all; }
.json.small { max-height: 220px; }
.up-item { padding: 8px 12px; border: 1px solid var(--hr); border-radius: 8px; margin-bottom: 8px; }
.up-head { display: flex; align-items: center; gap: 8px; font-size: 12px; }
.up-time { color: var(--text2); font-size: 11px; }
.up-msg { margin-top: 4px; font-size: 12px; word-break: break-all; }
.empty { padding: 24px 0; text-align: center; font-size: 13px; color: var(--text2); }
</style>
