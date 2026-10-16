<script setup>
/* 分组管理：列表 + 新增/编辑弹窗 + 4 个操作弹窗 + 列表能力（二期 B）
 * 接口（全部与旧实现/后端路由核实）：GET/POST /admin/groups（筛选 search/platform/status/is_exclusive +
 *   服务端排序 sort_by/sort_order）、GET /admin/groups/all、PUT/DELETE /admin/groups/:id、
 *   POST /admin/groups/:id/duplicate（Idempotency-Key 头，见 groupApi.duplicateGroup）、
 *   GET/PUT/DELETE /admin/groups/:id/rate-multipliers、PUT/DELETE /admin/groups/:id/rpm-overrides、
 *   POST /admin/groups/:id/accounts/bind、GET/POST/PUT/DELETE /admin/groups/:id/composite-routes(+
 *   batch-status/preview)、PUT /admin/groups/sort-order、
 *   GET /admin/groups/capacity-summary、GET /admin/groups/usage-summary
 * 弹窗（components/group/ 子组件）：GroupEditorModal（2A）、GroupRateMultipliersModal、
 *   GroupRPMOverridesModal、GroupBindAccountsModal、GroupCompositeRoutesModal、GroupSortOrderModal
 * 一期 24 字段与 2A 定价/映射/路由逻辑全部保留；删除确认文案区分订阅分组（旧 5285 行契约）。
 */
import { ref, reactive, computed, onMounted, onUnmounted, watch } from 'vue'
import { message, Modal } from 'ant-design-vue'
import {
  SortAscendingOutlined, SearchOutlined, SyncOutlined,
} from '@ant-design/icons-vue'
import GroupEditorModal from '../components/group/GroupEditorModal.vue'
import GroupRateMultipliersModal from '../components/group/GroupRateMultipliersModal.vue'
import GroupRPMOverridesModal from '../components/group/GroupRPMOverridesModal.vue'
import GroupBindAccountsModal from '../components/group/GroupBindAccountsModal.vue'
import GroupCompositeRoutesModal from '../components/group/GroupCompositeRoutesModal.vue'
import GroupSortOrderModal from '../components/group/GroupSortOrderModal.vue'
import GroupInsightsModal from '../components/group/GroupInsightsModal.vue'
import GroupRowActions from '../components/group/GroupRowActions.vue'
import GroupMobileList from '../components/group/GroupMobileList.vue'
import W12EmptyState from '../components/design/W12EmptyState.vue'
import { useGroupDialogRequests } from '../components/group/useGroupDialogRequests.js'
import { useGroupEditor } from '../components/group/useGroupEditor'
import { listGroups, getCapacitySummary, duplicateGroup } from '../components/group/groupApi'
import { request } from '../api'
import { isCancellation } from '../api/session'
import { useWindowContext } from '../core/windowContext'
import { useElementSize } from '../core/useElementSize'
import W12Button from '../components/design/W12Button.vue'
import W12Input from '../components/design/W12Input.vue'
import W12Select from '../components/design/W12Select.vue'
import W12Table from '../components/design/W12Table.vue'
import W12Toolbar from '../components/design/W12Toolbar.vue'
import W12Status from '../components/design/W12Status.vue'
import W12Dialog from '../components/design/W12Dialog.vue'

const root = ref(null), context = useWindowContext()
const { width } = useElementSize(root)
const narrow = computed(() => width.value > 0 && width.value < 620)
const filterOpen = ref(false)
const filtersCount = computed(() => [fPlatform.value, fStatus.value, fExclusive.value].filter(Boolean).length)
const controller = new AbortController()
let listController, capacityBusy = false
const columnsOpen = ref(false)
const COLUMN_KEY = 'w12-groups-columns'
const defaultColumns = ['name','platform','billing_type','rate','is_exclusive','account_count','capacity','ingress_requests','usage','status']
const allowedColumns = ['id', ...defaultColumns]
const selectedColumns = ref([...defaultColumns])
try {
  const stored = JSON.parse(localStorage.getItem(COLUMN_KEY))
  if (Array.isArray(stored)) selectedColumns.value = [...new Set(['name', ...stored.filter(key => allowedColumns.includes(key))])]
} catch { /* 损坏的本地偏好恢复默认，不阻断管理页。 */ }
watch(selectedColumns, value => {
  try { localStorage.setItem(COLUMN_KEY, JSON.stringify(value)) } catch { /* 隐私模式不持久化。 */ }
})

