<script setup>
/* 告警事件卡（二期）：4 筛选 + 滚动加载 + 详情弹窗（静默/手动解决/查看规则/同规则历史）
 * 契约来源：旧 OpsAlertEventsCard.vue + frontend/src/api/admin/ops.ts
 * - GET /admin/ops/alert-events?limit&time_range&severity&status&email_sent
 *   &before_fired_at&before_id&platform&group_id（游标滚动加载，PAGE_SIZE=10）
 * - GET /admin/ops/alert-events/:id（详情）
 * - PUT /admin/ops/alert-events/:id/status { status: 'manual_resolved' }（手动解决）
 * - POST /admin/ops/alert-silences { rule_id, platform, group_id, region, until, reason }（静默）
 * - 历史记录：limit 20 + time_range(7d/30d) + platform/group_id，前端再过滤同 rule_id + 同维度 */
import { ref, computed, onMounted, watch } from 'vue'
import { message } from 'ant-design-vue'
import { ReloadOutlined, CheckCircleOutlined, StopOutlined } from '@ant-design/icons-vue'
import { request } from '../../api'

const PAGE_SIZE = 10

const loading = ref(false)
const loadingMore = ref(false)
const events = ref([])
const hasMore = ref(true)

/* ---------- 4 项筛选（枚举与旧实现一致） ---------- */
const timeRange = ref('24h')
const TR_OPTS = ['5m', '30m', '1h', '6h', '24h', '7d', '30d'].map((v) => ({ value: v, label: v }))
const severity = ref('')
const SEV_OPTS = [{ value: '', label: '全部' }, ...['P0', 'P1', 'P2', 'P3'].map((v) => ({ value: v, label: v }))]
const status = ref('')
const ST_OPTS = [
  { value: '', label: '全部' },
  { value: 'firing', label: '告警中' },
  { value: 'resolved', label: '已恢复' },
  { value: 'manual_resolved', label: '手动已解决' },
]
const emailSent = ref('')
const MAIL_OPTS = [{ value: '', label: '全部' }, { value: 'true', label: '已发送' }, { value: 'false', label: '已忽略' }]

const buildQuery = (overrides = {}) => {
  const q = { limit: PAGE_SIZE, time_range: timeRange.value }
  if (severity.value) q.severity = severity.value
  if (status.value) q.status = status.value
  if (emailSent.value === 'true') q.email_sent = true
  if (emailSent.value === 'false') q.email_sent = false
  return { ...q, ...overrides }
}

/* ---------- 列表 + 滚动加载（游标：上一页最后一条的 fired_at + id） ---------- */
const loadFirstPage = async () => {
  loading.value = true
  try {
    const data = await request('/admin/ops/alert-events', { method: 'GET', query: buildQuery() })
    events.value = data || []
    hasMore.value = (data || []).length === PAGE_SIZE
  } catch (e) { message.error(`加载告警事件失败：${e.message}`); events.value = []; hasMore.value = false }
  finally { loading.value = false }
}
const loadMore = async () => {
  if (loadingMore.value || loading.value || !hasMore.value) return
  const last = events.value[events.value.length - 1]
  if (!last) return
  loadingMore.value = true
  try {
    const data = await request('/admin/ops/alert-events', {
      method: 'GET',
      query: buildQuery({ before_fired_at: last.fired_at || last.created_at, before_id: last.id }),
    }) || []
    if (!data.length) { hasMore.value = false; return }
    events.value = [...events.value, ...data]
    if (data.length < PAGE_SIZE) hasMore.value = false
  } catch { hasMore.value = false }
  finally { loadingMore.value = false }
}
const onScroll = (e) => {
  const el = e.target
  if (el && el.scrollTop + el.clientHeight >= el.scrollHeight - 120) loadMore()
}

watch([timeRange, severity, status, emailSent], () => { hasMore.value = true; loadFirstPage() })

