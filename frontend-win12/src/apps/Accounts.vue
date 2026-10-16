<script setup>
import W12Input from '../components/design/W12Input.vue'
import W12Select from '../components/design/W12Select.vue'
import W12Button from '../components/design/W12Button.vue'
import { SearchOutlined } from '@ant-design/icons-vue'
import { confirmAction } from '../utils/confirmAction'
/* 账号管理（三期收尾）
 * 一二期保留：编辑 8 字段 / 批量编辑（BulkEditModal）/ 行操作全套 / OAuth（OAuthCreateModal）/
 *   SSE 连通性测试（TestModal）/ 批量 Key 粘贴 / 模型健康 / 计费探测 / 影子账号 / CN 供应商。
 * 三期新增（对照旧 AccountsView.vue + admin/account/*）：
 *   A 表格列补齐（17 列等价，AccountsTable + 状态/容量单元格组件）
 *   B 列显隐设置（localStorage：w12-account-hidden-columns）
 *   C 服务端排序（?sort_by&sort_order）+ ETag 自动刷新（If-None-Match / 304 / 5/10/15/30s）
 *   D 导入导出等价（ImportExportModal：TOTP step-up 导出 + 文件预览导入）
 *   E 工具对话框（错误透传 / TLS 指纹 / Grok OAuth 对账 / CRS 同步）
 *   F 定时测试增强（ScheduledTestsModal：编辑 / 启停 toggle / 最近结果）
 *   G Grok 测试模式（TestModal：text/image/video/search/tts/stt/realtime + 上传） */
import { ref, computed, onMounted } from 'vue'
import { request as apiRequest, isCancellation } from '../api'
import { useRequestLifetime } from '../composables/useRequestLifetime'
import { message } from 'ant-design-vue'
import dayjs from 'dayjs'
import {
  DownOutlined, SyncOutlined, EyeOutlined, ToolOutlined,
} from '@ant-design/icons-vue'
import AccountEditorModal from '../components/account/AccountEditorModal.vue'
import QuotaModal from '../components/account/QuotaModal.vue'
import ShadowsModal from '../components/account/ShadowsModal.vue'
import HealthModal from '../components/account/HealthModal.vue'
import BulkApiKeyModal from '../components/account/BulkApiKeyModal.vue'
import { quotaPath } from '../components/account/quotaApi'
import BulkEditModal from '../components/account/BulkEditModal.vue'
import StatsModal from '../components/account/StatsModal.vue'
import TempUnschedModal from '../components/account/TempUnschedModal.vue'
import ReAuthModal from '../components/account/ReAuthModal.vue'
import OAuthCreateModal from '../components/account/OAuthCreateModal.vue'
import AccountsTable from '../components/account/AccountsTable.vue'
import TestModal from '../components/account/TestModal.vue'
import UsageModal from '../components/account/UsageModal.vue'
import ScheduledTestsModal from '../components/account/ScheduledTestsModal.vue'
import ImportExportModal from '../components/account/ImportExportModal.vue'
import ErrorPassthroughModal from '../components/account/ErrorPassthroughModal.vue'
import TlsFingerprintModal from '../components/account/TlsFingerprintModal.vue'
import GrokReconcileModal from '../components/account/GrokReconcileModal.vue'
import CrsSyncModal from '../components/account/CrsSyncModal.vue'
import AccountOpsModal from '../components/account/AccountOpsModal.vue'
import BatchAccountOpsModal from '../components/account/BatchAccountOpsModal.vue'
import { useAccountsQuery } from '../composables/useAccountsQuery'

