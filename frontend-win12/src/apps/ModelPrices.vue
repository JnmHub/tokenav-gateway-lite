<script setup>
import W12Input from '../components/design/W12Input.vue'
import W12Select from '../components/design/W12Select.vue'
import W12Table from '../components/design/W12Table.vue'
import W12Dialog from '../components/design/W12Dialog.vue'
import W12Button from '../components/design/W12Button.vue'
import { SearchOutlined } from '@ant-design/icons-vue'
/* 模型价格 v2（对照旧 frontend/src/views/admin/ModelPricesView.vue 全量契约）：
 * - 【目录状态卡】GET /admin/model-prices/sync/status 常驻展示：自动同步启用徽章、
 *   目录来源（remote/cache/embedded）、模型数、覆盖价数、上次成功时间、最近错误+时间
 * - 【立即同步】POST /admin/model-prices/sync（loading 期间禁用，成功后刷新列表+状态）
 * - 【筛选】供应商 ?provider= + 来源 ?source= + 搜索 300ms 防抖实时（后端同时匹配模型名/供应商）
 * - 【新增覆盖价】空状态引导 + 工具栏按钮（模型/供应商/mode 均可输入）
 * - 【取消覆盖】仅 override 行显示，Modal.confirm 确认后 DELETE /admin/model-prices?model=
 * - 【编辑弹窗】抽到 components/modelprice/PriceEditModal.vue（含归一化规则）
 * - rowKey 改为 model+provider+mode 组合，防同名模型不同供应商/模式时行冲突 */
import { ref, watch, onMounted, onUnmounted } from 'vue'
import dayjs from 'dayjs'
import { request as apiRequest, isCancellation } from '../api'
import { createLatestRequest, runLatest } from '../api/latestRequest'
import { useRequestLifetime } from '../composables/useRequestLifetime'
import { useWindowContext } from '../core/windowContext'
import { message, Modal } from 'ant-design-vue'
import { DatabaseOutlined, SyncOutlined, FormOutlined } from '@ant-design/icons-vue'
import PriceEditModal from '../components/modelprice/PriceEditModal.vue'

