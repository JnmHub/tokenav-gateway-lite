<script setup>
/* 运维中心一期：核心观测能力
 * 编排：顶栏查询状态(时间范围/平台/分组/自动刷新) → 健康分 + 6 指标卡 + 系统健康 +
 *       实时 QPS/TPS + 趋势图表 4 张 + 最近错误简表 + 系统日志 + 三级下钻弹窗
 * 接口（字段全部自旧 frontend/src/api/admin/ops.ts 核实并经真实后端冒烟）：
 * - GET /admin/ops/dashboard/overview?time_range=&start_time=&end_time=&platform=&group_id=&mode=auto
 * - GET /admin/ops/dashboard/throughput-trend / latency-histogram / error-trend / error-distribution
 * - GET /admin/ops/errors?limit=20（最近错误简表，兼容接口）
 * - GET /admin/ops/advanced-settings（仅取 display_alert_events / display_openai_token_stats 卡片显示开关）
 *
 * 二期（告警体系与运维管理卡）：
 *   1. 告警规则卡 OpsAlertRules（alert-rules CRUD + groups/all + 编辑弹窗）
 *   2. 告警事件卡 OpsAlertEvents（alert-events 4 筛选 + 滚动加载 + 静默/手动解决/历史）
 *   3. 并发面板 OpsConcurrency（concurrency / user-concurrency / account-availability，4 视图）
 *   4. 入口防护卡 OpsIngressProtection（runtime/panel-rate-limit GET/PUT/reset + ingress-rejections）
 *   5. OpenAI Token 统计卡 OpsTokenStats（dashboard/openai-token-stats）
 *   6. 运维设置弹窗 OpsSettingsDialog（runtime/alert + email-notification + advanced-settings
 *      + settings/metric-thresholds，保存前拉取合并防覆盖）
 * 三期（收官）：
 *   1. Codex 尾部爆发统计整页 OpsTailBurstStats（抽屉：GET /admin/ops/codex-tail-burst/stats）
 *   2. Schema 分析器整页 OpsSchemaAnalyzer（抽屉：request-debug 配置/记录 + full-config/full-records）
 *   3. URL 深链（对照旧 OpsDashboard.vue）：
 *      挂载时读 query —— time_range（兼容旧键 tr）/ platform / group_id / open_error_details=1&error_type=
 *      / open_alert_rules=1 / alert_rule_id；错误详情弹窗打开后经 history.replaceState 同步回 query */
import { ref, computed, onMounted, onUnmounted, watch, nextTick } from 'vue'
import { message } from 'ant-design-vue'
import { FireOutlined, ExperimentOutlined } from '@ant-design/icons-vue'
import { request } from '../api'
import OpsTopBar from '../components/ops/OpsTopBar.vue'
import OpsHealthScore from '../components/ops/OpsHealthScore.vue'
import OpsMetricCards from '../components/ops/OpsMetricCards.vue'
import OpsSystemHealth from '../components/ops/OpsSystemHealth.vue'
import OpsRealtimePanel from '../components/ops/OpsRealtimePanel.vue'
import OpsQpsStreamPanel from '../components/ops/OpsQpsStreamPanel.vue'
import OpsSnapshotV2Card from '../components/ops/OpsSnapshotV2Card.vue'
import OpsTrendCharts from '../components/ops/OpsTrendCharts.vue'
import OpsErrorDetailsModal from '../components/ops/OpsErrorDetailsModal.vue'
import OpsErrorDetailModal from '../components/ops/OpsErrorDetailModal.vue'
import OpsRequestDetailsModal from '../components/ops/OpsRequestDetailsModal.vue'
import OpsSystemLogs from '../components/ops/OpsSystemLogs.vue'
import OpsAlertRules from '../components/ops/OpsAlertRules.vue'
import OpsAlertEvents from '../components/ops/OpsAlertEvents.vue'
import OpsConcurrency from '../components/ops/OpsConcurrency.vue'
import OpsIngressProtection from '../components/ops/OpsIngressProtection.vue'
import OpsTokenStats from '../components/ops/OpsTokenStats.vue'
import OpsIngressRejects from '../components/ops/OpsIngressRejects.vue'
import OpsSettingsDialog from '../components/ops/OpsSettingsDialog.vue'
import OpsTailBurstStats from '../components/ops/OpsTailBurstStats.vue'
import OpsSchemaAnalyzer from '../components/ops/OpsSchemaAnalyzer.vue'

