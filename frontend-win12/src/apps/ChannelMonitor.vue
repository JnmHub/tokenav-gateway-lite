<script setup>
import { SearchOutlined } from '@ant-design/icons-vue'
/* ============================================================
 * 渠道监控（Win12 重构版）—— V1/V2 双视图
 * 契约来源（唯一权威）：旧 views/admin/ChannelMonitorView.vue +
 *   components/admin/monitor/*（10 组件），字段/枚举另经后端
 *   internal/handler/admin/channel_monitor_handler.go 与
 *   internal/service/channel_monitor_v2.go 核实，并经真实后端冒烟。
 *
 * 视图切换：GET /settings/public 下发 channel_monitor_mode（v1/v2）与
 *   channel_monitor_default_interval_seconds（新建表单默认间隔）。
 *   V2 = 被动统计面板（MonitorV2Panel）；V1 = 主动探测列表。
 *   注意：V2 读取接口在 v1 模式被后端 403 保护，故 v1 模式下展示切换提示卡片。
 *
 * V1 列表能力：搜索（300ms 防抖）/ 提供商筛选 / 启用筛选 / 分页（页码+每页条数）/
 *   行内启用 Toggle（PUT :id {enabled}）/ 复制（Idempotency-Key 幂等，确认弹窗）/
 *   立即检测（results 逐模型弹窗）/ 删除 / api_key_decrypt_failed 警示图标。
 *
 * TODO（本轮不做，留待下轮）：模板管理器弹窗体系已接入（MonitorTemplatesDialog）；
 *   主模型列附加模型 hover 状态表（MonitorExtraModelsBadge）与监控表单高级请求
 *   配置（模板/extra_headers/body_override，MonitorAdvancedRequestConfig）已接入。
 * ============================================================ */
import { ref, watch, onMounted } from 'vue'
import { message } from 'ant-design-vue'
import { WarningOutlined, CopyOutlined, EditOutlined, DeleteOutlined, PlayCircleOutlined, HistoryOutlined } from '@ant-design/icons-vue'
import {
  listMonitors, deleteMonitor, updateMonitor, runMonitor, duplicateMonitor, getPublicSettings,
} from '../components/monitor/api'
import MonitorV2Panel from '../components/monitor/MonitorV2Panel.vue'
import MonitorV2MatrixPanel from '../components/monitor/MonitorV2MatrixPanel.vue'
import MonitorFormDialog from '../components/monitor/MonitorFormDialog.vue'
import MonitorRunResultDialog from '../components/monitor/MonitorRunResultDialog.vue'
import MonitorHistoryDialog from '../components/monitor/MonitorHistoryDialog.vue'
import MonitorTemplatesDialog from '../components/monitor/MonitorTemplatesDialog.vue'
import MonitorExtraModelsBadge from '../components/monitor/MonitorExtraModelsBadge.vue'
import {
  PROVIDERS, PROVIDER_LABELS, PROVIDER_META,
  statusLabel, statusColor, fmtAvailability, DEFAULT_INTERVAL_SECONDS,
} from '../components/monitor/constants'

/* ---------- 视图模式（V1/V2） ---------- */
const monitorMode = ref('v1') // public 设置未加载前按 v1 兜底
const view = ref('v1')
const systemInterval = ref(DEFAULT_INTERVAL_SECONDS)
/* V2 视图内的子页签：overview = 既有 MonitorV2Panel，matrix = 维度矩阵（/dimensions + /matrix） */
const v2Tab = ref('overview')

async function loadSettings() {
  try {
    const s = await getPublicSettings()
    monitorMode.value = s.channel_monitor_mode === 'v2' ? 'v2' : 'v1'
    if (s.channel_monitor_default_interval_seconds > 0) systemInterval.value = s.channel_monitor_default_interval_seconds
    if (s.table_default_page_size > 0) pageSize.value = s.table_default_page_size
    // 模式加载后再定默认视图（v2 模式默认进 V2 面板）
    view.value = monitorMode.value === 'v2' ? 'v2' : 'v1'
  } catch { /* 设置读取失败不阻塞页面，按 v1 兜底 */ }
}

function onViewChange(val) {
  // v1 模式下允许切到 V2 视图位置，但展示提示卡片（后端读取接口会 403）
  view.value = val
}

/* ---------- V1 列表：筛选 / 分页 / 加载 ---------- */
const loading = ref(false)
const rows = ref([])
const total = ref(0)
const err = ref('')
const page = ref(1)
const pageSize = ref(20)
const search = ref('')
const providerFilter = ref('') // '' = 全部
const enabledFilter = ref('') // '' | 'true' | 'false'

const providerOptions = [
  { value: '', label: '全部供应商' },
  ...PROVIDERS.map((p) => ({ value: p, label: PROVIDER_LABELS[p] })),
]
const enabledOptions = [
  { value: '', label: '全部状态' },
  { value: 'true', label: '仅启用' },
  { value: 'false', label: '仅禁用' },
]

