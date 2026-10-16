<script setup>
import W12Button from '../components/design/W12Button.vue'
import W12Input from '../components/design/W12Input.vue'
import W12Select from '../components/design/W12Select.vue'
import W12Table from '../components/design/W12Table.vue'
import W12Dialog from '../components/design/W12Dialog.vue'
/* 渠道管理 v2：列表 + 平台维度编辑弹窗 + 模型定价编辑器
 * 接口：GET/POST /admin/channels、PUT/DELETE /admin/channels/:id、
 *   GET /admin/groups/all、GET /admin/channels/pricing/sync-models（PlatformSection 内）、
 *   GET /admin/channels/model-pricing（PricingEntryCard 内）、GET /admin/settings/web-search-emulation
 * 契约来源：旧 views/admin/ChannelsView.vue（handleSubmit/formToAPI/apiToForm）、
 *   components/admin/channel/*、internal/handler/admin/channel_handler.go 请求结构
 * 提交 body（PUT/POST）：name/description/status/group_ids/model_pricing/model_mapping/
 *   billing_model_source/restrict_models/features_config/apply_pricing_to_account_stats/
 *   account_stats_pricing_rules（StatsRulesEditor 全量编辑；PUT 语义 = 整组替换）
 * features_config 写入格式（以网关运行时读取为准，internal/service/channel.go）：
 *   web_search_emulation={anthropic:bool}、codex_image_generation_bridge={openai:bool}、
 *   bedrock_cc_compat=bool（渠道级；旧前端写平台 map 为 bug，后端只读布尔） */
import { ref, reactive, computed, onMounted, onUnmounted } from 'vue'
import { setChannelPlatform } from '../composables/channelPlatforms'
import { confirmAction } from '../utils/confirmAction'
import { request as apiRequest, isCancellation } from '../api'
import { useRequestLifetime } from '../composables/useRequestLifetime'
import { createLatestRequest, runLatest } from '../api/latestRequest'
import { channelToSections, validateChannelForm, channelFormToBody } from '../composables/channelFormContract'
import { message } from 'ant-design-vue'
import { FormOutlined, DeleteOutlined, SearchOutlined } from '@ant-design/icons-vue'
import PlatformSection from '../components/channel/PlatformSection.vue'
import StatsRulesEditor from '../components/channel/StatsRulesEditor.vue'
import { apiStatsRulesToForm } from '../components/channel/statsRules'
import { PLATFORM_ORDER, PLATFORM_META, platformLabel } from '../components/channel/pricing'

/* ===== 列表 =====
 * 筛选/排序契约 = internal/handler/admin/channel_handler.go List + channelListOrderBy：
 * status（active/disabled）、search（name/description ILIKE，超 100 字后端截断）、
 * sort_by 白名单 id/name/status/created_at，sort_order asc/desc，默认 created_at desc */
