<script setup>
/* 代理管理 v2（对照旧 ProxiesView.vue 审计清单）：
 * - 【安全修复】编辑时必须回传 expires_at/fallback_mode/backup_proxy_id/expiry_warn_days——
 *   后端 UpdateProxy 对这 4 个字段是无条件覆盖（admin_service.go），缺了会把既有到期时间/
 *   回退配置清掉（旧版审计发现的数据破坏 BUG）
 * - 新增：过期时间（预设天数+日期）、回退模式+备用代理、临期提醒天数、协议补 socks5h
 * IPv6 出口自动管理卡已接入（components/proxy/IPv6EgressCard.vue）。
 * 已补齐审计清单全部 7 项：测速/质量检测/统计+引用账号/批量创建/导出/导入
 * 列表能力（对照后端 proxy_handler.go）：
 * - 筛选：protocol/status/search（search 后端 trim+≤100 截断，仅按 name ContainsFold 匹配）
 * - 排序：sort_by/sort_order 服务端白名单 name/protocol/status/created_at/expiry/account_count
 *   （默认 id desc）；表头点击走 antd sorter:true 受控列
 * - 批量删除：POST /admin/proxies/batch-delete {ids} → {deleted_ids, skipped:[{id,reason}]}；
 *   被账号引用的代理后端自动跳过（BatchDeleteProxies 逐个检查 CountAccountsByProxyID）
 * - 延迟列：latency_ms 为 *int64 omitempty（无值=未测），latency_status=failed 显示失败
 * 【安全】列表响应含明文 password（导出依赖），任何界面严禁渲染该字段 */
import { ref, reactive, computed, onMounted } from 'vue'
import { confirmAction } from '../utils/confirmAction'
import dayjs from 'dayjs'
import { request } from '../api'
import { FormOutlined, DeleteOutlined, ThunderboltOutlined, SafetyCertificateOutlined, InfoCircleOutlined, PlusOutlined, SearchOutlined } from '@ant-design/icons-vue'
import IPv6EgressCard from '../components/proxy/IPv6EgressCard.vue'
import { message } from 'ant-design-vue'

