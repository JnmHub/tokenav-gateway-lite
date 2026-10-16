<script setup>
/* ============================================================
 * 用量清理任务弹窗（旧 UsageCleanupDialog 等价，Win12 重构版）
 * 接口：GET/POST /admin/usage/cleanup-tasks、POST /admin/usage/cleanup-tasks/:id/cancel
 * 时区契约：创建任务的 start_date/end_date 按请求体 timezone 字段解析
 * （后端 CreateUsageCleanupTaskRequest.timezone → ParseInUserLocation），
 * 这里统一传浏览器 IANA 时区，避免跨时区把「今天」清错一天。
 * 可选维度（用户/账号/Key/分组）跟随页面当前筛选，由父组件传入。
 * ============================================================ */
import { ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { request } from '../../api'
import { fmtTime, REQ_TYPE_OPTS, BROWSER_TZ } from './usageFormat'

const props = defineProps({
  open: Boolean,
  userId: { type: Number, default: null },
  accountId: { type: Number, default: null },
  apiKeyId: { type: Number, default: null },
  groupId: { type: Number, default: null },
})
const emit = defineEmits(['update:open'])
const close = () => emit('update:open', false)

const saving = ref(false)
const range = ref()
const tasks = ref([])
const tasksLoading = ref(false)
// 模型与请求类型手动填；其余维度跟随页面筛选（见下方 body 组装）
const form = ref({ model: '', request_type: undefined })

const statusInfo = (s) => ({
  pending: { text: '等待中', color: 'default' },
  running: { text: '执行中', color: 'processing' },
  completed: { text: '已完成', color: 'green' },
  canceled: { text: '已取消', color: 'orange' },
  failed: { text: '失败', color: 'red' },
}[s] || { text: s, color: 'default' })

const loadTasks = async () => {
  tasksLoading.value = true
  try {
    const res = await request('/admin/usage/cleanup-tasks', { method: 'GET', query: { page: 1, page_size: 10 } })
    tasks.value = res?.items || []
  } catch (e) { message.error(`清理任务加载失败：${e.message}`) } finally { tasksLoading.value = false }
}
// 弹窗每次打开都刷新最近任务列表（v-model:open 受控）
watch(() => props.open, (v) => { if (v) loadTasks() })

const createTask = async () => {
  if (!range.value?.length) { message.error('请选择要清理的时间范围'); return }
  saving.value = true
  try {
    // start_time/end_time 必填；时区显式声明，边界条件：后端按 [start_date, end_date+1d) 半开区间删除
    const body = {
      start_date: range.value[0],
      end_date: range.value[1],
      timezone: BROWSER_TZ,
      user_id: props.userId,
      account_id: props.accountId,
      api_key_id: props.apiKeyId,
      group_id: props.groupId,
      model: form.value.model.trim() || undefined,
      request_type: form.value.request_type,
    }
    await request('/admin/usage/cleanup-tasks', { method: 'POST', body })
    message.success('清理任务已创建，将在后台执行')
    range.value = undefined
    await loadTasks()
  } catch (e) { message.error(`创建失败：${e.message}`) } finally { saving.value = false }
}
const cancelTask = async (t) => {
  try {
    await request(`/admin/usage/cleanup-tasks/${t.id}/cancel`, { method: 'POST' })
    message.success(`任务 #${t.id} 已请求取消`)
    await loadTasks()
  } catch (e) { message.error(`取消失败：${e.message}`) }
}
</script>

<template>
  <a-modal :open="open" title="清理用量数据" :footer="null" width="680px" @cancel="close">
    <div class="warn">删除操作不可恢复：将按下方条件物理删除用量明细，请谨慎选择时间范围。</div>
    <div class="form">
      <!-- 后端 CreateUsageCleanupTaskRequest 按 YYYY-MM-DD + timezone 解析日期（整天半开区间），
           不传 show-time 且显式 value-format，避免 ISO 时间串解析失败 -->
      <div class="crow"><span class="ck">时间范围（必选）</span>
        <a-range-picker v-model:value="range" value-format="YYYY-MM-DD" style="flex: 1" />
      </div>
      <div class="crow"><span class="ck">模型（可选）</span>
        <a-input v-model:value="form.model" placeholder="留空 = 全部模型" style="flex: 1" />
      </div>
      <div class="crow"><span class="ck">请求类型（可选）</span>
        <a-select v-model:value="form.request_type" :options="REQ_TYPE_OPTS" allow-clear placeholder="留空 = 全部类型" style="flex: 1" />
      </div>
      <div class="crow"><span class="ck">随页面筛选</span>
        <span class="note">
          用户{{ userId ? ` #${userId}` : '：未筛选' }} · 账号{{ accountId ? ` #${accountId}` : '：未筛选' }} ·
          Key{{ apiKeyId ? ` #${apiKeyId}` : '：未筛选' }} · 分组{{ groupId ? ` #${groupId}` : '：未筛选' }}
          （在页面上设置筛选后再打开本窗口即可缩小清理范围）
        </span>
      </div>
    </div>
    <div class="actions">
      <a-button type="primary" danger :loading="saving" @click="createTask">创建清理任务</a-button>
    </div>
    <div class="sec-title">最近任务
      <a-button size="small" type="text" :loading="tasksLoading" @click="loadTasks">刷新</a-button>
    </div>
    <div v-if="!tasks.length && !tasksLoading" class="note">暂无清理任务</div>
    <div v-for="t in tasks" :key="t.id" class="task">
      <div class="task-head">
        <span>
          <a-tag :color="statusInfo(t.status).color">{{ statusInfo(t.status).text }}</a-tag>
          <span class="mono">#{{ t.id }}</span>
        </span>
        <span class="note">{{ fmtTime(t.created_at) }}</span>
      </div>
      <div class="task-meta">
        <span>范围：{{ (t.filters?.start_time || '').replace('T', ' ').slice(0, 16) }} ~ {{ (t.filters?.end_time || '').replace('T', ' ').slice(0, 16) }}</span>
        <span>已删除 {{ Number(t.deleted_rows || 0).toLocaleString() }} 行</span>
        <a-button v-if="t.status === 'pending' || t.status === 'running'" size="small" type="text" danger @click="cancelTask(t)">取消</a-button>
      </div>
      <div v-if="t.error_message" class="note err-text">{{ t.error_message }}</div>
    </div>
  </a-modal>
</template>

<style scoped>
.warn { padding: 10px 12px; border-radius: 8px; background: rgba(217, 119, 6, 0.1); color: #9a5b00; font-size: 12px; margin-bottom: 12px; }
.form { display: flex; flex-direction: column; gap: 10px; }
.form .crow { display: flex; align-items: center; gap: 12px; }
.form .ck { flex: none; width: 120px; font-size: 12px; color: var(--text2); text-align: right; }
.note { font-size: 12px; color: var(--text2); }
.actions { display: flex; justify-content: flex-end; margin: 14px 0 4px; }
.sec-title { font-weight: 500; font-size: 13px; margin: 14px 0 8px; display: flex; align-items: center; gap: 8px; }
.task { border: 1px solid var(--hr); border-radius: 8px; padding: 8px 12px; margin-bottom: 8px; }
.task-head { display: flex; justify-content: space-between; align-items: center; }
.task-meta { display: flex; gap: 14px; align-items: center; margin-top: 6px; font-size: 12px; color: var(--text2); flex-wrap: wrap; }
.err-text { color: #dc2626; }
.mono { font-family: ui-monospace, monospace; font-size: 12px; }
</style>