/* ---------- 展示辅助（与旧实现同语义） ---------- */
const dimStr = (ev, key) => {
  const v = ev?.dimensions?.[key]
  if (v == null) return ''
  return typeof v === 'object' ? '' : String(v)
}
const fmtTime = (t) => (t ? String(t).replace('T', ' ').slice(5, 19) : '-')
const fmtDurMs = (ms) => {
  const sec = Math.floor(Math.max(0, ms) / 1000)
  if (sec < 60) return `${sec}s`
  const min = Math.floor(sec / 60); if (min < 60) return `${min}m`
  const hr = Math.floor(min / 60); if (hr < 24) return `${hr}h`
  return `${Math.floor(hr / 24)}d`
}
const durLabel = (ev) => {
  const fired = new Date(ev.fired_at || ev.created_at)
  if (Number.isNaN(fired.getTime())) return '-'
  if (ev.resolved_at) {
    const r = new Date(ev.resolved_at)
    if (!Number.isNaN(r.getTime())) {
      const prefix = String(ev.status).toLowerCase() === 'manual_resolved' ? '手动已解决' : '已恢复'
      return `${prefix} ${fmtDurMs(r.getTime() - fired.getTime())}`
    }
  }
  return `告警中 ${fmtDurMs(Date.now() - fired.getTime())}`
}
const dimsSummary = (ev) => {
  const parts = []
  if (dimStr(ev, 'platform')) parts.push(`platform=${dimStr(ev, 'platform')}`)
  if (ev.dimensions?.group_id != null && ev.dimensions?.group_id !== '') parts.push(`group_id=${ev.dimensions.group_id}`)
  if (dimStr(ev, 'region')) parts.push(`region=${dimStr(ev, 'region')}`)
  return parts.length ? parts.join(' ') : '-'
}
const sevColor = (s) => ({ P0: 'red', P1: 'orange', P2: 'blue', P3: 'default' }[String(s || '').toUpperCase()] || 'default')
const stColor = (s) => ({ firing: 'red', resolved: 'green', manual_resolved: 'default' }[String(s || '').toLowerCase()] || 'default')
const stLabel = (s) => ({ firing: '告警中', resolved: '已恢复', manual_resolved: '手动已解决' }[String(s || '').toLowerCase()] || String(s || '-').toUpperCase())

const columns = [
  { title: '时间', key: 'time', width: 110 },
  { title: '级别/状态', key: 'sev', width: 150 },
  { title: '平台', key: 'plat', width: 90 },
  { title: '规则', key: 'rule', width: 70 },
  { title: '标题', key: 'title', ellipsis: true },
  { title: '持续时间', key: 'dur', width: 130 },
  { title: '维度', key: 'dims', width: 170, ellipsis: true },
  { title: '邮件', key: 'mail', width: 84 },
]

/* ---------- 详情弹窗 ---------- */
const emit = defineEmits(['view-rule'])
const detailOpen = ref(false)
const selected = ref(null)
const detailLoading = ref(false)
const actionLoading = ref(false)
const history = ref([])
const historyLoading = ref(false)
const historyRange = ref('7d')
const silenceDuration = ref('1h')

const openDetail = async (row) => {
  detailOpen.value = true
  selected.value = row
  detailLoading.value = true
  historyLoading.value = true
  try { selected.value = await request(`/admin/ops/alert-events/${row.id}`, { method: 'GET' }) }
  catch (e) { message.error(`加载告警详情失败：${e.message}`) }
  finally { detailLoading.value = false }
  await loadHistory()
}

