<script setup>
/* 告警规则卡（二期）：规则列表 + 新建/编辑弹窗 + 删除确认
 * 契约来源：旧 OpsAlertRulesCard.vue + frontend/src/api/admin/ops.ts
 * - GET/POST /admin/ops/alert-rules、PUT/DELETE /admin/ops/alert-rules/:id
 * - 分组选项 GET /admin/groups/all → { id, name }
 * - 14 种指标分 system/group/account 3 组；group_* 指标 filters.group_id 必填
 * - 严重级 P0-P3、窗口 1/5/60m、连续样本 1-1440、冷却 0-1440
 * - 选择指标时自动填推荐操作符 + 阈值（任务要求，推荐值自旧 metricDefinitions） */
import { ref, reactive, computed, onMounted } from 'vue'
import { message, Modal } from 'ant-design-vue'
import { PlusOutlined, ReloadOutlined } from '@ant-design/icons-vue'
import { request } from '../../api'

const loading = ref(false)
const rules = ref([])

/* ---------- 14 种指标定义（label/推荐值全部自旧代码 metricDefinitions 核实） ---------- */
const METRIC_DEFS = [
  // 系统级 6 种
  { type: 'success_rate', group: 'system', label: '成功率 (%)', desc: '统计窗口内成功请求占比（0~100）。', op: '<', th: 99, unit: '%' },
  { type: 'error_rate', group: 'system', label: '错误率 (%)', desc: '统计窗口内失败请求占比（0~100）。', op: '>', th: 1, unit: '%' },
  { type: 'upstream_error_rate', group: 'system', label: '上游错误率 (%)', desc: '统计窗口内上游错误占比（0~100）。', op: '>', th: 1, unit: '%' },
  { type: 'cpu_usage_percent', group: 'system', label: 'CPU 使用率 (%)', desc: '当前实例 CPU 使用率（0~100）。', op: '>', th: 80, unit: '%' },
  { type: 'memory_usage_percent', group: 'system', label: '内存使用率 (%)', desc: '当前实例内存使用率（0~100）。', op: '>', th: 80, unit: '%' },
  { type: 'concurrency_queue_depth', group: 'system', label: '并发排队深度', desc: '统计窗口内并发队列排队深度（等待中的请求数）。', op: '>', th: 10, unit: '' },
  // 分组级 3 种（必须选分组）
  { type: 'group_available_accounts', group: 'group', label: '分组可用账号数', desc: '指定分组中当前可用账号数量（需要 group_id 过滤）。', op: '<', th: 1, unit: '' },
  { type: 'group_available_ratio', group: 'group', label: '分组可用比例 (%)', desc: '指定分组中可用账号占比（0~100，需要 group_id 过滤）。', op: '<', th: 50, unit: '%' },
  { type: 'group_rate_limit_ratio', group: 'group', label: '分组限流比例 (%)', desc: '指定分组中账号被限流的比例（0~100，需要 group_id 过滤）。', op: '>', th: 10, unit: '%' },
  // 账号级 5 种
  { type: 'account_rate_limited_count', group: 'account', label: '限流账号数', desc: '统计窗口内被限流的账号数量。', op: '>', th: 0, unit: '' },
  { type: 'account_error_count', group: 'account', label: '错误账号数（不含临时不可调度）', desc: '统计窗口内产生错误的账号数量（不含临时不可调度）。', op: '>', th: 0, unit: '' },
  { type: 'account_error_ratio', group: 'account', label: '错误账号比例 (%)', desc: '统计窗口内错误账号占比（0~100）。', op: '>', th: 5, unit: '%' },
  { type: 'account_temp_unscheduled_count', group: 'account', label: '临时不可调度账号数', desc: '当前处于临时不可调度状态的账号数量（如代理/凭据故障被自动摘除）。', op: '>', th: 0, unit: '' },
  { type: 'overload_account_count', group: 'account', label: '过载账号数', desc: '统计窗口内过载账号数量。', op: '>', th: 0, unit: '' },
]
const GROUP_LABEL = { system: '系统指标', group: '分组级别指标（需 group_id）', account: '账号级别指标' }
const GROUP_METRICS = new Set(METRIC_DEFS.filter((m) => m.group === 'group').map((m) => m.type))

