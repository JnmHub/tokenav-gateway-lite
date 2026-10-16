<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import { opsAPI, type OpsRequestDebugConfig, type OpsRequestDebugRecord, type OpsLocalKeyCaptureConfig, type OpsLocalKeyCaptureRecord, type OpsLocalKeyCaptureRecordsResponse } from '@/api/admin/ops'
import { adminAPI } from '@/api/admin'
import localKeysAPI, { type GatewayLiteLocalKey } from '@/api/admin/gatewayLiteLocalKeys'
import type { AdminGroup } from '@/types'

const groups = ref<AdminGroup[]>([])
const keys = ref<GatewayLiteLocalKey[]>([])
const config = ref<OpsRequestDebugConfig | null>(null)
const records = ref<OpsRequestDebugRecord[]>([])
const loading = ref(true)
const saving = ref(false)
const selected = ref('all')
const expanded = ref<string | null>(null)
const compareLeft = ref('')
const compareRight = ref('')
const compareParts = ref<string[]>(['request_headers', 'request_schema', 'response_headers', 'response_schema'])
// 工作台右侧 Tab：结构详情 / 结构对照 / 完整调试。
// 旧版把三块内容铺在三栏 grid 里互相挤压，改为 Tab 后每块都能用满宽度。
const activeTab = ref<'detail' | 'compare' | 'full'>('detail')
// 对照默认只展示差异行（GitHub diff 的 hunk 折叠思想：相同字段折叠成摘要行）。
const onlyDiff = ref(true)
const fullConfig = ref<OpsLocalKeyCaptureConfig | null>(null)
const fullRecords = ref<OpsLocalKeyCaptureRecord[]>([])
const fullLoading = ref(false)
const fullSaving = ref(false)
const fullSelectedKey = ref('')
const fullPreset = ref<'5m' | '15m' | '30m'>('15m')
const fullMaxRecords = ref(50)
const fullMaxBodyBytes = ref(1024 * 1024)
const fullExpanded = ref<string | null>(null)
const selectedLabel = computed(() => {
  if (selected.value === 'all') return '全部请求'
  const [kind, rawID] = selected.value.split(':')
  if (kind === 'group') return groups.value.find(g => String(g.id) === rawID)?.name || '指定分组'
  if (kind === 'api_key') return keys.value.find(k => String(k.id) === rawID)?.name || '指定 API Key（接口密钥）'
  return '全部请求'
})

