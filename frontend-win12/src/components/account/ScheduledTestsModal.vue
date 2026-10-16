<script setup>
/* 定时测试计划弹窗（三期增强）——旧实现契约：frontend/src/components/admin/account/ScheduledTestsPanel.vue
 * 接口：
 * - GET  /admin/accounts/:id/scheduled-test-plans（裸数组，无计划返回 null）
 * - POST /admin/scheduled-test-plans {account_id, model_id, cron_expression, enabled, max_results, auto_recover}
 * - PUT  /admin/scheduled-test-plans/:id（部分字段更新：启停 toggle 只传 {enabled}，编辑传全量）
 * - DELETE /admin/scheduled-test-plans/:id
 * - GET  /admin/scheduled-test-plans/:id/results?limit=20 → [{id,status,latency_ms,started_at,error_message,response_text}]
 * 三期新增等价能力：行内编辑 / 启停 toggle / 展开查看最近结果（成功/运行/失败 + 耗时 + 错误或响应文本）。 */
import { ref, reactive, watch } from 'vue'
import { confirmAction } from '../../utils/confirmAction'
import { request } from '../../api'
import { message } from 'ant-design-vue'
import dayjs from 'dayjs'

const open = defineModel('open', { type: Boolean, default: false })
const props = defineProps({ account: { type: Object, default: null } })
const emit = defineEmits(['changed'])

const loading = ref(false)
const plans = ref([])
const saving = ref(false)
// 新增表单（默认值对照旧 newPlan）
const form = reactive({ model_id: '', cron_expression: '*/30 * * * *', max_results: 100, enabled: true, auto_recover: false })
// 行内编辑状态
const editingId = ref(null)
const editForm = reactive({ model_id: '', cron_expression: '', max_results: 100, enabled: true, auto_recover: false })
// 最近结果
const expandedId = ref(null)
const results = ref([])
const resultsLoading = ref(false)
const showErr = reactive({})

const PLANS_URL = '/admin/scheduled-test-plans'
const fmtTime = (v) => (v ? dayjs(v).format('MM-DD HH:mm:ss') : '-')

const load = async () => {
  if (!props.account?.id) return
  loading.value = true
  try {
    const list = await request(`/admin/accounts/${props.account.id}/scheduled-test-plans`, { method: 'GET' })
    plans.value = Array.isArray(list) ? list : []
  } catch (e) {
    message.error(`读取计划失败：${e.message}`)
    plans.value = []
  } finally {
    loading.value = false
  }
}
watch(open, (o) => {
  if (o) { load(); editingId.value = null; expandedId.value = null; results.value = [] }
})

const create = async () => {
  if (!form.cron_expression.trim()) { message.warning('CRON 表达式必填'); return }
  saving.value = true
  try {
    await request(PLANS_URL, {
      method: 'POST',
      body: {
        account_id: props.account.id,
        model_id: form.model_id.trim() || undefined,
        cron_expression: form.cron_expression.trim(),
        enabled: form.enabled,
        max_results: Number(form.max_results) || 100,
        auto_recover: form.auto_recover,
      },
    })
    message.success('计划已创建')
    await load()
    emit('changed')
  } catch (e) {
    message.error(`创建失败：${e.message}`)
  } finally {
    saving.value = false
  }
}

// 启停 toggle：仅传 enabled（旧 handleToggleEnabled）
const toggleEnabled = async (p, val) => {
  try {
    const updated = await request(`${PLANS_URL}/${p.id}`, { method: 'PUT', body: { enabled: val } })
    const i = plans.value.findIndex((x) => x.id === p.id)
    if (i !== -1) plans.value[i] = updated
    message.success(val ? '计划已启用' : '计划已停用')
  } catch (e) {
    message.error(`更新失败：${e.message}`)
  }
}