/* 同规则历史：按 platform/group_id 服务端过滤后，前端收窄到同 rule_id + 同维度 */
const loadHistory = async () => {
  const ev = selected.value
  if (!ev) { history.value = []; return }
  historyLoading.value = true
  try {
    const platform = dimStr(ev, 'platform')
    const gidRaw = ev.dimensions?.group_id
    const query = { limit: 20, time_range: historyRange.value, status: '' }
    if (platform) query.platform = platform
    if (typeof gidRaw === 'number') query.group_id = gidRaw
    const items = await request('/admin/ops/alert-events', { method: 'GET', query }) || []
    history.value = items.filter((it) => {
      if (it.rule_id !== ev.rule_id) return false
      if ((dimStr(it, 'platform') || '') !== (dimStr(ev, 'platform') || '')) return false
      return (it.dimensions?.group_id ?? null) === (ev.dimensions?.group_id ?? null)
    })
  } catch { history.value = [] }
  finally { historyLoading.value = false }
}
watch(historyRange, () => { if (detailOpen.value) loadHistory() })

/* 静默：POST /admin/ops/alert-silences（until = now + 1h/24h/7d，RFC3339） */
const silenceAlert = async () => {
  const ev = selected.value
  if (!ev || actionLoading.value) return
  actionLoading.value = true
  try {
    const hours = { '1h': 1, '24h': 24, '7d': 24 * 7 }[silenceDuration.value] || 1
    const gidRaw = ev.dimensions?.group_id
    const body = {
      rule_id: ev.rule_id,
      platform: dimStr(ev, 'platform') || '',
      until: new Date(Date.now() + hours * 3600 * 1000).toISOString(),
      reason: `silence from UI (${silenceDuration.value})`,
    }
    if (typeof gidRaw === 'number') body.group_id = gidRaw
    const region = dimStr(ev, 'region')
    if (region) body.region = region
    await request('/admin/ops/alert-silences', { method: 'POST', body })
    message.success(`已静默该告警 ${silenceDuration.value}`)
  } catch (e) { message.error(`静默失败：${e.message}`) }
  finally { actionLoading.value = false }
}

/* 手动解决：PUT /admin/ops/alert-events/:id/status { status: 'manual_resolved' } */
const manualResolve = async () => {
  if (!selected.value || actionLoading.value) return
  actionLoading.value = true
  try {
    await request(`/admin/ops/alert-events/${selected.value.id}/status`, { method: 'PUT', body: { status: 'manual_resolved' } })
    message.success('已标记为手动解决')
    selected.value = await request(`/admin/ops/alert-events/${selected.value.id}`, { method: 'GET' })
    await loadFirstPage(); await loadHistory()
  } catch (e) { message.error(`标记为手动解决失败：${e.message}`) }
  finally { actionLoading.value = false }
}

const viewRule = () => emit('view-rule', selected.value?.rule_id)

onMounted(loadFirstPage)
defineExpose({ refresh: loadFirstPage })
</script>

