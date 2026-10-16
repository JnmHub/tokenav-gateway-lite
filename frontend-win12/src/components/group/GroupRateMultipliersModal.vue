<script setup>
import W12Dialog from '../design/W12Dialog.vue'
import W12Table from '../design/W12Table.vue'
/* 用户倍率弹窗（二期 B）
 * 契约来源（唯一权威）：旧 frontend/src/components/admin/group/GroupRateMultipliersModal.vue
 * 接口：GET /admin/groups/:id/rate-multipliers（列表，含 rpm_override 字段但本弹窗不管）
 *       PUT  /admin/groups/:id/rate-multipliers { entries:[{user_id, rate_multiplier}] } 全量保存
 *       DELETE /admin/groups/:id/rate-multipliers 清空（保留 rpm_override）
 * 行为（与旧实现一致）：
 * - 仅展示 rate_multiplier 已设置的条目；全部编辑先落本地（dirty 检测），保存一次性全量提交
 * - 批量×系数只作用于本地；系数生效时显示「最终倍率」预览列 =（用户倍率 ?? 分组倍率）× 系数
 * - 用户搜索 300ms 防抖走 /admin/users?search=；当前区域网关若未启用该路由则下拉提示不可用
 */
import { ref, computed, watch, onBeforeUnmount } from 'vue'
import { useDiscardGuard } from '../../core/windowContext'
import { useGroupDialogRequests } from './useGroupDialogRequests.js'
import { isCancellation } from '../../api/session.js'
import { message } from 'ant-design-vue'
import { DeleteOutlined } from '@ant-design/icons-vue'
import { getRateMultipliers, batchSetRateMultipliers, searchUsers } from './groupApi'

const props = defineProps({
  open: { type: Boolean, default: false },
  group: { type: Object, default: null },
})
const emit = defineEmits(['update:open', 'saved'])
const requests = useGroupDialogRequests(props)

const loading = ref(false)
const ready = ref(false), loadError = ref('')
const saving = ref(false)
const canEdit = computed(() => ready.value && !loading.value && !saving.value)
const serverEntries = ref([])
const localEntries = ref([])
// 添加用户（搜索下拉）
const searchQuery = ref('')
const searchResults = ref([])
const searching = ref(false)
const selectedUser = ref(null)
const newRate = ref(null)
// 批量系数
const batchFactor = ref(null)
// 本地分页
const page = ref(1)
const pageSize = 10

let searchTimer = null
onBeforeUnmount(() => clearTimeout(searchTimer))
const handleUserSearch = (q) => {
  clearTimeout(searchTimer)
  requests.cancel('search'); searching.value = false
  selectedUser.value = null
  const kw = (q || '').trim()
  if (!kw) { searchResults.value = []; return }
  const turn = requests.begin('search')
  searchTimer = setTimeout(async () => {
    if (!turn.isCurrent()) return
    searching.value = true
    try {
      const res = await searchUsers(kw, 10, turn.signal)
      if (!turn.isCurrent()) return
      const items = res?.items || []
      searchResults.value = items.map((u) => ({ value: u.id, label: `#${u.id} ${u.username || u.email}`, user: u }))
      if (!items.length) searchResults.value = [{ value: -1, label: '未找到匹配用户', disabled: true }]
    } catch {
      if (!turn.isCurrent()) return
      // 旧实现：搜索失败静默清空（区域网关可能未启用用户管理路由）
      searchResults.value = [{ value: -1, label: '用户搜索不可用', disabled: true }]
    } finally { if (turn.isCurrent()) searching.value = false }
  }, 300)
}
const onUserSelect = (value, opt) => { selectedUser.value = opt.user; searchQuery.value = opt.user.email }

