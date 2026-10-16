<script setup>
/* 批量添加 API Key（接口密钥）账号弹窗：每行一条「名称,APIKey」或仅 APIKey
 * 后端无批量端点，逐条 POST /admin/accounts（apikey 类别：credentials.api_key + 可选 base_url）
 * 既有 bug 修复：此前模板引用了从未声明的 bulkOpen/bulkCreate，弹窗实际不可用。
 * expose open(r)；创建结果 emit created（父刷新列表）；开合 emit open-change（父暂停轮询） */
import { ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { request } from '../../api'
const emit = defineEmits(['created', 'open-change'])

const open = ref(false)
watch(open, (v) => emit('open-change', v))
const platform = ref('openai')
const baseUrl = ref('')
const text = ref('')
const saving = ref(false)

const create = async () => {
  /* 解析：跳过空行与 # 注释行；逗号分隔「名称,Key」，逗号后内容里的引号剥掉 */
  const items = text.value
    .split(/\r?\n/)
    .map((l) => l.trim())
    .filter((l) => l && !l.startsWith('#'))
    .map((l) => {
      const i = l.indexOf(',')
      if (i < 0) return { name: '', key: l }
      return { name: l.slice(0, i).trim(), key: l.slice(i + 1).trim().replace(/^["']|["']$/g, '') }
    })
  if (!items.length) { message.warning('请先粘贴账号，每行一条。'); return }
  const bad = items.filter((x) => !x.key)
  if (bad.length) { message.error(`有 ${bad.length} 行缺少 API Key，请检查。`); return }
  saving.value = true
  let ok = 0
  const failed = []
  try {
    for (const it of items) {
      try {
        const creds = { api_key: it.key }
        if (baseUrl.value.trim()) creds.base_url = baseUrl.value.trim()
        await request('/admin/accounts', {
          method: 'POST',
          body: { name: it.name || `bulk-${it.key.slice(-6)}`, platform: platform.value, type: 'apikey', credentials: creds },
        })
        ok++
      } catch (e) { failed.push(`${it.name || it.key.slice(-6)}：${e.message}`) }
    }
    if (failed.length) message.warning(`批量创建完成：成功 ${ok}，失败 ${failed.length}（${failed[0]}${failed.length > 1 ? ' 等' : ''}）`)
    else message.success(`批量创建完成：成功 ${ok} 个`)
    text.value = ''
    open.value = false
    emit('created')
  } finally { saving.value = false }
}
defineExpose({ open: () => { open.value = true } })
</script>

<template>
  <a-modal v-model:open="open" title="批量添加 API Key 账号" width="560px" :confirm-loading="saving" ok-text="批量创建" cancel-text="取消" @ok="create">
    <a-form layout="vertical" size="small">
      <div class="two">
        <a-form-item label="平台">
          <a-select v-model:value="platform" :options="['openai', 'anthropic', 'gemini'].map((v) => ({ value: v, label: v }))" />
        </a-form-item>
        <a-form-item label="Base URL（可选，官方留空）"><a-input v-model:value="baseUrl" /></a-form-item>
      </div>
      <a-form-item label="每行一条：`名称,APIKey` 或仅 APIKey" required>
        <a-textarea v-model:value="text" :rows="8" placeholder="my-account,sk-xxxx&#10;sk-yyyy" />
      </a-form-item>
    </a-form>
  </a-modal>
</template>

<style scoped>
.two { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
@media (max-width: 640px) { .two { grid-template-columns: 1fr; } }
</style>