/* ---------- 公共查询状态（顶栏 → 各卡片） ---------- */
const loading = ref(false)
const err = ref('')
const lastLoaded = ref('')
const timeRange = ref('1h')
const customStart = ref('')
const customEnd = ref('')
const platform = ref('')
const groupId = ref(null)
const autoRefresh = ref(false)
const refreshInterval = ref(30)
const countdown = ref(0)

const TR_MIN = { '5m': 5, '30m': 30, '1h': 60, '6h': 360, '24h': 1440 }
const rangeMinutes = computed(() => (timeRange.value === 'custom' ? 1440 : TR_MIN[timeRange.value] || 60))

const rangeLabel = computed(() => {
  if (timeRange.value === 'custom') {
    const f = (t) => String(t).replace('T', ' ').slice(5, 16)
    return `自定义 ${f(customStart.value)} ~ ${f(customEnd.value)}`
  }
  const map = { '5m': '近 5 分钟', '30m': '近 30 分钟', '1h': '近 1 小时', '6h': '近 6 小时', '24h': '近 24 小时' }
  return map[timeRange.value] || timeRange.value
})

/* 顶栏时间范围：普通枚举直接切换；自定义则收到 {start,end}（ISO） */
const onTimeRange = (v) => {
  if (v && typeof v === 'object') {
    customStart.value = v.start; customEnd.value = v.end; timeRange.value = 'custom'
  } else {
    timeRange.value = v
  }
}

/* ---------- 数据加载（overview + 4 趋势接口 + 最近错误） ---------- */
const ov = ref(null)
const thresholds = ref(null)
const throughput = ref(null)
const latency = ref(null)
const errorTrend = ref(null)
const errorDist = ref(null)
const recentErrors = ref([])

const buildQuery = () => {
  const query = { mode: 'auto' }
  if (platform.value) query.platform = platform.value
  if (groupId.value) query.group_id = groupId.value
  if (timeRange.value === 'custom') {
    if (customStart.value && customEnd.value) {
      query.start_time = customStart.value; query.end_time = customEnd.value
    } else {
      query.time_range = '1h' // 自定义未就绪时回退，避免后端报错（旧实现同策略）
    }
  } else {
    query.time_range = timeRange.value
  }
  return query
}

const tryLoad = async (path, keep) => {
  const data = await request(path, { method: 'GET', query: buildQuery() })
  keep.value = data
}

const loadThresholds = async () => {
  try { thresholds.value = await request('/admin/ops/settings/metric-thresholds', { method: 'GET' }) } catch { thresholds.value = null }
}

const load = async () => {
  loading.value = true; err.value = ''
  const results = await Promise.allSettled([
    tryLoad('/admin/ops/dashboard/overview', ov),
    tryLoad('/admin/ops/dashboard/throughput-trend', throughput),
    tryLoad('/admin/ops/dashboard/error-trend', errorTrend),
    request('/admin/ops/dashboard/latency-histogram', { method: 'GET', query: buildQuery() }).then((d) => (latency.value = d)),
    request('/admin/ops/dashboard/error-distribution', { method: 'GET', query: buildQuery() }).then((d) => (errorDist.value = d)),
    request('/admin/ops/errors', { method: 'GET', query: { limit: 20, time_range: timeRange.value === 'custom' ? '24h' : timeRange.value } })
      .then((d) => (recentErrors.value = d.items || d || [])),
  ])
  const failed = results.filter((r) => r.status === 'rejected')
  if (failed.length === results.length) err.value = failed[0].reason?.message || '加载失败'
  else if (failed.length) message.warning(`部分面板加载失败：${failed[0].reason?.message || ''}`)
  lastLoaded.value = new Date().toTimeString().slice(0, 8)
  loading.value = false
  realtimeToken.value += 1 // 通知实时面板跟随主节奏刷新
}
const realtimeToken = ref(0)

