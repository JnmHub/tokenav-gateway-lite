<script setup>
/* 账号表格（三期）——旧实现契约：frontend/src/views/admin/AccountsView.vue 的
 * allColumns / DataTable 各 cell 模板 / AccountActionMenu 可见性条件。
 * - 全部 17 列等价（select/name/priority/id/platform_type/capacity/status/schedulable/
 *   today_stats/groups/usage/proxy/rate_multiplier/last_used_at/created_at/expires_at/notes/actions）
 * - 服务端排序（sort_by/sort_order，白名单见 columnsConfig）
 * - 列显隐由父组件受控传入（localStorage 持久化在父层）
 * - 今日统计批量拉取：POST /admin/accounts/today-stats/batch（today_stats 与 usage 列同时隐藏时跳过）
 * 行操作菜单沿用二期入口，通过 menu 事件分发回父组件。 */
import { ref, computed, watch } from 'vue'
import { message } from 'ant-design-vue'
import { ThunderboltOutlined, FormOutlined, MoreOutlined } from '@ant-design/icons-vue'
import { ALL_COLUMNS, SORTABLE_KEYS } from './columnsConfig'
import { platformLabel } from './platformPresets'
import { getBatchTodayStats } from './accountsApi'
import AccountStatusCell from './AccountStatusCell.vue'
import AccountCapacityCell from './AccountCapacityCell.vue'
import AccountHealthBadges from './AccountHealthBadges.vue'
import AccountUsageCell from './AccountUsageCell.vue'
import { fmtDateTime, fmtRelative, fmtNum, fmtMoney, fmtTokens } from './format'

const props = defineProps({
  rows: { type: Array, default: () => [] },
  loading: { type: Boolean, default: false },
  selectedIds: { type: Array, default: () => [] },
  hiddenKeys: { type: Array, default: () => [] },
  sortKey: { type: String, default: 'priority' },
  sortOrder: { type: String, default: 'asc' },
  page: { type: Number, default: 1 },
  pageSize: { type: Number, default: 20 },
  total: { type: Number, default: 0 },
  refreshSeq: { type: Number, default: 0 },
})
const emit = defineEmits(['sort', 'page', 'selchange', 'update:selectedIds', 'menu', 'edit', 'test', 'delete', 'usage', 'show-temp-unsched'])

// ===== 列定义（title/key/width/sorter），可见性由 hiddenKeys 过滤 =====
// width 给全列（Fluent 数据网格：列宽受控，避免 max-content 无限撑宽溢出窗口）
const COL_W = {
  priority: 70, id: 64, platform_type: 150, capacity: 120, status: 96, schedulable: 72,
  today_stats: 132, groups: 150, usage: 280, proxy: 160, rate_multiplier: 92,
  last_used_at: 150, created_at: 150, expires_at: 150, notes: 160,
}
const rawColumns = computed(() => {
  const c = [
    { title: '名称', key: 'name', dataIndex: 'name', ellipsis: true, sorter: SORTABLE_KEYS.has('name'), width: 150 },
    ...ALL_COLUMNS.map((col) => ({ title: col.label, key: col.key, dataIndex: col.key, sorter: SORTABLE_KEYS.has(col.key), width: COL_W[col.key] || 120 })),
    { title: '操作', key: 'action', width: 112, fixed: 'right' },
  ]
  return c
})
const columns = computed(() => {
  const hidden = new Set(props.hiddenKeys)
  // 非受控排序：不在 column 上注入 sortOrder——antd-vue 4.x 受控模式（列对象带
  // sortOrder 键）下点击表头不派发 change（回归 P1-2 根因）。排序方向高亮由
  // a-table 内部状态维护，实际排序由父组件按 @change 重新请求服务端数据。
  return rawColumns.value
    .filter((c) => !hidden.has(c.key) || c.key === 'name' || c.key === 'action')
})