/* a-select 分组选项结构：{ label, options } */
const metricOptions = ['system', 'group', 'account'].map((g) => ({
  label: GROUP_LABEL[g],
  options: METRIC_DEFS.filter((m) => m.group === g).map((m) => ({ value: m.type, label: m.label })),
}))
const operatorOptions = ['>', '>=', '<', '<=', '==', '!='].map((v) => ({ value: v, label: v }))
const severityOptions = ['P0', 'P1', 'P2', 'P3'].map((v) => ({ value: v, label: v }))
const windowOptions = [1, 5, 60].map((v) => ({ value: v, label: `${v}m` }))

/* ---------- 数据加载 ---------- */
const load = async () => {
  loading.value = true
  try { rules.value = await request('/admin/ops/alert-rules', { method: 'GET' }) || [] }
  catch (e) { message.error(`加载告警规则失败：${e.message}`); rules.value = [] }
  finally { loading.value = false }
}
const sortedRules = computed(() => [...rules.value].sort((a, b) => (b.id || 0) - (a.id || 0)))

/* ---------- 分组选项 ---------- */
const groups = ref([])
const loadGroups = async () => {
  try { groups.value = await request('/admin/groups/all', { method: 'GET' }) || [] }
  catch { groups.value = [] }
}
const groupOptions = computed(() => groups.value.map((g) => ({ value: g.id, label: g.name })))
const groupName = (id) => groups.value.find((g) => g.id === id)?.name || `#${id}`

/* ---------- 编辑器 ---------- */
const open = ref(false)
const saving = ref(false)
const editingId = ref(null)
const draft = reactive({ name: '', description: '', enabled: true, metric_type: 'error_rate', operator: '>', threshold: 1, window_minutes: 1, sustained_minutes: 2, severity: 'P1', cooldown_minutes: 10, notify_email: true, filters: {} })

const newDraft = () => Object.assign(draft, { name: '', description: '', enabled: true, metric_type: 'error_rate', operator: '>', threshold: 1, window_minutes: 1, sustained_minutes: 2, severity: 'P1', cooldown_minutes: 10, notify_email: true, filters: {} })

const openCreate = () => { editingId.value = null; newDraft(); open.value = true }
const openEdit = (r) => {
  editingId.value = r.id
  Object.assign(draft, JSON.parse(JSON.stringify(r)))
  if (!draft.filters) draft.filters = {}
  open.value = true
}

const isGroupMetric = computed(() => GROUP_METRICS.has(draft.metric_type))
const curDef = computed(() => METRIC_DEFS.find((m) => m.type === draft.metric_type) || null)
const draftGroupId = computed({
  get: () => { const n = Number(draft.filters?.group_id); return Number.isFinite(n) && n > 0 ? n : null },
  set: (v) => {
    if (v == null) delete draft.filters.group_id
    else draft.filters.group_id = v
  },
})

/* 指标变更：自动填推荐操作符 + 阈值（新建/编辑一致，与任务要求一致） */
const onMetricChange = () => {
  const d = METRIC_DEFS.find((m) => m.type === draft.metric_type)
  if (!d) return
  draft.operator = d.op
  draft.threshold = d.th
  if (!GROUP_METRICS.has(draft.metric_type)) delete draft.filters.group_id
}

/* 表单校验面板（规则与旧 editorValidation 完全一致） */
const validation = computed(() => {
  const errors = []
  if (!draft.name || !String(draft.name).trim()) errors.push('名称不能为空')
  if (!draft.metric_type) errors.push('指标不能为空')
  if (GROUP_METRICS.has(draft.metric_type) && !draftGroupId.value) errors.push('分组级别指标必须指定 group_id')
  if (!draft.operator) errors.push('运算符不能为空')
  if (!(typeof draft.threshold === 'number' && Number.isFinite(draft.threshold))) errors.push('阈值必须为数字')
  if (!([1, 5, 60].includes(draft.window_minutes))) errors.push('统计窗口必须为 1 / 5 / 60 分钟之一')
  if (!(typeof draft.sustained_minutes === 'number' && Number.isFinite(draft.sustained_minutes) && draft.sustained_minutes >= 1 && draft.sustained_minutes <= 1440)) errors.push('连续样本数必须在 1 到 1440 之间')
  if (!(typeof draft.cooldown_minutes === 'number' && Number.isFinite(draft.cooldown_minutes) && draft.cooldown_minutes >= 0 && draft.cooldown_minutes <= 1440)) errors.push('冷却期必须在 0 到 1440 分钟之间')
  return { valid: errors.length === 0, errors }
})