const loading = ref(false)
const rows = ref([])
const total = ref(0)
const page = ref(1)
const PAGE_SIZE_KEY = 'w12-groups-page-size'
// 每页条数 localStorage 持久化（旧 usePersistedPageSize 等价）
const storedPageSize = Number(localStorage.getItem(PAGE_SIZE_KEY))
const pageSize = ref([10, 20, 50, 100].includes(storedPageSize) ? storedPageSize : 20)
const err = ref('')

// ===== 筛选（参数名已对照后端 List handler：search/platform/status/is_exclusive） =====
const fSearch = ref('')
const fPlatform = ref('')
const fStatus = ref('')
const fExclusive = ref('')
// ===== 服务端排序（后端白名单 10 列，默认 sort_order asc） =====
const sortState = reactive({ sort_by: 'sort_order', sort_order: 'asc' })

const platformFilterOptions = [
  { value: '', label: '全部平台' },
  { value: 'anthropic', label: 'Anthropic' }, { value: 'openai', label: 'OpenAI' },
  { value: 'gemini', label: 'Gemini' }, { value: 'antigravity', label: 'Antigravity' },
  { value: 'grok', label: 'Grok' }, { value: 'kimi', label: 'Kimi' },
  { value: 'zhipu', label: '智谱 GLM' }, { value: 'deepseek', label: 'DeepSeek' },
  { value: 'composite', label: '聚合模型（多平台）' },
]
const statusFilterOptions = [
  { value: '', label: '全部状态' }, { value: 'active', label: '启用' }, { value: 'inactive', label: '停用' },
]
const exclusiveFilterOptions = [
  { value: '', label: '全部分组' }, { value: 'true', label: '独占' }, { value: 'false', label: '非独占' },
]

// 可排序列（sorter: true → 服务端排序）；capacity/usage/操作列不可排序。
// computed：sortState 变化时表头受控排序箭头同步刷新。
const allColumns = computed(() => [
  { title: 'ID', dataIndex: 'id', key: 'id', width: 72, sorter: true, sortOrder: orderOf('id') },
  { title: '名称', dataIndex: 'name', key: 'name', width: 200, ellipsis: true, sorter: true, sortOrder: orderOf('name') },
  { title: '平台', dataIndex: 'platform', key: 'platform', width: 100, sorter: true, sortOrder: orderOf('platform') },
  { title: '类型', dataIndex: 'subscription_type', key: 'billing_type', width: 90, sorter: true, sortOrder: orderOf('subscription_type') },
  { title: '倍率', dataIndex: 'rate_multiplier', key: 'rate', width: 90, sorter: true, sortOrder: orderOf('rate_multiplier') },
  { title: '独占', key: 'is_exclusive', width: 74, sorter: true, sortOrder: orderOf('is_exclusive') },
  { title: '账号', dataIndex: 'account_count', key: 'account_count', width: 138, sorter: true, sortOrder: orderOf('account_count') },
  { title: '容量', key: 'capacity', width: 230 },
  { title: '入口请求', key: 'ingress_requests', width: 100 },
  { title: '用量 今日/累计', key: 'usage', width: 150 },
  { title: '状态', dataIndex: 'status', key: 'status', width: 80, sorter: true, sortOrder: orderOf('status') },
  { title: '操作', key: 'action', width: 100, fixed: 'right' },
])
const columns = computed(() => allColumns.value.filter(column => ['name','action'].includes(column.key) || selectedColumns.value.includes(column.key)))
const columnOptions = computed(() => allColumns.value.filter(column => !['name','action'].includes(column.key)).map(column => ({ label: column.title, value: column.key })))
function orderOf(key) {
  if (sortState.sort_by !== key) return false
  return sortState.sort_order === 'asc' ? 'ascend' : 'descend'
}

// ===== 编辑弹窗（2A composable，保存成功后回调刷新列表） =====
const editor = useGroupEditor(() => load())
const { openCreate, openEdit } = editor