const loading = ref(false)
const rows = ref([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const err = ref('')
const groups = ref([])
const fSearch = ref('')
const fStatus = ref('')
const sortState = reactive({ sort_by: 'created_at', sort_order: 'desc' })

const statusFilterOptions = [
  { value: '', label: '全部状态' },
  { value: 'active', label: '启用' },
  { value: 'disabled', label: '停用' },
]

function orderOf(key) {
  if (sortState.sort_by !== key) return false
  return sortState.sort_order === 'asc' ? 'ascend' : 'descend'
}

// computed：sortState 变化时受控排序箭头同步刷新（对照 Groups.vue）
const columns = computed(() => [
  { title: '名称', dataIndex: 'name', key: 'name', ellipsis: true },
  { title: '描述', dataIndex: 'description', key: 'description', ellipsis: true },
  { title: '状态', key: 'status', width: 80 },
  { title: '平台', key: 'platforms', width: 150 },
  { title: '绑定分组', key: 'groups', width: 120 },
  { title: '定价条目', key: 'pricing', width: 90 },
  { title: '创建时间', dataIndex: 'created_at', key: 'created_at', width: 104, sorter: true, sortOrder: orderOf('created_at') },
  { title: '操作', key: 'action', width: 130 },
])

/* ===== 编辑弹窗 ===== */
const editOpen = ref(false)
const editSaving = ref(false)
const editing = ref(null) // 编辑中的渠道行（null = 新建）
const activeKeys = ref([]) // a-collapse 展开的平台面板

// 计费模型来源四选一（对照旧 billingModelSourceOptions + 后端 oneof 校验）
const billingSourceOptions = [
  { value: 'channel_mapped', label: '按映射后模型计费', hint: '按渠道映射完成后的模型价格扣费。' },
  { value: 'requested', label: '按用户请求模型计费', hint: '客户请求什么模型，就按这个模型的价格扣费。（推荐）' },
  { value: 'upstream', label: '按实际上游模型计费', hint: '按上游实际使用的模型价格扣费。' },
  { value: 'response_model', label: '按响应声明模型计费', hint: '只在声明唯一、价格可信时采用，否则自动回退。' },
]

const blankForm = () => ({
  name: '',
  description: '',
  status: 'active',
  restrict_models: false,
  billing_model_source: 'channel_mapped',
  apply_pricing_to_account_stats: false,
  platforms: [],
  stats_rules: [], // 账号统计定价规则（编辑器就地变更，保存时全量携带）
})
const form = ref(blankForm())
const lane = createLatestRequest()
const groupLane = createLatestRequest()
const groupsReady = ref(false), groupError = ref(''), editLoadError = ref('')
let editSequence = 0
let searchTimer = null
const editLoading = ref(false)
const lifetime = useRequestLifetime(() => {
  lane.cancel(); groupLane.cancel(); editSequence += 1; clearTimeout(searchTimer)
  rows.value = []; total.value = 0; groups.value = []; allChannels.value = []
  groupsReady.value = false; groupError.value = ''; editLoadError.value = ''
  form.value = blankForm(); editing.value = null; editOpen.value = false; activeKeys.value = []
})
const request = (path, options) => apiRequest(path, { ...lifetime, ...options })

const enabledSections = computed(() => form.value.platforms.filter((s) => s.enabled))
// 规则可引用分组 = 渠道已绑定分组的并集；规则定价条目的平台候选 = 已启用平台
const channelGroupIds = computed(() => enabledSections.value.flatMap((s) => s.group_ids))
const enabledPlatforms = computed(() => enabledSections.value.map((s) => s.platform))

/* 平台勾选按事件目标状态启停；禁用仍保留原定价与映射草稿。 */
function togglePlatform(p, enabled) {
  const sec = setChannelPlatform(form.value.platforms, p, enabled)
  if (sec.enabled) {
    if (!activeKeys.value.includes(p)) activeKeys.value.push(p)
  } else {
    activeKeys.value = activeKeys.value.filter((k) => k !== p)
  }
}

/* ===== 数据加载 ===== */
// 跨渠道分组占用检测：全量渠道构建 gid→渠道名（排除编辑中的渠道）
const allChannels = ref([])
const groupChannelMap = computed(() => {
  const map = {}
  for (const ch of allChannels.value) {
    if (editing.value && ch.id === editing.value.id) continue
    for (const gid of ch.group_ids || []) map[gid] = ch.name
  }
  return map
})

const groupsLoading = ref(false)
const loadGroups = async () => {
  groupsLoading.value = true
  groupsReady.value = false
  const result = await runLatest(groupLane, (signal) => request('/admin/groups/all', { method: 'GET', signal }), {
    apply: (items) => { groups.value = items || []; groupsReady.value = true; groupError.value = '' },
    error: (e) => { groupError.value = `分组读取失败：${e.message}，请重试后再保存渠道` },
    finish: () => { groupsLoading.value = false },
  })
  return result !== undefined && groupsReady.value
}
const loadAllChannels = async () => {
  try {
    const data = await request('/admin/channels', { method: 'GET', query: { page: 1, page_size: 1000 } })
    allChannels.value = data.items || []
  } catch (e) {
    if (!isCancellation(e)) allChannels.value = rows.value // 兜底用当前页
  }
}

const groupsByPlatform = (platform) => groups.value.filter((g) => g.platform === platform)

// 全局搜索仿真开关（web_search_emulation 开关显隐，对照旧 loadWebSearchGlobalState：
// enabled 且配置了服务商才显示）
const webSearchGlobalEnabled = ref(false)
const loadWebSearchState = async () => {
  try {
    const cfg = await request('/admin/settings/web-search-emulation', { method: 'GET' })
    webSearchGlobalEnabled.value = cfg?.enabled === true && (cfg?.providers?.length ?? 0) > 0
  } catch {
    webSearchGlobalEnabled.value = false
  }
}

const openCreate = () => {
  editSequence += 1
  editLoading.value = false
  editing.value = null
  form.value = blankForm()
  activeKeys.value = []
  editLoadError.value = ''
  loadGroups()
  loadAllChannels()
  editOpen.value = true
}

const openEdit = (r) => {
  editing.value = r
  form.value = {
    name: r.name,
    description: r.description || '',
    status: r.status || 'active',
    restrict_models: !!r.restrict_models,
    billing_model_source: r.billing_model_source || 'channel_mapped',
    apply_pricing_to_account_stats: !!r.apply_pricing_to_account_stats,
    platforms: [],
    // 先给空数组兜底（编辑器 watch/template 依赖），Promise 回填真实规则
    stats_rules: [],
  }
  activeKeys.value = []
  editOpen.value = true
  const turn = ++editSequence
  const targetForm = form.value
  editLoading.value = true
  editLoadError.value = ''
  // 分组决定原绑定的平台；读取失败必须停在重试态，不能把空列表转换成丢绑定的可保存表单。
  Promise.all([loadGroups(), loadAllChannels()]).then(([groupsLoaded]) => {
    if (!lifetime.isAlive() || turn !== editSequence || !editOpen.value || form.value !== targetForm) return
    if (!groupsLoaded) { editLoadError.value = groupError.value || '分组尚未读取完成，请重试'; return }
    form.value.platforms = channelToSections(r, groups.value)
    activeKeys.value = form.value.platforms.map((s) => s.platform)
    form.value.stats_rules = apiStatsRulesToForm(r.account_stats_pricing_rules)
  }).finally(() => { if (turn === editSequence) editLoading.value = false })
}

const save = async () => {
  if (editSaving.value || editLoading.value || !lifetime.isAlive()) return
  if (!groupsReady.value || editLoadError.value) { message.error(editLoadError.value || groupError.value || '请先成功读取平台分组'); return }
  const bad = validateChannelForm(form.value)
  if (bad) {
    message.error(bad.msg)
    if (bad.platform) activeKeys.value = [bad.platform] // 展开出错平台
    return
  }
  const { id, ...body } = channelFormToBody(form.value, editing.value)
  editSaving.value = true
  try {
    if (id) await request(`/admin/channels/${id}`, { method: 'PUT', body })
    else await request('/admin/channels', { method: 'POST', body })
    editOpen.value = false
    message.success(id ? '渠道已更新' : '渠道已创建')
    await load()
  } catch (e) {
    if (!isCancellation(e)) message.error(`保存失败：${e.message}`)
  } finally {
    editSaving.value = false
  }
}

const remove = async (r) => {
  if (!(await confirmAction({ title: '删除渠道', content: `确认删除渠道「${r.name}」？删除后其分组计费将失去渠道策略。`, okText: '删除', danger: true }))) return
  try {
    await request(`/admin/channels/${r.id}`, { method: 'DELETE' })
    message.success('渠道已删除')
    await load()
  } catch (e) {
    if (!isCancellation(e)) message.error(`删除失败：${e.message}`)
  }
}

/* ===== 列表加载 ===== */
const load = async () => {
  loading.value = true
  const query = {
    page: page.value, page_size: pageSize.value,
    status: fStatus.value || undefined, search: fSearch.value.trim() || undefined,
    sort_by: sortState.sort_by, sort_order: sortState.sort_order,
  }
  await runLatest(lane, (signal) => request('/admin/channels', { method: 'GET', query, signal }), {
    apply: (data) => { rows.value = data.items || []; total.value = data.total || 0; err.value = '' },
    error: (e) => { err.value = e.message; rows.value = []; total.value = 0 },
    finish: () => { loading.value = false },
  })
}
// 输入变化即取消旧读取，不能在防抖等待期显示旧条件的迟到响应。
const onSearchInput = () => {
  lane.cancel()
  clearTimeout(searchTimer)
  searchTimer = setTimeout(() => { page.value = 1; load() }, 300)
}
const onFilter = () => { clearTimeout(searchTimer); page.value = 1; load() }
// 表头排序/翻页统一走 table @change（避免 pagination 回调 + change 双载）
const onTableChange = (pg, _filters, sorter) => {
  if (sorter && sorter.order) {
    // sorter.field = dataIndex（created_at）；白名单外的列后端会回退 id 排序。
    // 排序时 pg.current 仍是点击前的旧页码，必须跳过页码同步，否则会覆盖下面的重置。
    sortState.sort_by = sorter.field || sorter.column?.key
    sortState.sort_order = sorter.order === 'ascend' ? 'asc' : 'desc'
    page.value = 1
  } else if (pg.current !== page.value) {
    page.value = pg.current
  }
  load()
}

/** 行内平台标签：分组 id → 平台名去重 */
const rowPlatforms = (r) => {
  const set = new Set()
  for (const gid of r.group_ids || []) {
    const p = groups.value.find((g) => g.id === gid)?.platform
    if (p) set.add(p)
  }
  for (const p of r.model_pricing || []) if (p.platform) set.add(p.platform)
  return [...set]
}

onMounted(() => {
  load()
  loadGroups()
  loadWebSearchState()
})
onUnmounted(() => { clearTimeout(searchTimer); lane.dispose(); groupLane.dispose() })
</script>

<template>
  <div class="channels w12-design">
    <div class="toolbar">
      <!-- 操作组 -->
      <div class="tgroup">
        <W12Button variant="primary" @click="openCreate">新增渠道</W12Button>
      </div>
      <span class="sep" />
      <!-- 筛选组：状态（GET /admin/channels?status=） -->
      <div class="tgroup">
        <W12Select v-model:value="fStatus" style="width: 104px" :options="statusFilterOptions" @change="onFilter" />
      </div>
      <!-- 搜索组：name/description 模糊匹配，300ms 防抖（GET /admin/channels?search=） -->
      <div class="tgroup">
        <W12Input
          v-model:value="fSearch" placeholder="搜索名称/描述" style="width: 200px" allow-clear
          @change="onSearchInput" @press-enter="onFilter"
        >
          <template #suffix><SearchOutlined class="suf-ico" @click="onFilter" /></template>
        </W12Input>
      </div>
      <span class="grow" />
      <span class="total">共 {{ total }} 个渠道</span>
      <W12Button v-if="err" compact @click="load" variant="danger">加载失败，点击重试</W12Button>
      <span v-if="err" class="total err">{{ err }}</span>
    </div>
    <W12Table
      :columns="columns" :data-source="rows" :loading="loading" row-key="id" size="small" :scroll="{ x: 'max-content' }"
      :pagination="{ current: page, pageSize, total, showSizeChanger: false }"
      @change="onTableChange"
    >
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'status'">
          <a-tag :color="record.status === 'active' ? 'green' : 'default'">{{ record.status === 'active' ? '启用' : '停用' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'created_at'">
          {{ (record.created_at || '').slice(0, 10) }}
        </template>
        <template v-else-if="column.key === 'platforms'">
          <span
            v-for="p in rowPlatforms(record)" :key="p"
            class="ptag" :style="{ color: PLATFORM_META[p]?.color, background: PLATFORM_META[p]?.bg }"
          >{{ PLATFORM_META[p]?.label || p }}</span>
        </template>
        <template v-else-if="column.key === 'groups'">
          <a-tag>{{ (record.group_ids || []).length }} 分组</a-tag>
        </template>
        <template v-else-if="column.key === 'pricing'">
          {{ (record.model_pricing || []).length }}
        </template>
        <template v-else-if="column.key === 'action'">
          <a-space>
            <a-tooltip title="编辑"><W12Button compact variant="text" @click="openEdit(record)"><FormOutlined /></W12Button></a-tooltip>
            <a-tooltip title="删除"><W12Button compact variant="danger" @click="remove(record)"><DeleteOutlined /></W12Button></a-tooltip>
          </a-space>
        </template>
      </template>
    </W12Table>

    <W12Dialog
      v-model:open="editOpen" :title="editing ? '编辑渠道' : '新增渠道'" :confirm-loading="editSaving" :busy="editSaving"
      ok-text="保存" cancel-text="取消" width="880px" wrap-class-name="channel-edit-modal" :ok-button-props="{ disabled: editLoading || !groupsReady || !!editLoadError }" @ok="save"
    >
      <a-alert v-if="editLoadError || groupError" type="error" show-icon :message="editLoadError || groupError">
        <template #action><W12Button :disabled="editLoading" @click="editing && editLoadError ? openEdit(editing) : loadGroups()">重试分组读取</W12Button></template>
      </a-alert>
      <a-form layout="vertical" size="small" class="cform">
        <!-- ===== 基础 ===== -->
        <div class="sect first">基础</div>
        <div class="grid2">
          <a-form-item label="名称" required>
            <W12Input v-model:value="form.name" placeholder="渠道名称" />
          </a-form-item>
          <a-form-item label="状态">
            <W12Select
              v-model:value="form.status"
              :options="[{ value: 'active', label: '启用' }, { value: 'disabled', label: '停用' }]"
            />
          </a-form-item>
        </div>
        <a-form-item label="描述">
          <a-textarea v-model:value="form.description" :rows="2" placeholder="选填" />
        </a-form-item>

        <!-- 平台多选 chips（对照旧 platformOrder 勾选区） -->
        <a-form-item label="启用平台">
          <div class="p-chips">
            <label
              v-for="p in PLATFORM_ORDER" :key="p"
              class="p-chip" :class="{ on: form.platforms.find((s) => s.platform === p)?.enabled }"
            >
              <input
                type="checkbox"
                :checked="!!form.platforms.find((s) => s.platform === p)?.enabled"
                @change="togglePlatform(p, $event.target.checked)"
              />
              <span :style="{ color: PLATFORM_META[p].color }">{{ PLATFORM_META[p].label }}</span>
            </label>
          </div>
          <div class="hint">勾选平台后可在下方展开区配置该平台的分组、映射与定价。</div>
        </a-form-item>

        <!-- 模型限制开关 -->
        <div class="frow">
          <div class="frow-main">
            <div class="frow-t">限制可调用模型</div>
            <div class="frow-d">开启后仅允许定价清单内的模型，其余请求将被拒绝。</div>
          </div>
          <a-switch v-model:checked="form.restrict_models" size="small" />
        </div>

        <!-- 计费模型来源（四选一卡片，对照旧 billingModelSourceOptions） -->
        <a-form-item label="计费模型来源">
          <div class="cards">
            <button
              v-for="o in billingSourceOptions" :key="o.value" type="button" class="card"
              :class="{ on: form.billing_model_source === o.value }" @click="form.billing_model_source = o.value"
            >
              <span class="card-t">{{ o.label }}</span>
              <span class="card-d">{{ o.hint }}</span>
            </button>
          </div>
          <div class="hint">决定价格表按哪个模型名称查找计费。</div>
        </a-form-item>

        <!-- 定价计入账号统计开关 -->
        <div class="frow">
          <div class="frow-main">
            <div class="frow-t">定价计入账号用量统计</div>
            <div class="frow-d">开启后按本渠道定价折算的金额会计入账号用量统计。</div>
          </div>
          <a-switch v-model:checked="form.apply_pricing_to_account_stats" size="small" />
        </div>

        <!-- ===== 账号统计定价规则（后端始终优先于上方开关与目录价生效，编辑器常驻） ===== -->
        <div class="sect">账号统计定价规则</div>
        <StatsRulesEditor
          :rules="form.stats_rules"
          :groups="groups"
          :channel-group-ids="channelGroupIds"
          :platforms="enabledPlatforms"
        />

        <!-- ===== 每平台折叠区 ===== -->
        <template v-if="enabledSections.length">
          <div class="sect">平台配置</div>
          <a-collapse v-model:active-key="activeKeys" class="p-collapse" expand-icon-position="end" ghost>
            <a-collapse-panel v-for="sec in enabledSections" :key="sec.platform">
              <template #header>
                <span class="pc-head">
                  <span class="ptag" :style="{ color: PLATFORM_META[sec.platform]?.color, background: PLATFORM_META[sec.platform]?.bg }">
                    {{ platformLabel(sec.platform) }}
                  </span>
                  <span class="pc-meta">{{ sec.group_ids.length }} 分组 · {{ sec.model_pricing.length }} 定价 · {{ Object.keys(sec.model_mapping).length }} 映射</span>
                </span>
              </template>
              <PlatformSection
                :section="sec"
                :groups="groupsByPlatform(sec.platform)"
                :groups-loading="groupsLoading"
                :group-channel-map="groupChannelMap"
                :web-search-global-enabled="webSearchGlobalEnabled"
              />
            </a-collapse-panel>
          </a-collapse>
        </template>
        <div v-else class="empty-tip">尚未启用任何平台，先在「启用平台」勾选至少一个平台</div>
      </a-form>
    </W12Dialog>
  </div>
</template>

<style scoped>
.channels { height: 100%; }
/* ===== 工具栏：操作组｜筛选组｜搜索组（组间 16px、底部 1px 分割线，Win12 规范） ===== */
.toolbar {
  display: flex; align-items: center; gap: 16px; flex-wrap: wrap; margin-bottom: 10px;
  padding-bottom: 10px; border-bottom: 1px solid var(--hr);
}
.toolbar .sep { width: 1px; height: 20px; background: var(--hr); flex: none; }
.toolbar .grow { flex: 1; }
.tgroup { display: flex; align-items: center; gap: 8px; }
.suf-ico { color: var(--text2); cursor: pointer; font-size: 12px; }
.total { font-size: 12px; color: var(--text2); }
.total.err { color: #d13438; }

/* 行内平台小标签 */
.ptag {
  display: inline-flex; align-items: center; height: 20px; margin: 0 4px 2px 0;
  padding: 0 7px; border-radius: 4px; font-size: 11.5px;
}

/* ===== 弹窗表单（延续 Groups.vue 的紧凑分区风格） ===== */
.cform :deep(.ant-form-item) { margin-bottom: 12px; }
.sect {
  font-size: 13px; font-weight: 600; margin: 14px 0 10px; padding-top: 12px;
  border-top: 1px solid var(--hr);
}
.sect.first { margin-top: 0; padding-top: 0; border-top: none; }
.grid2 { display: grid; grid-template-columns: 1fr 1fr; gap: 0 16px; }
.hint { font-size: 12px; color: var(--text2); line-height: 1.6; margin-top: 4px; }

/* 平台多选 chips */
.p-chips { display: flex; flex-wrap: wrap; gap: 6px; }
.p-chip {
  display: inline-flex; align-items: center; gap: 6px; padding: 4px 10px;
  border: 1px solid var(--hr); border-radius: 6px; font-size: 12.5px; cursor: pointer;
  background: var(--card); transition: border-color 100ms, background 100ms;
}
.p-chip:hover { background: var(--hover-b); }
.p-chip.on { border-color: var(--theme-2); background: color-mix(in srgb, var(--theme-2) 12%, transparent); }
.p-chip input { margin: 0; }

/* 开关行：标签左、控件右 */
.frow {
  display: flex; align-items: center; justify-content: space-between; gap: 16px;
  padding: 8px 10px; border-radius: 8px; min-height: 40px; margin-bottom: 8px;
  background: var(--card);
}
.frow:hover { filter: brightness(1.03); }
.frow-main { min-width: 0; }
.frow-t { font-size: 13px; }
.frow-d { font-size: 12px; color: var(--text2); margin-top: 1px; }

/* 卡片单选（计费模型来源） */
.cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 8px; }
.card {
  text-align: left; border: 1px solid var(--hr); background: var(--card);
  border-radius: 8px; padding: 8px 10px; cursor: pointer; display: grid; gap: 3px; font-family: inherit;
}
.card:hover { border-color: var(--text2); }
.card.on { border-color: var(--theme-2); background: color-mix(in srgb, var(--theme-2) 12%, transparent); }
.card-t { font-size: 12.5px; font-weight: 500; }
.card-d { font-size: 11.5px; color: var(--text2); line-height: 1.5; }

/* 平台折叠区 */
.p-collapse { background: transparent; }
.p-collapse :deep(.ant-collapse-item) {
  border: 1px solid var(--hr); border-radius: 8px !important; margin-bottom: 8px;
  background: var(--card); overflow: hidden;
}
.p-collapse :deep(.ant-collapse-header) { padding: 8px 12px !important; align-items: center; }
.p-collapse :deep(.ant-collapse-content-box) { padding: 4px 12px 12px !important; }
.pc-head { display: inline-flex; align-items: center; gap: 10px; }
.pc-meta { font-size: 11.5px; color: var(--text2); }
.empty-tip {
  padding: 14px; text-align: center; font-size: 12px; color: var(--text2);
  border: 1px dashed var(--hr); border-radius: 8px; margin: 10px 0;
}

/* 手机窄屏：栅格降单列 */
@media (max-width: 640px) {
  .grid2 { grid-template-columns: 1fr; }
}
</style>

<style>
/* a-modal 挂载在 body 下，scoped 覆盖不到：限高滚动，避免长表单撑出屏幕 */
.channel-edit-modal .ant-modal-body { max-height: 68vh; overflow-y: auto; }
</style>