const startEdit = (p) => {
  editingId.value = p.id
  editForm.model_id = p.model_id || ''
  editForm.cron_expression = p.cron_expression || ''
  editForm.max_results = p.max_results || 100
  editForm.enabled = !!p.enabled
  editForm.auto_recover = !!p.auto_recover
}
const saveEdit = async () => {
  if (!editForm.cron_expression.trim()) { message.warning('CRON 表达式必填'); return }
  saving.value = true
  try {
    const updated = await request(`${PLANS_URL}/${editingId.value}`, {
      method: 'PUT',
      body: {
        model_id: editForm.model_id.trim() || undefined,
        cron_expression: editForm.cron_expression.trim(),
        max_results: Number(editForm.max_results) || 100,
        enabled: editForm.enabled,
        auto_recover: editForm.auto_recover,
      },
    })
    const i = plans.value.findIndex((x) => x.id === editingId.value)
    if (i !== -1) plans.value[i] = updated
    message.success('计划已更新')
    editingId.value = null
  } catch (e) {
    message.error(`更新失败：${e.message}`)
  } finally {
    saving.value = false
  }
}

const remove = async (p) => {
  if (!(await confirmAction({ title: '删除确认', content: `确认删除计划（${p.cron_expression}）？`, okText: '删除', danger: true }))) return
  try {
    await request(`${PLANS_URL}/${p.id}`, { method: 'DELETE' })
    message.success('计划已删除')
    if (expandedId.value === p.id) { expandedId.value = null; results.value = [] }
    await load()
    emit('changed')
  } catch (e) {
    message.error(`删除失败：${e.message}`)
  }
}

// 展开查看最近结果（旧 toggleExpand，limit=20）
const toggleExpand = async (p) => {
  if (expandedId.value === p.id) { expandedId.value = null; results.value = []; return }
  expandedId.value = p.id
  resultsLoading.value = true
  try {
    const list = await request(`${PLANS_URL}/${p.id}/results`, { method: 'GET', query: { limit: 20 } })
    results.value = Array.isArray(list) ? list : []
  } catch (e) {
    message.error(`读取结果失败：${e.message}`)
    results.value = []
  } finally {
    resultsLoading.value = false
  }
}
</script>

