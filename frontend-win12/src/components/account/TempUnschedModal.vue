<script setup>
/* 临时停调状态弹窗——旧实现契约：frontend/src/components/account/TempUnschedStatusModal.vue
 * 查询：GET /admin/accounts/:id/temp-unschedulable
 *   → { active: false } 或 { active: true, state: { until_unix, triggered_at_unix,
 *       status_code, matched_keyword, rule_index, error_message } }
 * 恢复按钮走统一恢复接口：POST /admin/accounts/:id/recover-state（旧版 handleReset 同款） */
import { ref, computed, watch } from 'vue'
import { request } from '../../api'
import { message } from 'ant-design-vue'
import dayjs from 'dayjs'

const open = defineModel('open', { type: Boolean, default: false })
const props = defineProps({
  account: { type: Object, default: null },
})
const emit = defineEmits(['recovered'])

const loading = ref(false)
const resetting = ref(false)
const status = ref(null)

const state = computed(() => status.value?.state || null)
const isActive = computed(() => {
  if (!status.value?.active || !state.value) return false
  return state.value.until_unix * 1000 > Date.now()
})
const fmt = (unix) => (unix ? dayjs(unix * 1000).format('YYYY-MM-DD HH:mm:ss') : '-')
const remainingText = computed(() => {
  if (!state.value) return '-'
  const ms = state.value.until_unix * 1000 - Date.now()
  if (ms <= 0) return '已到期'
  const minutes = Math.ceil(ms / 60000)
  if (minutes < 60) return `${minutes} 分钟`
  return `${Math.floor(minutes / 60)} 小时 ${minutes % 60} 分`
})

const load = async () => {
  if (!props.account?.id) return
  loading.value = true
  status.value = null
  try {
    status.value = await request(`/admin/accounts/${props.account.id}/temp-unschedulable`, { method: 'GET' })
  } catch (e) {
    message.error(`读取临时停调状态失败：${e.message}`)
  } finally {
    loading.value = false
  }
}

const recover = async () => {
  resetting.value = true
  try {
    await request(`/admin/accounts/${props.account.id}/recover-state`, { method: 'POST' })
    message.success('已恢复调度')
    open.value = false
    emit('recovered')
  } catch (e) {
    message.error(`恢复失败：${e.message}`)
  } finally {
    resetting.value = false
  }
}

watch(open, (o) => { if (o) load() })
</script>

<template>
  <a-modal v-model:open="open" :title="`临时停调状态 · ${account?.name || ''}`" width="520px" :footer="null">
    <a-spin :spinning="loading">
      <div v-if="!isActive" class="idle">该账号当前没有生效中的临时停调（temp-unschedulable，触发错误冷却规则后的临时暂停调度）。</div>
      <template v-else>
        <a-alert type="info" show-icon message="命中错误冷却规则被临时停调，可立即恢复调度。" style="margin-bottom: 12px" />
        <div class="kv">
          <div class="kitem"><span>触发时间</span><b>{{ fmt(state?.triggered_at_unix) }}</b></div>
          <div class="kitem"><span>恢复时间</span><b>{{ fmt(state?.until_unix) }}</b></div>
          <div class="kitem"><span>剩余</span><b>{{ remainingText }}</b></div>
          <div class="kitem"><span>状态码</span><b>{{ state?.status_code || '-' }}</b></div>
          <div class="kitem"><span>命中关键词</span><b>{{ state?.matched_keyword || '-' }}</b></div>
          <div class="kitem"><span>规则序号</span><b>{{ state ? state.rule_index + 1 : '-' }}</b></div>
        </div>
        <div class="errmsg"><span>错误信息</span><div>{{ state?.error_message || '-' }}</div></div>
        <div class="foot">
          <a-button type="primary" :loading="resetting" @click="recover">立即恢复</a-button>
        </div>
      </template>
    </a-spin>
  </a-modal>
</template>

<style scoped>
.idle { color: var(--text2); font-size: 13px; padding: 12px 0; }
.kv { display: grid; grid-template-columns: 1fr 1fr; gap: 8px 16px; }
.kitem { display: flex; justify-content: space-between; gap: 8px; padding: 7px 10px; border-radius: 8px; background: var(--card); font-size: 13px; }
.kitem span { color: var(--text2); flex: none; }
.kitem b { font-weight: 500; }
.errmsg { margin-top: 10px; padding: 8px 10px; border-radius: 8px; background: var(--card); font-size: 12px; }
.errmsg span { color: var(--text2); }
.errmsg div { margin-top: 4px; word-break: break-all; }
.foot { display: flex; justify-content: flex-end; margin-top: 14px; }
</style>