// a-table @change：仅排序变化时上报（分页走 pagination.onChange）
let lastSort = `${props.sortKey}:${props.sortOrder}`
const onTableChange = (_pag, _filters, sorter) => {
  // antd-vue 4.x：change 第三参的对象上没有 key 字段（在 column.key/columnKey 上），
  // 直接取 sorter.key 会得到 undefined 导致排序请求永远不发（回归 P1-2 根因）
  const s = Array.isArray(sorter) ? sorter[0] : sorter
  const key = s?.column?.key || (Array.isArray(s?.columnKey) ? s.columnKey[0] : s?.columnKey)
  const order = s?.order
  if (!key || !order) return // 第三次点击取消排序（order=null）时保持现排序
  const next = `${key}:${order === 'descend' ? 'desc' : 'asc'}`
  if (next === lastSort) return
  lastSort = next
  emit('sort', key, order === 'descend' ? 'desc' : 'asc')
}

const rowSelection = computed(() => ({
  selectedRowKeys: props.selectedIds,
  // [修复] 同步 emit v-model 事件（update:selectedIds）与兼容事件（selchange），
  // 否则父组件 v-model:selected-ids 收不到更新，复选框永远勾不上
  onChange: (keys) => {
    emit('update:selectedIds', keys)
    emit('selchange', keys)
  },
}))

// ===== 今日统计批量拉取（today_stats 或 usage 任一可见时拉取，旧 refreshTodayStatsBatch） =====
const todayStats = ref({})
const todayLoading = ref(false)
const todayError = ref('')
const anyStatsVisible = computed(() => {
  const hidden = new Set(props.hiddenKeys)
  return !(hidden.has('today_stats') && hidden.has('usage'))
})
const todayHidden = computed(() => new Set(props.hiddenKeys).has('today_stats'))
const idsSig = computed(() => props.rows.map((r) => r.id).join(','))
let statsGen = 0
watch([idsSig, anyStatsVisible, () => props.refreshSeq], async () => {
  if (!anyStatsVisible.value || !props.rows.length) { todayStats.value = {}; todayError.value = ''; return }
  const gen = ++statsGen
  todayLoading.value = true
  todayError.value = ''
  try {
    const res = await getBatchTodayStats(props.rows.map((r) => r.id))
    if (gen !== statsGen) return
    todayStats.value = res?.stats || {}
  } catch (e) {
    if (gen !== statsGen) return
    todayStats.value = {}
    todayError.value = e?.message || '今日统计读取失败'
  } finally {
    if (gen === statsGen) todayLoading.value = false
  }
}, { immediate: true })
const statsOf = (id) => todayStats.value[String(id)] || todayStats.value[id] || null

// ===== 平台/类型列徽章辅助（旧 AccountsView 对应函数等价） =====
const firstNonBlank = (...vs) => vs.find((v) => typeof v === 'string' && v.trim())
// 旧 getAccountPlanType：grok 优先用量快照，其余平台取 credentials.plan_type
const planTypeOf = (r) => {
  if (r.platform !== 'grok') return firstNonBlank(r.credentials?.plan_type, r.parent_plan_type)
  const e = r.extra || {}
  return firstNonBlank(
    e.grok_usage_snapshot?.subscription_tier,
    e.grok_billing_snapshot?.plan,
    e.grok_quota_snapshot?.subscription_tier,
    r.credentials?.subscription_tier,
    e.subscription_tier,
    r.credentials?.plan_type,
    r.parent_plan_type,
  )
}
const wsProbeOf = (r) => (r.platform === 'openai' ? (r.extra?.openai_ws_probe_status || '') : '')
const wsProbeBadge = (r) => ({ supported: ['WS✓', 'p-ok'], unsupported: ['WS✗', 'p-gray'], unknown: ['WS?', 'p-warn'] }[wsProbeOf(r)] || null)
// 旧 getOpenAICompactState：force_on/force_off/compact_supported/auto
const compactOf = (r) => {
  if (r.platform !== 'openai' || !['oauth', 'apikey'].includes(r.type)) return null
  const mode = r.extra?.openai_compact_mode
  if (mode === 'force_on') return 'compact 支持'
  if (mode === 'force_off') return 'compact 不支持'
  if (typeof r.extra?.openai_compact_supported === 'boolean') return r.extra.openai_compact_supported ? 'compact 支持' : 'compact 不支持'
  return 'compact 自动'
}
const antigravityTierOf = (r) => {
  if (r.platform !== 'antigravity') return null
  const lca = r.extra?.load_code_assist
  const tier = lca?.paidTier?.id || lca?.currentTier?.id
  return { 'free-tier': '免费版', 'g1-pro-tier': 'Pro 版', 'g1-ultra-tier': 'Ultra 版' }[tier] || null
}