/* ---------- 自动刷新：开关 + 15/30/60s 档位 + 1s 倒计时（旧 useIntervalFn 同节奏） ---------- */
let timer = null
const stopTimer = () => { if (timer) { clearInterval(timer); timer = null } }
const armTimer = () => {
  stopTimer()
  if (!autoRefresh.value) { countdown.value = 0; return }
  countdown.value = refreshInterval.value
  timer = setInterval(() => {
    if (loading.value) return
    countdown.value -= 1
    if (countdown.value <= 0) { countdown.value = refreshInterval.value; load() }
  }, 1000)
}
const onToggleAuto = (v) => { autoRefresh.value = v; armTimer() }
const onInterval = (v) => { refreshInterval.value = v; if (autoRefresh.value) armTimer() }

/* ---------- 三级下钻弹窗状态 ---------- */
const errListOpen = ref(false)
const errListType = ref('request') // 'request' | 'upstream'
const errDetailOpen = ref(false)
const errDetailId = ref(null)
const errDetailType = ref('request')
const reqOpen = ref(false)
const reqPreset = ref({ title: '请求明细', kind: 'all', sort: 'created_at_desc' })

const openErrList = (type = 'request') => {
  errListType.value = type; errListOpen.value = true
}
const openErrDetail = (id, type = 'request') => {
  errDetailId.value = id; errDetailType.value = type
  errListOpen.value = false; reqOpen.value = false
  errDetailOpen.value = true
}
/* 指标卡「详情」：字符串=错误列表类型；对象=请求明细 preset（旧 Header 同约定） */
const onMetricDetail = (arg) => {
  if (typeof arg === 'string') return openErrList(arg)
  reqPreset.value = { title: arg.title || '请求明细', kind: arg.kind || 'all', sort: arg.sort || 'created_at_desc' }
  errListOpen.value = false
  reqOpen.value = true
}

const fmtTime = (t) => (t ? String(t).replace('T', ' ').slice(5, 19) : '-')

/* ---------- 三期：URL 深链（对照旧 OpsDashboard.vue applyRouteQueryToState） ---------- */
/* time_range：白名单枚举（旧版还收 custom，但 custom 依赖 start/end 键，深链不做）。
 * 兼容旧外链键名：time_range 优先，回退旧 OpsDashboard 使用的 tr */
const TR_WHITE = new Set(['5m', '30m', '1h', '6h', '24h'])
const pendingRuleHighlight = ref(false)

const setQueryParams = (patch) => {
  const url = new URL(window.location.href)
  for (const [k, v] of Object.entries(patch)) {
    if (v === null || v === undefined || v === '') url.searchParams.delete(k)
    else url.searchParams.set(k, String(v))
  }
  history.replaceState(history.state, '', url)
}

const applyDeepLink = () => {
  const q = new URLSearchParams(window.location.search)
  const tr = q.get('time_range') || q.get('tr')
  if (tr && TR_WHITE.has(tr)) timeRange.value = tr
  const plat = q.get('platform')
  if (plat) platform.value = plat
  const gid = Number.parseInt(q.get('group_id') || '', 10)
  if (Number.isFinite(gid) && gid > 0) groupId.value = gid

  const openErr = q.get('open_error_details')
  if (openErr === '1' || openErr === 'true') {
    // error_type 只认 upstream / request（其余一律回落 request，与旧实现一致）
    errListType.value = q.get('error_type') === 'upstream' ? 'upstream' : 'request'
    errListOpen.value = true
  }

  const openRules = q.get('open_alert_rules')
  const ruleID = Number.parseInt(q.get('alert_rule_id') || '', 10)
  if (openRules === '1' || openRules === 'true' || (Number.isFinite(ruleID) && ruleID > 0)) {
    pendingRuleHighlight.value = true // 规则卡随 ov 渲染，等首屏数据就绪后滚动+高亮
  }
}

