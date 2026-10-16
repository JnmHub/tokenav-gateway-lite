<script setup>
import W12Dialog from '../design/W12Dialog.vue'
import { SearchOutlined } from '@ant-design/icons-vue'
/* 快速绑定账号弹窗（二期 B）
 * 契约来源（唯一权威）：旧 frontend/src/components/admin/group/QuickBindAccountsModal.vue
 * 接口：GET  /admin/accounts?page=1&page_size=100&platform=&search=&status=active&sort_by=priority&sort_order=asc
 *       （composite 聚合分组不传 platform；参数与旧实现逐项一致）
 *       POST /admin/groups/:id/accounts/bind { account_ids, confirm_mixed_channel_risk: false }
 * 行为：250ms 防抖搜索 + 勾选 + 绑定；绑定保留账号在其他分组的绑定关系；
 *       混合渠道风险由服务端校验，确认提示直接透传后端错误。
 */
import { ref, computed, watch, onBeforeUnmount } from 'vue'
import { useDiscardGuard } from '../../core/windowContext'
import { useGroupDialogRequests } from './useGroupDialogRequests.js'
import { isCancellation } from '../../api/session.js'
import { message } from 'ant-design-vue'
import { searchAccounts, bindAccounts } from './groupApi'

const props = defineProps({
  open: { type: Boolean, default: false },
  group: { type: Object, default: null },
})
const emit = defineEmits(['update:open', 'saved'])
const requests = useGroupDialogRequests(props)

const search = ref('')
const accounts = ref([])
const selectedIds = ref([])
const loading = ref(false)
const saving = ref(false)
let searchTimer = null
onBeforeUnmount(() => clearTimeout(searchTimer))
const beforeClose = useDiscardGuard(() => props.open && selectedIds.value.length > 0, () => saving.value)
const close = async () => { if (await beforeClose()) emit('update:open', false) }