const isAnyModalOpen = computed(() => (
  auxModalOpen.value || editorOpen.value || oauthOpen.value || bulkEditOpen.value ||
  opsOpen.value || batchOpsOpen.value || statsOpen.value || tempOpen.value || reauthOpen.value ||
  testOpen.value || plansOpen.value || ieOpen.value || epOpen.value || tlsOpen.value ||
  grokRecOpen.value || crsOpen.value || usageOpen.value
))
const {
  loading, statsEpoch, rows, total, page, pageSize, fPlatform, fStatus, fSearch, fType, fGroup, fPrivacy, fWsProbe,
  selectedIds, selectedRows, err, buildFilters, sortKey, sortOrder, onSort, hiddenKeys, toggleColumn,
  load, onFilter, onPage, autoEnabled, autoInterval, autoCountdown, autoFetching, setAutoEnabled, setAutoIntervalSec,
  enterSilent, ALL_COLUMNS, AUTO_REFRESH_INTERVALS,
} = useAccountsQuery(isAnyModalOpen)

const lifetime = useRequestLifetime(() => {
  editorRow.value = statsRow.value = tempRow.value = reauthRow.value = testRow.value = usageRow.value = plansRow.value = opsRow.value = null
  probing.value = {}; wsProbing.value = {}
  groupOptions.value = []; proxyOptions.value = []; editOptionsLoaded = false
  for (const open of [editorOpen, oauthOpen, bulkEditOpen, opsOpen, batchOpsOpen, statsOpen, tempOpen, reauthOpen,
    testOpen, plansOpen, ieOpen, epOpen, tlsOpen, grokRecOpen, crsOpen, usageOpen]) open.value = false
})
const request = (path, options) => apiRequest(path, { ...options, ...lifetime })

// ===== 行操作（一二期保留 + 三期 schedulable 开关 / 代理回退还原） =====
const toggle = async (r) => {
  const next = r.status === 'active' ? 'inactive' : 'active'
  try {
    await request(`/admin/accounts/${r.id}`, { method: 'PUT', body: { status: next } })
    message.success(next === 'active' ? '已启用' : '已停用')
    enterSilent()
    await load()
  } catch (e) { if (!isCancellation(e)) message.error(e.message) }
}
// 表格内可调度开关（旧 handleToggleSchedulable：POST :id/schedulable）
const toggleSchedulable = async (r) => {
  try {
    await request(`/admin/accounts/${r.id}/schedulable`, { method: 'POST', body: { schedulable: !!r.__next } })
    const i = rows.value.findIndex((x) => x.id === r.id)
    if (i !== -1) rows.value[i] = { ...rows.value[i], schedulable: !!r.__next }
    enterSilent()
    message.success(r.__next ? '已参与调度' : '已暂停调度')
  } catch (e) { if (!isCancellation(e)) message.error(`切换失败：${e.message}`) }
}
// 代理回退还原（旧 onRevertFallback：POST :id/revert-proxy-fallback）
const revertProxy = async (r) => {
  try {
    await request(`/admin/accounts/${r.id}/revert-proxy-fallback`, { method: 'POST' })
    message.success('已还原为原代理')
    await load()
  } catch (e) { if (!isCancellation(e)) message.error(`还原失败：${e.message}`) }
}
const duplicate = async (r) => {
  try {
    await request(`/admin/accounts/${r.id}/duplicate`, { method: 'POST' })
    message.success('已复制')
    await load()
  } catch (e) { if (!isCancellation(e)) message.error(`复制失败：${e.message}`) }
}
const clearError = async (r) => {
  try {
    await request(`/admin/accounts/${r.id}/clear-error`, { method: 'POST' })
    message.success('已清除错误状态')
    await load()
  } catch (e) { if (!isCancellation(e)) message.error(e.message) }
}
const clearRateLimit = async (r) => {
  try {
    await request(`/admin/accounts/${r.id}/clear-rate-limit`, { method: 'POST' })
    message.success('已清除限流冷却')
    await load()
  } catch (e) { if (!isCancellation(e)) message.error(e.message) }
}
const remove = async (r) => {
  if (!(await confirmAction({ title: '删除账号', content: `确认删除账号「${r.name}」？此操作不可恢复。`, okText: '删除', danger: true }))) return
  try {
    await request(`/admin/accounts/${r.id}`, { method: 'DELETE' })
    message.success('已删除')
    await load()
  } catch (e) { if (!isCancellation(e)) message.error(`删除失败：${e.message}`) }
}