/* 错误详情弹窗打开/关闭与 error_type 切换 → 同步回地址栏（可分享深链） */
watch(errListOpen, (v) => {
  setQueryParams(v ? { open_error_details: '1', error_type: errListType.value } : { open_error_details: null, error_type: null })
})
watch(errListType, (t) => { if (errListOpen.value) setQueryParams({ error_type: t }) })

/* ---------- 二期：管理卡状态 ---------- */
/* advanced-settings 仅取卡片显示开关（display_alert_events / display_openai_token_stats），
 * 保存设置后重拉，让开关即时生效（默认：告警事件开、Token 统计关，与旧实现一致） */
const advanced = ref(null)
const loadAdvanced = async () => {
  try { advanced.value = await request('/admin/ops/advanced-settings', { method: 'GET' }) } catch { advanced.value = null }
}
const showAlertEvents = computed(() => advanced.value?.display_alert_events !== false)
const showTokenStats = computed(() => advanced.value?.display_openai_token_stats === true)

const settingsOpen = ref(false)
const alertRulesRef = ref(null)
const rulesFlash = ref(false)
/* 三期：Codex 尾部爆发统计 / Schema 分析器整页抽屉 */
const tailBurstOpen = ref(false)
const schemaOpen = ref(false)
/* 告警事件「查看规则」：滚动到规则卡并短暂高亮 */
const onViewRule = (ruleId) => {
  alertRulesRef.value?.rootEl?.scrollIntoView({ behavior: 'smooth', block: 'start' })
  rulesFlash.value = true
  setTimeout(() => { rulesFlash.value = false }, 1600)
}

const errColumns = [  { title: '时间', key: 'time', width: 110 },
  { title: '类型', key: 'scope', width: 90 },
  { title: '状态码', key: 'code', width: 78 },
  { title: '错误信息', key: 'msg', ellipsis: true },
  { title: '上游', dataIndex: 'upstream_endpoint', key: 'upstream', width: 150, ellipsis: true },
  { title: '模型', dataIndex: 'model', key: 'model', width: 130, ellipsis: true },
  { title: '来源 IP', key: 'ip', width: 120 },
]
const openRecent = (r) => {
  // 兼容接口 /admin/ops/errors 无 error_source 字段时按 phase 归类
  openErrDetail(r.id, r.phase === 'upstream' ? 'upstream' : 'request')
}

onMounted(() => {
  // 深链先行：在首屏请求前应用 query 里的 time_range/platform/group_id/弹窗打开态
  applyDeepLink()
  load().then(() => {
    if (pendingRuleHighlight.value) {
      pendingRuleHighlight.value = false
      nextTick(() => onViewRule(null)) // 规则卡渲染完成后滚动+短暂高亮
    }
  })
  loadThresholds()
  loadAdvanced()
})
onUnmounted(stopTimer)
</script>

