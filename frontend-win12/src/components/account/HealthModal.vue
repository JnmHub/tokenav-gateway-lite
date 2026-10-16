<script setup>
/* 模型健康弹窗：GET /admin/accounts/:id/model-health 的声明清单展示
 * 从 Accounts.vue 拆出（600 行约束）；expose open(r) */
import { ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { request } from '../../api'

const emit = defineEmits(['open-change'])
const open = ref(false)
watch(open, (v) => emit('open-change', v))
const loading = ref(false)
const account = ref(null)
const rows = ref([])

const openHealth = async (r) => {
  account.value = r
  open.value = true
  loading.value = true
  try {
    const res = await request(`/admin/accounts/${r.id}/model-health`, { method: 'GET' })
    rows.value = res?.declarations || []
    message.info(res?.runtime_available ? '运行时探测可用' : '运行时探测不可用（显示声明清单）')
  } catch (e) {
    rows.value = []
    message.error(e.message)
  } finally { loading.value = false }
}
defineExpose({ open: openHealth })

const columns = [
  { title: '请求模型', dataIndex: 'requested_model', key: 'rm', ellipsis: true },
  { title: '上游模型', dataIndex: 'upstream_model', key: 'um', ellipsis: true },
  { title: '范围', dataIndex: 'scope', key: 'scope', width: 90 },
]
</script>

<template>
  <a-modal v-model:open="open" :title="`模型健康：${account?.name || ''}`" width="640px" :footer="null">
    <a-spin :spinning="loading">
      <a-table
        :columns="columns"
        :data-source="rows" row-key="requested_model" size="small" :pagination="false" :scroll="{ y: 360 }"
      />
    </a-spin>
  </a-modal>
</template>