// ===== 过期时间（Unix 秒） =====
const expInfo = (r) => {
  if (!r.expires_at) return null
  const ms = r.expires_at * 1000
  return { text: fmtDateTime(ms), expired: ms <= Date.now() }
}

// ===== 行操作菜单可见性（旧 AccountActionMenu 条件，二期已核实） =====
const isFuture = (t) => t && new Date(t).getTime() > Date.now()
const canReauth = (r) => ['oauth', 'setup-token'].includes(r.type) && r.parent_account_id == null
const supportsPrivacy = (r) => ['antigravity', 'openai'].includes(r.platform) && r.type === 'oauth' && r.parent_account_id == null
const isRecoverable = (r) => r.status === 'error' || isFuture(r.rate_limit_reset_at) || isFuture(r.overload_until) || isFuture(r.temp_unschedulable_until)
const CN_PLATFORMS = ['kimi', 'zhipu', 'deepseek']
const onMenu = (key, r) => emit('menu', key, r)
</script>

<template>
  <a-table
    class="acc-table"
    :columns="columns" :data-source="rows" :loading="loading" row-key="id" size="small" :scroll="{ x: 'max-content' }"
    :row-selection="rowSelection"
    :pagination="{ current: page, pageSize, total, showSizeChanger: true, onChange: (p, ps) => emit('page', p, ps) }"
    @change="onTableChange"
  >
    <template #bodyCell="{ column, record }">
      <!-- 名称 -->
      <template v-if="column.key === 'name'">
        <span :title="record.name">{{ record.name }}</span>
        <span v-if="record.parent_account_id != null" class="badge shadow-badge" :title="`Spark 影子账号（母账号 #${record.parent_account_id}）`">影子</span>
      </template>
      <!-- ID -->
      <template v-else-if="column.key === 'id'"><span class="mono">#{{ record.id }}</span></template>
      <!-- 平台/类型 + 徽章组 -->
      <template v-else-if="column.key === 'platform_type'">
        <div class="pt">
          <div class="ptrow">
            <a-tag class="ptag">{{ platformLabel(record.platform) }}</a-tag>
            <a-tag class="ttag">{{ record.type }}</a-tag>
            <span v-if="planTypeOf(record)" class="badge plan">{{ planTypeOf(record) }}</span>
            <span v-if="antigravityTierOf(record)" class="badge tier">{{ antigravityTierOf(record) }}</span>
            <span v-if="wsProbeBadge(record)" :class="['badge', wsProbeBadge(record)[1]]" :title="record.extra?.openai_ws_probe_detail || ''">{{ wsProbeBadge(record)[0] }}</span>
          </div>
          <div v-if="compactOf(record)" class="compact">
            <span :class="['dot', record.extra?.openai_compact_mode === 'force_off' || record.extra?.openai_compact_supported === false ? 'd-red' : record.extra?.openai_compact_mode === 'force_on' || record.extra?.openai_compact_supported === true ? 'd-green' : 'd-gray']" />
            {{ compactOf(record) }}
          </div>
        </div>
      </template>
      <!-- 并发/负载 -->
      <template v-else-if="column.key === 'capacity'"><AccountCapacityCell :account="record" /></template>
      <!-- 状态徽章组 -->
      <template v-else-if="column.key === 'status'">
        <AccountStatusCell :account="record" @show-temp-unsched="(r) => emit('show-temp-unsched', r)" />
        <!-- 只读健康徽章：Claude 会话窗口 / Codex 配额健康 / Grok 新媒体资格 -->
        <AccountHealthBadges :account="record" />
      </template>
      <!-- 可调度开关 -->
      <template v-else-if="column.key === 'schedulable'">
        <a-switch :checked="!!record.schedulable" size="small" @change="(v) => onMenu('schedulable', { ...record, __next: v })" />
      </template>
      <!-- 今日统计：次数 / token / 账号采购 / 用户实扣 / 标准价 -->
      <template v-else-if="column.key === 'today_stats'">
        <div v-if="statsOf(record.id)" class="ts">
          <div class="ts-top">
            <b>{{ fmtNum(statsOf(record.id).requests) }}</b><span class="dim">次</span>
            <span class="dim">{{ fmtTokens(statsOf(record.id).tokens) }} tok</span>
          </div>
          <div class="ts-bills">
            <span class="bill-a" title="账号采购成本：实际上游模型价格 × 账号倍率">账 {{ fmtMoney(statsOf(record.id).cost) }}</span>
            <span class="bill-u" title="用户计费：actual_cost，受分组倍率影响">户 {{ fmtMoney(statsOf(record.id).user_cost) }}</span>
            <span class="bill-s" title="标准费用：total_cost，不含倍率">标 {{ fmtMoney(statsOf(record.id).standard_cost) }}</span>
          </div>
        </div>
        <span v-else-if="todayLoading" class="dim">读取中</span>
        <span v-else-if="todayError" class="bill-err" :title="todayError">统计失败</span>
        <span v-else class="dim">—</span>
      </template>
      <!-- 分组 -->
      <template v-else-if="column.key === 'groups'">
        <a-tooltip v-if="(record.groups || []).length" :title="(record.groups || []).map((g) => g.name).join('、')">
          <span>
            <a-tag v-for="g in (record.groups || []).slice(0, 2)" :key="g.id" class="gtag">{{ g.name }}</a-tag>
            <span v-if="(record.groups || []).length > 2" class="dim">+{{ record.groups.length - 2 }}</span>
          </span>
        </a-tooltip>
        <span v-else class="dim">-</span>
      </template>
      <!-- 用量窗口：行内进度条 + 三口径计费；详情仍走弹窗 -->
      <template v-else-if="column.key === 'usage'">
        <AccountUsageCell
          :account="record"
          :today-stats="statsOf(record.id)"
          :today-loading="todayLoading"
          :refresh-token="refreshSeq"
          :embed-today="todayHidden"
          @detail="emit('usage', record)"
        />
      </template>
      <!-- 代理 -->
      <template v-else-if="column.key === 'proxy'">
        <div class="px">
          <template v-if="record.proxy">
            <span>{{ record.proxy.name }}<template v-if="record.proxy.country_code"> ({{ record.proxy.country_code }})</template></span>
            <span v-if="record.proxy.expires_at" class="dim">{{ fmtDateTime(record.proxy.expires_at) }}</span>
          </template>
          <span v-else class="dim">-</span>
          <div v-if="record.proxy_fallback_origin_id" class="fb">
            <span class="badge p-warn" :title="`已回退到源代理 #${record.proxy_fallback_origin_name || record.proxy_fallback_origin_id}`">已回退</span>
            <a @click.prevent="onMenu('revertproxy', record)">还原</a>
          </div>
          <div v-for="st in (record.proxy_stats || [])" :key="st.proxy_key" class="pxstat" :title="`${st.name} · 在途 ${st.in_flight}/${st.limit}`">
            <span :class="['dot', st.in_flight > 0 ? 'd-cyan' : 'd-gray']" />{{ st.name }} {{ st.in_flight }}/{{ st.limit }}
            <span class="recents">
              <i v-for="(ok, i) in (st.recent || []).slice(-5)" :key="i" :class="['rdot', ok ? 'r-ok' : 'r-bad']" />
            </span>
          </div>
        </div>
      </template>
      <!-- 计费倍率 -->
      <template v-else-if="column.key === 'rate_multiplier'"><span class="mono">×{{ record.rate_multiplier ?? 1 }}</span></template>
      <!-- 最后使用 / 创建时间 -->
      <template v-else-if="column.key === 'last_used_at'"><span class="dim">{{ fmtRelative(record.last_used_at) }}</span></template>
      <template v-else-if="column.key === 'created_at'"><span class="dim">{{ fmtDateTime(record.created_at) }}</span></template>
      <!-- 过期时间 -->
      <template v-else-if="column.key === 'expires_at'">
        <div v-if="expInfo(record)" class="ptrow">
          <span class="dim">{{ expInfo(record).text }}</span>
          <span v-if="expInfo(record).expired" class="badge p-warn">已过期</span>
          <span v-if="record.auto_pause_on_expired" class="badge p-ok" title="到期后自动暂停调度">到期自动停</span>
        </div>
        <span v-else class="dim">-</span>
      </template>
      <!-- 备注 -->
      <template v-else-if="column.key === 'notes'">
        <a-tooltip v-if="record.notes" :title="record.notes"><span class="notes">{{ record.notes }}</span></a-tooltip>
        <span v-else class="dim">-</span>
      </template>
      <!-- 操作：图标化低噪（Fluent：低频收「更多」，行内仅高频 icon 按钮） -->
      <template v-else-if="column.key === 'action'">
        <div class="rowops">
          <a-tooltip title="测试">
            <a-button size="small" type="text" @click="emit('test', record)"><ThunderboltOutlined /></a-button>
          </a-tooltip>
          <a-tooltip title="编辑">
            <a-button size="small" type="text" @click="emit('edit', record)"><FormOutlined /></a-button>
          </a-tooltip>
          <a-dropdown :trigger="['click']">
            <a-button size="small" type="text"><MoreOutlined /></a-button>
            <template #overlay>
              <a-menu @click="({ key }) => onMenu(key, record)" style="max-height: 70vh; overflow-y: auto">
                <a-menu-item key="stats">用量统计</a-menu-item>
                <a-menu-item v-if="record.platform === 'openai'" key="wsprobe">WS 探测</a-menu-item>
                <a-menu-item key="plans">计划测试</a-menu-item>
                <a-menu-item key="toggle">{{ record.status === 'active' ? '停用' : '启用' }}</a-menu-item>
                <a-menu-item key="duplicate">复制</a-menu-item>
                <a-menu-item key="ops">账号运维（今日统计 / 档位 / 凭据）</a-menu-item>
                <a-menu-item-group v-if="canReauth(record) || isRecoverable(record) || supportsPrivacy(record)" title="凭据与恢复">
                  <a-menu-item v-if="canReauth(record)" key="reauth">重新授权</a-menu-item>
                  <a-menu-item v-if="canReauth(record)" key="refreshtoken">刷新凭据</a-menu-item>
                  <a-menu-item v-if="isRecoverable(record)" key="recover">恢复调度状态</a-menu-item>
                  <a-menu-item v-if="supportsPrivacy(record)" key="privacy">隐私设置</a-menu-item>
                  <a-menu-item key="tempunsched">临时停调状态</a-menu-item>
                </a-menu-item-group>
                <a-menu-item key="health">模型健康</a-menu-item>
                <a-menu-item key="billing">计费探测</a-menu-item>
                <a-menu-item v-if="record.platform === 'openai' && record.type === 'oauth'" key="shadows">影子账号</a-menu-item>
                <a-menu-item v-if="CN_PLATFORMS.includes(record.platform)" key="cnquota">查询额度</a-menu-item>
                <a-menu-item v-if="CN_PLATFORMS.includes(record.platform)" key="cnbalance">查询余额</a-menu-item>
                <a-menu-item v-if="record.platform === 'openai' || record.platform === 'grok'" key="quota">查询配额</a-menu-item>
                <a-menu-item v-if="record.platform === 'openai' || record.platform === 'grok'" key="refresh">刷新 Token</a-menu-item>
                <a-menu-item v-if="record.platform === 'openai' || record.platform === 'grok'" key="resetquota">重置配额</a-menu-item>
                <a-menu-item v-if="record.status === 'error'" key="clearerr">清除错误</a-menu-item>
                <a-menu-item key="clearrl">清除限流</a-menu-item>
                <a-menu-item danger key="del">删除</a-menu-item>
              </a-menu>
            </template>
          </a-dropdown>
        </div>
      </template>
    </template>
  </a-table>