function schemaEntries(schema: OpsRequestDebugRecord['request_schema']) {
  return schema?.fields || []
}
function fieldDepth(path: string) {
  // 路径中的点号和数组索引代表一层嵌套，用于保留树状阅读层级。
  return Math.min(8, (path.match(/[.[]/g) || []).length)
}
function formatTime(value: string) { return new Date(value).toLocaleString() }
const leftRecord = computed(() => records.value.find(r => r.id === compareLeft.value))
const rightRecord = computed(() => records.value.find(r => r.id === compareRight.value))
const selectedRecord = computed(() => records.value.find(r => r.id === expanded.value) || records.value[0])
function toggleCompare(id: string) {
  if (compareLeft.value === id) compareLeft.value = ''
  else if (compareRight.value === id) compareRight.value = ''
  else if (!compareLeft.value) compareLeft.value = id
  else if (!compareRight.value) compareRight.value = id
  // 选齐两条后自动跳到对照 Tab，省一次点击。
  if (compareLeft.value && compareRight.value) activeTab.value = 'compare'
}
function fieldMap(record: OpsRequestDebugRecord | undefined, part: string) {
  if (!record) return new Map<string, string>()
  if (part.endsWith('_headers')) return new Map((record[part as 'request_headers' | 'response_headers'] || []).map(name => [name, 'header']))
  const schema = record[part as 'request_schema' | 'response_schema']
  return new Map((schema?.fields || []).map(field => [field.path, `${field.type}${field.length == null ? '' : ` · ${field.length}`}`]))
}
type CompareState = 'added' | 'removed' | 'changed' | 'same'
interface CompareRow { path: string; depth: number; left: string; right: string; state: CompareState }

// 树状排序键：把 a.b[0].c 拆成段数组，父字段排在子字段前、同前缀相邻。
// 旧版直接字母排序，嵌套关系全被打散，是"看不出结构"的主因之一。
function pathSegments(path: string): (string | number)[] {
  return path
    .replace(/\[(\d+)\]/g, '.$1')
    .split('.')
    .filter(Boolean)
    .map(seg => (/^\d+$/.test(seg) ? Number(seg) : seg))
}
function compareSegments(a: (string | number)[], b: (string | number)[]): number {
  const len = Math.min(a.length, b.length)
  for (let i = 0; i < len; i++) {
    const av = a[i], bv = b[i]
    if (av === bv) continue
    // 数字段（数组下标）按数值排且排在命名段之后，保持数组顺序可读。
    const aNum = typeof av === 'number', bNum = typeof bv === 'number'
    if (aNum && bNum) return (av as number) - (bv as number)
    if (aNum !== bNum) return aNum ? 1 : -1
    return String(av).localeCompare(String(bv))
  }
  return a.length - b.length
}
function comparisonRows(part: string): CompareRow[] {
  const left = fieldMap(leftRecord.value, part)
  const right = fieldMap(rightRecord.value, part)
  const rows = [...new Set([...left.keys(), ...right.keys()])].map(path => ({
    path,
    depth: fieldDepth(path),
    left: left.get(path) || '',
    right: right.get(path) || '',
    state: (!left.has(path) ? 'added' : !right.has(path) ? 'removed' : left.get(path) !== right.get(path) ? 'changed' : 'same') as CompareState,
  }))
  rows.sort((x, y) => compareSegments(pathSegments(x.path), pathSegments(y.path)))
  return rows
}
// 对照结果缓存：comparisonRows 会对两侧字段做并集+排序，模板里统计胶囊和
// 行渲染会多次调用，字段量大时（数百字段 × 4 区域）重复计算明显卡顿，
// 用 computed 按（左记录、右记录）维度整体缓存一次。
const ALL_PARTS = ['request_headers', 'request_schema', 'response_headers', 'response_schema'] as const
const comparisonCache = computed(() => {
  const cache: Record<string, CompareRow[]> = {}
  if (!leftRecord.value || !rightRecord.value) return cache
  for (const part of ALL_PARTS) cache[part] = comparisonRows(part)
  return cache
})
function cachedRows(part: string): CompareRow[] { return comparisonCache.value[part] || [] }
// 差异统计：GitHub 风格的 +n/-n/~n 摘要，打开页面前先看规模。
function partStats(part: string) {
  const rows = cachedRows(part)
  return {
    added: rows.filter(r => r.state === 'added').length,
    removed: rows.filter(r => r.state === 'removed').length,
    changed: rows.filter(r => r.state === 'changed').length,
    same: rows.filter(r => r.state === 'same').length,
  }
}
// 路径拆分显示：树状缩进下完整路径会前缀冗长，拆成弱化前缀 + 强调末段，
// 保留上下文又不挤占列宽（hover title 仍给完整路径）。
function splitPath(path: string): { prefix: string; last: string } {
  const segs = path.replace(/\[(\d+)\]/g, '.$1').split('.').filter(Boolean)
  if (segs.length <= 1) return { prefix: '', last: path }
  return { prefix: segs.slice(0, -1).join('.') + '.', last: segs[segs.length - 1] }
}
function partTitle(part: string) { return ({ request_headers: '请求头', request_schema: '请求内容字段', response_headers: '响应头', response_schema: '响应内容字段' } as Record<string, string>)[part] }
async function load() {
  loading.value = true
  try {
    const [g, k, c, r, fc, fr] = await Promise.all([adminAPI.groups.getAllIncludingInactive(), localKeysAPI.list(), opsAPI.getRequestDebugConfig(), opsAPI.listRequestDebugRecords(), opsAPI.getLocalKeyCaptureConfig().catch(() => null), opsAPI.listLocalKeyCaptureRecords().catch((): OpsLocalKeyCaptureRecordsResponse => ({ items: [] }))])
    groups.value = g; keys.value = k; config.value = c; records.value = r.records || r.items || []
    selected.value = c.scope.type && c.scope.type !== 'all' && c.scope.value ? `${c.scope.type}:${c.scope.value}` : 'all'
    fullConfig.value = fc
    fullRecords.value = fr.items || fr.records || []
    fullSelectedKey.value = fc?.api_key_id || fc?.key_id ? String(fc.api_key_id || fc.key_id) : ''
    fullPreset.value = fc?.preset || '15m'
    fullMaxRecords.value = fc?.max_records || 50
    fullMaxBodyBytes.value = fc?.max_body_bytes || 1024 * 1024
  } finally { loading.value = false }
}
async function save() {
  if (!config.value) return
  saving.value = true
  try {
    const [kind, rawID] = selected.value.split(':')
    const scope = selected.value === 'all'
      ? { type: 'all' as const, value: null }
      : { type: kind as 'group' | 'api_key', value: kind === 'group' ? Number(rawID) : rawID }
    // 保存后保留当前选择；旧网关返回的精简状态可能没有 scope，不能因此把界面回填为“全部请求”。
    const savedSelection = selected.value
    // “保存调试范围”同时负责开启结构采集；初始配置 enabled=false 时不能把关闭状态传给更新接口，否则会误走清空并停用分支。
    config.value = await opsAPI.updateRequestDebugConfig({ ...config.value, enabled: true, scope })
    selected.value = savedSelection
    await Promise.all([opsAPI.listRequestDebugRecords().then(r => { records.value = r.records || r.items || [] }), refreshFullCapture().catch(() => undefined)])
  }
  finally { saving.value = false }
}
async function clear() { await opsAPI.clearRequestDebugRecords(); records.value = [] }
async function saveFullCapture() {
  fullSaving.value = true
  try {
    fullConfig.value = await opsAPI.updateLocalKeyCaptureConfig({
      enabled: true,
      api_key_id: fullSelectedKey.value ? Number(fullSelectedKey.value) : null,
      preset: fullPreset.value,
      max_records: Number(fullMaxRecords.value) || 50,
      max_body_bytes: Number(fullMaxBodyBytes.value) || 1024 * 1024,
    })
    await refreshFullCapture()
  } finally { fullSaving.value = false }
}
async function stopFullCapture() {
  fullSaving.value = true
  try {
    fullConfig.value = await opsAPI.disableLocalKeyCapture()
    fullRecords.value = []
  } finally { fullSaving.value = false }
}
async function refreshFullCapture() {
  fullLoading.value = true
  try {
    const result = await opsAPI.listLocalKeyCaptureRecords()
    fullRecords.value = result.items || result.records || []
  } finally { fullLoading.value = false }
}
async function clearFullCapture() { await opsAPI.clearLocalKeyCaptureRecords(); fullRecords.value = [] }
function headerEntries(headers?: Record<string, string | string[]>) { return Object.entries(headers || {}) }
function displayHeaderValue(name: string, value: string | string[]) { return /authorization|cookie|api[-_]?key|token|secret/i.test(name) ? '[已脱敏]' : Array.isArray(value) ? value.join(', ') : value }
function downloadFullRecords() {
  const payload = JSON.stringify(fullRecords.value, null, 2)
  const blob = new Blob([payload], { type: 'application/json;charset=utf-8' })
  const url = URL.createObjectURL(blob); const anchor = document.createElement('a'); anchor.href = url; anchor.download = `local-key-debug-${new Date().toISOString().replace(/[:.]/g, '-')}.json`; anchor.click(); URL.revokeObjectURL(url)
}
function bodyData(body?: OpsLocalKeyCaptureRecord['request_body']) {
  if (body == null) return null
  if (typeof body === 'object' && 'data' in body) return body.data
  return body
}
function prettyBody(body?: OpsLocalKeyCaptureRecord['request_body']) {
  const data = bodyData(body)
  if (data == null || data === '') return ''
  if (typeof data === 'string') { try { return JSON.stringify(JSON.parse(data), null, 2) } catch { return data } }
  try { return JSON.stringify(data, null, 2) } catch { return String(data) }
}
function bodyTruncated(body: OpsLocalKeyCaptureRecord['request_body'], topLevel?: boolean) {
  return topLevel || (typeof body === 'object' && body !== null && 'truncated' in body && Boolean(body.truncated))
}
function bodyEncoding(body: OpsLocalKeyCaptureRecord['request_body']) {
  return typeof body === 'object' && body !== null && 'encoding' in body ? String(body.encoding || '') : ''
}
function selectRecord(id: string) {
  expanded.value = expanded.value === id ? null : id
  activeTab.value = 'detail'
}
onMounted(load)
</script>

<template>
  <AppLayout>
    <div class="schema-workbench mx-auto max-w-[1680px] pb-12">
      <!-- 顶部：采集范围（横跨全宽） -->
      <section class="wb-card wb-topbar">
        <div class="flex items-start gap-3">
          <span class="flex h-11 w-11 shrink-0 items-center justify-center rounded-2xl wb-accent-bg"><Icon name="chart" size="md" /></span>
          <div class="min-w-0">
            <h1 class="text-xl font-bold wb-strong">字段结构分析</h1>
            <p class="mt-1 text-sm wb-muted">只查看字段路径、类型和请求头名称，不展示任何字段值。左侧选择记录，右侧对照结构。</p>
          </div>
          <div class="ml-auto flex shrink-0 gap-2">
            <button class="btn btn-secondary btn-sm" :disabled="loading" @click="load"><Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />刷新</button>
            <button class="btn btn-secondary btn-sm" :disabled="!records.length" @click="clear">清空</button>
          </div>
        </div>
        <div v-if="config" class="wb-toolbar-grid">
          <div><label class="wb-label">采集范围</label><select v-model="selected" class="input w-full"><option value="all">全部请求</option><optgroup label="按分组"><option v-for="group in groups" :key="`group-${group.id}`" :value="`group:${group.id}`">{{ group.name }}（{{ group.platform }}）</option></optgroup><optgroup v-if="keys.length" label="按 API Key（接口密钥）"><option v-for="key in keys" :key="`key-${key.id}`" :value="`api_key:${key.id}`">{{ key.name }} · {{ key.key_masked }}</option></optgroup></select></div>
          <div class="wb-hint"><span class="font-semibold wb-strong">{{ selectedLabel }}</span><span class="mx-2 wb-faint">·</span>采集 {{ config.preset || '15m' }}，最多 {{ config.max_records }} 条</div>
          <button class="btn btn-primary self-end" :disabled="saving" @click="save">{{ saving ? '保存中…' : '保存调试范围' }}</button>
        </div>
      </section>

      <!-- 主区：左记录列表 + 右工作台（对照/详情/完整调试共用全部宽度） -->
      <div class="wb-main">
        <aside class="wb-card wb-records">
          <div class="flex items-center justify-between border-b px-4 py-3 wb-border">
            <div><h2 class="text-sm font-semibold wb-strong">采集记录</h2><p class="mt-0.5 text-xs wb-muted">{{ records.length }} 条 · 勾选两条对照</p></div>
            <span v-if="config?.enabled" class="badge badge-success">采集中</span><span v-else class="badge">已关闭</span>
          </div>
          <div v-if="loading" class="py-12 text-center text-sm wb-muted">加载中…</div>
          <div v-else-if="!records.length" class="py-12 text-center text-sm wb-muted">暂无记录，请先开启调试并向该分组发送请求。</div>
          <div v-else class="wb-records-scroll divide-y" style="--wb-divide: rgba(36,66,87,.7)">
            <article v-for="record in records" :key="record.id" class="record-item px-4 py-3" :class="{ 'record-active': selectedRecord?.id === record.id }">
              <div class="flex items-center gap-2.5">
                <input type="checkbox" :checked="compareLeft === record.id || compareRight === record.id" class="mt-0.5 shrink-0" :aria-label="`选择${record.path}用于结构对照`" @click.stop @change="toggleCompare(record.id)">
                <button class="min-w-0 flex-1 text-left" @click="selectRecord(record.id)">
                  <div class="flex items-center gap-2">
                    <span class="record-method shrink-0">{{ record.method }}</span>
                    <span class="truncate font-mono text-xs wb-strong" :title="record.path">{{ record.path }}</span>
                  </div>
                  <div class="mt-1 flex items-center gap-2 text-[11px] wb-muted">
                    <span :class="record.response_status && record.response_status < 400 ? 'wb-ok' : 'wb-err'">{{ record.response_status || '—' }}</span>
                    <span>{{ record.duration_ms || 0 }}ms</span>
                    <span class="truncate">{{ formatTime(record.created_at) }}</span>
                  </div>
                </button>
                <span v-if="compareLeft === record.id || compareRight === record.id" class="wb-side-tag">{{ compareLeft === record.id ? '左' : '右' }}</span>
              </div>
            </article>
          </div>
        </aside>

        <main class="wb-card wb-workspace">
          <nav class="flex items-center gap-1 border-b px-3 py-2 wb-border">
            <button class="wb-tab" :class="{ 'wb-tab-active': activeTab === 'detail' }" @click="activeTab = 'detail'"><Icon name="chart" size="sm" class="mr-1.5" />结构详情</button>
            <button class="wb-tab" :class="{ 'wb-tab-active': activeTab === 'compare', 'wb-tab-hint': !leftRecord || !rightRecord }" @click="activeTab = 'compare'"><Icon name="search" size="sm" class="mr-1.5" />结构对照<span v-if="leftRecord && rightRecord" class="wb-tab-count">2</span></button>
            <button class="wb-tab" :class="{ 'wb-tab-active': activeTab === 'full' }" @click="activeTab = 'full'">完整调试<span v-if="fullConfig?.enabled" class="ml-1 inline-block h-1.5 w-1.5 rounded-full bg-amber-400 align-middle" /></button>
          </nav>

          <!-- Tab 1：结构详情（唯一展示字段树与请求头的地方，消除旧版三处重复） -->
          <div v-if="activeTab === 'detail'" class="wb-panel">
            <template v-if="selectedRecord">
              <div class="mb-4 flex flex-wrap items-center justify-between gap-2 px-1">
                <div class="min-w-0"><h2 class="truncate font-mono text-sm wb-strong" :title="selectedRecord.path">{{ selectedRecord.path }}</h2><p class="mt-0.5 text-xs wb-muted">{{ selectedRecord.method }} · {{ formatTime(selectedRecord.created_at) }}</p></div>
                <span class="wb-status-chip" :class="(selectedRecord.response_status || 0) < 400 ? 'wb-status-ok' : 'wb-status-err'">{{ selectedRecord.response_status || '—' }}</span>
              </div>
              <div class="grid gap-4 xl:grid-cols-2">
                <div class="wb-subcard">
                  <div class="mb-2 flex items-center justify-between"><h3 class="text-xs font-bold uppercase tracking-wider wb-muted">请求结构</h3><span class="text-[11px] wb-faint">{{ selectedRecord.request_schema?.bytes || 0 }} bytes</span></div>
                  <div class="wb-field-list">
                    <div v-for="field in schemaEntries(selectedRecord.request_schema)" :key="`tree-req-${field.path}`" class="schema-field" :style="{ paddingLeft: `${8 + fieldDepth(field.path) * 16}px` }">
                      <code>{{ field.path }}</code><span class="wb-type-chip">{{ field.type }}<template v-if="field.length != null"> · {{ field.length }}</template></span>
                    </div>
                    <p v-if="!schemaEntries(selectedRecord.request_schema).length" class="py-3 text-xs wb-muted">无字段</p>
                  </div>
                </div>
                <div class="wb-subcard">
                  <div class="mb-2 flex items-center justify-between"><h3 class="text-xs font-bold uppercase tracking-wider wb-muted">响应结构</h3><span class="text-[11px] wb-faint">{{ selectedRecord.response_schema?.bytes || 0 }} bytes</span></div>
                  <div class="wb-field-list">
                    <div v-for="field in schemaEntries(selectedRecord.response_schema)" :key="`tree-resp-${field.path}`" class="schema-field" :style="{ paddingLeft: `${8 + fieldDepth(field.path) * 16}px` }">
                      <code>{{ field.path }}</code><span class="wb-type-chip">{{ field.type }}<template v-if="field.length != null"> · {{ field.length }}</template></span>
                    </div>
                    <p v-if="!schemaEntries(selectedRecord.response_schema).length" class="py-3 text-xs wb-muted">无字段</p>
                  </div>
                </div>
              </div>
              <div class="wb-subcard mt-4">
                <h3 class="mb-2 text-xs font-bold uppercase tracking-wider wb-muted">请求头（仅名称）</h3>
                <div class="flex flex-wrap gap-2"><span v-for="header in selectedRecord.request_headers" :key="header" class="header-chip">{{ header }}</span><span v-if="!selectedRecord.request_headers.length" class="text-xs wb-muted">无</span></div>
              </div>
            </template>
            <div v-else class="py-16 text-center text-sm wb-muted">暂无结构记录</div>
          </div>

          <!-- Tab 2：结构对照（GitHub/jsondiffpatch 风格重做） -->
          <div v-else-if="activeTab === 'compare'" class="wb-panel">
            <template v-if="leftRecord && rightRecord">
              <div class="mb-3 flex flex-wrap items-center gap-2 px-1">
                <div v-for="part in ['request_headers','request_schema','response_headers','response_schema']" :key="`part-${part}`">
                  <button class="wb-part-chip" :class="{ 'wb-part-on': compareParts.includes(part) }" @click="compareParts = compareParts.includes(part) ? compareParts.filter(p => p !== part) : [...compareParts, part]">
                    {{ partTitle(part) }}
                    <span v-if="compareParts.includes(part)" class="wb-part-stat">
                      <template v-if="partStats(part).added">+{{ partStats(part).added }}</template>
                      <template v-if="partStats(part).removed"> -{{ partStats(part).removed }}</template>
                      <template v-if="partStats(part).changed"> ~{{ partStats(part).changed }}</template>
                      <template v-if="!partStats(part).added && !partStats(part).removed && !partStats(part).changed">一致</template>
                    </span>
                  </button>
                </div>
                <label class="ml-auto inline-flex cursor-pointer items-center gap-1.5 text-xs wb-muted"><input v-model="onlyDiff" type="checkbox" class="accent-cyan-500">仅看差异</label>
              </div>

              <div v-for="part in compareParts" :key="`diff-${part}`" class="mb-4 overflow-hidden rounded-lg wb-border-solid">
                <div class="diff-head">
                  <span class="font-semibold wb-strong">{{ partTitle(part) }}</span>
                  <span class="ml-auto flex items-center gap-3 text-[11px]">
                    <span class="flex items-center gap-1.5 min-w-0"><i class="wb-side-dot" />{{ leftRecord.path.length > 28 ? leftRecord.path.slice(0, 28) + '…' : leftRecord.path }}</span>
                    <span class="flex items-center gap-1.5 min-w-0"><i class="wb-side-dot wb-side-dot-r" />{{ rightRecord.path.length > 28 ? rightRecord.path.slice(0, 28) + '…' : rightRecord.path }}</span>
                  </span>
                </div>
                <div class="diff-grid diff-grid-head wb-muted">
                  <span />
                  <span>字段路径</span><span>左侧（旧）</span><span>右侧（新）</span>
                </div>
                <template v-if="cachedRows(part).length">
                  <template v-if="onlyDiff">
                    <div v-if="partStats(part).same" class="diff-same-summary" @click="onlyDiff = false">… {{ partStats(part).same }} 个字段完全一致（点击展开）</div>
                    <div v-for="row in cachedRows(part).filter(r => r.state !== 'same')" :key="row.path" class="diff-grid diff-row" :class="`diff-${row.state}`">
                      <span class="diff-gut">{{ row.state === 'added' ? '+' : row.state === 'removed' ? '−' : '~' }}</span>
                      <code class="diff-path" :style="{ paddingLeft: `${4 + row.depth * 12}px` }" :title="row.path"><span v-if="splitPath(row.path).prefix" class="diff-path-prefix">{{ splitPath(row.path).prefix }}</span>{{ splitPath(row.path).last }}</code>
                      <span class="diff-cell diff-cell-old">{{ row.left || '—' }}</span>
                      <span class="diff-cell diff-cell-new">{{ row.right || '—' }}</span>
                    </div>
                  </template>
                  <div v-for="row in cachedRows(part)" v-else :key="`all-${row.path}`" class="diff-grid diff-row" :class="`diff-${row.state}`">
                    <span class="diff-gut">{{ row.state === 'added' ? '+' : row.state === 'removed' ? '−' : row.state === 'changed' ? '~' : '' }}</span>
                    <code class="diff-path" :style="{ paddingLeft: `${4 + row.depth * 12}px` }" :title="row.path"><span v-if="splitPath(row.path).prefix" class="diff-path-prefix">{{ splitPath(row.path).prefix }}</span>{{ splitPath(row.path).last }}</code>
                    <span class="diff-cell diff-cell-old">{{ row.left || '—' }}</span>
                    <span class="diff-cell diff-cell-new">{{ row.right || '—' }}</span>
                  </div>
                </template>
                <div v-else class="px-4 py-5 text-center text-xs wb-muted">两次请求均没有可展示的结构</div>
              </div>
              <div class="flex flex-wrap gap-3 px-1 text-[11px] wb-muted">
                <span><i class="mr-1 inline-block h-2 w-2 rounded-sm" style="background:#34d399" />右侧新增</span>
                <span><i class="mr-1 inline-block h-2 w-2 rounded-sm" style="background:#fb7185" />左侧删除</span>
                <span><i class="mr-1 inline-block h-2 w-2 rounded-sm" style="background:#fbbf24" />类型变化</span>
              </div>
            </template>
            <div v-else class="py-16 text-center text-sm wb-muted">
              在左侧记录列表勾选两条请求即可对照{{ activeTab === 'compare' && records.length < 2 ? '（当前记录不足两条）' : '' }}
            </div>
          </div>

          <!-- Tab 3：完整调试（原功能平移） -->
          <div v-else class="wb-panel">
            <div class="mb-4 flex flex-wrap items-start justify-between gap-3 px-1">
              <div class="flex items-start gap-3">
                <span class="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-amber-100 text-lg text-amber-700 dark:bg-amber-900/30 dark:text-amber-300">⌁</span>
                <div><h2 class="font-semibold wb-strong">本地密钥完整调试</h2><p class="mt-1 text-xs wb-muted">仅捕获选中的 gateway-lite 本地密钥；请求体和响应体只保存在网关内存中，自动过期后清除。</p></div>
              </div>
              <span v-if="fullConfig?.enabled" class="badge badge-warning">完整调试已开启</span><span v-else class="badge">未开启</span>
            </div>
            <p class="mb-4 px-1 text-xs text-amber-600 dark:text-amber-300">敏感请求头（Authorization、Cookie、API Key）始终脱敏，下载内容请妥善保管。</p>
            <div class="wb-subcard mb-4 grid gap-3 md:grid-cols-4 md:items-end">
              <label class="wb-label">本地 API Key（接口密钥）<select v-model="fullSelectedKey" class="input mt-1.5 w-full"><option value="">请选择本地密钥</option><option v-for="key in keys" :key="`full-key-${key.id}`" :value="String(key.id)">{{ key.name }} · {{ key.key_masked }}</option></select></label>
              <label class="wb-label">自动关闭<select v-model="fullPreset" class="input mt-1.5 w-full"><option value="5m">5 分钟</option><option value="15m">15 分钟</option><option value="30m">30 分钟</option></select></label>
              <label class="wb-label">最多记录数<input v-model.number="fullMaxRecords" min="1" max="500" type="number" class="input mt-1.5 w-full"></label>
              <label class="wb-label">单条正文上限<input v-model.number="fullMaxBodyBytes" min="4096" max="5242880" step="4096" type="number" class="input mt-1.5 w-full"><span class="mt-1 block text-[11px] wb-faint">字节，超出会标记为截断</span></label>
              <div class="flex flex-wrap gap-2 md:col-span-4"><button class="btn btn-primary" :disabled="fullSaving || !fullSelectedKey" @click="saveFullCapture">{{ fullSaving ? '保存中…' : '开启并清空旧记录' }}</button><button class="btn btn-secondary" :disabled="fullSaving || !fullConfig?.enabled" @click="stopFullCapture">立即停止</button><button class="btn btn-secondary" :disabled="fullLoading" @click="refreshFullCapture">刷新</button><button class="btn btn-secondary" :disabled="!fullRecords.length" @click="downloadFullRecords">下载 JSON</button><button class="btn btn-secondary" :disabled="!fullRecords.length" @click="clearFullCapture">清空记录</button><span v-if="fullConfig?.expires_at" class="self-center text-xs wb-muted">预计 {{ formatTime(fullConfig.expires_at) }} 自动关闭</span></div>
            </div>
            <div v-if="fullLoading" class="py-10 text-center text-sm wb-muted">加载中…</div>
            <div v-else-if="!fullRecords.length" class="py-10 text-center text-sm wb-muted">暂无完整内容记录。开启后向该本地密钥发送一条请求即可查看。</div>
            <div v-else class="divide-y" style="--wb-divide: rgba(36,66,87,.7)">
              <article v-for="record in fullRecords" :key="record.id" class="px-1 py-4">
                <button class="flex w-full items-center gap-3 text-left" @click="fullExpanded = fullExpanded === record.id ? null : record.id">
                  <span class="record-method">{{ record.method }}</span>
                  <span class="min-w-0 truncate font-mono text-sm wb-strong">{{ record.path }}</span>
                  <span class="ml-auto shrink-0 text-xs wb-muted">{{ record.response_status }} · {{ record.duration_ms || 0 }}ms · {{ formatTime(record.created_at) }}</span>
                  <Icon name="chevronDown" size="sm" :class="fullExpanded === record.id ? 'rotate-180' : ''" />
                </button>
                <div v-if="fullExpanded === record.id" class="mt-4 grid gap-4 xl:grid-cols-2">
                  <div class="wb-subcard">
                    <div class="mb-3 flex items-center justify-between"><h3 class="text-xs font-bold uppercase tracking-wider wb-muted">请求</h3><span v-if="bodyEncoding(record.request_body)" class="text-[11px] wb-faint">{{ bodyEncoding(record.request_body) }}</span></div>
                    <div class="mb-3 space-y-1 text-xs"><div v-for="entry in headerEntries(record.request_headers)" :key="entry[0]" class="flex gap-2"><code class="wb-muted">{{ entry[0] }}</code><span class="wb-faint">{{ displayHeaderValue(entry[0], entry[1]) }}</span></div></div>
                    <pre class="max-h-[360px] overflow-auto rounded-lg bg-gray-900 p-3 text-xs leading-relaxed text-gray-100">{{ prettyBody(record.request_body) || '（无正文）' }}</pre>
                    <p v-if="bodyTruncated(record.request_body, record.request_body_truncated)" class="mt-2 text-xs text-amber-500">正文已按单条上限截断</p>
                  </div>
                  <div class="wb-subcard">
                    <div class="mb-3 flex items-center justify-between"><h3 class="text-xs font-bold uppercase tracking-wider wb-muted">响应</h3><span v-if="bodyEncoding(record.response_body)" class="text-[11px] wb-faint">{{ bodyEncoding(record.response_body) }}</span></div>
                    <div class="mb-3 space-y-1 text-xs"><div v-for="entry in headerEntries(record.response_headers)" :key="entry[0]" class="flex gap-2"><code class="wb-muted">{{ entry[0] }}</code><span class="wb-faint">{{ entry[1] }}</span></div></div>
                    <pre class="max-h-[360px] overflow-auto rounded-lg bg-gray-900 p-3 text-xs leading-relaxed text-gray-100">{{ prettyBody(record.response_body) || '（无正文）' }}</pre>
                    <p v-if="bodyTruncated(record.response_body, record.response_body_truncated)" class="mt-2 text-xs text-amber-500">正文已按单条上限截断</p>
                  </div>
                </div>
              </article>
            </div>
          </div>
        </main>
      </div>
    </div>
  </AppLayout>
</template>

<style scoped>
/* 结构分析页使用独立的深色工作台。
   布局原则（对齐 GitHub diff / jsondiffpatch 的可读性惯例）：
   1. 顶部配置横跨全宽，主区固定"左列表 + 右工作台"两栏，互不牵连高度；
   2. 对照视图用固定四列网格（符号/路径/左/右）+ sticky 表头，滚动不失锚点；
   3. 差异行用半透明底色 + 行首 +/−/~ 符号（色弱可辨），相同字段可折叠。 */
.schema-workbench {
  --wb-bg: #0b1722;
  --wb-surface: #102130;
  --wb-surface-alt: #0d1c29;
  --wb-border: #244257;
  --wb-text: #e5eef5;
  --wb-muted: #8fa9bb;
  --wb-faint: #5f7d91;
  --wb-accent: #19c5d4;
  padding: 16px;
  border-radius: 10px;
  background: var(--wb-bg);
  color: var(--wb-text);
}
.wb-card { min-width: 0; overflow: hidden; border: 1px solid var(--wb-border); border-radius: 8px; background: var(--wb-surface); box-shadow: 0 12px 30px rgba(2, 12, 20, .24); }
.wb-topbar { margin-bottom: 14px; padding: 18px 20px; }
.wb-toolbar-grid { display: grid; gap: 14px; margin-top: 16px; grid-template-columns: minmax(240px, 360px) 1fr auto; align-items: end; }
.wb-main { display: grid; gap: 14px; grid-template-columns: minmax(300px, 380px) minmax(0, 1fr); align-items: start; }
.wb-records { position: sticky; top: 16px; max-height: calc(100vh - 32px); display: flex; flex-direction: column; }
.wb-records-scroll { overflow-y: auto; }
.wb-workspace { min-height: 560px; display: flex; flex-direction: column; }
.wb-panel { padding: 16px 18px 20px; overflow-y: auto; }

/* 文字/边框语义色（替代旧版 40 行 !important 覆盖） */
.wb-strong { color: var(--wb-text); }
.wb-muted { color: var(--wb-muted); }
.wb-faint { color: var(--wb-faint); }
.wb-border { border-color: var(--wb-border) !important; }
.wb-border-solid { border: 1px solid var(--wb-border); border-radius: 8px; }
.wb-accent-bg { background: rgba(25, 197, 212, .14); color: var(--wb-accent); }
.wb-ok { color: #43e0a6; }
.wb-err { color: #fb7185; }
.wb-label { display: block; font-size: 11px; font-weight: 500; color: var(--wb-muted); }

/* 记录列表 */
.record-item { transition: background .2s ease; cursor: pointer; }
.record-item:hover { background: rgba(25, 197, 212, .05); }
.record-item.record-active { background: rgba(25, 197, 212, .09); box-shadow: inset 2px 0 0 var(--wb-accent); }
.record-method { border-radius: 4px; background: rgba(25, 197, 212, .14); color: #56dce7; padding: 2px 7px; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 11px; }
.wb-side-tag { border-radius: 4px; background: rgba(25, 197, 212, .16); padding: 2px 7px; font-size: 11px; font-weight: 600; color: #56dce7; }

/* Tab */
.wb-tab { display: inline-flex; align-items: center; border-radius: 6px; padding: 7px 13px; font-size: 13px; font-weight: 500; color: var(--wb-muted); transition: all .15s; }
.wb-tab:hover { background: rgba(25, 197, 212, .07); color: var(--wb-text); }
.wb-tab-active { background: rgba(25, 197, 212, .13); color: var(--wb-accent); }
.wb-tab-hint { opacity: .6; }
.wb-tab-count { margin-left: 6px; border-radius: 999px; background: rgba(25, 197, 212, .2); padding: 0 6px; font-size: 10px; }

/* 结构树 */
.wb-subcard { border: 1px solid var(--wb-border); border-radius: 8px; background: var(--wb-surface-alt); padding: 14px; }
.wb-field-list { max-height: 460px; overflow-y: auto; }
.schema-field { display: flex; align-items: center; justify-content: space-between; gap: 12px; border-bottom: 1px solid rgba(36, 66, 87, .6); padding: 7px 0; font-size: 13px; }
.schema-field:last-of-type { border-bottom: 0; }
.schema-field > code { min-width: 0; overflow-wrap: anywhere; color: #d8e7ef; }
.schema-field > code::before { content: '├─'; display: inline-block; margin-right: 8px; color: var(--wb-faint); }
.schema-field:last-of-type > code::before { content: '└─'; }
.wb-type-chip { flex: none; border-radius: 4px; background: rgba(25, 197, 212, .12); padding: 2px 7px; color: var(--wb-accent); font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 11px; }
.header-chip { border: 1px solid rgba(66, 153, 178, .35); border-radius: 4px; background: rgba(37, 99, 122, .2); padding: 3px 8px; color: #a8c9d8; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 11px; }
.wb-status-chip { border-radius: 6px; padding: 3px 10px; font-size: 12px; font-weight: 600; }
.wb-status-ok { background: rgba(16, 185, 129, .15); color: #43e0a6; }
.wb-status-err { background: rgba(244, 63, 94, .15); color: #fb7185; }

/* 对照区（diff） */
.wb-part-chip { display: inline-flex; align-items: center; gap: 6px; border: 1px solid var(--wb-border); border-radius: 6px; background: var(--wb-surface-alt); padding: 5px 10px; font-size: 12px; color: var(--wb-muted); transition: all .15s; }
.wb-part-chip:hover { color: var(--wb-text); }
.wb-part-on { border-color: rgba(25, 197, 212, .5); background: rgba(25, 197, 212, .1); color: var(--wb-accent); }
.wb-part-stat { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 10px; opacity: .9; }
.diff-head { display: flex; align-items: center; gap: 8px; border-bottom: 1px solid var(--wb-border); background: var(--wb-surface-alt); padding: 9px 14px; font-size: 13px; }
.wb-side-dot { display: inline-block; height: 7px; width: 7px; flex: none; border-radius: 999px; background: #8fa9bb; }
.wb-side-dot-r { background: var(--wb-accent); }
/* 四列固定网格：符号 | 路径 | 左 | 右。列宽锁定后左右值天然按行对齐（Meld 惯例）。 */
.diff-grid { display: grid; align-items: baseline; grid-template-columns: 26px minmax(200px, 1.15fr) minmax(90px, .55fr) minmax(90px, .55fr); gap: 0 10px; padding: 4px 14px; font-size: 12px; }
.diff-grid-head { position: sticky; top: 0; z-index: 1; border-bottom: 1px solid var(--wb-border); background: var(--wb-surface); padding: 6px 14px; font-size: 11px; font-weight: 600; }
.diff-gut { width: 100%; text-align: center; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-weight: 700; }
.diff-path { min-width: 0; overflow-wrap: anywhere; color: #d8e7ef; }
.diff-path-prefix { color: var(--wb-faint); font-size: 11px; }
.diff-cell { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; color: var(--wb-muted); }
/* 差异行底色：半透明深色系（替代旧版浅色 emerald-50 等直接贴深底），文字对比稳定。 */
.diff-row.diff-added { background: rgba(16, 185, 129, .1); }
.diff-row.diff-added .diff-gut, .diff-row.diff-added .diff-cell-new { color: #43e0a6; }
.diff-row.diff-removed { background: rgba(244, 63, 94, .1); }
.diff-row.diff-removed .diff-gut, .diff-row.diff-removed .diff-cell-old { color: #fb7185; }
.diff-row.diff-changed { background: rgba(245, 158, 11, .1); }
.diff-row.diff-changed .diff-gut { color: #fbbf24; }
.diff-row.diff-same .diff-path { color: var(--wb-faint); }
.diff-same-summary { cursor: pointer; border-bottom: 1px solid rgba(36, 66, 87, .5); padding: 6px 14px; font-size: 11px; color: var(--wb-faint); }
.diff-same-summary:hover { color: var(--wb-muted); }

/* 表单控件适配深色工作台（少量、集中，不再全局 !important 覆盖） */
.schema-workbench :deep(select),
.schema-workbench :deep(input.input) { border-color: var(--wb-border) !important; background: #0b1a28 !important; color: var(--wb-text) !important; }
.schema-workbench :deep(.btn-secondary) { border-color: var(--wb-border) !important; background: #132a3b !important; color: #c6d9e5 !important; }
.schema-workbench :deep(.btn-secondary:hover) { background: #1a3a4f !important; }
.schema-workbench :deep(.btn-primary) { background: var(--wb-accent) !important; color: #06212c !important; box-shadow: 0 0 18px rgba(25, 197, 212, .2); }
.schema-workbench :deep(.badge-success) { background: rgba(16, 185, 129, .16) !important; color: #43e0a6 !important; }
.schema-workbench :deep(.badge-warning) { background: rgba(245, 158, 11, .16) !important; color: #fbbf54 !important; }
.schema-workbench :deep(.badge) { border: 1px solid var(--wb-border); background: var(--wb-surface-alt); color: var(--wb-muted); }

/* 窄屏：两栏退化为上下堆叠，记录列表取消 sticky。 */
@media (max-width: 1100px) {
  .wb-main { grid-template-columns: 1fr; }
  .wb-records { position: static; max-height: 360px; }
  .wb-toolbar-grid { grid-template-columns: 1fr; }
  .diff-grid { grid-template-columns: 22px minmax(140px, 1fr) minmax(70px, .55fr) minmax(70px, .55fr); }
}
</style>