// ===== 容量列数据：capacity-summary（5s 轮询开关默认开） =====
const capacityMap = ref(new Map())
const capacityAuto = ref(true)
const capacityUpdatedAt = ref(null)
let capacityTimer = null
const capacityError = ref('')
const loadCapacity = async () => {
  if (capacityBusy || controller.signal.aborted) return
  capacityBusy = true
  try {
    const data = await getCapacitySummary(controller.signal)
    const map = new Map()
    for (const it of data || []) map.set(it.group_id, it)
    capacityMap.value = map
    capacityUpdatedAt.value = new Date(); capacityError.value = ''
  } catch (error) { if (!isCancellation(error)) { capacityMap.value = new Map(); capacityError.value = '容量读取失败，可刷新重试' } }
  finally { capacityBusy = false }
}
const fmtCap = (g) => {
  const c = capacityMap.value.get(g.id)
  if (!c) return '—'
  const seg = (u, m) => (m > 0 ? `${u}/${m}` : `${u}/∞`)
  return `入口请求 ${c.ingress_requests_used} · 账号并发 ${seg(c.concurrency_used, c.concurrency_max)} · 会话 ${seg(c.sessions_used, c.sessions_max)} · 每分钟请求 ${seg(c.rpm_used, c.rpm_max)}`
}
// 容量条辅助：取某维度 {used, max}；max=0 表示无上限
const cOf = (g, kind) => {
  const c = capacityMap.value.get(g.id) || {}
  return { used: c[`${kind}_used`] || 0, max: c[`${kind}_max`] || 0 }
}
// 占比色阶：<80% 主题蓝、≥80% 橙、≥95% 红；无上限不填充
const capStyle = ({ used, max }) => {
  if (!max) return { width: '0%' }
  const pct = Math.min((used / max) * 100, 100)
  const color = pct >= 95 ? '#c42b1c' : pct >= 80 ? '#9a5b00' : 'var(--accent)'
  return { width: Math.max(pct, used > 0 ? 4 : 0) + '%', background: color }
}
const capText = ({ used, max }) => (max > 0 ? `${used}/${max}` : `${used}/∞`)
// ===== 用量列数据：usage-summary 轻量缓存（旧 loadUsageSummary 等价） =====
const usageMap = ref(new Map())
let usageCacheAt = 0
const USAGE_CACHE_MS = 30_000
// 列表请求序号：防止快速搜索/排序时旧响应覆盖新状态
let loadSeq = 0
const loadUsageCache = async () => {
  try {
    if (controller.signal.aborted) return
    const list = await request('/admin/groups/usage-summary', { method: 'GET', signal: controller.signal })
    const map = new Map()
    for (const it of list || []) map.set(it.group_id, it)
    usageMap.value = map
    usageCacheAt = Date.now()
  } catch { /* 静默 */ }
}

const load = async () => {
  // 请求序号：快速搜索/排序/翻页时，旧响应不得覆盖新状态。
  if (controller.signal.aborted) return
  listController?.abort(); listController = new AbortController()
  const seq = ++loadSeq
  loading.value = true
  try {
    const data = await listGroups(page.value, pageSize.value, {
      search: fSearch.value.trim(), platform: fPlatform.value, status: fStatus.value,
      is_exclusive: fExclusive.value, sort_by: sortState.sort_by, sort_order: sortState.sort_order,
    }, listController.signal)
    if (seq !== loadSeq) return
    rows.value = data.items || []
    total.value = data.total || 0
    err.value = ''
  } catch (e) {
    if (seq !== loadSeq || isCancellation(e)) return
    err.value = e.message
    rows.value = []; total.value = 0
  } finally {
    if (seq === loadSeq) loading.value = false
  }
  if (seq !== loadSeq) return
  // 容量随分组变化，需跟列表刷新；用量汇总与筛选无关，改用 TTL 避免每次搜索/排序都重复请求。
  loadCapacity()
  if (Date.now() - usageCacheAt > USAGE_CACHE_MS) loadUsageCache()
}

