<script setup>
import W12Dialog from '../design/W12Dialog.vue'
import W12Table from '../design/W12Table.vue'
/* RPM 覆写弹窗（二期 B，与用户倍率弹窗同构）
 * 契约来源（唯一权威）：旧 frontend/src/components/admin/group/GroupRPMOverridesModal.vue
 * 接口：GET 复用 /admin/groups/:id/rate-multipliers（前端过滤 rpm_override != null）
 *       PUT  /admin/groups/:id/rpm-overrides { entries:[{user_id, rpm_override}] } 全量保存
 *       DELETE /admin/groups/:id/rpm-overrides 直接清空（保留 rate_multiplier）
 * 与倍率弹窗的差异（旧实现如此）：
 * - 清空按钮直接调 DELETE 即时生效（不落本地）；倍率弹窗的清空只改本地
 * - 整数输入；分组信息条显示分组默认 RPM（group.rpm_limit）
 */
import { ref, computed, watch, onBeforeUnmount } from 'vue'
import { useDiscardGuard } from '../../core/windowContext'
import { useGroupDialogRequests } from './useGroupDialogRequests.js'
import { isCancellation } from '../../api/session.js'
import { message } from 'ant-design-vue'
import { DeleteOutlined } from '@ant-design/icons-vue'
import { getRateMultipliers, batchSetRPMOverrides, clearRPMOverrides, searchUsers } from './groupApi'

const props = defineProps({
  open: { type: Boolean, default: false },
  group: { type: Object, default: null },
})
const emit = defineEmits(['update:open', 'saved'])
const requests = useGroupDialogRequests(props)

const loading = ref(false)
const ready = ref(false), loadError = ref('')
const saving = ref(false)
const clearing = ref(false)
const canEdit = computed(() => ready.value && !loading.value && !saving.value && !clearing.value)
const serverEntries = ref([])
const localEntries = ref([])
const searchQuery = ref('')
const searchResults = ref([])
const searching = ref(false)
const selectedUser = ref(null)
const newRpm = ref(null)
const page = ref(1)
const pageSize = 10

const cols = [
  { title: '用户', key: 'user', ellipsis: true },
  { title: '备注', key: 'notes', width: 130, ellipsis: true },
  { title: '状态', key: 'status', width: 80 },
  { title: 'RPM 覆写', key: 'rpm', width: 120 },
  { title: '', key: 'act', width: 48 },
]

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
      searchResults.value = [{ value: -1, label: '用户搜索不可用', disabled: true }]
    } finally { if (turn.isCurrent()) searching.value = false }
  }, 300)
}
const onUserSelect = (value, opt) => { selectedUser.value = opt.user; searchQuery.value = opt.user.email }

const isDirty = computed(() => {
  if (localEntries.value.length !== serverEntries.value.length) return true
  const serverMap = new Map(serverEntries.value.map((e) => [e.user_id, e.rpm_override]))
  return localEntries.value.some((e) => serverMap.get(e.user_id) !== e.rpm_override)
})
const pagedEntries = computed(() => localEntries.value.slice((page.value - 1) * pageSize, page.value * pageSize))
const adjustPage = () => { page.value = Math.min(page.value, Math.max(1, Math.ceil(localEntries.value.length / pageSize))) }

const load = async () => {
  // 全量覆写/即时清空只能在成功读取后操作，不能把读取失败当成空配置。
  if (!props.open || !props.group || saving.value || clearing.value) return
  const turn = requests.begin('load')
  loading.value = true; ready.value = false; loadError.value = ''
  try {
    const raw = await getRateMultipliers(turn.groupId, turn.signal)
    if (!turn.isCurrent()) return
    // 与旧 API getGroupRPMOverrides 相同的过滤映射：只保留 rpm_override 已设置的条目
    serverEntries.value = (raw || []).filter((e) => e.rpm_override != null)
      .map((e) => ({ user_id: e.user_id, user_name: e.user_name, user_email: e.user_email, user_notes: e.user_notes, user_status: e.user_status, rpm_override: e.rpm_override }))
    localEntries.value = serverEntries.value.map((e) => ({ ...e }))
    page.value = 1; ready.value = true
  } catch (e) {
    if (turn.isCurrent() && !isCancellation(e)) {
      loadError.value = `加载 RPM 覆写失败：${e.message}`; message.error(loadError.value)
    }
  } finally { if (turn.isCurrent()) loading.value = false }
}

watch(() => [props.open, props.group?.id], ([val]) => {
  clearTimeout(searchTimer); searching.value = false; loading.value = false; saving.value = false; clearing.value = false
  ready.value = false; loadError.value = ''
  localEntries.value = []; serverEntries.value = []
  if (val && props.group) {
    page.value = 1; searchQuery.value = ''; searchResults.value = []
    selectedUser.value = null; newRpm.value = null
    load()
  }
}, { immediate: true, flush: 'sync' })