<template>
  <a-modal v-model:open="open" :title="`定时测试 · ${account?.name || ''}`" width="680px" :footer="null">
    <a-spin :spinning="loading">
      <div v-if="!plans.length && !loading" class="empty">该账号暂无计划测试</div>
      <div v-for="p in plans" :key="p.id" class="plan">
        <!-- 行头 -->
        <div class="prow">
          <div class="pmain" @click="toggleExpand(p)">
            <span class="pcron">{{ p.cron_expression }}</span>
            <span class="pmodel">{{ p.model_id || '默认模型' }}</span>
            <a-tag v-if="p.auto_recover" color="blue">自动恢复</a-tag>
            <span class="pmeta">上次 {{ fmtTime(p.last_run_at) }} · 下次 {{ fmtTime(p.next_run_at) }}</span>
          </div>
          <div class="pops">
            <a-switch :checked="!!p.enabled" size="small" @change="(v) => toggleEnabled(p, v)" />
            <a-button size="small" @click.stop="startEdit(p)">编辑</a-button>
            <a-button size="small" danger @click.stop="remove(p)">删除</a-button>
            <a @click.prevent="toggleExpand(p)">{{ expandedId === p.id ? '收起' : '最近结果' }}</a>
          </div>
        </div>
        <!-- 编辑表单 -->
        <div v-if="editingId === p.id" class="edit">
          <div class="erow"><span>模型 ID（留空默认）</span><a-input v-model:value="editForm.model_id" size="small" style="width: 200px" /></div>
          <div class="erow"><span>CRON 表达式</span><a-input v-model:value="editForm.cron_expression" size="small" placeholder="*/30 * * * *" style="width: 200px" /></div>
          <div class="erow"><span>最多保留结果数</span><a-input-number v-model:value="editForm.max_results" :min="1" size="small" style="width: 120px" /></div>
          <div class="erow"><span>启用</span><a-switch v-model:checked="editForm.enabled" size="small" /></div>
          <div class="erow"><span>失败自动恢复调度</span><a-switch v-model:checked="editForm.auto_recover" size="small" /></div>
          <div class="eops">
            <a-button size="small" @click="editingId = null">取消</a-button>
            <a-button size="small" type="primary" :loading="saving" @click="saveEdit">保存</a-button>
          </div>
        </div>
        <!-- 最近结果 -->
        <div v-if="expandedId === p.id" class="results">
          <a-spin :spinning="resultsLoading">
            <div v-if="!results.length && !resultsLoading" class="dim" style="padding: 6px 0">暂无执行结果</div>
            <div v-for="r in results" :key="r.id" class="rrow">
              <div class="rhead">
                <a-tag :color="r.status === 'success' ? 'green' : r.status === 'running' ? 'blue' : 'red'">
                  {{ r.status === 'success' ? '成功' : r.status === 'running' ? '运行中' : '失败' }}
                </a-tag>
                <span v-if="r.latency_ms > 0" class="dim">{{ r.latency_ms }}ms</span>
                <span class="flex1" />
                <span class="dim">{{ fmtTime(r.started_at) }}</span>
              </div>
              <a v-if="r.error_message || r.response_text" class="rtoggle" @click="showErr[r.id] = !showErr[r.id]">
                {{ r.error_message ? '错误详情' : '响应内容' }} {{ showErr[r.id] ? '▲' : '▼' }}
              </a>
              <pre v-if="showErr[r.id] && r.error_message" class="rpre err">{{ r.error_message }}</pre>
              <pre v-if="showErr[r.id] && !r.error_message && r.response_text" class="rpre">{{ r.response_text }}</pre>
            </div>
          </a-spin>
        </div>
      </div>

      <!-- 新增计划 -->
      <div class="newplan">
        <div class="ntitle">新增计划</div>
        <div class="erow"><span>模型 ID（留空默认）</span><a-input v-model:value="form.model_id" size="small" placeholder="gpt-4o" style="width: 200px" /></div>
        <div class="erow"><span>CRON 表达式</span><a-input v-model:value="form.cron_expression" size="small" placeholder="*/30 * * * *" style="width: 200px" /></div>
        <div class="erow"><span>最多保留结果数</span><a-input-number v-model:value="form.max_results" :min="1" size="small" style="width: 120px" /></div>
        <div class="erow"><span>启用</span><a-switch v-model:checked="form.enabled" size="small" /></div>
        <div class="erow"><span>失败自动恢复调度</span><a-switch v-model:checked="form.auto_recover" size="small" /></div>
        <div class="eops"><a-button type="primary" size="small" :loading="saving" @click="create">创建计划</a-button></div>
      </div>
    </a-spin>
  </a-modal>
</template>

<style scoped>
.empty { color: var(--text2); font-size: 13px; padding: 8px 0; }
.plan { border: 1px solid var(--hr); border-radius: 8px; padding: 8px 10px; margin-bottom: 8px; }
.prow { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.pmain { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; cursor: pointer; flex: 1; }
.pcron { font-family: Consolas, monospace; font-weight: 500; font-size: 13px; }
.pmodel { color: var(--text2); font-size: 12px; }
.pmeta { color: var(--text2); font-size: 11px; }
.pops { display: flex; align-items: center; gap: 6px; flex: none; }
.edit, .newplan { margin-top: 8px; padding: 10px; border-radius: 8px; background: var(--card); display: flex; flex-direction: column; gap: 8px; }
.erow { display: flex; align-items: center; justify-content: space-between; font-size: 12px; color: var(--text2); }
.eops { display: flex; justify-content: flex-end; gap: 8px; }
.results { margin-top: 8px; border-top: 1px solid var(--hr); padding-top: 6px; }
.rrow { padding: 4px 0; border-bottom: 1px solid var(--hr); }
.rrow:last-child { border-bottom: none; }
.rhead { display: flex; align-items: center; gap: 8px; }
.rtoggle { font-size: 11px; cursor: pointer; }
.rpre { margin: 4px 0 0; padding: 6px 8px; border-radius: 6px; background: var(--card); font-size: 11px; max-height: 130px; overflow: auto; white-space: pre-wrap; word-break: break-all; }
.rpre.err { color: #c13438; }
.ntitle { font-weight: 500; font-size: 13px; }
.flex1 { flex: 1; }
.dim { color: var(--text2); font-size: 11px; }
</style>