const loading = ref(false)
const rows = ref([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const err = ref('')

// ===== 服务端筛选/排序（参数名与后端 proxy_handler.go List 一致） =====
const fSearch = ref('')
const fProtocol = ref('')
const fStatus = ref('')
// 默认 id desc（后端 DefaultQuery 同款）；antd 表头点击受控刷新箭头
const sortState = reactive({ sort_by: 'id', sort_order: 'desc' })

const protocolFilterOptions = [
  { value: '', label: '全部协议' },
  { value: 'http', label: 'HTTP' }, { value: 'https', label: 'HTTPS' },
  { value: 'socks5', label: 'SOCKS5' }, { value: 'socks5h', label: 'SOCKS5H' },
]
// 后端 status 筛选是 StatusEQ 直通，库里只有 active/inactive 两值
// （expired 是由 expires_at 推导的展示概念，提供该选项只会得到恒空结果，不放）
const statusFilterOptions = [
  { value: '', label: '全部状态' },
  { value: 'active', label: '启用' }, { value: 'inactive', label: '停用' },
]

// 创建时间列走服务端排序（sort_by=created_at）；其余列不加 sorter 保持简约
const orderOf = (key) => {
  if (sortState.sort_by !== key) return false
  return sortState.sort_order === 'asc' ? 'ascend' : 'descend'
}
const columns = computed(() => [
  { title: '名称', dataIndex: 'name', key: 'name', ellipsis: true },
  { title: '地址', key: 'addr', width: 220 },
  { title: '状态', dataIndex: 'status', key: 'status', width: 90 },
  { title: '延迟', key: 'latency', width: 90 },
  { title: '有效期', key: 'expiry', width: 130 },
  { title: '出口 IP', dataIndex: 'ip_address', key: 'ip_address', width: 130 },
  { title: '国家', dataIndex: 'country', key: 'country', width: 80 },
  { title: '创建时间', dataIndex: 'created_at', key: 'created_at', width: 140, sorter: true, sortOrder: orderOf('created_at') },
  { title: '引用账号', dataIndex: 'account_count', key: 'account_count', width: 90 },
  { title: '操作', key: 'action', width: 190 },
])

const statusColor = (s) => ({ active: 'green', inactive: 'default', expired: 'red' }[s] || 'default')
// 临期/过期展示（旧版语义：永不过期/正常/临期徽章）
const expiryInfo = (r) => {
  if (!r.expires_at) return { text: '永不过期', color: 'default' }
  const days = dayjs(r.expires_at).diff(dayjs(), 'day')
  if (days < 0) return { text: `已过期 ${-days} 天`, color: 'red' }
  if (days <= 7) return { text: `${days} 天后到期`, color: 'orange' }
  return { text: dayjs(r.expires_at).format('YYYY-MM-DD'), color: 'default' }
}

// ===== 新增/编辑弹窗 =====
const editOpen = ref(false)
const editSaving = ref(false)
// 原始行快照：编辑保存时回传后端会无条件覆盖的字段，防数据破坏
const editOrig = ref(null)
const blankForm = () => ({ id: 0, name: '', protocol: 'http', host: '', port: 8080, username: '', password: '', expires_at: null, fallback_mode: 'none', backup_proxy_id: null, expiry_warn_days: 7 })
const editForm = ref(blankForm())

const PROTOCOLS = ['http', 'https', 'socks5', 'socks5h']
const FALLBACK_MODES = [{ value: 'none', label: '无回退' }, { value: 'proxy', label: '回退到备用代理' }, { value: 'direct', label: '回退直连' }]

const openCreate = () => {
  editForm.value = blankForm()
  editOrig.value = null
  editOpen.value = true
}
const openEdit = (r) => {
  editForm.value = {
    id: r.id, name: r.name, protocol: r.protocol || 'http', host: r.host, port: r.port,
    username: r.username || '', password: '',
    expires_at: r.expires_at ? dayjs(r.expires_at) : null,
    fallback_mode: r.fallback_mode || 'none',
    backup_proxy_id: r.backup_proxy_id || null,
    expiry_warn_days: r.expiry_warn_days ?? 7,
  }
  editOrig.value = r // 保存回传用的原值快照
  editOpen.value = true
}

// 备用代理候选：排除自身（旧版规则）
const backupOptions = computed(() => rows.value
  .filter((p) => p.id !== editForm.value.id)
  .map((p) => ({ value: p.id, label: p.name })))

const setExpiryDays = (d) => { editForm.value.expires_at = dayjs().add(d, 'day') }

const save = async () => {
  const f = editForm.value
  if (!f.name.trim() || !f.host.trim() || !f.port) {
    message.error('名称、主机、端口为必填项')
    return
  }
  if (f.fallback_mode === 'proxy' && !f.backup_proxy_id) {
    message.error('回退到备用代理需选择备用代理')
    return
  }
  editSaving.value = true
  try {
    const body = {
      name: f.name.trim(), protocol: f.protocol, host: f.host.trim(), port: Number(f.port) || 8080,
      username: f.username || null, password: f.password || null,
      // ISO 时间提交；清空=永不过期
      expires_at: f.expires_at ? dayjs(f.expires_at).toISOString() : null,
      fallback_mode: f.fallback_mode,
      backup_proxy_id: f.fallback_mode === 'proxy' ? f.backup_proxy_id : null,
      expiry_warn_days: Number(f.expiry_warn_days) || 0,
    }
    if (f.id) await request(`/admin/proxies/${f.id}`, { method: 'PUT', body })
    else await request('/admin/proxies', { method: 'POST', body })
    editOpen.value = false
    await load()
  } catch (e) {
    message.error(`保存失败：${e.message}`)
  } finally {
    editSaving.value = false
  }
}

const remove = async (r) => {
  // 有账号引用时前端先阻止（旧版规则，避免误删在用代理）
  if ((r.account_count || 0) > 0) {
    message.warning(`该代理仍被 ${r.account_count} 个账号引用，请先解绑`)
    return
  }
  if (!(await confirmAction({ title: '删除代理', content: `确认删除代理「${r.name}」？`, okText: '删除', danger: true }))) return
  try {
    await request(`/admin/proxies/${r.id}`, { method: 'DELETE' })
    await load()
  } catch (e) {
    message.error(`删除失败：${e.message}`)
  }
}

const load = async () => {
  loading.value = true
  try {
    // 空值筛选不传参（与后端 c.Query 空串语义一致，减少 URL 噪音）
    const query = { page: page.value, page_size: pageSize.value }
    if (fProtocol.value) query.protocol = fProtocol.value
    if (fStatus.value) query.status = fStatus.value
    const kw = fSearch.value.trim().slice(0, 100) // 后端 search 上限 100 字符，前端同步截断
    if (kw) query.search = kw
    query.sort_by = sortState.sort_by
    query.sort_order = sortState.sort_order
    const data = await request('/admin/proxies', { method: 'GET', query })
    rows.value = data.items || []
    total.value = data.total || 0
    err.value = ''
  } catch (e) {
    err.value = e.message
    rows.value = []
    total.value = 0
  } finally {
    loading.value = false
  }
}

// 搜索 300ms 防抖（旧 handleSearch 等价）；下拉筛选立即生效
let searchTimer = null
const onSearchInput = () => {
  clearTimeout(searchTimer)
  searchTimer = setTimeout(() => { page.value = 1; load() }, 300)
}
const onFilter = () => { page.value = 1; load() }

// 翻页/表头排序统一走 table @change（避免 pagination 回调 + change 双载）
const onTableChange = (pg, _filters, sorter) => {
  // 纯翻页时 antd 传入的 sorter 是空对象 {}，必须用 field/order 判断是否真为排序操作
  if (sorter && (sorter.field || sorter.order)) {
    if (sorter.order) {
      // sorter.field=dataIndex（创建时间列=created_at，命中后端白名单）
      sortState.sort_by = sorter.field || sorter.column?.key || 'id'
      sortState.sort_order = sorter.order === 'ascend' ? 'asc' : 'desc'
    } else {
      // 第三次点击取消排序 → 回后端默认（id desc）
      sortState.sort_by = 'id'
      sortState.sort_order = 'desc'
    }
    // 排序操作一律回第一页；此时 pg.current 仍是旧页码，绝不能再被下面的翻页分支覆盖
    page.value = 1
  } else if (pg.current !== page.value) {
    page.value = pg.current
  }
  load()
}

// ===== 批量删除（POST /admin/proxies/batch-delete）：勾选即用 selectedIds =====
const batchDeleting = ref(false)
const doBatchDelete = async () => {
  const ids = selectedIds.value.map(Number).filter(Boolean)
  if (!ids.length) return
  batchDeleting.value = true
  try {
    // 后端逐个检查引用：被账号引用/删除失败的进 skipped（含 id+reason），不阻断其他
    const r = await request('/admin/proxies/batch-delete', { method: 'POST', body: { ids } })
    const deleted = (r.deleted_ids || []).length
    const skipped = (r.skipped || []).length
    if (deleted > 0 && skipped > 0) message.success(`已删除 ${deleted} 个，跳过 ${skipped} 个（被账号引用或删除失败）`)
    else if (deleted > 0) message.success(`已删除 ${deleted} 个代理`)
    else if (skipped > 0) message.info(`未删除任何代理：${skipped} 个被跳过（被账号引用或删除失败）`)
    selectedIds.value = [] // 已删/已跳过的勾选没有后续意义，统一清空
    await load()
  } catch (e) {
    message.error(`批量删除失败：${e.message}`)
  } finally {
    batchDeleting.value = false
  }
}

// ===== 测速（POST /admin/proxies/{id}/test）：结果弹窗展示出口信息 =====
const testOpen = ref(false)
const testLoading = ref(false)
const testTarget = ref(null)
const testResult = ref(null)
const runTest = async (r) => {
  testTarget.value = r
  testResult.value = null
  testOpen.value = true
  testLoading.value = true
  try {
    // 后端真实探测出口：返回 {success, message, latency_ms, ip_address, country_code...}
    testResult.value = await request(`/admin/proxies/${r.id}/test`, { method: 'POST' })
  } catch (e) {
    testResult.value = { success: false, message: e.message }
  } finally { testLoading.value = false }
}

// ===== 质量检测（POST /admin/proxies/{id}/quality-check）：评分/等级/目标明细 =====
const qcOpen = ref(false)
const qcLoading = ref(false)
const qcTarget = ref(null)
const qcResult = ref(null)
const qcStatusColor = (s) => ({ pass: 'green', warn: 'orange', fail: 'red', challenge: 'purple' }[s] || 'default')
const qcStatusText = (s) => ({ pass: '通过', warn: '告警', fail: '失败', challenge: '人机校验' }[s] || s)
// 明细延迟条：以本次明细里的最大延迟为满格基准，<1s 绿 / <3s 橙 / 其余红
const qcLatMax = () => Math.max(...(qcResult.value?.items || []).map((i) => i.latency_ms || 0), 1)
const qcLatPct = (ms) => (ms == null ? 0 : Math.max((ms / qcLatMax()) * 100, 4))
const qcLatColor = (ms) => (ms == null ? 'transparent' : ms < 1000 ? '#1f7a33' : ms < 3000 ? '#9a5b00' : '#c42b1c')
const runQuality = async (r) => {
  qcTarget.value = r
  qcResult.value = null
  qcOpen.value = true
  qcLoading.value = true
  try {
    // 跨常见 AI 目标的可达性检测，返回评分/等级/明细
    qcResult.value = await request(`/admin/proxies/${r.id}/quality-check`, { method: 'POST' })
  } catch (e) {
    message.error(`质量检测失败：${e.message}`)
    qcOpen.value = false
  } finally { qcLoading.value = false }
}
const qcColumns = [
  { title: '目标', dataIndex: 'target', key: 'target', ellipsis: true },
  { title: '状态', key: 'status', width: 90 },
  { title: 'HTTP', dataIndex: 'http_status', key: 'http_status', width: 70 },
  { title: '延迟', key: 'latency', width: 90 },
  { title: '说明', dataIndex: 'message', key: 'message', ellipsis: true },
]

// ===== 统计 + 引用账号（GET stats + GET accounts）：详情弹窗 =====
const infoOpen = ref(false)
const infoLoading = ref(false)
const infoTarget = ref(null)
const infoStats = ref(null)
const infoAccounts = ref([])
const openInfo = async (r) => {
  infoTarget.value = r
  infoStats.value = null
  infoAccounts.value = []
  infoOpen.value = true
  infoLoading.value = true
  try {
    // 两路并发：用量统计 + 引用账号清单
    const [st, acc] = await Promise.all([
      request(`/admin/proxies/${r.id}/stats`, { method: 'GET' }),
      request(`/admin/proxies/${r.id}/accounts`, { method: 'GET' }).catch(() => []),
    ])
    infoStats.value = st
    infoAccounts.value = Array.isArray(acc) ? acc : (acc?.items || [])
  } catch (e) { message.error(`详情加载失败：${e.message}`) } finally { infoLoading.value = false }
}
const accColumns = [
  { title: '名称', dataIndex: 'name', key: 'name', ellipsis: true },
  { title: '平台', dataIndex: 'platform', key: 'platform', width: 110 },
  { title: '类型', dataIndex: 'type', key: 'type', width: 110 },
]

// ===== 批量创建（POST /admin/proxies/batch）：多行粘贴解析 =====
const batchOpen = ref(false)
const batchSaving = ref(false)
const batchText = ref('')
const batchResult = ref(null)
// 解析一行：protocol://user:pass@host:port 或 host:port（默认 http）
const parseProxyLine = (line) => {
  const s = line.trim()
  if (!s) return null
  let protocol = 'http', rest = s
  const pm = s.match(/^(\w+):\/\//)
  if (pm) { protocol = pm[1]; rest = s.slice(pm[0].length) }
  let username = null, password = null
  const at = rest.lastIndexOf('@')
  if (at >= 0) {
    const cred = rest.slice(0, at)
    rest = rest.slice(at + 1)
    const ci = cred.indexOf(':')
    if (ci >= 0) { username = cred.slice(0, ci); password = cred.slice(ci + 1) } else { username = cred }
  }
  const hi = rest.lastIndexOf(':')
  if (hi < 0) return null
  const host = rest.slice(0, hi)
  const port = Number(rest.slice(hi + 1))
  if (!host || !port || port < 1 || port > 65535) return null
  return { protocol, host, port, username, password }
}
const batchParse = computed(() => batchText.value.split('\n').map(parseProxyLine).filter(Boolean))
const batchBadCount = computed(() => batchText.value.split('\n').filter((l) => l.trim() && !parseProxyLine(l)).length)
const runBatch = async () => {
  if (!batchParse.value.length) { message.error('没有可识别的代理行'); return }
  batchSaving.value = true
  try {
    // 后端按 host:port 去重，已存在的跳过
    batchResult.value = await request('/admin/proxies/batch', { method: 'POST', body: { proxies: batchParse.value } })
    message.success(`新增 ${batchResult.value.created} 个，跳过重复 ${batchResult.value.skipped} 个`)
    await load()
  } catch (e) { message.error(`批量创建失败：${e.message}`) } finally { batchSaving.value = false }
}

// ===== 导出 / 导入（GET/POST /admin/proxies/data）=====
// row-selection 勾选：有勾选导出所选，否则导出全部
const selectedIds = ref([])
const rowSelection = computed(() => ({
  selectedRowKeys: selectedIds.value,
  onChange: (keys) => { selectedIds.value = keys },
}))
const doExport = async () => {
  try {
    const query = selectedIds.value.length ? { ids: selectedIds.value.join(',') } : {}
    const payload = await request('/admin/proxies/data', { method: 'GET', query })
    // 直接下载为 JSON 备份文件（含代理凭据，仅限工作室内部流转）
    const blob = new Blob([JSON.stringify(payload, null, 2)], { type: 'application/json' })
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = `proxies-export-${dayjs().format('YYYYMMDD-HHmmss')}.json`
    a.click()
    URL.revokeObjectURL(a.href)
    message.success(`已导出 ${(payload.proxies || []).length} 个代理`)
  } catch (e) { message.error(`导出失败：${e.message}`) }
}
const importInput = ref(null)
const importing = ref(false)
const onImportFile = async (ev) => {
  const file = ev.target.files?.[0]
  ev.target.value = ''
  if (!file) return
  let payload
  try {
    payload = JSON.parse(await file.text())
  } catch { message.error('文件不是合法 JSON'); return }
  if (!payload.proxies) { message.error('文件缺少 proxies 字段'); return }
  importing.value = true
  try {
    // 后端按 proxy_key 复用已存在代理：返回创建/复用/失败计数
    const r = await request('/admin/proxies/data', { method: 'POST', body: { data: payload } })
    message.success(`导入完成：新增 ${r.proxy_created}，复用 ${r.proxy_reused}，失败 ${r.proxy_failed}`)
    await load()
  } catch (e) { message.error(`导入失败：${e.message}`) } finally { importing.value = false }
}
onMounted(load)
</script>

<template>
  <div class="proxies">
    <!-- 工具栏按业务分组：操作组｜筛选组｜搜索组｜导入导出组（组间 1px 竖线 + 16px 间距） -->
    <div class="toolbar">
      <div class="tgroup">
        <a-button type="primary" @click="openCreate"><PlusOutlined /> 新增代理</a-button>
        <a-button @click="batchOpen = true">批量导入地址</a-button>
        <!-- 批量删除：勾选后出现，popconfirm 二次确认；被引用的由后端自动跳过 -->
        <a-popconfirm
          v-if="selectedIds.length"
          :title="`确认删除选中的 ${selectedIds.length} 个代理？被账号引用的将自动跳过`"
          ok-text="删除" cancel-text="取消" @confirm="doBatchDelete"
        >
          <a-button danger :loading="batchDeleting"><DeleteOutlined /> 删除选中（{{ selectedIds.length }}）</a-button>
        </a-popconfirm>
      </div>
      <span class="sep" />
      <div class="tgroup">
        <a-select v-model:value="fProtocol" style="width: 120px" :options="protocolFilterOptions" @change="onFilter" />
        <a-select v-model:value="fStatus" style="width: 110px" :options="statusFilterOptions" @change="onFilter" />
      </div>
      <div class="tgroup">
        <a-input v-model:value="fSearch" placeholder="搜索名称" style="width: 180px" allow-clear @input="onSearchInput" @change="onSearchInput" @press-enter="onFilter">
          <template #suffix><SearchOutlined class="suf-ico" @click="onFilter" /></template>
        </a-input>
      </div>
      <span class="sep" />
      <a-button :loading="importing" @click="importInput?.click()">导入备份</a-button>
      <input ref="importInput" type="file" accept="application/json,.json" style="display: none" @change="onImportFile" />
      <a-button @click="doExport">导出备份{{ selectedIds.length ? `（已选 ${selectedIds.length}）` : '' }}</a-button>
      <span class="grow"></span>
      <span class="total">共 {{ total }} 个代理</span>
      <a-button v-if="err" size="small" danger @click="load">加载失败，点击重试</a-button>
      <span v-if="err" class="total err">{{ err }}</span>
    </div>
    <IPv6EgressCard />
    <a-table
      :columns="columns" :data-source="rows" :loading="loading" row-key="id" size="small" :scroll="{ x: 'max-content' }"
      :pagination="{ current: page, pageSize, total, showSizeChanger: false }"
      :row-selection="rowSelection"
      @change="onTableChange"
    >
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'addr'">
          <!-- 主行 host:port，次行 protocol·username（灰小字），避免单列过宽；password 严禁展示 -->
          <div class="addr-cell">
            <span class="mono">{{ record.host }}:{{ record.port }}</span>
            <span class="addr-sub">{{ record.protocol || '-' }}<template v-if="record.username"> · {{ record.username }}</template></span>
          </div>
        </template>
        <template v-else-if="column.key === 'status'">
          <a-tag :color="statusColor(record.status)">{{ record.status }}</a-tag>
        </template>
        <template v-else-if="column.key === 'latency'">
          <!-- 最近一次测速缓存延迟：failed 显示失败（tooltip 带原因），无值显示 - -->
          <span v-if="record.latency_status === 'failed'" class="lat-fail" :title="record.latency_message || '测速失败'">失败</span>
          <span v-else-if="record.latency_ms != null" class="lat-val" :class="record.latency_ms < 200 ? 'lat-fast' : 'lat-slow'">{{ record.latency_ms }} ms</span>
          <span v-else class="dim">-</span>
        </template>
        <template v-else-if="column.key === 'expiry'">
          <a-tag :color="expiryInfo(record).color">{{ expiryInfo(record).text }}</a-tag>
        </template>
        <template v-else-if="column.key === 'created_at'">
          <span class="dim">{{ record.created_at ? dayjs(record.created_at).format('YYYY-MM-DD HH:mm') : '-' }}</span>
        </template>
        <template v-else-if="column.key === 'action'">
          <a-space>
            <a-tooltip title="测速"><a-button size="small" type="text" :loading="testLoading && testTarget?.id === record.id" @click="runTest(record)"><ThunderboltOutlined /></a-button></a-tooltip>
            <a-tooltip title="质量检测"><a-button size="small" type="text" @click="runQuality(record)"><SafetyCertificateOutlined /></a-button></a-tooltip>
            <a-tooltip title="统计与引用账号"><a-button size="small" type="text" @click="openInfo(record)"><InfoCircleOutlined /></a-button></a-tooltip>
            <a-tooltip title="编辑"><a-button size="small" type="text" @click="openEdit(record)"><FormOutlined /></a-button></a-tooltip>
            <a-tooltip title="删除"><a-button size="small" type="text" danger @click="remove(record)"><DeleteOutlined /></a-button></a-tooltip>
          </a-space>
        </template>
      </template>
    </a-table>

    <a-modal v-model:open="editOpen" :title="editForm.id ? '编辑代理' : '新增代理'" :confirm-loading="editSaving" ok-text="保存" cancel-text="取消" @ok="save">
      <a-form layout="vertical" size="small">
        <a-form-item label="名称" required><a-input v-model:value="editForm.name" /></a-form-item>
        <a-form-item label="协议">
          <a-select v-model:value="editForm.protocol" :options="PROTOCOLS.map((v) => ({ value: v, label: v }))" />
        </a-form-item>
        <a-form-item label="主机" required><a-input v-model:value="editForm.host" /></a-form-item>
        <a-form-item label="端口" required><a-input-number v-model:value="editForm.port" :min="1" :max="65535" style="width: 100%" /></a-form-item>
        <a-form-item label="用户名（可选）"><a-input v-model:value="editForm.username" /></a-form-item>
        <a-form-item label="密码（可选，留空保留原值）"><a-input-password v-model:value="editForm.password" /></a-form-item>
        <a-form-item label="过期时间（留空=永不过期）">
          <div class="expiry-row">
            <a-date-picker v-model:value="editForm.expires_at" show-time style="flex: 1" />
            <a-button v-for="d in [7, 30, 90, 180]" :key="d" size="small" @click="setExpiryDays(d)">{{ d }}天</a-button>
          </div>
        </a-form-item>
        <a-form-item label="临期提醒（提前天数）"><a-input-number v-model:value="editForm.expiry_warn_days" :min="0" :max="90" style="width: 100%" /></a-form-item>
        <a-form-item label="回退模式">
          <a-select v-model:value="editForm.fallback_mode" :options="FALLBACK_MODES" />
        </a-form-item>
        <a-form-item v-if="editForm.fallback_mode === 'proxy'" label="备用代理（不含自身）">
          <a-select v-model:value="editForm.backup_proxy_id" :options="backupOptions" placeholder="选择备用代理" />
        </a-form-item>
      </a-form>
    </a-modal>

    <!-- 测速结果 -->
    <a-modal v-model:open="testOpen" :title="`测速：${testTarget?.name || ''}`" :footer="null" width="440px">
      <a-spin :spinning="testLoading">
        <div v-if="testResult" class="kv-list">
          <div class="kv"><span>结果</span><a-tag :color="testResult.success ? 'green' : 'red'">{{ testResult.success ? '连通' : '失败' }}</a-tag></div>
          <div class="kv"><span>延迟</span><span>{{ testResult.latency_ms != null ? `${testResult.latency_ms} ms` : '-' }}</span></div>
          <div class="kv"><span>出口 IP</span><span class="mono">{{ testResult.ip_address || '-' }}</span></div>
          <div class="kv"><span>位置</span><span>{{ [testResult.city, testResult.region, testResult.country || testResult.country_code].filter(Boolean).join(' · ') || '-' }}</span></div>
          <div v-if="!testResult.success" class="kv"><span>错误</span><span class="err-text">{{ testResult.message }}</span></div>
        </div>
        <div v-else class="hint">正在通过该代理发起真实请求探测…</div>
      </a-spin>
    </a-modal>

    <!-- 质量检测结果 -->
    <a-modal v-model:open="qcOpen" :title="`质量检测：${qcTarget?.name || ''}`" :footer="null" width="680px">
      <a-spin :spinning="qcLoading">
        <div v-if="qcResult">
          <div class="qc-head">
            <!-- 弧形评分仪表：270° 弧按 score 填充，色阶 绿≥80/黄≥50/红，中心大数字+等级 -->
            <div class="qc-gauge" :class="qcResult.score >= 80 ? 'good' : qcResult.score >= 50 ? 'mid' : 'bad'">
              <svg viewBox="0 0 84 84" width="84" height="84">
                <circle cx="42" cy="42" r="34" fill="none" stroke="var(--hr)" stroke-width="8"
                  stroke-linecap="round" :stroke-dasharray="`${270 / 360 * 2 * Math.PI * 34} ${2 * Math.PI * 34}`"
                  transform="rotate(135 42 42)" />
                <circle class="qc-arc" cx="42" cy="42" r="34" fill="none" stroke="currentColor" stroke-width="8"
                  stroke-linecap="round" :stroke-dasharray="`${(qcResult.score / 100) * 270 / 360 * 2 * Math.PI * 34} ${2 * Math.PI * 34}`"
                  transform="rotate(135 42 42)" />
              </svg>
              <div class="qc-center">
                <b>{{ qcResult.score }}</b>
                <span>{{ qcResult.grade }}</span>
              </div>
            </div>
            <div class="qc-meta">
              <div><span class="qc-summary">{{ qcResult.summary }}</span></div>
              <div class="qc-counts">
                <span class="c-ok">通过 {{ qcResult.passed_count }}</span>
                <span class="c-warn">告警 {{ qcResult.warn_count }}</span>
                <span class="c-bad">失败 {{ qcResult.failed_count }}</span>
                <span v-if="qcResult.challenge_count" class="c-warn">人机校验 {{ qcResult.challenge_count }}</span>
                <span class="total">基础延迟 {{ qcResult.base_latency_ms != null ? `${qcResult.base_latency_ms}ms` : '-' }} · 出口 {{ qcResult.exit_ip || '-' }} {{ qcResult.country_code || '' }}</span>
              </div>
            </div>
          </div>
          <a-table :columns="qcColumns" :data-source="qcResult.items || []" row-key="target" size="small" :pagination="false">
            <template #bodyCell="{ column, record }">
              <template v-if="column.key === 'status'"><a-tag :color="qcStatusColor(record.status)">{{ qcStatusText(record.status) }}</a-tag></template>
              <template v-else-if="column.key === 'latency'">
              <div class="lat-cell">
                <div class="lat-bar"><i :style="{ width: qcLatPct(record.latency_ms) + '%', background: qcLatColor(record.latency_ms) }"></i></div>
                <span>{{ record.latency_ms != null ? `${record.latency_ms}ms` : '-' }}</span>
              </div>
            </template>
            </template>
          </a-table>
        </div>
        <div v-else class="hint">正在对常见 AI 目标逐一探测，约需数秒…</div>
      </a-spin>
    </a-modal>

    <!-- 统计 + 引用账号 -->
    <a-modal v-model:open="infoOpen" :title="`代理详情：${infoTarget?.name || ''}`" :footer="null" width="560px">
      <a-spin :spinning="infoLoading">
        <div class="kv-list">
          <div class="kv"><span>总请求数</span><span>{{ infoStats?.total_requests ?? '-' }}</span></div>
          <div class="kv"><span>成功率</span><span>{{ infoStats?.success_rate != null ? `${(infoStats.success_rate * 100).toFixed(1)}%` : (infoStats?.success_rate ?? '-') }}</span></div>
          <div class="kv"><span>平均延迟</span><span>{{ infoStats?.average_latency != null ? `${Math.round(infoStats.average_latency)} ms` : '-' }}</span></div>
          <div class="kv"><span>引用账号</span><span>{{ infoStats?.total_accounts ?? '-' }} 个（活跃 {{ infoStats?.active_accounts ?? '-' }}）</span></div>
        </div>
        <div class="sec-title">引用账号清单</div>
        <a-table :columns="accColumns" :data-source="infoAccounts" row-key="id" size="small" :pagination="false" :scroll="{ y: 220 }">
          <template #empty><span class="total">该代理暂无账号引用</span></template>
        </a-table>
      </a-spin>
    </a-modal>

    <!-- 批量导入地址 -->
    <a-modal v-model:open="batchOpen" title="批量导入代理地址" :confirm-loading="batchSaving" ok-text="导入" cancel-text="取消" @ok="runBatch">
      <div class="hint" style="margin-bottom: 8px">每行一条，支持 protocol://用户:密码@主机:端口 或 主机:端口（默认 http）</div>
      <a-textarea v-model:value="batchText" :rows="8" placeholder="http://127.0.0.1:7890&#10;socks5://user:pass@1.2.3.4:1080" />
      <div class="batch-parse">
        <span class="total">识别 {{ batchParse.length }} 条</span>
        <span v-if="batchBadCount" class="total err">{{ batchBadCount }} 条无法解析</span>
      </div>
      <div v-if="batchResult" class="total" style="margin-top: 6px">上次结果：新增 {{ batchResult.created }} · 跳过重复 {{ batchResult.skipped }}</div>
    </a-modal>
  </div>
</template>

<style scoped>
.proxies { height: 100%; overflow-y: auto; }
/* 工具栏分组：组内 8px、组间竖线分隔、底部 1px 浅灰分割线（Win12 规范，同 Groups） */
.toolbar { display: flex; align-items: center; gap: 16px; margin-bottom: 10px; flex-wrap: wrap; padding-bottom: 10px; border-bottom: 1px solid var(--hr); }
.toolbar .tgroup { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.toolbar .sep { width: 1px; height: 20px; background: var(--hr); flex: none; }
.toolbar .grow { flex: 1; }
.suf-ico { color: var(--text2); cursor: pointer; }
.total { font-size: 12px; color: var(--text2); }
.total.err { color: #d13438; }
.mono { font-family: Consolas, monospace; font-size: 12px; }
/* 地址合并列：主行 host:port，次行 protocol·username 灰小字 */
.addr-cell { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.addr-cell .mono { line-height: 1.4; }
.addr-sub { font-size: 11px; color: var(--text2); line-height: 1.3; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* 延迟列：<200ms 绿（旧版阈值），其余橙；测速失败红字 */
.lat-val { font-family: Consolas, monospace; font-size: 12px; }
.lat-fast { color: #1f7a33; }
.lat-slow { color: #9a5b00; }
.lat-fail { font-size: 12px; color: #c42b1c; }
.dim { color: var(--text2); font-size: 12px; }
.expiry-row { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; }
.kv-list { display: flex; flex-direction: column; gap: 8px; }
.kv { display: flex; align-items: center; justify-content: space-between; gap: 12px; font-size: 13px; }
.kv > span:first-child { color: var(--text2); }
.err-text { color: #d13438; word-break: break-all; }
.hint { font-size: 12px; color: var(--text2); }
.qc-head { display: flex; gap: 16px; align-items: center; margin-bottom: 12px; }
/* 弧形评分仪表：环色跟随分档，中心 score + 等级徽章 */
.qc-gauge { position: relative; width: 84px; height: 84px; flex: none; }
.qc-gauge.good { color: #1f7a33; }
.qc-gauge.mid { color: #9a5b00; }
.qc-gauge.bad { color: #c42b1c; }
.qc-center { position: absolute; inset: 0; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 0; }
.qc-center b { font-size: 24px; font-weight: 600; line-height: 1; color: var(--text); }
.qc-center span { font-size: 11px; font-weight: 600; color: currentColor; margin-top: 2px; }
.qc-meta { display: flex; flex-direction: column; gap: 6px; min-width: 0; }
.qc-summary { font-size: 12px; color: var(--text2); }
.qc-counts { display: flex; gap: 10px; font-size: 12px; flex-wrap: wrap; }
.qc-counts .c-ok { color: #1f7a33; }
.qc-counts .c-warn { color: #9a5b00; }
.qc-counts .c-bad { color: #c42b1c; }
/* 目标延迟迷你条：相对最大延迟的比例条 + 等宽数值 */
.lat-cell { display: flex; align-items: center; gap: 8px; min-width: 130px; }
.lat-bar { flex: 1; height: 5px; border-radius: 3px; background: var(--hr); overflow: hidden; }
.lat-bar i { display: block; height: 100%; border-radius: 3px; }
.lat-cell span { font-family: Consolas, monospace; font-size: 12px; color: var(--text2); white-space: nowrap; }
.sec-title { font-weight: 500; font-size: 13px; margin: 14px 0 8px; }
.batch-parse { display: flex; gap: 12px; margin-top: 8px; }
</style>