const addLocal = () => {
  if (!canEdit.value || !selectedUser.value || !Number.isInteger(newRpm.value) || newRpm.value < 0) return
  const u = selectedUser.value
  const entry = { user_id: u.id, user_name: u.username || '', user_email: u.email, user_notes: u.notes || '', user_status: u.status || 'active', rpm_override: newRpm.value }
  const idx = localEntries.value.findIndex((e) => e.user_id === u.id)
  if (idx >= 0) localEntries.value[idx] = entry
  else localEntries.value.push(entry)
  searchQuery.value = ''; selectedUser.value = null; newRpm.value = null
  adjustPage()
}
const updateRpm = (userId, value) => {
  if (!canEdit.value) return
  const e = localEntries.value.find((x) => x.user_id === userId)
  if (!e || value == null || value < 0) return
  e.rpm_override = Math.floor(value)
}
const removeLocal = (userId) => { if (!canEdit.value) return; localEntries.value = localEntries.value.filter((e) => e.user_id !== userId); adjustPage() }
// 清空：直接 DELETE 即时生效（旧 clearAllLocal）
const clearAll = async () => {
  if (!props.open || !props.group || !canEdit.value) return
  const turn = requests.begin('clear')
  clearing.value = true
  try {
    await clearRPMOverrides(turn.groupId, turn.signal)
    if (!turn.isCurrent()) return
    localEntries.value = []; serverEntries.value = []
    emit('saved')
    message.success('已清空全部 RPM 覆写')
  } catch (e) {
    if (turn.isCurrent() && !isCancellation(e)) message.error(`清空失败：${e.message}`)
  } finally { if (turn.isCurrent()) clearing.value = false }
}
const revert = () => { if (!canEdit.value) return; localEntries.value = serverEntries.value.map((e) => ({ ...e })); adjustPage() }

const save = async () => {
  if (!props.open || !props.group || !canEdit.value) return
  const turn = requests.begin('save')
  saving.value = true
  try {
    const entries = localEntries.value.map((e) => ({ user_id: e.user_id, rpm_override: e.rpm_override }))
    await batchSetRPMOverrides(turn.groupId, entries, turn.signal)
    if (!turn.isCurrent()) return
    message.success('RPM 覆写已保存')
    emit('saved')
    emit('update:open', false)
  } catch (e) {
    if (turn.isCurrent() && !isCancellation(e)) message.error(`保存失败：${e.message}`)
  } finally { if (turn.isCurrent()) saving.value = false }
}
const beforeClose = useDiscardGuard(() => props.open && isDirty.value, () => saving.value || clearing.value)
const close = async () => {
  const turn = requests.begin('close')
  if (!await beforeClose() || !turn.isCurrent()) return
  if (isDirty.value) localEntries.value = serverEntries.value.map((e) => ({ ...e }))
  emit('update:open', false)
}
</script>

<template>
  <W12Dialog
    :open="open" :close-key="group?.id" :title="`RPM 覆写 · ${group?.name || ''}`" width="860px" wrap-class-name="group-rpm-modal"
    :before-close="beforeClose" :busy="saving || clearing" :footer="null" @cancel="emit('update:open', false)"
  >
    <!-- 分组信息条：显示分组默认 RPM -->
    <div class="gbar">
      <span class="gbar-name">{{ group?.name }}</span>
      <span class="gbar-sep">|</span>
      <span class="gbar-dim">{{ group?.platform }}</span>
      <span class="gbar-sep">|</span>
      <span class="gbar-dim">分组默认 RPM：{{ group?.rpm_limit || 0 }}（覆写留空 = 用默认；0 = 不限制）</span>
    </div>

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
      <a-input-number v-model:value="newRpm" :disabled="!canEdit" :min="0" :step="1" :precision="0" placeholder="100" style="width: 110px" />
      <a-button type="primary" :disabled="!canEdit || !selectedUser || newRpm == null || newRpm < 0" @click="addLocal">添加</a-button>
    </div>

    <div v-if="localEntries.length" class="batchrow">
      <span class="hint">共 {{ localEntries.length }} 条覆写</span>
      <div style="flex: 1"></div>
      <a-button size="small" danger :loading="clearing" @click="clearAll">全部清空</a-button>
    </div>

    <!-- 覆写列表 -->
    <W12Table
      :columns="cols" :data-source="pagedEntries" :loading="loading" row-key="user_id" size="small"
      :pagination="{
        current: page, pageSize, total: localEntries.length, showSizeChanger: false,
        onChange: (p) => (page = p), hideOnSinglePage: true,
      }"
      :locale="{ emptyText: '还没有设置 RPM 覆写' }"
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
        <template v-else-if="column.key === 'rpm'">
          <a-input-number
            :value="record.rpm_override" :disabled="!canEdit" :min="0" :step="1" :precision="0" size="small" style="width: 96px"
            @change="(v) => updateRpm(record.user_id, v)"
          />
        </template>
        <template v-else-if="column.key === 'act'">
          <a-button size="small" type="text" danger @click="removeLocal(record.user_id)"><DeleteOutlined /></a-button>
        </template>
      </template>
    </W12Table>
    </fieldset>

    <!-- 底部操作栏 -->
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