<template>
  <div class="card">
    <div class="card-h">
      <div>
        <div class="sec-t">告警事件</div>
        <div class="sub-t">最近的告警触发/恢复记录（仅邮件通知）</div>
      </div>
      <div class="h-btns">
        <a-select v-model:value="timeRange" :options="TR_OPTS" size="small" style="width: 88px" />
        <a-select v-model:value="severity" :options="SEV_OPTS" size="small" style="width: 82px" />
        <a-select v-model:value="status" :options="ST_OPTS" size="small" style="width: 108px" />
        <a-select v-model:value="emailSent" :options="MAIL_OPTS" size="small" style="width: 92px" />
        <a-button size="small" :loading="loading" @click="loadFirstPage"><template #icon><ReloadOutlined /></template></a-button>
      </div>
    </div>

    <div v-if="!loading && events.length === 0" class="empty">暂无告警事件</div>
    <div v-else class="ev-scroll" @scroll="onScroll">
      <a-table
        :columns="columns" :data-source="events" row-key="id" size="small" :loading="loading"
        :scroll="{ x: 'max-content' }" :pagination="false"
        :custom-row="(r) => ({ style: 'cursor: pointer', onClick: () => openDetail(r) })"
      >
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'time'">{{ fmtTime(record.fired_at || record.created_at) }}</template>
          <template v-else-if="column.key === 'sev'">
            <a-space :size="4">
              <a-tag :color="sevColor(record.severity)" style="margin: 0">{{ record.severity || '-' }}</a-tag>
              <a-tag :color="stColor(record.status)" style="margin: 0">{{ stLabel(record.status) }}</a-tag>
            </a-space>
          </template>
          <template v-else-if="column.key === 'plat'">{{ dimStr(record, 'platform') || '-' }}</template>
          <template v-else-if="column.key === 'rule'"><span class="mono">#{{ record.rule_id }}</span></template>
          <template v-else-if="column.key === 'title'">
            <div class="r-name">{{ record.title || '-' }}</div>
            <div v-if="record.description" class="r-desc">{{ record.description }}</div>
          </template>
          <template v-else-if="column.key === 'dur'"><span class="dim">{{ durLabel(record) }}</span></template>
          <template v-else-if="column.key === 'dims'"><span class="dim">{{ dimsSummary(record) }}</span></template>
          <template v-else-if="column.key === 'mail'">
            <span class="dim" :style="record.email_sent ? 'color:#16a34a' : ''">
              <CheckCircleOutlined v-if="record.email_sent" />
              <StopOutlined v-else style="color: var(--text2)" />
              {{ record.email_sent ? '已发送' : '已忽略' }}
            </span>
          </template>
        </template>
      </a-table>
      <div v-if="loadingMore" class="more-tip">加载中...</div>
      <div v-else-if="!hasMore && events.length" class="more-tip">— 没有更多了 —</div>
    </div>

    <!-- 详情弹窗：基础信息 + 静默/解决 + 同规则历史 -->
    <a-modal v-model:open="detailOpen" title="告警详情" width="720px" :footer="null">
      <a-spin :spinning="detailLoading">
        <template v-if="selected">
          <div class="dt-head">
            <a-space :size="6" wrap>
              <a-tag :color="sevColor(selected.severity)" style="margin: 0">{{ selected.severity || '-' }}</a-tag>
              <a-tag :color="stColor(selected.status)" style="margin: 0">{{ stLabel(selected.status) }}</a-tag>
            </a-space>
            <div class="dt-title">{{ selected.title || '-' }}</div>
            <div v-if="selected.description" class="dt-desc">{{ selected.description }}</div>
            <a-space :size="8" wrap style="margin-top: 10px">
              <span class="sil-box">
                忽略此告警
                <a-select v-model:value="silenceDuration" size="small" style="width: 76px"
                  :options="[{ value: '1h', label: '1h' }, { value: '24h', label: '24h' }, { value: '7d', label: '7d' }]" />
                <a-button size="small" :disabled="actionLoading" @click="silenceAlert"><StopOutlined /></a-button>
              </span>
              <a-button size="small" :disabled="actionLoading" @click="manualResolve"><CheckCircleOutlined />标记为已解决</a-button>
            </a-space>
          </div>

          <div class="dt-grid">
            <div class="dt-cell"><div class="dt-k">触发时间</div><div>{{ fmtTime(selected.fired_at || selected.created_at) }}</div></div>
            <div class="dt-cell"><div class="dt-k">解决时间</div><div>{{ selected.resolved_at ? fmtTime(selected.resolved_at) : '-' }}</div></div>
            <div class="dt-cell">
              <div class="dt-k">规则 ID</div>
              <div class="mono">#{{ selected.rule_id }}
                <a class="link" @click="viewRule">查看规则</a>
              </div>
            </div>
            <div class="dt-cell">
              <div class="dt-k">指标 / 阈值</div>
              <div>
                <template v-if="typeof selected.metric_value === 'number' && typeof selected.threshold_value === 'number'">
                  {{ selected.metric_value.toFixed(2) }} / {{ selected.threshold_value.toFixed(2) }}
                </template>
                <template v-else>-</template>
              </div>
            </div>
            <div class="dt-cell"><div class="dt-k">维度信息</div><div>{{ dimsSummary(selected) }}</div></div>
            <div class="dt-cell"><div class="dt-k">邮件</div><div>{{ selected.email_sent ? '已发送' : '已忽略' }}</div></div>
          </div>

          <div class="hist">
            <div class="hist-h">
              <div>
                <div class="sec-t">历史记录</div>
                <div class="sub-t">同一规则 + 相同维度的最近事件</div>
              </div>
              <a-select v-model:value="historyRange" size="small" style="width: 92px"
                :options="[{ value: '7d', label: '近7天' }, { value: '30d', label: '近30天' }]" />
            </div>
            <div v-if="historyLoading" class="empty">加载历史中...</div>
            <div v-else-if="!history.length" class="empty">暂无历史记录</div>
            <a-table v-else :columns="[{ title: '时间', key: 't', width: 120 }, { title: '状态', key: 's', width: 110 }, { title: '指标 / 阈值', key: 'm' }]"
              :data-source="history" row-key="id" size="small" :pagination="false" :scroll="{ y: 220 }">
              <template #bodyCell="{ column, record }">
                <template v-if="column.key === 't'">{{ fmtTime(record.fired_at || record.created_at) }}</template>
                <template v-else-if="column.key === 's'"><a-tag :color="stColor(record.status)" style="margin: 0">{{ stLabel(record.status) }}</a-tag></template>
                <template v-else-if="column.key === 'm'">
                  <template v-if="typeof record.metric_value === 'number' && typeof record.threshold_value === 'number'">
                    {{ record.metric_value.toFixed(2) }} / {{ record.threshold_value.toFixed(2) }}
                  </template>
                  <template v-else>-</template>
                </template>
              </template>
            </a-table>
          </div>
        </template>
        <div v-else class="empty">暂无详情</div>
      </a-spin>
    </a-modal>
  </div>