<template>
  <div class="ops">
    <!-- 顶栏：时间范围 / 平台 / 分组 / 自动刷新 / 手动刷新 -->
    <OpsTopBar
      :time-range="timeRange" :custom-start="customStart" :custom-end="customEnd"
      v-model:platform="platform" v-model:group-id="groupId"
      :auto-refresh="autoRefresh" :refresh-interval="refreshInterval" :countdown="countdown" :loading="loading"
      @update:time-range="onTimeRange"
      @update:auto-refresh="onToggleAuto" @update:refresh-interval="onInterval"
      @refresh="load"
    />

    <a-spin :spinning="loading">
      <div v-if="err && !ov" class="errbar">
        加载失败：{{ err }}
        <a-button size="small" danger @click="load">重试</a-button>
      </div>

      <template v-if="ov">
        <!-- 行 1：健康分圆环 + 6 指标卡 -->
        <div class="row1">
          <OpsHealthScore :overview="ov" />
          <OpsMetricCards :overview="ov" :thresholds="thresholds" @detail="onMetricDetail" />
        </div>

        <!-- 行 2：实时 QPS/TPS（HTTP 轮询 + WebSocket 推送双面板）+ 系统健康 6 小卡 -->
        <div class="row2">
          <div class="rt-cell rt-stack">
            <OpsRealtimePanel :platform="platform" :group-id="groupId" :time-range-minutes="rangeMinutes" :refresh-token="realtimeToken" />
            <OpsQpsStreamPanel />
          </div>
          <div class="sys-cell">
            <div class="sec-t">系统健康</div>
            <OpsSystemHealth :overview="ov" />
          </div>
        </div>

        <!-- 运维快照 V2（单接口聚合卡：overview + 吞吐量/错误趋势） -->
        <div class="row-token">
          <OpsSnapshotV2Card
            :time-range="timeRange" :custom-start="customStart" :custom-end="customEnd"
            :platform="platform" :group-id="groupId"
          />
        </div>

        <!-- 行 3：趋势图表 4 张 -->
        <OpsTrendCharts
          :throughput="throughput" :latency="latency" :error-trend="errorTrend" :error-dist="errorDist"
          @open-errors="openErrList"
        />

        <!-- 最近错误简表（兼容接口 /admin/ops/errors） -->
        <div class="lg-bar">
          <span class="sec-t">最近错误（{{ recentErrors.length }}）</span>
          <a class="link" @click="openErrList('request')">全部请求错误</a>
          <a class="link" @click="openErrList('upstream')">全部上游错误</a>
          <div style="flex: 1"></div>
          <span class="hint">范围 {{ rangeLabel }}<template v-if="lastLoaded"> · 更新于 {{ lastLoaded }}</template></span>
        </div>
        <a-table
          v-if="recentErrors.length" :columns="errColumns" :data-source="recentErrors" row-key="id"
          size="small" :scroll="{ x: 'max-content' }" :pagination="false"
          :custom-row="(r) => ({ style: 'cursor: pointer', onClick: () => openRecent(r) })"
        >
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'time'">{{ fmtTime(record.created_at) }}</template>
            <template v-else-if="column.key === 'scope'">{{ record.phase === 'upstream' ? '上游' : '请求' }}</template>
            <template v-else-if="column.key === 'code'">
              <a-tag :color="(record.status_code || 0) >= 500 ? 'red' : (record.status_code || 0) >= 400 ? 'orange' : 'default'">{{ record.status_code || '-' }}</a-tag>
            </template>
            <template v-else-if="column.key === 'msg'">{{ record.message || '-' }}</template>
            <template v-else-if="column.key === 'ip'">{{ (record.client_ip || '').replace('/32', '') || '-' }}</template>
          </template>
        </a-table>
        <div v-else class="empty">当前范围暂无错误</div>

        <!-- 行 4：告警区（规则 + 事件，受 advanced-settings 显示开关控制） -->
        <div class="zone-bar">
          <span class="zone-t">告警体系</span>
          <span class="hint">规则触发 → 事件记录 → 邮件通知（可在运维设置中配置）</span>
        </div>
        <div class="row-alert">
          <div class="ar-cell" :class="{ flash: rulesFlash }">
            <OpsAlertRules ref="alertRulesRef" />
          </div>
          <div v-if="showAlertEvents" class="ae-cell">
            <OpsAlertEvents @view-rule="onViewRule" />
          </div>
        </div>

        <!-- 行 5：管理区（并发 / 入口防护 / Token 统计 + 三期整页入口 + 运维设置入口） -->
        <div class="zone-bar">
          <span class="zone-t">运维管理</span>
          <div style="flex: 1"></div>
          <a-button size="small" @click="tailBurstOpen = true"><FireOutlined />尾部爆发统计</a-button>
          <a-button size="small" @click="schemaOpen = true"><ExperimentOutlined />Schema 分析器</a-button>
          <a-button size="small" type="primary" @click="settingsOpen = true">运维设置</a-button>
        </div>
        <div class="row-mgmt">
          <div class="mc-cell"><OpsConcurrency :platform="platform" :group-id="groupId" :refresh-token="realtimeToken" /></div>
          <div class="mc-cell"><OpsIngressProtection /></div>
        </div>
        <div v-if="showTokenStats" class="row-token">
          <OpsTokenStats :platform="platform" :group-id="groupId" :refresh-token="realtimeToken" />
        </div>

        <!-- 入口拒绝统计（官方 v0.2.5 对齐）：独立一行，避免挤压双列管理区 -->
        <div class="row-token">
          <OpsIngressRejects />
        </div>

        <!-- 系统日志：11 项筛选 + 清理 + 运行时配置 -->
        <OpsSystemLogs />
      </template>
    </a-spin>

    <!-- 三级下钻弹窗 -->
    <OpsErrorDetailsModal
      v-model:open="errListOpen" :error-type="errListType"
      :time-range="timeRange === 'custom' ? '24h' : timeRange"
      :platform="platform" :group-id="groupId"
      @switch-type="(t) => (errListType = t)"
      @open-detail="openErrDetail"
    />
    <OpsErrorDetailModal v-model:open="errDetailOpen" :error-id="errDetailId" :error-type="errDetailType" />
    <OpsRequestDetailsModal
      v-model:open="reqOpen" :title="reqPreset.title"
      :time-range="timeRange" :custom-start="customStart" :custom-end="customEnd"
      :platform="platform" :group-id="groupId" :preset="reqPreset"
      @open-error-detail="openErrDetail"
    />

    <!-- 运维设置弹窗（二期）：保存后重拉 advanced-settings 使显示开关即时生效 -->
    <OpsSettingsDialog v-model:open="settingsOpen" @saved="loadAdvanced" />

    <!-- 三期整页抽屉：Codex 尾部爆发统计 / Schema 分析器 -->
    <OpsTailBurstStats v-model:open="tailBurstOpen" />
    <OpsSchemaAnalyzer v-model:open="schemaOpen" />
  </div>