const columns = [
  { title: '名称', dataIndex: 'name', key: 'name', ellipsis: true },
  { title: '提供商', dataIndex: 'provider', key: 'provider', width: 110 },
  { title: '主模型', dataIndex: 'primary_model', key: 'primary_model', width: 190, ellipsis: true },
  { title: '状态', key: 'status', width: 90 },
  { title: '延迟', key: 'latency', width: 90, align: 'right' },
  { title: '7 天可用率', key: 'avail', width: 100, align: 'right' },
  { title: '间隔', key: 'interval', width: 80, align: 'right' },
  { title: '启用', key: 'enabled', width: 80 },
  { title: '操作', key: 'action', width: 270 },
]

let searchTimer = null
/* a-input-search 的内容变化统一走 watch（组件不派发原生 input 事件），
 * 300ms 防抖（对照旧 handleSearch），输入稳定后回到第一页再查询 */
watch(search, () => {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => {
    page.value = 1
    load()
  }, 300)
})

function onFilterChange() {
  page.value = 1
  load()
}

function onPageChange(p, size) {
  page.value = p
  if (size && size !== pageSize.value) pageSize.value = size
  load()
}

function onPageSizeChange(size) {
  pageSize.value = size
  page.value = 1
  load()
}

async function load() {
  loading.value = true
  try {
    // 空串筛选不下发（对照旧 ListParams 的可选语义）
    const query = { page: page.value, page_size: pageSize.value }
    if (providerFilter.value) query.provider = providerFilter.value
    if (enabledFilter.value) query.enabled = enabledFilter.value
    if (search.value.trim()) query.search = search.value.trim()
    const data = await listMonitors(query)
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

/* ---------- 行内启用 / 禁用（PUT :id {enabled}，失败回滚 UI） ---------- */
const togglingId = ref(null)
async function toggleEnabled(row) {
  const next = !row.enabled
  togglingId.value = row.id
  try {
    await updateMonitor(row.id, { enabled: next })
    row.enabled = next
  } catch (e) {
    message.error(`切换失败：${e.message}`)
  } finally {
    togglingId.value = null
  }
}

/* ---------- 立即检测：results 逐模型弹窗（替代单一 toast） ---------- */
const runningId = ref(null)
const runOpen = ref(false)
const runResults = ref([])
async function runNow(row) {
  if (runningId.value != null) return
  runningId.value = row.id
  try {
    const res = await runMonitor(row.id)
    runResults.value = res.results || []
    runOpen.value = true
    await load() // 刷新行状态（对照旧 void reload()）
  } catch (e) {
    // V2 模式下后端 403：active probes 已退役，原样展示后端 message
    message.error(`检测失败：${e.message}`)
  } finally {
    runningId.value = null
  }
}

/* ---------- 检测历史弹窗：GET /admin/channel-monitors/:id/history（逻辑在 MonitorHistoryDialog） ---------- */
const historyOpen = ref(false)
const historyTarget = ref(null)
function openHistory(row) { historyTarget.value = row; historyOpen.value = true }

/* ---------- 监控复制：确认弹窗 → POST :id/duplicate（Idempotency-Key） ---------- */
const dupOpen = ref(false)
const dupTarget = ref(null)
const duplicatingId = ref(null)
const dupConfirmMessage = () => {
  const s = dupTarget.value
  if (!s) return ''
  return `将复制「${s.name}」的服务商（${PROVIDER_LABELS[s.provider] || s.provider}）、主模型（${s.primary_model}）、目标分组（${s.group_name || '未分组'}）、协议、Header/Body 模板、检测间隔和服务端加密密钥。副本默认停用且不继承历史结果，确认继续吗？`
}
async function confirmDuplicate() {
  const s = dupTarget.value
  if (!s || duplicatingId.value != null) return
  duplicatingId.value = s.id
  try {
    const dup = await duplicateMonitor(s.id)
    dupOpen.value = false
    dupTarget.value = null
    await load()
    message.success(`渠道监控已复制为「${dup.name}」，当前保持停用，请确认分组和模型后再启用`)
    openEdit(dup) // 复制后直接进入编辑（对照旧 confirmDuplicate）
  } catch (e) {
    message.error(`复制渠道监控失败：${e.message}`)
  } finally {
    duplicatingId.value = null
  }
}

/* ---------- 删除（popconfirm 二次确认） ---------- */
async function remove(row) {
  try {
    await deleteMonitor(row.id)
    message.success('监控删除成功')
    await load()
  } catch (e) {
    message.error(`删除失败：${e.message}`)
  }
}

/* ---------- 新增 / 编辑弹窗 ---------- */
const formOpen = ref(false)
const editing = ref(null)
const openCreate = () => { editing.value = null; formOpen.value = true }
const openEdit = (row) => { editing.value = row; formOpen.value = true }
const tplOpen = ref(false)

onMounted(() => {
  loadSettings()
  load()
})
</script>

<template>
  <div class="cmon">
    <!-- 顶部：V1/V2 双视图切换 -->
    <div class="view-bar">
      <a-segmented
        :value="view"
        :options="[
          { value: 'v2', label: '真实流量 V2' },
          { value: 'v1', label: monitorMode === 'v2' ? 'V1 历史配置' : '主动探测 V1' },
        ]"
        @change="onViewChange"
      />
      <span class="view-desc">{{ view === 'v2' ? '根据真实网关请求被动统计，不额外消耗上游额度。' : '监测各渠道的可用性、延迟和状态' }}</span>
      <span style="flex: 1"></span>
      <a-button size="small" @click="tplOpen = true">请求模板</a-button>
    </div>

    <!-- ===== V2 被动统计面板（总览 / 矩阵视图 双子页签） ===== -->
    <template v-if="view === 'v2' && monitorMode === 'v2'">
      <div class="v2-tabs">
        <a-segmented
          v-model:value="v2Tab"
          :options="[
            { value: 'overview', label: '总览' },
            { value: 'matrix', label: '矩阵视图' },
          ]"
        />
      </div>
      <MonitorV2Panel v-if="v2Tab === 'overview'" />
      <MonitorV2MatrixPanel v-else />
    </template>
    <div v-else-if="view === 'v2'" class="v2-hint">
      <div class="v2-hint-t">当前使用 V1 主动探测</div>
      <div class="v2-hint-d">请在系统设置中切换到 V2 被动统计后查看真实流量指标。</div>
    </div>

    <!-- ===== V1 主动探测列表 ===== -->
    <template v-else>
      <!-- 工具栏：操作组｜筛选组｜搜索组 -->
      <div class="toolbar">
        <a-button type="primary" @click="openCreate">+ 新增监控</a-button>
        <span class="total">共 {{ total }} 个监控项</span>
        <a-select v-model:value="providerFilter" :options="providerOptions" style="width: 130px" @change="onFilterChange" />
        <a-select v-model:value="enabledFilter" :options="enabledOptions" style="width: 110px" @change="onFilterChange" />
        <a-input v-model:value="search" placeholder="搜索监控名称..." style="width: 220px" allow-clear>
          <template #suffix><SearchOutlined class="suf-ico" /></template>
        </a-input>
        <a-button v-if="err" danger size="small" @click="load">加载失败，点击重试</a-button>
      </div>
      <div v-if="err" class="err-line">{{ err }}</div>

      <a-table
        :columns="columns" :data-source="rows" :loading="loading" row-key="id" size="small"
        :scroll="{ x: 'max-content' }"
        :pagination="{
          current: page, pageSize, total, showSizeChanger: true, pageSizeOptions: ['10', '20', '50'],
          showTotal: (t) => `共 ${t} 条`, onChange: onPageChange, onShowSizeChange: onPageSizeChange,
        }"
      >
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'name'">
            <span class="name-cell">
              {{ record.name }}
              <!-- api_key_decrypt_failed：Key 解密失败，后端已跳过该监控的检测 -->
              <a-tooltip v-if="record.api_key_decrypt_failed" title="API Key 解密失败，请重新编辑该监控并填入新的 Key">
                <WarningOutlined class="warn-icon" />
              </a-tooltip>
            </span>
          </template>
          <template v-else-if="column.key === 'provider'">
            <span
              class="prov-tag"
              :style="{ color: PROVIDER_META[record.provider]?.color, background: PROVIDER_META[record.provider]?.bg }"
            >{{ PROVIDER_LABELS[record.provider] || record.provider }}</span>
          </template>
          <!-- 主模型 + 附加模型徽标（hover 弹逐模型状态表，等价旧 MonitorPrimaryModelCell；
               数据源行内 extra_models_status，字段 {model,status,latency_ms}） -->
          <template v-else-if="column.key === 'primary_model'">
            <span class="pm-cell">
              <span class="mono">{{ record.primary_model || '-' }}</span>
              <MonitorExtraModelsBadge v-if="(record.extra_models || []).length" :row="record" />
            </span>
          </template>
          <template v-else-if="column.key === 'status'">
            <a-tag v-if="record.primary_status" :color="statusColor(record.primary_status)">{{ statusLabel(record.primary_status) }}</a-tag>
            <span v-else class="dim">未检测</span>
          </template>
          <template v-else-if="column.key === 'latency'">
            {{ record.primary_latency_ms != null ? `${record.primary_latency_ms}ms` : '—' }}
          </template>
          <template v-else-if="column.key === 'avail'">
            {{ fmtAvailability(record) }}
          </template>
          <template v-else-if="column.key === 'interval'">
            {{ record.interval_seconds }}s
          </template>
          <template v-else-if="column.key === 'enabled'">
            <a-switch
              size="small"
              :checked="record.enabled"
              :loading="togglingId === record.id"
              @change="toggleEnabled(record)"
            />
          </template>
          <template v-else-if="column.key === 'action'">
            <a-space :size="4">
              <a-button size="small" :loading="runningId === record.id" @click="runNow(record)">
                <template #icon><PlayCircleOutlined /></template>检测
              </a-button>
              <a-button size="small" :loading="duplicatingId === record.id" @click="dupTarget = record; dupOpen = true">
                <template #icon><CopyOutlined /></template>复制
              </a-button>
              <a-button size="small" type="primary" @click="openEdit(record)">
                <template #icon><EditOutlined /></template>编辑
              </a-button>
              <a-button size="small" @click="openHistory(record)">
                <template #icon><HistoryOutlined /></template>历史
              </a-button>
              <a-popconfirm
                :title="`确定要删除监控「${record.name}」吗？此操作不可撤销。`"
                ok-text="删除" cancel-text="取消" :ok-button-props="{ danger: true }"
                @confirm="remove(record)"
              >
                <a-button size="small" danger>
                  <template #icon><DeleteOutlined /></template>
                </a-button>
              </a-popconfirm>
            </a-space>
          </template>
        </template>
      </a-table>
    </template>

    <!-- 新增/编辑（表单弹窗组件内部自校验与提交） -->
    <MonitorFormDialog
      :open="formOpen"
      :monitor="editing"
      :system-interval="systemInterval"
      @close="formOpen = false"
      @saved="load"
    />

    <!-- 立即检测结果（逐模型） -->
    <MonitorRunResultDialog :open="runOpen" :results="runResults" @close="runOpen = false" />

    <!-- 检测历史（GET :id/history，模型筛选与分页在组件内） -->
    <MonitorHistoryDialog :open="historyOpen" :monitor="historyTarget" @close="historyOpen = false" />

    <!-- 复制确认弹窗 -->
    <a-modal
      v-model:open="dupOpen"
      title="确认复制渠道监控"
      width="520px"
      :confirm-loading="duplicatingId != null"
      ok-text="复制"
      cancel-text="取消"
      @ok="confirmDuplicate"
      @cancel="dupTarget = null"
    >
      <p class="dup-msg">{{ dupConfirmMessage() }}</p>
    </a-modal>

    <!-- 请求模板管理（旧 MonitorTemplateManagerDialog 体系） -->
    <MonitorTemplatesDialog v-model:open="tplOpen" @changed="load" />
  </div>