// 搜索 300ms 防抖（旧 handleSearch 等价）
let searchTimer = null
const onSearchInput = () => {
  clearTimeout(searchTimer)
  searchTimer = setTimeout(() => { page.value = 1; load() }, 300)
}
const onFilter = () => { clearTimeout(searchTimer); page.value = 1; load() }
// 表头排序/翻页/每页条数统一走 table @change（避免 pagination 回调 + change 双载）
const onTableChange = (pg, _filters, sorter) => {
  if (sorter && sorter.order) {
    // 后端同时支持 subscription_type/billing_type，倍率使用 rate_multiplier，不能发送显示用的 rate。
    sortState.sort_by = sorter.field || sorter.column?.key
    sortState.sort_order = sorter.order === 'ascend' ? 'asc' : 'desc'
    page.value = 1
  } else if (sorter?.column) {
    sortState.sort_by = 'sort_order'; sortState.sort_order = 'asc'; page.value = 1
  }
  if (pg.pageSize !== pageSize.value) {
    // 每页条数：写 localStorage（旧 setPersistedPageSize 等价）
    pageSize.value = pg.pageSize; page.value = 1
    try { localStorage.setItem(PAGE_SIZE_KEY, String(pg.pageSize)) } catch { /* 隐私模式忽略 */ }
  } else if (pg.current !== page.value) {
    page.value = pg.current
  }
  clearTimeout(searchTimer)
  load()
}
onMounted(() => {
  load()
  // 容量 5s 轮询（旧 capacityRefreshTimer 等价），工具栏可关闭
  capacityTimer = setInterval(() => { if (!loading.value && capacityAuto.value && (context?.active.value ?? true) && !document.hidden) loadCapacity() }, 5000)
})
watch(() => context?.active.value, active => { if (active && capacityAuto.value) loadCapacity() })
onUnmounted(() => { controller.abort(); listController?.abort(); loadSeq += 1; clearTimeout(searchTimer); clearInterval(capacityTimer) })
watch(() => context?.target.value, async (target, _previous, onCleanup) => {
  if (!target?.id || controller.signal.aborted) return
  const targetController = new AbortController()
  onCleanup(() => targetController.abort())
  try { const group = await request(`/admin/groups/${target.id}`, { method: 'GET', signal: targetController.signal }); if (!controller.signal.aborted) openEdit(group) }
  catch (error) { if (!isCancellation(error)) message.error(`打开分组失败：${error.message}`) }
}, { immediate: true })

// ===== 删除差异化：订阅分组文案不同（旧 deleteConfirmMessage 5285 行 + zh.ts 2144） =====
const remove = (r) => {
  const isSub = r.subscription_type === 'subscription'
  Modal.confirm({
    getContainer: context?.getContainer,
    title: isSub ? '删除订阅分组' : '删除分组',
    content: isSub
      ? `确定要删除订阅分组 '${r.name}' 吗？此操作会让所有绑定此订阅的用户的 API Key 失效，并删除所有相关的订阅记录。此操作无法撤销。`
      : `确定要删除分组 '${r.name}' 吗？所有关联的 API 密钥将不再属于任何分组。`,
    okText: '删除', okType: 'danger', cancelText: '取消',
    onOk: async () => {
      try {
        await request(`/admin/groups/${r.id}`, { method: 'DELETE', signal: controller.signal })
        await load()
      } catch (e) { if (!isCancellation(e)) message.error(`删除失败：${e.message}`) }
    },
  })
}

// ===== 复制分组：Idempotency-Key 头（groupApi.duplicateGroup），成功后直接打开副本编辑 =====
const duplicatingIds = reactive(new Set())
const confirmDuplicate = (g) => {
  if (duplicatingIds.has(g.id)) return
  Modal.confirm({
    getContainer: context?.getContainer,
    title: '确认复制分组',
    content: `将复制「${g.name}」的全部计价、模型路由和 ${g.account_count ?? 0} 个账号优先级（平台：${g.platform}）。副本默认停用，成功后会自动打开编辑页。`,
    okText: '复制并检查配置', cancelText: '取消',
    onOk: async () => {
      duplicatingIds.add(g.id)
      try {
        const dup = await duplicateGroup(g.id, controller.signal)
        message.success(`分组已复制为「${dup.name}」，当前保持停用，请确认配置后再启用`)
        await load()
        await openEdit(dup) // 副本默认停用：直接进入编辑引导确认（旧 confirmDuplicate 等价）
      } catch (e) {
        if (!isCancellation(e)) message.error(`复制分组失败：${e.message}`)
      } finally { duplicatingIds.delete(g.id) }
    },
  })
}