// 「更多」菜单分发（可见性条件已收进 AccountsTable）
const rowMenu = (key, r) => {
  if (key === 'toggle') toggle(r)
  else if (key === 'schedulable') toggleSchedulable(r)
  else if (key === 'revertproxy') revertProxy(r)
  else if (key === 'duplicate') duplicate(r)
  else if (key === 'health') openHealth(r)
  else if (key === 'billing') probeBilling(r)
  else if (key === 'clearerr') clearError(r)
  else if (key === 'clearrl') clearRateLimit(r)
  else if (key === 'quota') queryQuota(r)
  else if (key === 'refresh') refreshToken(r)
  else if (key === 'resetquota') resetQuota(r)
  else if (key === 'plans') { plansRow.value = r; plansOpen.value = true }
  else if (key === 'cnquota') queryCn(r, 'quota')
  else if (key === 'cnbalance') queryCn(r, 'balance')
  else if (key === 'shadows') openShadows(r)
  else if (key === 'stats') openStats(r)
  else if (key === 'wsprobe') wsProbe(r)
  else if (key === 'reauth') openReauth(r)
  else if (key === 'refreshtoken') refreshCredentials(r)
  else if (key === 'ops') { opsRow.value = r; opsOpen.value = true }
  else if (key === 'recover') recoverState(r)
  else if (key === 'privacy') setPrivacy(r)
  else if (key === 'tempunsched') openTempUnsched(r)
  else if (key === 'del') remove(r)
}

// ===== 批量（一二期保留） =====
const batchClearError = async () => {
  if (!selectedIds.value.length) { message.warning('请先勾选账号'); return }
  try {
    const res = await request('/admin/accounts/batch-clear-error', { method: 'POST', body: { account_ids: selectedIds.value } })
    message.success(`批量清错完成：成功 ${res?.success ?? selectedIds.value.length} 个`)
    await load()
  } catch (e) { if (!isCancellation(e)) message.error(e.message) }
}
const batchRefresh = async () => {
  if (!selectedIds.value.length) { message.warning('请先勾选账号'); return }
  try {
    const res = await request('/admin/accounts/batch-refresh', { method: 'POST', body: { account_ids: selectedIds.value } })
    message.success(`批量刷新已触发：${res?.success ?? selectedIds.value.length} 个`)
    await load()
  } catch (e) { if (!isCancellation(e)) message.error(e.message) }
}
const batchDelete = async () => {
  if (!selectedIds.value.length) { message.warning('请先勾选账号'); return }
  if (!(await confirmAction({ title: '批量删除账号', content: `确认删除选中的 ${selectedIds.value.length} 个账号？此操作不可恢复。`, okText: '删除', danger: true }))) return
  let ok = 0, fail = 0
  for (const id of selectedIds.value) {
    try { await request(`/admin/accounts/${id}`, { method: 'DELETE' }); ok++ } catch (e) { if (isCancellation(e)) return; fail++ }
  }
  if (fail) message.warning(`删除完成：成功 ${ok}，失败 ${fail}`)
  else message.success(`已删除 ${ok} 个账号`)
  selectedIds.value = []
  await load()
}
const batchSchedulable = async (val) => {
  if (!selectedIds.value.length) { message.warning('请先勾选账号'); return }
  try {
    const res = await request('/admin/accounts/bulk-update', { method: 'POST', body: { account_ids: selectedIds.value, schedulable: val } })
    message.success(`${val ? '已启用调度' : '已停用调度'}：${res?.success ?? selectedIds.value.length} 个`)
    await load()
  } catch (e) { if (!isCancellation(e)) message.error(e.message) }
}
const batchMenu = (key) => {
  if (key === 'delete') batchDelete()
  else if (key === 'clear') batchClearError()
  else if (key === 'refresh') batchRefresh()
  else if (key === 'sched-on') batchSchedulable(true)
  else if (key === 'sched-off') batchSchedulable(false)
  else if (key === 'ops-batch') batchOpsOpen.value = true
}