// 最终倍率预览列：批量系数有效时显示（旧 showFinalRate）
const showFinalRate = computed(() => batchFactor.value != null && batchFactor.value > 0 && batchFactor.value !== 1)
// 列定义：最终倍率列仅批量系数生效时出现（旧实现同）
const cols = computed(() => {
  const base = [
    { title: '用户', key: 'user', ellipsis: true },
    { title: '备注', key: 'notes', width: 130, ellipsis: true },
    { title: '状态', key: 'status', width: 80 },
    { title: '基础倍率', key: 'rate', width: 120 },
  ]
  if (showFinalRate.value) base.push({ title: '最终倍率', key: 'final', width: 90 })
  base.push({ title: '', key: 'act', width: 48 })
  return base
})
const computeFinalRate = (rate) => {
  const base = rate ?? props.group?.rate_multiplier ?? 1
  if (!batchFactor.value) return base
  return parseFloat((base * batchFactor.value).toFixed(6))
}
// dirty：与服务器快照逐条对比（旧 isDirty）
const isDirty = computed(() => {
  if (localEntries.value.length !== serverEntries.value.length) return true
  const serverMap = new Map(serverEntries.value.map((e) => [e.user_id, e.rate_multiplier ?? null]))
  return localEntries.value.some((e) => serverMap.get(e.user_id) !== (e.rate_multiplier ?? null))
})
const pagedEntries = computed(() => localEntries.value.slice((page.value - 1) * pageSize, page.value * pageSize))
const adjustPage = () => { page.value = Math.min(page.value, Math.max(1, Math.ceil(localEntries.value.length / pageSize))) }

const load = async () => {
  // 全量 PUT 必须建立在成功读取的快照上；失败的空列表不代表服务器没有配置。
  if (!props.open || !props.group || saving.value) return
  const turn = requests.begin('load')
  loading.value = true; ready.value = false; loadError.value = ''
  try {
    const raw = await getRateMultipliers(turn.groupId, turn.signal)
    if (!turn.isCurrent()) return
    // 仅显示已设置 rate_multiplier 的条目；rpm_override 由 RPM 弹窗管理，保存时不触碰
    serverEntries.value = (raw || []).filter((e) => e.rate_multiplier != null)
    localEntries.value = serverEntries.value.map((e) => ({ ...e }))
    page.value = 1; ready.value = true
  } catch (e) {
    if (turn.isCurrent() && !isCancellation(e)) {
      loadError.value = `加载用户倍率失败：${e.message}`; message.error(loadError.value)
    }
  } finally { if (turn.isCurrent()) loading.value = false }
}

watch(() => [props.open, props.group?.id], ([val]) => {
  clearTimeout(searchTimer); searching.value = false; loading.value = false; saving.value = false
  ready.value = false; loadError.value = ''
  localEntries.value = []; serverEntries.value = []
  if (val && props.group) {
    page.value = 1; batchFactor.value = null; searchQuery.value = ''
    searchResults.value = []; selectedUser.value = null; newRate.value = null
    load()
  }
}, { immediate: true, flush: 'sync' })

// 本地添加（或覆盖同用户）
const addLocal = () => {
  if (!canEdit.value || !selectedUser.value || !newRate.value) return
  const u = selectedUser.value
  const entry = { user_id: u.id, user_name: u.username || '', user_email: u.email, user_notes: u.notes || '', user_status: u.status || 'active', rate_multiplier: newRate.value, rpm_override: null }
  const idx = localEntries.value.findIndex((e) => e.user_id === u.id)
  if (idx >= 0) localEntries.value[idx] = entry
  else localEntries.value.push(entry)
  searchQuery.value = ''; selectedUser.value = null; newRate.value = null
  adjustPage()
}
// 行内编辑（清空 = null，保存时该条会被剔除）
const updateRate = (userId, value) => {
  if (!canEdit.value) return
  const e = localEntries.value.find((x) => x.user_id === userId)
  if (!e) return
  const n = parseFloat(value)
  e.rate_multiplier = value === '' || value == null ? null : (isNaN(n) ? e.rate_multiplier : n)
}
const removeLocal = (userId) => { if (!canEdit.value) return; localEntries.value = localEntries.value.filter((e) => e.user_id !== userId); adjustPage() }
// 批量乘数只改本地（旧 applyBatchFactor）
const applyBatchFactor = () => {
  if (!canEdit.value || !batchFactor.value || batchFactor.value <= 0) return
  for (const e of localEntries.value) {
    if (e.rate_multiplier != null) e.rate_multiplier = parseFloat((e.rate_multiplier * batchFactor.value).toFixed(6))
  }
  batchFactor.value = null
}
const clearAllLocal = () => { if (!canEdit.value) return; localEntries.value = []; page.value = 1 }
const revert = () => { if (!canEdit.value) return; localEntries.value = serverEntries.value.map((e) => ({ ...e })); batchFactor.value = null; adjustPage() }