// ===== 4 个操作弹窗 + 排序弹窗的挂载状态 =====
const ratesOpen = ref(false); const ratesGroup = ref(null)
const rpmOpen = ref(false); const rpmGroup = ref(null)
const bindOpen = ref(false); const bindGroup = ref(null)
const routesOpen = ref(false); const routesGroup = ref(null)
const insOpen = ref(false); const insGroup = ref(null)
const sortOpen = ref(false)
const openWith = (key, r) => {
  if (key === 'rates') { ratesGroup.value = r; ratesOpen.value = true }
  else if (key === 'rpm') { rpmGroup.value = r; rpmOpen.value = true }
  else if (key === 'bind') { bindGroup.value = r; bindOpen.value = true }
  else if (key === 'routes') { routesGroup.value = r; routesOpen.value = true }
  else if (key === 'sort') sortOpen.value = true
  else if (key === 'ins') { insGroup.value = r; insOpen.value = true }
}
// 「更多」下拉：低频行操作收纳（Win12 规范）
const onMoreMenu = ({ key }, r) => {
  if (key === 'dup') confirmDuplicate(r)
  else if (key === 'del') remove(r)
  else openWith(key, r)
}

/* 用量汇总弹窗：今日/累计 请求、成本、改道（一期保留） */
const sumOpen = ref(false)
const sumLoading = ref(false)
const sumRows = ref([])
const summaryRequests = useGroupDialogRequests({ get open() { return sumOpen.value } })
watch(sumOpen, open => { if (!open) { sumRows.value = []; sumLoading.value = false } })
const fmt$ = (n) => `$${parseFloat(Number(n || 0).toFixed(4))}`
const openSummary = async () => {
  sumOpen.value = true
  sumLoading.value = true
  const turn = summaryRequests.begin('summary')
  try {
    const list = await request('/admin/groups/usage-summary', { method: 'GET', signal: turn.signal })
    if (!turn.isCurrent()) return
    const nameOf = (id) => rows.value.find((g) => g.id === id)?.name || `分组 ${id}`
    sumRows.value = (list || []).map((s) => ({ ...s, name: nameOf(s.group_id) }))
      .sort((a, b) => (b.total_cost || 0) - (a.total_cost || 0))
  } catch (e) {
    if (turn.isCurrent() && !isCancellation(e)) { message.error(`获取汇总失败：${e.message}`); sumRows.value = [] }
  } finally { if (turn.isCurrent()) sumLoading.value = false }
}
</script>