// ===== 编辑 / 新增（一二期保留） =====
const editorOpen = ref(false)
const editorRow = ref(null)
const groupOptions = ref([])
const proxyOptions = ref([])
// 分组筛选选项（复用编辑器分组选项源；含「未分组」圈选——后端约定值 ungrouped）
const groupFilterOptions = computed(() => [
  { value: '', label: '全部分组' },
  { value: 'ungrouped', label: '未分组' },
  ...groupOptions.value.map((g) => ({ value: String(g.id), label: g.name })),
])
let editOptionsLoaded = false
const loadEditOptions = async () => {
  try {
    const [gs, ps] = await Promise.all([
      request('/admin/groups/all', { method: 'GET' }),
      request('/admin/proxies/all', { method: 'GET' }),
    ])
    groupOptions.value = gs || []
    proxyOptions.value = ps || []
    editOptionsLoaded = true
  } catch (e) {
    if (!isCancellation(e)) message.warning(`分组/代理选项加载失败：${e.message}（仍可编辑其他字段）`)
  }
}
const openCreate = () => {
  if (!editOptionsLoaded) loadEditOptions()
  editorRow.value = null
  editorOpen.value = true
}
const openEdit = (r) => {
  if (!editOptionsLoaded) loadEditOptions()
  editorRow.value = r
  editorOpen.value = true
}
// ===== 弹窗转发：健康/配额/影子逻辑在子组件（HealthModal/QuotaModal/ShadowsModal） =====
const healthRef = ref(null)
const quotaRef = ref(null)
const shadowsRef = ref(null)
const bulkKeyRef = ref(null)
const openHealth = (r) => healthRef.value.open(r)
const queryQuota = (r) => quotaRef.value.query(r)
const queryCn = (r, kind) => quotaRef.value.queryCn(r, kind)
const openShadows = (r) => shadowsRef.value.open(r)
const auxModalOpen = ref(false)
const onAuxOpenChange = (v) => { auxModalOpen.value = v }

/* 计费探测：POST 探测上游计费结构，成功后进入静默窗避免轮询闪烁 */
const probing = ref({})
const probeBilling = async (r) => {
  probing.value = { ...probing.value, [r.id]: true }
  try {
    const res = await request(`/admin/accounts/${r.id}/upstream-billing-probe`, { method: 'POST' })
    message.success(`计费探测完成：${JSON.stringify(res).slice(0, 80)}`)
    enterSilent()
  } catch (e) { if (!isCancellation(e)) message.error(`探测失败：${e.message}`) } finally {
    probing.value = { ...probing.value, [r.id]: false }
  }
}

