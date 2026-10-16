<script setup>
/* 影子账号弹窗（openai oauth）：列出 + 创建 + 同步分组代理
 * 从 Accounts.vue 拆出（600 行约束）；expose open(r)；创建成功 emit changed 让父刷新列表 */
import { ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { request } from '../../api'
const emit = defineEmits(['changed', 'open-change'])

const open = ref(false)
watch(open, (v) => emit('open-change', v))
const loading = ref(false)
const account = ref(null)
const rows = ref([])
const busy = ref(false)

const load = async (r) => {
  loading.value = true
  try {
    rows.value = (await request(`/admin/accounts/${r.id}/shadows`, { method: 'GET' })) || []
  } catch (e) {
    message.error(`读取影子失败：${e.message}`)
    rows.value = []
  } finally { loading.value = false }
}
const openShadows = (r) => {
  account.value = r
  open.value = true
  load(r)
}
const createShadow = async () => {
  busy.value = true
  try {
    await request(`/admin/accounts/${account.value.id}/shadow`, { method: 'POST' })
    message.success('影子账号已创建')
    await load(account.value)
    emit('changed')
  } catch (e) { message.error(`创建失败：${e.message}`) } finally { busy.value = false }
}
const syncShadow = async () => {
  busy.value = true
  try {
    await request(`/admin/accounts/${account.value.id}/shadow/sync`, { method: 'POST' })
    message.success('影子已同步（分组与代理）')
  } catch (e) { message.error(`同步失败：${e.message}`) } finally { busy.value = false }
}
defineExpose({ open: openShadows })
</script>

<template>
  <a-modal v-model:open="open" :title="`影子账号 · ${account?.name || ''}`" :footer="null" width="560px">
    <a-spin :spinning="loading">
      <div v-for="s in rows" :key="s.id" class="quota-row">
        <span class="quota-k">{{ s.name || `影子 ${s.id}` }}</span>
        <span class="quota-v">{{ s.enabled === false ? '已停用' : (s.status || '正常') }}</span>
      </div>
      <div v-if="!rows.length && !loading" class="empty-hint" style="padding: 8px 0">暂无影子账号</div>
      <div style="display: flex; gap: 8px; justify-content: flex-end; margin-top: 14px">
        <a-button :loading="busy" @click="syncShadow">同步分组与代理</a-button>
        <a-button type="primary" :loading="busy" @click="createShadow">创建影子</a-button>
      </div>
    </a-spin>
  </a-modal>
</template>

<style scoped>
.quota-row { display: flex; justify-content: space-between; gap: 12px; padding: 8px 4px; border-bottom: 1px solid var(--hr); font-size: 13px; }
.quota-k { color: var(--text2); flex: none; }
.quota-v { text-align: right; word-break: break-all; }
.empty-hint { font-size: 12px; color: var(--text2); }
</style>