<template>
  <div ref="root" class="groups w12-design">
    <W12Toolbar aria-label="分组操作">
      <div class="w12-toolbar-group"><W12Button variant="primary" @click="openCreate">新增分组</W12Button><W12Button @click="openWith('sort')"><SortAscendingOutlined />排序</W12Button></div>
      <div class="w12-toolbar-group search"><W12Input v-model:value="fSearch" placeholder="搜索名称/描述" aria-label="搜索分组" allow-clear @input="onSearchInput" @press-enter="onFilter"><template #suffix><SearchOutlined class="suf-ico" @click="onFilter" /></template></W12Input></div>
      <div class="w12-toolbar-group"><W12Button :aria-expanded="filterOpen" @click="filterOpen = !filterOpen">筛选{{ filtersCount ? `（${filtersCount}）` : '' }}</W12Button><W12Button @click="columnsOpen = true">显示列</W12Button><W12Button @click="openSummary">用量汇总</W12Button><W12Button @click="load"><SyncOutlined />刷新</W12Button></div>
    </W12Toolbar>
    <div v-if="filterOpen || !narrow" class="group-filters">
      <W12Select v-model:value="fPlatform" :options="platformFilterOptions" aria-label="平台筛选" @change="onFilter" />
      <W12Select v-model:value="fStatus" :options="statusFilterOptions" aria-label="状态筛选" @change="onFilter" />
      <W12Select v-model:value="fExclusive" :options="exclusiveFilterOptions" aria-label="独占筛选" @change="onFilter" />
    </div>
    <div class="group-summary"><span>共 {{ total }} 个分组</span><W12Button variant="text" compact :aria-pressed="capacityAuto" @click="capacityAuto = !capacityAuto">容量自动刷新{{ capacityAuto ? ' · 5秒' : ' · 已关闭' }}</W12Button><span v-if="capacityUpdatedAt">更新于 {{ capacityUpdatedAt.toLocaleTimeString() }}</span><span v-if="capacityError" class="error">{{ capacityError }}</span></div>
    <p v-if="err" class="load-error" role="alert">{{ err }}<W12Button compact @click="load">重试</W12Button></p>
    <W12Table v-if="!narrow"
      :columns="columns" :data-source="rows" :loading="loading" row-key="id" size="small" :scroll="{ x: 'max-content' }"
      :pagination="{
        current: page, pageSize, total, showSizeChanger: true, pageSizeOptions: ['10', '20', '50', '100'],
      }"
      @change="onTableChange"
    >
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'name'"><span class="group-name">{{ record.name }}</span><span class="name-labels"><W12Status v-if="record.local_only" tone="info">本地</W12Status><W12Status v-if="record.allow_blocked_keyword_requests">关键词放行</W12Status><W12Status v-if="record.minimum_tier != null">T{{ record.minimum_tier }}</W12Status></span></template>
        <template v-else-if="column.key === 'account_count'"><span>{{ record.account_count || 0 }} 个</span><span class="account-counts">正常 {{ record.active_account_count || 0 }} · 限流 {{ record.rate_limited_account_count || 0 }}</span></template>
        <template v-else-if="column.key === 'rate'">
          <W12Status tone="info">×{{ record.rate_multiplier }}</W12Status>
        </template>
        <template v-else-if="column.key === 'billing_type'">
          <W12Status>{{ record.subscription_type === 'subscription' ? '订阅' : '标准' }}</W12Status>
        </template>
        <template v-else-if="column.key === 'is_exclusive'">
          <W12Status>{{ record.is_exclusive ? '独占' : '公开' }}</W12Status>
        </template>
        <template v-else-if="column.key === 'capacity'">
          <!-- 入口请求与账号容量分别采集，不能把账号并发当成分组入口请求数。 -->
          <div v-if="capacityMap.get(record.id)" class="cap2" :title="fmtCap(record)">
            <div class="cap2-row">
              <span class="cap2-k">并发</span>
              <div class="cap2-bar"><i :style="capStyle(cOf(record, 'concurrency'))"></i></div>
              <span class="cap2-v">{{ capText(cOf(record, 'concurrency')) }}</span>
            </div>
            <div class="cap2-row"><span class="cap2-k">会话</span><div class="cap2-bar"><i :style="capStyle(cOf(record, 'sessions'))"></i></div><span class="cap2-v">{{ capText(cOf(record, 'sessions')) }}</span></div>
            <div class="cap2-row">
              <span class="cap2-k">RPM</span>
              <div class="cap2-bar"><i :style="capStyle(cOf(record, 'rpm'))"></i></div>
              <span class="cap2-v">{{ capText(cOf(record, 'rpm')) }}</span>
            </div>
          </div>
          <span v-else class="dim">—</span>
        </template>
        <template v-else-if="column.key === 'ingress_requests'"><span class="cap2-live" :class="{ live: (capacityMap.get(record.id)?.ingress_requests_used || 0) > 0 }">{{ capacityMap.get(record.id)?.ingress_requests_used ?? '—' }}</span></template>
        <template v-else-if="column.key === 'usage'">
          <span v-if="!usageMap.get(record.id)" class="dim">—</span>
          <span v-else class="usage">${{ Number(usageMap.get(record.id).today_cost || 0).toFixed(2) }} / ${{ Number(usageMap.get(record.id).total_cost || 0).toFixed(2) }}<small>{{ (usageMap.get(record.id).today_requests || 0).toLocaleString() }} / {{ (usageMap.get(record.id).total_requests || 0).toLocaleString() }} 次请求</small></span>
        </template>
        <template v-else-if="column.key === 'status'">
          <W12Status :tone="record.status === 'active' ? 'success' : 'error'">{{ record.status === 'active' ? '启用' : '停用' }}</W12Status>
        </template>
        <template v-else-if="column.key === 'action'">
          <GroupRowActions :group="record" :duplicating="duplicatingIds.has(record.id)" @edit="openEdit" @more="onMoreMenu" />
        </template>
      </template>
    </W12Table>
    <template v-else>
      <div class="mobile-sort"><W12Select :value="sortState.sort_by" aria-label="分组排序字段" :options="[{ value: 'sort_order', label: '自定义顺序' }, ...allColumns.filter(column => column.sorter).map(column => ({ value: column.dataIndex || column.key, label: column.title }))]" @change="value => { sortState.sort_by = value; onFilter() }" /><W12Button @click="sortState.sort_order = sortState.sort_order === 'asc' ? 'desc' : 'asc'; onFilter()">{{ sortState.sort_order === 'asc' ? '升序' : '降序' }}</W12Button></div>
      <GroupMobileList v-if="rows.length" :rows="rows" :loading="loading" :usage-map="usageMap" :capacity-map="capacityMap" :duplicating-ids="duplicatingIds" @edit="openEdit" @more="onMoreMenu" />
      <W12EmptyState v-else :loading="loading" :error="err" text="暂无匹配分组" @retry="load" />
      <a-pagination class="mobile-pagination" :current="page" :page-size="pageSize" :total="total" simple show-size-changer :page-size-options="['10','20','50','100']" @change="(current, size) => onTableChange({ current, pageSize: size })" />
    </template>

    <W12Dialog v-model:open="columnsOpen" title="显示列" :footer="null" width="420px"><a-checkbox-group v-model:value="selectedColumns" :options="columnOptions" class="column-options" /></W12Dialog>
    <!-- 新增/编辑弹窗：collapse 分区（2A 全部保留） -->
    <GroupEditorModal :editor="editor" />
    <!-- 4 个操作弹窗 + 排序弹窗 -->
    <GroupRateMultipliersModal v-model:open="ratesOpen" :group="ratesGroup" @saved="load" />
    <GroupRPMOverridesModal v-model:open="rpmOpen" :group="rpmGroup" @saved="load" />
    <GroupBindAccountsModal v-model:open="bindOpen" :group="bindGroup" @saved="load" />
    <GroupCompositeRoutesModal v-model:open="routesOpen" :group="routesGroup" @changed="load" />
    <GroupSortOrderModal v-model:open="sortOpen" @saved="load" />
    <GroupInsightsModal v-model:open="insOpen" :group="insGroup" />

    <!-- 用量汇总：今日/累计 请求与成本（一期保留） -->
    <W12Dialog v-model:open="sumOpen" title="分组用量汇总（今日起）" :footer="null" width="640px">
      <a-spin :spinning="sumLoading">
        <W12Table
          :columns="[
            { title: '分组', dataIndex: 'name', key: 'name', ellipsis: true },
            { title: '今日请求', key: 'tr', width: 90 },
            { title: '累计请求', key: 'cr', width: 90 },
            { title: '今日成本', key: 'tc', width: 100 },
            { title: '累计成本', key: 'cc', width: 110 },
            { title: '今日改道', key: 'todayRerouted', width: 90 },
            { title: '累计改道', key: 'rr', width: 90 },
          ]"
          :data-source="sumRows" row-key="group_id" size="small" :pagination="false" :scroll="{ x: 760, y: 380 }"
        >
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'tr'">{{ (record.today_requests || 0).toLocaleString('zh-CN') }}</template>
            <template v-else-if="column.key === 'cr'">{{ (record.total_requests || 0).toLocaleString('zh-CN') }}</template>
            <template v-else-if="column.key === 'tc'">{{ fmt$(record.today_cost) }}</template>
            <template v-else-if="column.key === 'cc'">{{ fmt$(record.total_cost) }}</template>
            <template v-else-if="column.key === 'todayRerouted'">{{ (record.today_rerouted_requests || 0).toLocaleString('zh-CN') }}</template>
            <template v-else-if="column.key === 'rr'">{{ (record.total_rerouted_requests || 0).toLocaleString('zh-CN') }}</template>
          </template>
        </W12Table>
      </a-spin>
    </W12Dialog>
  </div>
</template>

<style scoped src="../components/group/groupsList.css"></style>