// 配额 / Token 刷新 / 配额重置（openai / grok 按平台路由；quotaPath 见 quotaApi.js）
const refreshToken = async (r) => {
  try {
    await quotaPath(r, 'refresh', undefined, lifetime)
    message.success('Token 已刷新')
    await load()
  } catch (e) { if (!isCancellation(e)) message.error(`刷新失败：${e.message}`) }
}
const resetQuota = async (r) => {
  try {
    await quotaPath(r, 'reset-quota', undefined, lifetime)
    message.success('配额已重置')
    await load()
  } catch (e) { if (!isCancellation(e)) message.error(`重置失败：${e.message}`) }
}
// ===== 二期行操作弹窗（保留） =====
const statsOpen = ref(false)
const statsRow = ref(null)
const tempOpen = ref(false)
const tempRow = ref(null)
const reauthOpen = ref(false)
const reauthRow = ref(null)
const wsProbing = ref({})
const openStats = (r) => { statsRow.value = r; statsOpen.value = true }
const openTempUnsched = (r) => { tempRow.value = r; tempOpen.value = true }
const openReauth = (r) => { reauthRow.value = r; reauthOpen.value = true }
const isFuture = (t) => t && new Date(t).getTime() > Date.now()
const isRecoverable = (r) => r.status === 'error' || isFuture(r.rate_limit_reset_at) || isFuture(r.overload_until) || isFuture(r.temp_unschedulable_until)
const wsProbe = async (r) => {
  wsProbing.value = { ...wsProbing.value, [r.id]: true }
  try {
    const res = await request(`/admin/accounts/${r.id}/ws-probe`, { method: 'POST' })
    message.success(`WS 探测完成：${res?.status}${res?.detail ? `（${res.detail}）` : ''}`)
    enterSilent()
    await load()
  } catch (e) { if (!isCancellation(e)) message.error(`WS 探测失败：${e.message}`) } finally {
    wsProbing.value = { ...wsProbing.value, [r.id]: false }
  }
}
const recoverState = async (r) => {
  try {
    await request(`/admin/accounts/${r.id}/recover-state`, { method: 'POST' })
    message.success('已恢复调度状态')
    enterSilent()
    await load()
  } catch (e) { if (!isCancellation(e)) message.error(`恢复失败：${e.message}`) }
}
const setPrivacy = async (r) => {
  try {
    const updated = await request(`/admin/accounts/${r.id}/set-privacy`, { method: 'POST' })
    const mode = updated?.extra?.privacy_mode || ''
    if (r.platform === 'openai') {
      if (mode === 'training_off') message.success('隐私已开启（关闭训练）')
      else if (mode === 'training_set_cf_blocked') message.warning('隐私已设置，但被 Cloudflare 拦截，请检查账号')
      else message.error('隐私设置未生效')
    } else if (mode === 'privacy_set') message.success('隐私已开启')
    else message.error('隐私设置未生效')
    enterSilent()
    await load()
  } catch (e) { if (!isCancellation(e)) message.error(`隐私设置失败：${e.message}`) }
}
const refreshCredentials = async (r) => {
  try {
    await request(`/admin/accounts/${r.id}/refresh`, { method: 'POST' })
    message.success('Token 已刷新')
    enterSilent()
    await load()
  } catch (e) { if (!isCancellation(e)) message.error(`刷新失败：${e.message}`) }
}

// ===== 三期弹窗入口 =====
const oauthOpen = ref(false)
const bulkEditOpen = ref(false)
const testOpen = ref(false)
const testRow = ref(null)
const usageOpen = ref(false)
const usageRow = ref(null)
const plansOpen = ref(false)
const plansRow = ref(null)
const ieOpen = ref(false)
const ieMode = ref('export')
const epOpen = ref(false)
const tlsOpen = ref(false)
const grokRecOpen = ref(false)
const crsOpen = ref(false)
// 账号运维（今日统计 / 刷新档位 / 应用 OAuth 凭据）与批量运维（批量刷新档位 / 批量改凭据字段）
const opsOpen = ref(false)
const opsRow = ref(null)
const batchOpsOpen = ref(false)

const openTest = (r) => { testRow.value = r; testOpen.value = true }
const openUsage = (r) => { usageRow.value = r; usageOpen.value = true }
const openPlans = (r) => { plansRow.value = r; plansOpen.value = true }
const openIe = (mode) => { ieMode.value = mode; ieOpen.value = true }
const onImported = () => load()

// 计划测试打开包装（行菜单 plans 传账号）
const planTargetAccount = computed(() => plansRow.value)
onMounted(() => { if (!editOptionsLoaded) loadEditOptions() })
</script>