// 保存：一次性全量提交（只提交 rate_multiplier；服务端保留各用户 rpm_override）
const save = async () => {
  if (!props.open || !props.group || !canEdit.value) return
  const turn = requests.begin('save')
  saving.value = true
  try {
    const entries = localEntries.value.filter((e) => e.rate_multiplier != null)
      .map((e) => ({ user_id: e.user_id, rate_multiplier: e.rate_multiplier }))
    await batchSetRateMultipliers(turn.groupId, entries, turn.signal)
    if (!turn.isCurrent()) return
    message.success('用户倍率已保存')
    emit('saved')
    emit('update:open', false)
  } catch (e) {
    if (turn.isCurrent() && !isCancellation(e)) message.error(`保存失败：${e.message}`)
  } finally { if (turn.isCurrent()) saving.value = false }
}
// 关闭时若有未保存修改，恢复到服务器快照（旧 handleClose）
const beforeClose = useDiscardGuard(() => props.open && isDirty.value, () => saving.value)
const close = async () => {
  const turn = requests.begin('close')
  if (!await beforeClose() || !turn.isCurrent()) return
  if (isDirty.value) localEntries.value = serverEntries.value.map((e) => ({ ...e }))
  emit('update:open', false)
}
</script>

<template>
  <W12Dialog
    :open="open" :close-key="group?.id" :title="`用户倍率 · ${group?.name || ''}`" width="860px" wrap-class-name="group-rates-modal"
    :before-close="beforeClose" :busy="saving" :footer="null" @cancel="emit('update:open', false)"
  >
    <!-- 分组信息条 -->
    <div class="gbar">
      <span class="gbar-name">{{ group?.name }}</span>
      <span class="gbar-sep">|</span>
      <span class="gbar-dim">{{ group?.platform }}</span>
      <span class="gbar-sep">|</span>
      <span class="gbar-dim">分组倍率 x{{ group?.rate_multiplier }}</span>
    </div>
    <!-- 首读失败保留写入门禁，重试成功后才允许全量编辑。 -->
    <a-alert v-if="loadError" type="error" :message="loadError" show-icon class="load-error">
      <template #action><a-button :loading="loading" @click="load">重新读取</a-button></template>
    </a-alert>
    <fieldset :disabled="!canEdit" class="override-editor">
    <!-- 添加用户 -->
    <div class="addrow">
      <a-select
        v-model:value="searchQuery" :disabled="!canEdit" show-search :filter-option="false" :options="searchResults" :loading="searching"
        placeholder="搜索用户（邮箱/名称）" style="flex: 1; min-width: 200px" @search="handleUserSearch" @select="onUserSelect"
      />
      <a-input-number v-model:value="newRate" :disabled="!canEdit" :min="0" :step="0.001" placeholder="1.0" style="width: 110px" />
      <a-button type="primary" :disabled="!canEdit || !selectedUser || !newRate" @click="addLocal">添加</a-button>
    </div>

    <!-- 批量调整 + 清空 -->
    <div v-if="localEntries.length" class="batchrow">
      <span class="hint">批量调整</span>
      <span class="hint">×</span>
      <a-input-number v-model:value="batchFactor" :disabled="!canEdit" :min="0" :step="0.1" placeholder="0.5" style="width: 90px" />
      <a-button size="small" type="primary" :disabled="!batchFactor || batchFactor <= 0" @click="applyBatchFactor">应用乘数</a-button>
      <div style="flex: 1"></div>
      <a-button size="small" danger @click="clearAllLocal">全部清空</a-button>
    </div>

    <!-- 已设置倍率的用户 -->
    <W12Table
      :columns="cols" :data-source="pagedEntries" :loading="loading" row-key="user_id" size="small"
      :pagination="{
        current: page, pageSize, total: localEntries.length, showSizeChanger: false,
        onChange: (p) => (page = p), hideOnSinglePage: true,
      }"
      :locale="{ emptyText: '还没有设置用户倍率' }"
      :scroll="{ x: 'max-content' }"
    >
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'user'">
          <div class="uemail">{{ record.user_email }}</div>
          <div class="uname">#{{ record.user_id }} {{ record.user_name || '' }}</div>
        </template>
        <template v-else-if="column.key === 'notes'">
          <span class="hint" :title="record.user_notes">{{ record.user_notes || '-' }}</span>
        </template>
        <template v-else-if="column.key === 'status'">
          <span class="tag" :class="record.user_status === 'active' ? 'ok' : ''">{{ record.user_status }}</span>
        </template>
        <template v-else-if="column.key === 'rate'">
          <a-input-number
            :value="record.rate_multiplier ?? undefined" :disabled="!canEdit" :min="0.001" :step="0.001" size="small"
            :placeholder="String(group?.rate_multiplier ?? 1)" style="width: 96px"
            @change="(v) => updateRate(record.user_id, v ?? '')"
          />
        </template>
        <template v-else-if="column.key === 'final'">x{{ computeFinalRate(record.rate_multiplier) }}</template>
        <template v-else-if="column.key === 'act'">
          <a-button size="small" type="text" danger @click="removeLocal(record.user_id)"><DeleteOutlined /></a-button>
        </template>
      </template>
    </W12Table>
    </fieldset>

    <!-- 底部操作栏：未保存提示 + 撤销 | 关闭/保存 -->
    <div class="foot">
      <template v-if="isDirty">
        <span class="dirty">有未保存的修改</span>
        <a-button size="small" type="link" @click="revert">撤销修改</a-button>
      </template>
      <div style="flex: 1"></div>
      <a-button @click="close">关闭</a-button>
      <a-button v-if="isDirty" type="primary" :loading="saving" :disabled="!canEdit" @click="save">保存</a-button>
    </div>
  </W12Dialog>