</template>

<style scoped>
/* 应用内容只纵向滚动；横向溢出一律不允许（echarts canvas 首测宽度
 * 与 antd 表格 min-content 都可能在开窗瞬间撑破容器，用户反馈
 * 「打开时溢出一会才恢复」） */
.cmon { height: 100%; overflow-y: auto; overflow-x: hidden; }
/* grid 单列显式 minmax(0,1fr)：禁止子项 min-width:auto 把轨道撑到内容宽 */
.cmon > :deep(.v2), .v2-root { min-width: 0; }

/* V2 子页签 */
.v2-tabs { margin-bottom: 12px; }

/* 视图切换条 */
.view-bar {
  display: flex; align-items: center; gap: 12px; flex-wrap: wrap;
  margin-bottom: 12px;
}
.view-desc { font-size: 12px; color: var(--text2); }

/* V1 模式下的 V2 提示卡片 */
.v2-hint {
  padding: 40px 20px; text-align: center; background: var(--card); border-radius: 10px;
}
.v2-hint-t { font-size: 15px; font-weight: 500; }
.v2-hint-d { font-size: 12.5px; color: var(--text2); margin-top: 6px; }

/* 工具栏：操作组｜筛选组｜搜索组（组间 16px 由 gap 控制） */
.toolbar { display: flex; align-items: center; gap: 16px; flex-wrap: wrap; margin-bottom: 10px; }
.total { font-size: 12px; color: var(--text2); margin-right: 4px; }
.err-line { font-size: 12px; color: #d13438; margin: -4px 0 8px; }

/* 单元格 */
.name-cell { display: inline-flex; align-items: center; gap: 6px; font-weight: 500; }
.warn-icon { color: #d13438; font-size: 13px; }
.pm-cell { display: inline-flex; align-items: center; gap: 6px; min-width: 0; }
.prov-tag {
  display: inline-flex; align-items: center; height: 20px; padding: 0 7px;
  border-radius: 4px; font-size: 11.5px;
}
.mono { font-family: Consolas, monospace; font-size: 12.5px; }
.dim { color: var(--text2); font-size: 12px; }

.dup-msg { font-size: 13px; line-height: 1.7; margin: 0; }
</style>