<template>
  <div class="acc w12-design">
    <!-- 工具栏：单行三分区（操作组｜筛选组｜右侧视图组），组间 16px，底部 1px 分割线。
         批量操作仅在有选中时出现；导出/导入收进「更多工具」，避免窄窗口换行破碎 -->
    <div class="toolbar">
      <div class="tgroup">
        <W12Button variant="primary" @click="openCreate">新增账号</W12Button>
        <W12Button @click="oauthOpen = true">OAuth 接入</W12Button>
        <W12Button @click="bulkKeyRef?.open()">批量添加</W12Button>
        <!-- 批量操作统一收进下拉：选中后才可用（低频操作不上工具栏平面） -->
        <a-dropdown v-if="selectedIds.length" :trigger="['click']">
          <W12Button>批量操作（{{ selectedIds.length }}）<DownOutlined /></W12Button>
          <template #overlay>
            <a-menu @click="({ key }) => { if (key === 'edit') bulkEditOpen = true; else batchMenu(key) }">
              <a-menu-item key="edit">批量编辑…</a-menu-item>
              <a-menu-item key="clear">清除错误状态</a-menu-item>
              <a-menu-item key="refresh">刷新用量与状态</a-menu-item>
              <a-menu-divider />
              <a-menu-item key="sched-on">启用调度</a-menu-item>
              <a-menu-item key="sched-off">停用调度</a-menu-item>
              <a-menu-divider />
              <a-menu-item key="delete" danger>批量删除</a-menu-item>
              <a-menu-item key="ops-batch">账号运维（批量刷新档位 / 改凭据）</a-menu-item>
            </a-menu>
          </template>
        </a-dropdown>
      </div>
      <span class="sep" />
      <div class="tgroup">
        <W12Select v-model:value="fPlatform" style="width: 108px" :options="[
          { value: '', label: '全部平台' },
          { value: 'openai', label: 'openai' },
          { value: 'anthropic', label: 'anthropic' },
          { value: 'gemini', label: 'gemini' },
          { value: 'grok', label: 'grok' },
          { value: 'antigravity', label: 'antigravity' },
          { value: 'kimi', label: 'kimi' },
          { value: 'zhipu', label: 'zhipu' },
          { value: 'deepseek', label: 'deepseek' },
          { value: 'opencode_go', label: 'OpenCode' },
        ]" @change="onFilter" />
        <W12Select v-model:value="fStatus" style="width: 100px" :options="[
          { value: '', label: '全部状态' },
          { value: 'active', label: 'active' },
          { value: 'inactive', label: 'inactive' },
          { value: 'error', label: 'error' },
        ]" @change="onFilter" />
        <!-- 补齐旧版筛选四件套（AccountTableFilters 等价）：类型/分组/隐私模式/WS 探测 -->
        <W12Select v-model:value="fType" style="width: 118px" :options="[
          { value: '', label: '全部类型' },
          { value: 'oauth', label: 'OAuth' },
          { value: 'setup-token', label: 'Setup Token' },
          { value: 'apikey', label: 'API Key' },
          { value: 'bedrock', label: 'AWS Bedrock' },
        ]" @change="onFilter" />
        <W12Select v-model:value="fGroup" style="width: 130px" :options="groupFilterOptions" show-search option-filter-prop="label" @change="onFilter" />
        <W12Select v-model:value="fPrivacy" style="width: 116px" :options="[
          { value: '', label: '全部隐私' },
          { value: '__unset__', label: '未设置' },
          { value: 'training_off', label: '已关训练' },
          { value: 'training_set_cf_blocked', label: 'CF 拦截' },
          { value: 'training_set_failed', label: '设置失败' },
        ]" @change="onFilter" />
        <!-- WS 探测仅对 OpenAI 平台账号有意义（平台筛选为空或 openai 时展示，旧 showWSProbe 同款） -->
        <W12Select v-if="!fPlatform || fPlatform === 'openai'" v-model:value="fWsProbe" style="width: 112px" :options="[
          { value: '', label: '全部 WS' },
          { value: 'supported', label: '支持 WS' },
          { value: 'unsupported', label: '不支持' },
          { value: 'unknown', label: '未知' },
          { value: '__unset__', label: '未探测' },
        ]" @change="onFilter" />
        <W12Input v-model:value="fSearch" placeholder="搜索名称" style="width: 160px" @press-enter="onFilter">
          <template #suffix><SearchOutlined class="suf-ico" @click="onFilter" /></template>
        </W12Input>
      </div>
      <div class="grow" />
      <!-- 视图组：不可拆块，窄窗口换行时整体掉行并保持右对齐，避免逐元素流式打散 -->
      <div class="viewbar">
        <span class="total">共 {{ total }} 个账号</span>
        <!-- 列显示设置（localStorage 持久化） -->
        <a-dropdown :trigger="['click']">
          <W12Button><EyeOutlined /> 显示列</W12Button>
          <template #overlay>
            <div class="colmenu" @click.stop>
              <div class="colmenu-title">显示列（{{ ALL_COLUMNS.length - hiddenKeys.length }}/{{ ALL_COLUMNS.length }}）</div>
              <div v-for="col in ALL_COLUMNS" :key="col.key" class="colmenu-item" @click="toggleColumn(col.key)">
                <span>{{ col.label }}</span>
                <span v-if="!hiddenKeys.includes(col.key)" class="check">✓</span>
              </div>
            </div>
          </template>
        </a-dropdown>
        <!-- 更多工具（导入导出 + 三期：CRS / 错误透传 / TLS 指纹 / Grok 对账） -->
        <a-dropdown :trigger="['click']">
          <W12Button><ToolOutlined /> 更多工具<DownOutlined /></W12Button>
          <template #overlay>
            <a-menu @click="({ key }) => { if (key === 'ie-export') openIe('export'); else if (key === 'ie-import') openIe('import'); else if (key === 'crs') crsOpen = true; else if (key === 'ep') epOpen = true; else if (key === 'tls') tlsOpen = true; else if (key === 'grokrec') grokRecOpen = true }">
              <a-menu-item key="ie-export" :disabled="!total">导出账号</a-menu-item>
              <a-menu-item key="ie-import">导入账号</a-menu-item>
              <a-menu-divider />
              <a-menu-item key="crs">从 CRS 同步</a-menu-item>
              <a-menu-item key="ep">错误透传规则</a-menu-item>
              <a-menu-item key="tls">TLS 指纹配置</a-menu-item>
              <a-menu-item key="grokrec">Grok OAuth 对账</a-menu-item>
            </a-menu>
          </template>
        </a-dropdown>
        <!-- ETag 自动刷新（旧 autoRefresh 下拉等价：开关 + 间隔 + 倒计时） -->
        <a-dropdown :trigger="['click']">
          <W12Button :variant="autoEnabled ? 'primary' : 'secondary'">
            <SyncOutlined :spin="autoFetching" />
            {{ autoEnabled ? `自动刷新 ${autoCountdown}s` : '自动刷新' }}
          </W12Button>
          <template #overlay>
            <div class="colmenu" @click.stop>
              <div class="colmenu-item" @click="setAutoEnabled(!autoEnabled)">
                <span>启用自动刷新</span>
                <span v-if="autoEnabled" class="check">✓</span>
              </div>
              <div class="colmenu-divider" />
              <div v-for="sec in AUTO_REFRESH_INTERVALS" :key="sec" class="colmenu-item" @click="setAutoIntervalSec(sec)">
                <span>{{ sec }} 秒</span>
                <span v-if="autoInterval === sec" class="check">✓</span>
              </div>
            </div>
          </template>
        </a-dropdown>
      </div>
      <W12Button v-if="err" compact @click="load" variant="danger">重试加载</W12Button>
    </div>

    <!-- 表格（三期：AccountsTable 全列 + 排序 + 徽章组） -->
    <AccountsTable
      v-model:selected-ids="selectedIds"
      :rows="rows" :loading="loading" :hidden-keys="hiddenKeys"
      :sort-key="sortKey" :sort-order="sortOrder"
      :page="page" :page-size="pageSize" :total="total" :refresh-seq="statsEpoch"
      @sort="onSort" @page="onPage" @menu="rowMenu"
      @edit="openEdit" @test="openTest" @usage="openUsage" @show-temp-unsched="openTempUnsched"
    />

    <!-- 配额查询结果：kv 展平展示 -->
    <QuotaModal ref="quotaRef" @open-change="onAuxOpenChange" />

    <!-- 创建/编辑统一编辑器（类别分段 + 分区导航 + Bedrock/Vertex 服务账号 + 分组模型映射） -->
    <AccountEditorModal v-model:open="editorOpen" :account="editorRow" :groups="groupOptions" :proxies="proxyOptions" @saved="load" />

    <ShadowsModal ref="shadowsRef" @changed="load" @open-change="onAuxOpenChange" />
    <HealthModal ref="healthRef" @open-change="onAuxOpenChange" />

    <!-- 三期子组件挂载 -->
    <OAuthCreateModal v-model:open="oauthOpen" @created="load" />
    <BulkEditModal v-model:open="bulkEditOpen" :rows="selectedRows" @updated="load" />
    <StatsModal v-model:open="statsOpen" :account="statsRow" />
    <TempUnschedModal v-model:open="tempOpen" :account="tempRow" @recovered="load" />
    <ReAuthModal v-model:open="reauthOpen" :account="reauthRow" @reauthorized="load" />
    <TestModal v-model:open="testOpen" :account="testRow" />
    <UsageModal v-model:open="usageOpen" :account="usageRow" />
    <ScheduledTestsModal v-model:open="plansOpen" :account="planTargetAccount" @changed="load" />
    <ImportExportModal v-model:open="ieOpen" v-model:mode="ieMode" :selected-ids="selectedIds" :filters="buildFilters()" @imported="onImported" />
    <ErrorPassthroughModal v-model:open="epOpen" />
    <TlsFingerprintModal v-model:open="tlsOpen" />
    <GrokReconcileModal v-model:open="grokRecOpen" @repaired="load" />
    <CrsSyncModal v-model:open="crsOpen" @synced="load" />
    <AccountOpsModal v-model:open="opsOpen" :account="opsRow" @saved="load" />
    <BatchAccountOpsModal v-model:open="batchOpsOpen" :account-ids="selectedIds" @saved="load" />

    <!-- 批量 API Key 粘贴创建（自包含：解析/逐条创建/结果汇总） -->
    <BulkApiKeyModal ref="bulkKeyRef" @created="load" @open-change="onAuxOpenChange" />
  </div>
