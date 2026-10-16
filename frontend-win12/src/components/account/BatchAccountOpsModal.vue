<script setup>
/* 批量账号运维弹窗（补全官方 v0.2.13 缺失入口）
 * 契约（本机轻量网关 18080 实测）：
 * - POST /admin/accounts/batch-refresh-tier        { account_ids: number[] }
 * - POST /admin/accounts/batch-update-credentials  { account_ids: number[], field, value }
 *   field 只允许 account_uuid | org_uuid | intercept_warmup_requests（后端 binding oneof），
 *   值类型随之不同：前两者为字符串，intercept_warmup_requests 为布尔。
 */
import { ref, computed, watch } from 'vue'
import { request } from '../../api'
import { message } from 'ant-design-vue'
import { confirmAction } from '../../utils/confirmAction'

const open = defineModel('open', { type: Boolean, default: false })
const props = defineProps({
  accountIds: { type: Array, default: () => [] },
})

const FIELD_OPTIONS = [
  { label: 'account_uuid（账号 UUID）', value: 'account_uuid', kind: 'string' },
  { label: 'org_uuid（组织 UUID）', value: 'org_uuid', kind: 'string' },
  { label: 'intercept_warmup_requests（拦截预热请求）', value: 'intercept_warmup_requests', kind: 'bool' },
]

const field = ref('account_uuid')
const textValue = ref('')
const boolValue = ref(false)
const refreshing = ref(false)
const updating = ref(false)

const kind = computed(() => FIELD_OPTIONS.find((o) => o.value === field.value)?.kind || 'string')
const count = computed(() => props.accountIds.length)

watch(open, (o) => {
  if (o) { field.value = 'account_uuid'; textValue.value = ''; boolValue.value = false }
})

const refreshTier = async () => {
  if (!count.value) return
  refreshing.value = true
  try {
    const res = await request('/admin/accounts/batch-refresh-tier', { method: 'POST', body: { account_ids: props.accountIds } })
    const okCount = res?.success_count ?? res?.updated ?? res?.count
    message.success(okCount !== undefined ? `已刷新 ${okCount} / ${count.value} 个账号档位` : `已提交刷新（${count.value} 个账号）`)
  } catch (e) {
    message.error(`批量刷新档位失败：${e.message}`)
  } finally {
    refreshing.value = false
  }
}

const updateCredentials = async () => {
  if (!count.value) return
  const value = kind.value === 'bool' ? boolValue.value : textValue.value.trim()
  if (kind.value === 'string' && !value) {
    message.warning('请填写要写入的值')
    return
  }
  const label = FIELD_OPTIONS.find((o) => o.value === field.value)?.label || field.value
  const ok = await confirmAction({
    title: '批量更新凭据字段',
    content: `确认把 ${count.value} 个账号的「${label}」统一写为 ${JSON.stringify(value)}？该操作会影响这些账号的上游身份。`,
    okText: '写入',
    danger: true,
  })
  if (!ok) return
  updating.value = true
  try {
    const res = await request('/admin/accounts/batch-update-credentials', {
      method: 'POST',
      body: { account_ids: props.accountIds, field: field.value, value },
    })
    const okCount = res?.success_count ?? res?.updated ?? res?.count
    message.success(okCount !== undefined ? `已更新 ${okCount} / ${count.value} 个账号` : `已提交更新（${count.value} 个账号）`)
  } catch (e) {
    message.error(`批量更新失败：${e.message}`)
  } finally {
    updating.value = false
  }
}
</script>

<template>
  <a-modal v-model:open="open" title="批量账号运维" width="620px" :footer="null">
    <div class="hint">当前选中 <b>{{ count }}</b> 个账号。</div>

    <div class="sub">批量刷新套餐档位</div>
    <div class="hint">按上游重新读取每个账号的套餐档位（Claude/Google One 等），不修改任何计费配置。</div>
    <div class="row-actions">
      <a-button :loading="refreshing" :disabled="!count" @click="refreshTier">刷新选中账号档位</a-button>
    </div>

    <div class="sub">批量更新凭据字段</div>
    <a-form layout="vertical" style="margin-top: 4px">
      <a-form-item label="字段">
        <a-select v-model:value="field" :options="FIELD_OPTIONS.map((o) => ({ label: o.label, value: o.value }))" style="width: 100%" />
      </a-form-item>
      <a-form-item v-if="kind === 'bool'" label="值">
        <a-switch v-model:checked="boolValue" />
        <span class="inline-hint">开启后该账号的预热请求会被拦截</span>
      </a-form-item>
      <a-form-item v-else label="值">
        <a-input v-model:value="textValue" class="mono" placeholder="粘贴要统一写入的 UUID" />
      </a-form-item>
      <div class="row-actions">
        <a-button type="primary" :loading="updating" :disabled="!count" @click="updateCredentials">写入选中账号</a-button>
      </div>
    </a-form>
  </a-modal>
</template>

<style scoped>
.hint { color: var(--text2); font-size: 12px; line-height: 1.6; }
.inline-hint { color: var(--text2); font-size: 12px; margin-left: 10px; }
.sub { font-weight: 500; font-size: 13px; margin: 16px 0 6px; padding-top: 10px; border-top: 1px solid var(--hr); }
.row-actions { display: flex; gap: 8px; margin-top: 10px; flex-wrap: wrap; }
.mono :deep(input) { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; }
</style>