const loadAccounts = () => {
  clearTimeout(searchTimer)
  if (!props.open || !props.group) return
  const turn = requests.begin('accounts')
  searchTimer = setTimeout(async () => {
    if (!turn.isCurrent()) return
    loading.value = true
    try {
      const res = await searchAccounts(1, 100, { platform: props.group.platform, search: search.value.trim() }, turn.signal)
      if (!turn.isCurrent()) return
      accounts.value = res?.items || []
    } catch (e) {
      if (turn.isCurrent() && !isCancellation(e)) { message.error(`搜索账号失败：${e.message}`); accounts.value = [] }
    } finally { if (turn.isCurrent()) loading.value = false }
  }, 250)
}
// 保留关键字高亮不需要；勾选状态在搜索刷新后仍按 id 保留（Set 语义）
const isSelected = (id) => selectedIds.value.includes(id)
const toggle = (id) => {
  const next = new Set(selectedIds.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  selectedIds.value = [...next]
}
const allChecked = computed(() => accounts.value.length > 0 && accounts.value.every((a) => selectedIds.value.includes(a.id)))
const toggleAll = () => {
  if (allChecked.value) {
    // 只取消当前列表内的勾选，保留搜索前已选的其他账号
    const ids = new Set(accounts.value.map((a) => a.id))
    selectedIds.value = selectedIds.value.filter((id) => !ids.has(id))
  } else {
    const next = new Set(selectedIds.value)
    accounts.value.forEach((a) => next.add(a.id))
    selectedIds.value = [...next]
  }
}

const save = async () => {
  if (!props.open || !props.group || !selectedIds.value.length || saving.value || loading.value) return
  const turn = requests.begin('save'), selected = [...selectedIds.value]
  saving.value = true
  try {
    const res = await bindAccounts(turn.groupId, selected, false, turn.signal)
    if (!turn.isCurrent()) return
    message.success(`已绑定 ${res?.bound ?? selectedIds.value.length} 个账号`)
    emit('saved')
    emit('update:open', false)
  } catch (e) {
    // 混合渠道风险等校验错误直接透传后端 message（旧实现同）
    if (turn.isCurrent() && !isCancellation(e)) message.error(`绑定失败：${e.message}`)
  } finally { saving.value = false }
}

watch(() => [props.open, props.group?.id], ([val]) => {
  clearTimeout(searchTimer); loading.value = false; accounts.value = []; selectedIds.value = []
  if (val) {
    search.value = ''; selectedIds.value = []
    loadAccounts()
  }
})
</script>

<template>
  <W12Dialog
    :open="open" :title="`快速绑定账号 · ${group?.name || ''}`" width="640px" wrap-class-name="group-bind-modal"
    :before-close="beforeClose" :busy="saving" :footer="null" @cancel="emit('update:open', false)"
  >
    <!-- 分组信息 + 已选计数 -->
    <div class="gbar">
      <span class="gbar-name">{{ group?.name }}</span>
      <span class="gbar-sep">|</span>
      <span class="gbar-dim">{{ group?.platform }}</span>
      <div style="flex: 1"></div>
      <span class="selcount">已选 {{ selectedIds.length }} 个</span>
    </div>

    <a-input v-model:value="search" placeholder="搜索账号名称" style="width: 100%; margin-bottom: 10px" allow-clear @input="loadAccounts" @press-enter="loadAccounts">
      <template #suffix><SearchOutlined class="suf-ico" @click="loadAccounts" /></template>
    </a-input>

    <!-- 全选 + 账号勾选列表 -->
    <div class="listwrap">
      <label v-if="accounts.length" class="row head">
        <a-checkbox :checked="allChecked" @change="toggleAll" />
        <span class="row-name">全选（当前列表 {{ accounts.length }} 个）</span>
      </label>
      <a-spin :spinning="loading">
        <label v-for="a in accounts" :key="a.id" class="row">
          <a-checkbox :checked="isSelected(a.id)" @change="toggle(a.id)" />
          <span class="row-main">
            <span class="row-name">{{ a.name }}</span>
            <span class="row-sub">#{{ a.id }} · {{ a.type }}</span>
          </span>
          <span class="tag" :class="a.status === 'active' ? 'ok' : ''">{{ a.status }}</span>
        </label>
        <div v-if="!loading && !accounts.length" class="empty">没有符合条件的活跃账号</div>
      </a-spin>
    </div>

    <div class="hint">绑定不会移除账号在其他分组的绑定；账号的调度优先级保持不变。</div>

    <!-- 底部操作 -->
    <div class="foot">
      <div style="flex: 1"></div>
      <a-button @click="close">取消</a-button>
      <a-button type="primary" :loading="saving" :disabled="!selectedIds.length" @click="save">
        绑定（{{ selectedIds.length }}）
      </a-button>
    </div>
  </W12Dialog>
</template>

<style scoped>
.gbar { display: flex; align-items: center; gap: 10px; margin-bottom: 12px; padding: 6px 10px; border-radius: 8px; background: var(--card); }
.gbar-name { font-size: 13px; font-weight: 500; }
.gbar-sep { color: var(--hr); }
.gbar-dim { font-size: 12px; color: var(--text2); }
.selcount { font-size: 12px; color: var(--accent); }
.listwrap { max-height: 380px; overflow-y: auto; border: 1px solid var(--hr); border-radius: 8px; padding: 4px; }
.row { display: flex; align-items: center; gap: 10px; padding: 7px 8px; border-radius: 6px; cursor: pointer; }
.row:hover { background: color-mix(in srgb, var(--text2) 8%, transparent); }
.row.head { position: sticky; top: 0; background: var(--bg85, var(--card)); z-index: 1; border-bottom: 1px solid var(--hr); border-radius: 6px 6px 0 0; }
.row-main { min-width: 0; flex: 1; display: grid; }
.row-name { font-size: 12.5px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.row-sub { font-size: 11.5px; color: var(--text2); }
.tag { font-size: 11px; line-height: 20px; padding: 0 8px; border-radius: 4px; background: var(--card); color: var(--text2); }
.tag.ok { background: color-mix(in srgb, #2da44e 14%, transparent); color: #1a7f37; }
.empty { text-align: center; color: var(--text2); font-size: 12.5px; padding: 32px 0; }
.hint { font-size: 12px; color: var(--text2); margin-top: 10px; }
.foot { display: flex; align-items: center; gap: 8px; margin-top: 12px; }
</style>