</template>

<style scoped>
.mono { font-family: Consolas, monospace; font-size: 12px; }
.dim { color: var(--text2); font-size: 12px; }
.rowops { display: flex; align-items: center; gap: 6px; }
.ptrow { display: flex; align-items: center; gap: 4px; flex-wrap: wrap; }
.pt { display: flex; flex-direction: column; gap: 2px; }
.ptag, .ttag { margin: 0; font-size: 11px; line-height: 18px; padding: 0 6px; }
.compact { display: flex; align-items: center; gap: 4px; font-size: 11px; color: var(--text2); }
.dot { display: inline-block; width: 6px; height: 6px; border-radius: 50%; }
.d-green { background: #52c41a; } .d-red { background: #cf3030; } .d-gray { background: #b0b0b0; } .d-cyan { background: #1890ff; }
.badge { display: inline-block; height: 16px; line-height: 16px; padding: 0 5px; border-radius: 4px; font-size: 10px; white-space: nowrap; }
.plan { background: rgba(24, 144, 255, 0.12); color: #1668dc; }
.tier { background: rgba(130, 90, 200, 0.13); color: #7048b6; }
.p-ok { background: rgba(82, 196, 26, 0.13); color: #389e0d; }
.p-warn { background: rgba(212, 140, 20, 0.14); color: #a86307; }
.p-gray { background: rgba(128, 128, 128, 0.13); color: var(--text2); }
.shadow-badge { background: rgba(212, 140, 20, 0.14); color: #a86307; margin-left: 4px; }
.ts { display: flex; flex-direction: column; gap: 4px; font-variant-numeric: tabular-nums; }
.ts-top { display: flex; align-items: baseline; gap: 4px; }
.ts-top b { font-size: 15px; font-weight: 500; color: var(--text); }
.ts-bills { display: flex; flex-direction: column; gap: 1px; font-size: 11px; }
.bill-a { color: #389e0d; } .bill-u { color: var(--text); } .bill-s { color: var(--text2); }
.bill-err { color: #b54747; font-size: 12px; }
.px { display: flex; flex-direction: column; gap: 2px; font-size: 12px; }
.fb { display: flex; align-items: center; gap: 6px; }
.fb a { font-size: 11px; }
.pxstat { display: flex; align-items: center; gap: 4px; font-size: 11px; color: var(--text2); }
.recents { display: inline-flex; gap: 2px; }
.rdot { width: 4px; height: 4px; border-radius: 50%; display: inline-block; }
.r-ok { background: #52c41a; } .r-bad { background: #f5222d; }
.gtag { margin: 0 2px 0 0; font-size: 11px; line-height: 18px; padding: 0 6px; }
.notes { max-width: 160px; display: inline-block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; vertical-align: bottom; }
</style>
<style>
/* 账号表操作列加厚底，挡住壁纸字透出（全局 --cell-frost 0.85 不够） */
.acc-table .ant-table-tbody > tr > td { vertical-align: top; }
.acc-table .ant-table-tbody > tr > td.ant-table-cell-fix-right,
.acc-table .ant-table-thead > tr > th.ant-table-cell-fix-right {
  background: #f2f2f2 !important;
}
:root.dark .acc-table .ant-table-tbody > tr > td.ant-table-cell-fix-right,
:root.dark .acc-table .ant-table-thead > tr > th.ant-table-cell-fix-right {
  background: #2b2b2b !important;
}
.acc-table .ant-table-tbody > tr:hover > td.ant-table-cell-fix-right,
.acc-table .ant-table-tbody > tr.ant-table-row-selected > td.ant-table-cell-fix-right {
  background: #eaeaea !important;
}
:root.dark .acc-table .ant-table-tbody > tr:hover > td.ant-table-cell-fix-right,
:root.dark .acc-table .ant-table-tbody > tr.ant-table-row-selected > td.ant-table-cell-fix-right {
  background: #333 !important;
}
</style>