const save = async () => {
  if (!validation.value.valid) { message.error(validation.value.errors[0]); return }
  saving.value = true
  try {
    const body = { ...draft }
    if (!Object.keys(body.filters || {}).length) delete body.filters
    if (editingId.value) await request(`/admin/ops/alert-rules/${editingId.value}`, { method: 'PUT', body })
    else await request('/admin/ops/alert-rules', { method: 'POST', body })
    open.value = false
    message.success('警报规则保存成功')
    await load()
  } catch (e) { message.error(`保存告警规则失败：${e.message}`) }
  finally { saving.value = false }
}

const requestDelete = (r) => {
  Modal.confirm({
    title: '确认删除该规则？',
    content: `「${r.name}」将删除该规则及其关联的告警事件，是否继续？`,
    okText: '删除', okType: 'danger', cancelText: '取消',
    onOk: async () => {
      try { await request(`/admin/ops/alert-rules/${r.id}`, { method: 'DELETE' }); message.success('警报规则删除成功'); await load() }
      catch (e) { message.error(`删除告警规则失败：${e.message}`) }
    },
  })
}

const sevColor = (s) => ({ P0: 'red', P1: 'orange', P2: 'blue', P3: 'default' }[String(s || '').toUpperCase()] || 'default')

const columns = [
  { title: '名称', key: 'name', width: 220 },
  { title: '指标', key: 'metric', width: 260 },
  { title: '级别', key: 'sev', width: 70 },
  { title: '窗口/持续/冷却', key: 'win', width: 150 },
  { title: '邮件', key: 'mail', width: 70 },
  { title: '状态', key: 'st', width: 80 },
  { title: '更新时间', key: 'up', width: 140 },
  { title: '操作', key: 'act', width: 120 },
]
const fmtTime = (t) => (t ? String(t).replace('T', ' ').slice(5, 16) : '-')

onMounted(() => { load(); loadGroups() })
/* rootEl 供父组件「查看规则」滚动定位使用（script-setup 组件默认无 $el） */
const rootEl = ref(null)
defineExpose({ refresh: load, rootEl })
</script>

