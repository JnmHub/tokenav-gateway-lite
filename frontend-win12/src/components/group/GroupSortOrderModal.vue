<script setup>
import W12Dialog from '../design/W12Dialog.vue'
/* 分组排序弹窗（二期 B）
 * 契约来源（唯一权威）：旧 GroupsView.vue 排序弹窗（openSortModal/saveSortOrder，VueDraggable）
 * 接口：GET /admin/groups/all（全量）→ 按 sort_order 排序展示；
 *       PUT /admin/groups/sort-order { updates:[{id, sort_order}] }（sort_order = 下标×10）
 * 依赖说明：任务要求不引入 VueDraggable 新依赖 → 用「上移/下移按钮」+
 * 「原生 HTML5 dragstart/dragover/drop」等价实现，两者都支持、按钮为主。
 */
import { ref, watch } from 'vue'
import { useDiscardGuard } from '../../core/windowContext'
import { useGroupDialogRequests } from './useGroupDialogRequests.js'
import { isCancellation } from '../../api/session.js'
import { message } from 'ant-design-vue'
import { HolderOutlined, ArrowUpOutlined, ArrowDownOutlined } from '@ant-design/icons-vue'
import { getAllGroups, updateSortOrder } from './groupApi'

const props = defineProps({
  open: { type: Boolean, default: false },
})
const emit = defineEmits(['update:open', 'saved'])
const requests = useGroupDialogRequests(props)

const items = ref([])
const loading = ref(false)
const saving = ref(false)
const dragIndex = ref(-1), baseline = ref('[]')
const beforeClose = useDiscardGuard(() => props.open && JSON.stringify(items.value.map(group => group.id)) !== baseline.value, () => saving.value)
const close = async () => { if (await beforeClose()) emit('update:open', false) }

const platformText = (p) => ({ anthropic: 'Anthropic', openai: 'OpenAI', gemini: 'Gemini', antigravity: 'Antigravity', grok: 'Grok', kimi: 'Kimi', zhipu: '智谱 GLM', deepseek: 'DeepSeek', composite: '聚合模型' }[p] || p)

const load = async () => {
  if (!props.open) return
  const turn = requests.begin('load')
  loading.value = true
  try {
    const all = await getAllGroups(turn.signal)
    if (!turn.isCurrent()) return
    items.value = [...(all || [])].sort((a, b) => (a.sort_order ?? 0) - (b.sort_order ?? 0))
    baseline.value = JSON.stringify(items.value.map(group => group.id))
  } catch (e) {
    if (turn.isCurrent() && !isCancellation(e)) message.error(`加载分组失败：${e.message}`)
  } finally { if (turn.isCurrent()) loading.value = false }
}

watch(() => props.open, val => { items.value = []; baseline.value = '[]'; dragIndex.value = -1; loading.value = false; if (val) load() })

const move = (from, to) => {
  if (saving.value || loading.value || from < 0 || from >= items.value.length) return
  to = Math.max(0, Math.min(items.value.length - 1, to))
  if (from === to) return
  const [it] = items.value.splice(from, 1)
  items.value.splice(to, 0, it)
}

// ===== 原生 HTML5 拖拽（dragstart 记录来源下标，drop 交换） =====
const onDragStart = (i) => { dragIndex.value = i }
const onDrop = (i) => { if (dragIndex.value >= 0 && dragIndex.value !== i) move(dragIndex.value, i); dragIndex.value = -1 }
const onDragOver = (e) => e.preventDefault()

const save = async () => {
  if (!props.open || saving.value || loading.value || !items.value.length) return
  const turn = requests.begin('save')
  saving.value = true
  try {
    await updateSortOrder(items.value.map((g, i) => ({ id: g.id, sort_order: i * 10 })), turn.signal)
    if (!turn.isCurrent()) return
    message.success('排序已更新')
    emit('saved')
    emit('update:open', false)
  } catch (e) {
    if (turn.isCurrent() && !isCancellation(e)) message.error(`更新排序失败：${e.message}`)
  } finally { saving.value = false }
}
</script>

<template>
  <W12Dialog
    :open="open" title="分组排序" width="460px" wrap-class-name="group-sort-modal"
    :before-close="beforeClose" :busy="saving" :footer="null" @cancel="emit('update:open', false)"
  >
    <div class="hint">拖拽或用上移/下移按钮调整显示顺序，排在前面的分组会优先显示。</div>

    <a-spin :spinning="loading">
      <div class="list">
        <div
          v-for="(g, i) in items" :key="g.id" class="item" draggable="true"
          @dragstart="onDragStart(i)" @drop="onDrop(i)" @dragover="onDragOver" @dragend="dragIndex = -1"
        >
          <HolderOutlined class="grip" />
          <div class="main">
            <div class="name">{{ g.name }}</div>
            <div class="sub">{{ platformText(g.platform) }}</div>
          </div>
          <div class="ops">
            <a-button size="small" type="text" :disabled="i === 0" @click="move(i, i - 1)"><ArrowUpOutlined /></a-button>
            <a-button size="small" type="text" :disabled="i === items.length - 1" @click="move(i, i + 1)"><ArrowDownOutlined /></a-button>
          </div>
          <div class="gid">#{{ g.id }}</div>
        </div>
        <div v-if="!loading && !items.length" class="empty">暂无分组</div>
      </div>
    </a-spin>

    <div class="foot">
      <div style="flex: 1"></div>
      <a-button @click="close">取消</a-button>
      <a-button type="primary" :loading="saving" :disabled="!items.length" @click="save">保存</a-button>
    </div>
  </W12Dialog>
</template>

<style scoped>
.hint { font-size: 12px; color: var(--text2); margin-bottom: 10px; }
.list { display: grid; gap: 6px; max-height: 420px; overflow-y: auto; }
.item {
  display: flex; align-items: center; gap: 10px; padding: 8px 10px;
  border: 1px solid var(--hr); border-radius: 8px; background: var(--card);
  cursor: grab;
}
.item:active { cursor: grabbing; }
.grip { color: var(--text2); }
.main { min-width: 0; flex: 1; }
.name { font-size: 12.5px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.sub { font-size: 11.5px; color: var(--text2); }
.ops { display: flex; gap: 2px; }
.gid { font-size: 11.5px; color: var(--text2); }
.empty { text-align: center; color: var(--text2); font-size: 12.5px; padding: 32px 0; }
.foot { display: flex; align-items: center; gap: 8px; margin-top: 12px; }
</style>