const loading = ref(false)
const rows = ref([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const search = ref('')
const providerFilter = ref('')
const sourceFilter = ref('')
const err = ref('')
const lane = createLatestRequest()
const windowContext = useWindowContext()
const modelLane = createLatestRequest()
const statusLane = createLatestRequest()
const lifetime = useRequestLifetime(() => {
  lane.cancel(); modelLane.cancel(); statusLane.cancel(); rows.value = []; total.value = 0
  status.value = null; editing.value = null; editOpen.value = false; smOpen.value = false; smModels.value = []
  clearTimeout(searchTimer)
})
const request = (path, options) => apiRequest(path, { ...lifetime, ...options })

// ===== 目录同步状态卡 =====
const status = ref(null)
const syncing = ref(false)
const statusError = ref('')
const statusLoading = ref(false)

const SOURCE_LABELS = { remote: '远程目录', cache: '本地缓存', local_cache: '本地缓存', embedded: '内置目录', static_fallback: '内置目录' }
const catalogSourceLabel = (s) => SOURCE_LABELS[String(s || '').toLowerCase()] || s || '-'
// 后端无错误时 last_error_at 是零值时间（0001-01-01），要显示「从未」
const fmtTime = (v) => (!v || String(v).startsWith('0001-01-01') ? '从未' : dayjs(v).format('YYYY-MM-DD HH:mm'))
const fmtCount = (n) => (typeof n === 'number' ? n.toLocaleString('zh-CN') : '-')

const loadStatus = async () => {
  statusLoading.value = true
  await runLatest(statusLane, (signal) => request('/admin/model-prices/sync/status', { method: 'GET', signal }), {
    apply: (value) => { status.value = value; statusError.value = '' },
    error: (e) => { status.value = null; statusError.value = e.message },
    finish: () => { statusLoading.value = false },
  })
}

const syncCatalog = async () => {
  if (syncing.value) return
  statusLane.cancel()
  syncing.value = true
  try {
    status.value = await request('/admin/model-prices/sync', { method: 'POST' })
    message.success(`目录同步成功（${status.value?.model_count ?? '?'} 个模型，来源：${catalogSourceLabel(status.value?.source)}）`)
    await load()
  } catch (e) {
    if (!isCancellation(e)) { message.error(`同步失败：${e.message}`); loadStatus() }
  } finally {
    syncing.value = false
  }
}

// ===== 列表 =====
const columns = [
  { title: '模型', dataIndex: 'model', key: 'model', ellipsis: true, width: 220 },
  { title: '供应商', dataIndex: 'provider', key: 'provider', width: 100 },
  { title: '计费', key: 'billing', width: 76 },
  { title: '输入 $/1M', key: 'in', width: 90 },
  { title: '输出 $/1M', key: 'out', width: 90 },
  { title: '缓存写', key: 'cw', width: 80 },
  { title: '缓存读', key: 'cr', width: 80 },
  { title: '媒体价格', key: 'media', width: 160 },
  { title: '来源', key: 'source', width: 88 },
  { title: '操作', key: 'action', width: 140 },
]

const fmt$ = (n) => {
  const v = Number(n || 0)
  if (v <= 0) return '-'
  // 大数保留 4 位、小数保留 6 位（旧版 formatMoney 语义），再去尾零
  const fixed = v >= 1 ? v.toFixed(4) : v.toFixed(6)
  return `$${fixed.replace(/\.0+$/, '').replace(/(\.\d*?)0+$/, '$1')}`
}
const bmLabel = (m) => ({ image: '图片', video: '视频', per_request: '按次' }[m] || 'Token')
// 按次计费行：复用输入价格列的位置显示 $x/次（旧前端列表没有按次列，此处为补齐展示；
// 无价时保持 '-'，不追加「/次」后缀）
const perReqCell = (r) => {
  const s = fmt$(r.per_request_price)
  return s === '-' ? '-' : `${s}/次`
}
const compactThreshold = (v) => {
  const n = Number(v || 0)
  return n >= 1000 && n % 1000 === 0 ? `${n / 1000}K` : String(n)
}
const sourceTag = (s) => (s === 'override' ? { color: 'blue', text: '覆盖价' } : s === 'static_fallback' ? { color: 'default', text: '内置兜底' } : { color: 'green', text: '默认目录' })
// 媒体价格行：视频按秒档位优先，其次图片三价（旧版语义）
const mediaRows = (r) => {
  const video = [
    { label: '480p', value: Number(r.video_price_per_second_480p || 0) },
    { label: '720p', value: Number(r.video_price_per_second_720p || 0) },
    { label: '1080p', value: Number(r.video_price_per_second_1080p || 0) },
  ].filter((x) => x.value > 0)
  if (video.length) return { kind: 'video', rows: video }
  const image = [
    { label: '每张', value: r.output_cost_per_image },
    { label: '输入 Token', value: r.input_cost_per_image_token },
    { label: '输出 Token', value: r.output_cost_per_image_token },
  ].filter((x) => x.value != null && (x.value > 0 || x.label === '输入 Token'))
  return image.length ? { kind: 'image', rows: image } : null
}

// rowKey：model+provider+mode 组合，防同名模型在不同供应商/模式下冲突
const rowKey = (r) => `${r.model}|${r.provider}|${r.mode}`

// ===== 新增/编辑弹窗 =====
const editOpen = ref(false)
const editing = ref(null)
const openCreate = () => { editing.value = null; editOpen.value = true }
const openEdit = (r) => { editing.value = r; editOpen.value = true }
const onSaved = () => { load(); loadStatus() }

// ===== 取消覆盖（原 removePrice 死代码修复：确认弹窗 + DELETE ?model=） =====
const confirmReset = (r) => {
  Modal.confirm({
    title: '取消覆盖价',
    getContainer: windowContext?.getContainer,
    content: `确定取消「${r.model}」的覆盖价吗？删除后将回退到默认目录价格。`,
    okText: '取消覆盖',
    okType: 'danger',
    cancelText: '保留',
    onOk: async () => {
      try {
        await request('/admin/model-prices', { method: 'DELETE', query: { model: r.model } })
        message.success('覆盖价已取消，已回退默认价')
        await Promise.all([load(), loadStatus()])
      } catch (e) {
        if (!isCancellation(e)) message.error(`取消覆盖失败：${e.message}`)
      }
    },
  })
}

// ===== 从渠道同步模型：选平台预览 LiteLLM 侧可用模型名清单（GET sync-models?platform=x） =====
const SM_PLATFORMS = ['openai', 'anthropic', 'gemini', 'deepseek', 'kimi', 'xai', 'groq', 'mistral'].map((v) => ({ value: v, label: v }))
const smOpen = ref(false)
const smLoading = ref(false)
const smPlatform = ref('openai')
const smModels = ref([])

const syncModels = async () => {
  smLoading.value = true
  smModels.value = []
  smOpen.value = true
  await runLatest(modelLane, (signal) => request('/admin/channels/pricing/sync-models', { method: 'GET', query: { platform: smPlatform.value }, signal }), {
    apply: (res) => { smModels.value = res?.models || [] },
    error: (e) => message.error(`获取模型清单失败：${e.message}`),
    finish: () => { smLoading.value = false },
  })
}

const load = async () => {
  loading.value = true
  const q = { page: page.value, page_size: pageSize.value }
  if (search.value.trim()) q.search = search.value.trim()
  if (providerFilter.value) q.provider = providerFilter.value
  if (sourceFilter.value) q.source = sourceFilter.value
  await runLatest(lane, (signal) => request('/admin/model-prices', { method: 'GET', query: q, signal }), {
    apply: (data) => { rows.value = data.items || []; total.value = data.total || 0; err.value = '' },
    error: (e) => { err.value = e.message; rows.value = []; total.value = 0 },
    finish: () => { loading.value = false },
  })
}

// 搜索 300ms 防抖实时（旧版 handleSearchInput 同参数）；回车立即搜索
let searchTimer = null
watch(search, () => {
  lane.cancel()
  clearTimeout(searchTimer)
  searchTimer = setTimeout(() => { page.value = 1; load() }, 300)
})
const onSearchEnter = () => { clearTimeout(searchTimer); page.value = 1; load() }
const onFilterChange = () => { page.value = 1; load() }
const onPageChange = (p) => { page.value = p; load() }
onMounted(() => { load(); loadStatus() })
onUnmounted(() => { clearTimeout(searchTimer); lane.dispose(); modelLane.dispose(); statusLane.dispose() })
</script>

<template>
  <div class="prices w12-design">
    <!-- 价格目录状态卡：常驻展示同步状态，替代旧的 toast -->
    <div class="cat-card">
      <div class="cat-top">
        <div class="cat-title">
          <DatabaseOutlined class="cat-ico" />
          <span class="t">价格目录状态</span>
          <a-tag :color="status?.enabled ? 'green' : 'default'">
            {{ !status ? (statusLoading ? '状态读取中' : '同步状态未知') : status.enabled ? '自动同步已启用' : '自动同步未启用' }}
          </a-tag>
        </div>
        <W12Button compact :loading="syncing || !!status?.refreshing" :disabled="syncing || !!status?.refreshing" @click="syncCatalog">
          <template #icon><SyncOutlined /></template>
          {{ syncing || status?.refreshing ? '同步中…' : '立即同步' }}
        </W12Button>
      </div>
      <W12Button v-if="statusError" compact variant="danger" @click="loadStatus">状态读取失败：{{ statusError }}（重试）</W12Button>
      <div class="cat-metrics">
        <div class="m"><span class="k">目录来源</span><span class="v">{{ catalogSourceLabel(status?.source) }}</span></div>
        <div class="m"><span class="k">模型数</span><span class="v">{{ fmtCount(status?.model_count) }}</span></div>
        <div class="m"><span class="k">覆盖价</span><span class="v">{{ fmtCount(status?.override_count) }}</span></div>
        <div class="m"><span class="k">上次成功同步</span><span class="v">{{ fmtTime(status?.last_success_at) }}</span></div>
        <div class="m"><span class="k">目录版本</span><span class="v">{{ status?.catalog_version || '-' }}</span></div>
      </div>
      <div v-if="status?.last_error" class="cat-err">
        <span>最近同步错误：{{ status.last_error }}</span>
        <span class="t">（{{ fmtTime(status.last_error_at) }}）</span>
      </div>
    </div>

    <div class="toolbar">
      <W12Input v-model:value="search" placeholder="搜索模型或供应商…" style="width: 220px" allow-clear @press-enter="onSearchEnter">
        <template #suffix><SearchOutlined class="suf-ico" @click="onSearchEnter" /></template>
      </W12Input>
      <W12Select
        v-model:value="providerFilter" style="width: 140px" placeholder="全部供应商"
        :options="[{ value: '', label: '全部供应商' }, { value: 'xiaomi', label: 'Xiaomi' }, { value: 'openai', label: 'OpenAI' }, { value: 'anthropic', label: 'Anthropic' }, { value: 'google', label: 'Google' }, { value: 'custom', label: 'Custom' }]"
        @change="onFilterChange"
      />
      <W12Select
        v-model:value="sourceFilter" style="width: 140px" placeholder="全部来源"
        :options="[{ value: '', label: '全部来源' }, { value: 'catalog', label: '默认目录' }, { value: 'override', label: '覆盖价' }, { value: 'static_fallback', label: '内置兜底' }]"
        @change="onFilterChange"
      />
      <span class="total">共 {{ total }} 个模型价格</span>
      <span class="grow" />
      <W12Button :disabled="loading" @click="load">刷新</W12Button>
      <W12Button @click="syncModels">从渠道同步模型…</W12Button>
      <W12Button variant="primary" @click="openCreate">新增覆盖价</W12Button>
      <W12Button v-if="err" compact @click="load" variant="danger">加载失败，点击重试</W12Button>
    </div>
    <span v-if="err" class="total err">列表加载失败：{{ err }}</span>

    <W12Table
      :columns="columns" :data-source="rows" :loading="loading" :row-key="rowKey" size="small" :scroll="{ x: 'max-content' }"
      :pagination="{ current: page, pageSize, total, showSizeChanger: false, onChange: onPageChange }"
    >
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'model'">
          <div class="mcell">
            <div class="mname" :class="{ off: record.disabled }">{{ record.model }}</div>
            <div class="msub">{{ record.mode || 'chat' }}</div>
            <!-- 能力/状态徽标：Win12 规范 20px 高、4px 圆角、低饱和半透明底；停用优先展示 -->
            <div v-if="record.disabled || record.supports_prompt_caching || record.supports_service_tier || Number(record.long_context_input_token_threshold || 0) > 0" class="mbadges">
              <span v-if="record.disabled" class="badge b-gray">已停用</span>
              <span v-if="record.supports_prompt_caching" class="badge b-green">缓存</span>
              <span v-if="record.supports_service_tier" class="badge b-blue">Priority</span>
              <span v-if="Number(record.long_context_input_token_threshold || 0) > 0" class="badge b-gray">&gt;{{ compactThreshold(record.long_context_input_token_threshold) }} 长上下文</span>
            </div>
          </div>
        </template>
        <template v-else-if="column.key === 'billing'">{{ bmLabel(record.billing_mode) }}</template>
        <template v-else-if="column.key === 'in'">
          <!-- 按次计费：无 Token 价可显，借用输入价格列显示 $x/次，避免表格加宽 -->
          <span v-if="record.billing_mode === 'per_request'" class="prq">{{ perReqCell(record) }}</span>
          <template v-else>{{ fmt$(record.input_cost_per_1m_tokens) }}</template>
        </template>
        <template v-else-if="column.key === 'out'">{{ fmt$(record.output_cost_per_1m_tokens) }}</template>
        <template v-else-if="column.key === 'cw'">{{ fmt$(record.cache_creation_cost_per_1m_tokens) }}</template>
        <template v-else-if="column.key === 'cr'">{{ fmt$(record.cache_read_cost_per_1m_tokens) }}</template>
        <template v-else-if="column.key === 'media'">
          <template v-if="mediaRows(record)">
            <div v-for="item in mediaRows(record).rows" :key="item.label" class="media-row">
              <span class="k">{{ item.label }}</span>
              <span>{{ mediaRows(record).kind === 'image' && Number(item.value) === 0 ? '$0' : fmt$(item.value) }}<template v-if="mediaRows(record).kind === 'video'"> /s</template></span>
            </div>
          </template>
          <span v-else class="dim">-</span>
        </template>
        <template v-else-if="column.key === 'source'">
          <a-tag :color="sourceTag(record.source).color">{{ sourceTag(record.source).text }}</a-tag>
        </template>
        <template v-else-if="column.key === 'action'">
          <a-space :size="6">
            <a-tooltip title="编辑/覆盖"><W12Button compact variant="text" @click="openEdit(record)"><FormOutlined /></W12Button></a-tooltip>
            <W12Button v-if="record.is_override" compact @click="confirmReset(record)" variant="danger">取消覆盖</W12Button>
          </a-space>
        </template>
      </template>
      <template #emptyText>
        <div class="empty">
          <p class="t">暂无模型价格</p>
          <p class="d">当前筛选条件下没有数据，可新增覆盖价或检查默认价格目录是否同步成功</p>
          <W12Button variant="primary" @click="openCreate">新增覆盖价</W12Button>
        </div>
      </template>
    </W12Table>

    <!-- 新增/编辑弹窗（字段与归一化在子组件内，对照旧 ModelPricesView 契约） -->
    <PriceEditModal v-model:open="editOpen" :price="editing" @saved="onSaved" />

    <!-- 从渠道同步模型：平台选择 → 预览模型名清单 -->
    <W12Dialog v-model:open="smOpen" title="从渠道同步模型" :footer="null" width="480px">
      <div style="display: flex; align-items: center; gap: 12px; margin-bottom: 12px">
        <W12Select v-model:value="smPlatform" style="width: 160px" :options="SM_PLATFORMS" @change="syncModels" />
        <span class="total">共 {{ smModels.length }} 个模型</span>
      </div>
      <a-spin :spinning="smLoading">
        <div class="sm-list">
          <div v-for="m in smModels" :key="m" class="sm-item">{{ m }}</div>
          <div v-if="!smModels.length && !smLoading" class="total">该平台暂无模型清单</div>
        </div>
      </a-spin>
    </W12Dialog>
  </div>
</template>

<style scoped>
.prices { height: 100%; }
/* 目录状态卡：极淡半透卡片、无厚重描边（Win12 规范） */
.cat-card { background: var(--card); border-radius: 10px; padding: 12px 14px; margin-bottom: 12px; }
.cat-top { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.cat-title { display: flex; align-items: center; gap: 8px; min-width: 0; }
.cat-title .t { font-size: 13px; font-weight: 600; }
.cat-ico { font-size: 15px; color: var(--text2); }
.cat-metrics { display: flex; flex-wrap: wrap; gap: 6px 28px; margin-top: 10px; }
.cat-metrics .m { display: flex; flex-direction: column; gap: 1px; min-width: 0; }
.cat-metrics .k { font-size: 11px; color: var(--text2); }
.cat-metrics .v { font-size: 13px; font-weight: 600; font-variant-numeric: tabular-nums; }
.cat-err { margin-top: 10px; padding: 6px 10px; border-radius: 6px; background: rgba(230, 162, 60, 0.12); color: #b45309; font-size: 12px; word-break: break-all; }
:root.dark .cat-err { color: #f0b26a; }
.cat-err .t { color: inherit; opacity: 0.75; white-space: nowrap; }
.toolbar { display: flex; align-items: center; gap: 8px; margin-bottom: 10px; flex-wrap: wrap; }
.grow { flex: 1; }
.total { font-size: 12px; color: var(--text2); }
.total.err { display: block; color: #d13438; margin: -4px 0 8px; }
/* 模型列：主名 + mode 副行 + 能力徽章 */
.mcell { min-width: 160px; }
.mname { font-size: 13px; }
/* 停用行：主名淡化为次要灰，与「已停用」徽标呼应 */
.mname.off { color: var(--text2); }
.msub { font-size: 11px; color: var(--text2); margin-top: 1px; }
.mbadges { display: flex; align-items: center; flex-wrap: wrap; gap: 4px; margin-top: 3px; }
/* Win12 状态徽标：高 20px、圆角 4px、低饱和半透明底（对齐 AccountStatusCell 惯例） */
.badge { display: inline-flex; align-items: center; height: 20px; padding: 0 6px; border-radius: 4px; font-size: 11px; white-space: nowrap; }
.b-blue { background: rgba(24, 144, 255, 0.12); color: #1668dc; }
.b-green { background: rgba(82, 196, 26, 0.13); color: #389e0d; }
.b-gray { background: var(--bg70); color: var(--text2); }
.prq { font-variant-numeric: tabular-nums; }
.media-row { display: flex; justify-content: space-between; gap: 8px; font-size: 12px; line-height: 20px; }
.media-row .k { color: var(--text2); }
.dim { color: var(--text2); }
.empty { padding: 28px 0 20px; text-align: center; }
.empty .t { font-size: 14px; font-weight: 600; margin: 0 0 4px; }
.empty .d { font-size: 12px; color: var(--text2); margin: 0 0 14px; }
.sm-list { max-height: 380px; overflow-y: auto; display: flex; flex-direction: column; }
.sm-item { padding: 6px 8px; border-bottom: 1px solid var(--hr); font-size: 13px; font-family: Consolas, monospace; }
.sm-item:hover { background: var(--hover-b); }
</style>