</template>

<style scoped>
.override-editor { border: 0; padding: 0; margin: 0; min-width: 0; }
.load-error { margin-bottom: 12px; }
.gbar { display: flex; align-items: center; gap: 10px; margin-bottom: 12px; padding: 6px 10px; border-radius: 8px; background: var(--card); }
.gbar-name { font-size: 13px; font-weight: 500; }
.gbar-sep { color: var(--hr); }
.gbar-dim { font-size: 12px; color: var(--text2); }
.addrow { display: flex; gap: 8px; margin-bottom: 10px; }
.batchrow { display: flex; align-items: center; gap: 8px; margin-bottom: 10px; padding: 8px 10px; border-radius: 8px; background: var(--card); }
.hint { font-size: 12px; color: var(--text2); }
.uemail { font-size: 12.5px; }
.uname { font-size: 11.5px; color: var(--text2); }
.uname:empty { display: none; }
.tag { display: inline-block; font-size: 11px; line-height: 20px; padding: 0 8px; border-radius: 4px; background: var(--card); color: var(--text2); }
.tag.ok { background: color-mix(in srgb, #2da44e 14%, transparent); color: #1a7f37; }
.foot { display: flex; align-items: center; gap: 8px; margin-top: 12px; }
.dirty { font-size: 12px; color: #b58a00; }
</style>