<template>
  <div ref="rootEl" class="card">
    <div class="card-h">
      <div>
        <div class="sec-t">告警规则</div>
        <div class="sub-t">创建与管理系统阈值告警（仅邮件通知）</div>
      </div>
      <div class="h-btns">
        <a-button size="small" :loading="loading" @click="load"><template #icon><ReloadOutlined /></template></a-button>
        <a-button size="small" type="primary" @click="openCreate"><template #icon><PlusOutlined /></template>新建规则</a-button>
      </div>
    </div>

    <a-table
      :columns="columns" :data-source="sortedRules" row-key="id" size="small" :loading="loading"
      :scroll="{ x: 'max-content' }" :pagination="false" :locale="{ emptyText: '暂无告警规则' }"
    >
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'name'">
          <div class="r-name">{{ record.name }}</div>
          <div v-if="record.description" class="r-desc">{{ record.description }}</div>
          <div v-if="record.filters?.group_id" class="r-desc">分组：{{ groupName(record.filters.group_id) }}</div>
        </template>
        <template v-else-if="column.key === 'metric'">
          <span class="mono">{{ record.metric_type }}</span>
          <span class="op">{{ record.operator }}</span>
          <span class="mono">{{ record.threshold }}</span>
        </template>
        <template v-else-if="column.key === 'sev'"><a-tag :color="sevColor(record.severity)" style="margin: 0">{{ record.severity }}</a-tag></template>
        <template v-else-if="column.key === 'win'">
          <span class="dim">{{ record.window_minutes }}m / 持续{{ record.sustained_minutes }}m / 冷却{{ record.cooldown_minutes }}m</span>
        </template>
        <template v-else-if="column.key === 'mail'">
          <a-tag :color="record.notify_email ? 'green' : 'default'" style="margin: 0">{{ record.notify_email ? '是' : '否' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'st'">
          <a-tag :color="record.enabled ? 'green' : 'default'" style="margin: 0">{{ record.enabled ? '已启用' : '已停用' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'up'"><span class="dim">{{ fmtTime(record.updated_at) }}</span></template>
        <template v-else-if="column.key === 'act'">
          <a-space :size="4">
            <a-button size="small" @click="openEdit(record)">编辑</a-button>
            <a-button size="small" danger @click="requestDelete(record)">删除</a-button>
          </a-space>
        </template>
      </template>
    </a-table>

    <!-- 新建/编辑弹窗 -->
    <a-modal
      v-model:open="open" :title="editingId ? '编辑告警规则' : '新建告警规则'" width="680px"
      :confirm-loading="saving" ok-text="保存" cancel-text="取消" @ok="save"
    >
      <div v-if="!validation.valid" class="val-panel">
        <div class="val-t">请先修正以下问题</div>
        <ul><li v-for="e in validation.errors" :key="e">{{ e }}</li></ul>
      </div>

      <div class="f-grid">
        <label class="f-label">名称</label>
        <a-input v-model:value="draft.name" placeholder="规则名称" />
        <label class="f-label">描述</label>
        <a-input v-model:value="draft.description" placeholder="可选描述" />

        <label class="f-label">指标</label>
        <div>
          <a-select v-model:value="draft.metric_type" :options="metricOptions" style="width: 100%" @change="onMetricChange" />
          <div v-if="curDef" class="f-hint">
            <div>{{ curDef.desc }}</div>
            <div>推荐：运算符 {{ curDef.op }}，阈值 {{ curDef.th }}{{ curDef.unit }}（已自动填入）</div>
          </div>
        </div>

        <label class="f-label">运算符</label>
        <a-select v-model:value="draft.operator" :options="operatorOptions" style="width: 100%" />

        <label class="f-label">分组（group_id）<span v-if="isGroupMetric" class="req">*</span></label>
        <div>
          <a-select
            v-model:value="draftGroupId" :options="groupOptions" show-search option-filter-prop="label"
            style="width: 100%" allow-clear placeholder="请选择分组" :class="{ 'sel-err': isGroupMetric && !draftGroupId }"
          />
          <div class="f-hint">{{ isGroupMetric ? '该指标为分组级别指标，必须选择分组（group_id）。' : '可选：通过 group_id 将规则限定到某个分组。' }}</div>
        </div>

        <label class="f-label">阈值</label>
        <a-input-number v-model:value="draft.threshold" style="width: 100%" />
        <label class="f-label">级别</label>
        <a-select v-model:value="draft.severity" :options="severityOptions" style="width: 100%" />
        <label class="f-label">统计窗口（分钟）</label>
        <a-select v-model:value="draft.window_minutes" :options="windowOptions" style="width: 100%" />
        <label class="f-label">连续样本数（每分钟）</label>
        <a-input-number v-model:value="draft.sustained_minutes" :min="1" :max="1440" style="width: 100%" />
        <label class="f-label">冷却期（分钟）</label>
        <a-input-number v-model:value="draft.cooldown_minutes" :min="0" :max="1440" style="width: 100%" />

        <label class="f-label">启用</label>
        <a-checkbox v-model:checked="draft.enabled">启用该规则</a-checkbox>
        <label class="f-label">邮件通知</label>
        <a-checkbox v-model:checked="draft.notify_email">发送邮件通知</a-checkbox>
      </div>
    </a-modal>
  </div>
</template>

<style scoped>
.card { background: var(--card); border: 1px solid var(--hr); border-radius: 10px; padding: 12px 14px; min-width: 0; }
.card-h { display: flex; align-items: flex-start; justify-content: space-between; gap: 10px; margin-bottom: 10px; flex-wrap: wrap; }
.sec-t { font-size: 13px; font-weight: 500; }
.sub-t { font-size: 11px; color: var(--text2); margin-top: 2px; }
.h-btns { display: flex; gap: 8px; }
.r-name { font-weight: 500; }
.r-desc { font-size: 11px; color: var(--text2); margin-top: 2px; white-space: normal; word-break: break-all; }
.mono { font-family: Consolas, monospace; font-size: 12px; }
.op { margin: 0 6px; color: var(--text2); }
.dim { font-size: 12px; color: var(--text2); }
.val-panel { background: rgba(220, 38, 38, 0.08); border: 1px solid rgba(220, 38, 38, 0.25); border-radius: 8px; padding: 10px 12px; font-size: 12px; color: #dc2626; margin-bottom: 12px; }
.val-t { font-weight: 500; }
.val-panel ul { margin: 4px 0 0 18px; }
.f-grid { display: grid; grid-template-columns: 150px 1fr; gap: 12px 12px; align-items: center; }
.f-label { font-size: 12px; color: var(--text2); text-align: right; }
.f-hint { font-size: 11px; color: var(--text2); margin-top: 4px; }
.req { color: #dc2626; }
.sel-err :deep(.ant-select-selector) { border-color: #dc2626 !important; }
@media (max-width: 640px) {
  .f-grid { grid-template-columns: 1fr; }
  .f-label { text-align: left; }
}
</style>