</template>

<style scoped>
.card { background: var(--card); border: 1px solid var(--hr); border-radius: 10px; padding: 12px 14px; min-width: 0; display: flex; flex-direction: column; }
.card-h { display: flex; align-items: flex-start; justify-content: space-between; gap: 10px; margin-bottom: 10px; flex-wrap: wrap; }
.h-btns { display: flex; gap: 8px; flex-wrap: wrap; }
.ev-scroll { max-height: 520px; overflow-y: auto; }
.more-tip { text-align: center; font-size: 11px; color: var(--text2); padding: 6px 0; }
.empty { padding: 24px 0; text-align: center; font-size: 12px; color: var(--text2); }
.r-name { font-size: 12px; font-weight: 500; }
.r-desc { font-size: 11px; color: var(--text2); margin-top: 2px; white-space: normal; word-break: break-all; }
.mono { font-family: Consolas, monospace; font-size: 12px; }
.dim { font-size: 12px; color: var(--text2); }
.dt-head { padding: 12px; background: rgba(127, 127, 127, 0.06); border-radius: 8px; }
.dt-title { font-size: 14px; font-weight: 500; margin-top: 8px; }
.dt-desc { font-size: 12px; color: var(--text2); margin-top: 4px; white-space: pre-wrap; word-break: break-all; }
.sil-box { display: inline-flex; align-items: center; gap: 6px; font-size: 12px; color: var(--text2); }
.dt-grid { display: grid; grid-template-columns: 1fr 1fr 1fr; gap: 8px; margin-top: 12px; }
.dt-cell { padding: 8px 10px; background: rgba(127, 127, 127, 0.06); border-radius: 8px; font-size: 12px; }
.dt-k { font-size: 11px; color: var(--text2); margin-bottom: 2px; }
.link { font-size: 12px; color: #3b82f6; cursor: pointer; margin-left: 6px; }
.link:hover { text-decoration: underline; }
.hist { margin-top: 14px; border: 1px solid var(--hr); border-radius: 8px; padding: 10px 12px; }
.hist-h { display: flex; align-items: center; justify-content: space-between; gap: 10px; margin-bottom: 8px; }
@media (max-width: 720px) { .dt-grid { grid-template-columns: 1fr; } }
</style>