</template>

<style scoped>
.ops { height: 100%; overflow-y: auto; }
.row1 { display: grid; grid-template-columns: 300px 1fr; gap: 12px; align-items: stretch; }
.row2 { display: grid; grid-template-columns: minmax(320px, 4fr) 6fr; gap: 12px; margin-top: 12px; }
.rt-cell { min-width: 0; }
.rt-stack { display: flex; flex-direction: column; gap: 8px; }
.sys-cell { min-width: 0; }
.sys-cell .sec-t { font-size: 12px; color: var(--text2); margin-bottom: 6px; font-weight: 500; }
.lg-bar { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; margin: 16px 0 8px; }
.lg-bar .sec-t { font-size: 13px; font-weight: 500; }
.link { font-size: 12px; color: #3b82f6; cursor: pointer; }
.link:hover { text-decoration: underline; }
.hint { font-size: 11px; color: var(--text2); }
.errbar { display: flex; align-items: center; gap: 10px; padding: 10px 14px; border-radius: 10px; background: rgba(220,38,38,.08); border: 1px solid rgba(220,38,38,.25); color: #dc2626; font-size: 13px; margin-bottom: 10px; }
.empty { padding: 18px 0; text-align: center; font-size: 13px; color: var(--text2); }
/* 二期：告警区 / 管理区 */
.zone-bar { display: flex; align-items: center; gap: 12px; margin: 18px 0 10px; }
.zone-t { font-size: 14px; font-weight: 500; }
/* 告警行改 flex：事件卡被显示开关关闭时规则卡自动占满整行
 * （旧固定 2fr/3fr 网格在事件卡 v-if 掉后右侧留大空白） */
.row-alert { display: flex; flex-wrap: wrap; gap: 12px; align-items: flex-start; }
.ar-cell { flex: 1 1 420px; min-width: 0; }
.ae-cell { flex: 3 1 460px; min-width: 0; }
.ar-cell.flash { box-shadow: 0 0 0 2px rgba(59, 130, 246, 0.65); }
.ae-cell { min-width: 0; }
/* 管理行 stretch：并发卡与入口防护卡等高，避免矮卡下方留空白 */
.row-mgmt { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; align-items: stretch; }
.mc-cell { min-width: 0; display: flex; }
.mc-cell > * { flex: 1; }
.row-token { margin-top: 10px; }
@media (max-width: 1100px) {
  .row1 { grid-template-columns: 1fr; }
  .row2 { grid-template-columns: 1fr; }
  .row-alert { grid-template-columns: 1fr; }
  .row-mgmt { grid-template-columns: 1fr; }
}
</style>