</template>

<style scoped>
.acc { height: 100%; }
/* 工具栏：业务分组间分隔线，底部 1px 浅灰分割线，控件高统一 32px */
.toolbar {
  display: flex; align-items: center; gap: 12px; margin-bottom: 12px; padding-bottom: 12px;
  border-bottom: 1px solid var(--hr); flex-wrap: wrap;
}
.tgroup { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
/* 视图组（计数 + 显示列/更多工具/自动刷新）：flex:none 不可拆，
 * 窄窗口 flex-wrap 时整块掉行且 margin-left:auto 保持右对齐 */
.viewbar { display: flex; align-items: center; gap: 8px; flex: none; margin-left: auto; }
.toolbar .sep { width: 1px; height: 20px; background: var(--hr); flex: none; }
.toolbar .grow { flex: 1; }
.total { font-size: 12px; color: var(--text2); }
.two { display: grid; grid-template-columns: 1fr 1fr; gap: 0 12px; }
/* 列显示 / 自动刷新下拉面板（毛玻璃半透明、圆角 8px） */
.colmenu { background: var(--card); border-radius: 8px; box-shadow: 0 4px 16px rgba(0, 0, 0, 0.12); padding: 6px; min-width: 160px; max-height: 60vh; overflow-y: auto; }
.colmenu-title { font-size: 11px; color: var(--text2); padding: 6px 10px; }
.colmenu-item { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 6px 10px; border-radius: 6px; font-size: 13px; cursor: pointer; white-space: nowrap; }
.colmenu-item:hover { background: rgba(127, 127, 127, 0.1); }
.colmenu-item .check { color: #1668dc; }
.colmenu-divider { height: 1px; background: var(--hr); margin: 4px 0; }
</style>



