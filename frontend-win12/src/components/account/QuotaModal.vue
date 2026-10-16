<script setup>
/* 账号配额 / CN 供应商额度余额 KV 弹窗（从 Accounts.vue 拆出，600 行约束）
 * expose query(r)——openai/grok 配额；queryCn(r, kind)——kimi/zhipu/deepseek 额度或余额 */
import { ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { request, isCancellation } from '../../api'
import { quotaPath } from './quotaApi'
import { useDialogRequestLifetime } from '../../composables/useDialogRequestLifetime'
import W12Dialog from '../design/W12Dialog.vue'

const emit = defineEmits(['open-change'])
const open = ref(false)
watch(open, (v) => emit('open-change', v))
const loading = ref(false)
const title = ref('')
const kv = ref([])
const dialog = useDialogRequestLifetime(() => open.value, () => { kv.value = []; title.value = ''; loading.value = false })

const toKv = (res) => {
  const rows = Object.entries(res || {}).filter(([, v]) => v !== null && typeof v !== 'object').map(([k, v]) => [k, String(v)])
  return rows.length ? rows : [['原始响应', JSON.stringify(res).slice(0, 200)]]
}

const query = async (r) => {
  open.value = true
  dialog.begin()
  const turn = dialog.capture()
  title.value = `配额 · ${r.name}`
  loading.value = true
  kv.value = []
  try {
    const data = await quotaPath(r, 'quota', undefined, turn)
    turn.assertCurrent()
    kv.value = toKv(data)
  } catch (e) {
    if (!turn.isCurrent() || isCancellation(e)) return
    message.error(`查询配额失败：${e.message}`)
    open.value = false
  } finally { if (turn.isCurrent()) loading.value = false }
}

/* CN 供应商额度/余额（kimi/zhipu/deepseek） */
const queryCn = async (r, kind) => {
  open.value = true
  dialog.begin()
  const turn = dialog.capture()
  title.value = `${kind === 'quota' ? '额度' : '余额'} · ${r.name}`
  loading.value = true
  kv.value = []
  try {
    const data = await request(`/admin/cn-providers/accounts/${r.id}/${kind}`, { ...turn, method: 'GET' })
    turn.assertCurrent()
    kv.value = toKv(data)
  } catch (e) {
    if (!turn.isCurrent() || isCancellation(e)) return
    message.error(`查询失败：${e.message}`)
    open.value = false
  } finally { if (turn.isCurrent()) loading.value = false }
}

defineExpose({ query, queryCn })
</script>

<template>
  <W12Dialog v-model:open="open" :title="title" :footer="null" width="480px">
    <a-spin :spinning="loading">
      <div v-for="([k, v]) in kv" :key="k" class="quota-row">
        <span class="quota-k">{{ k }}</span>
        <span class="quota-v">{{ v }}</span>
      </div>
    </a-spin>
  </W12Dialog>
</template>

<style scoped>
.quota-row { display: flex; justify-content: space-between; gap: 12px; padding: 8px 4px; border-bottom: 1px solid var(--hr); font-size: 13px; }
.quota-k { color: var(--text2); flex: none; }
.quota-v { text-align: right; word-break: break-all; }
</style>
