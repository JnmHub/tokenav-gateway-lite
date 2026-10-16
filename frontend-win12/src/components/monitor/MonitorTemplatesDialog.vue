<template>
  <a-modal v-model:open="open" title="请求模板管理" width="760px" :footer="null">
    <div class="tpl-hint">模板 = 一组可复用的请求头与可选 body 覆盖。应用到监控 = 拷贝快照；模板后续变动不会自动同步，需手动「应用」刷新关联监控。</div>

    <!-- 表单（新建/编辑） -->
    <div v-if="formOpen" class="tpl-form">
      <div class="tf-title">{{ form.id ? `编辑模板：${form.name}` : '新建模板' }}</div>
      <div class="tf-grid">
        <div class="tf-item"><span class="tf-k">名称</span>
          <a-input v-model:value="form.name" placeholder="如：Grok 官网透传头" style="flex: 1" />
        </div>
        <div class="tf-item"><span class="tf-k">Provider</span>
          <a-select v-model:value="form.provider" :options="PROVIDERS" style="width: 180px" />
        </div>
        <div class="tf-item"><span class="tf-k">API 模式</span>
          <a-select v-model:value="form.api_mode" :options="API_MODES" style="width: 180px" />
        </div>
        <div class="tf-item"><span class="tf-k">说明</span>
          <a-input v-model:value="form.description" placeholder="可留空" style="flex: 1" />
        </div>
        <div class="tf-item" style="align-items: flex-start"><span class="tf-k" style="padding-top: 5px">额外请求头</span>
          <div style="flex: 1">
            <a-textarea v-model:value="form.headersText" :rows="3" placeholder="每行一条：Key: Value" />
            <div v-if="headersBad" class="tf-warn">有 {{ headersBad }} 行无法解析（应形如 Key: Value）</div>
          </div>
        </div>
        <div class="tf-item"><span class="tf-k">Body 覆盖</span>
          <a-radio-group v-model:value="form.body_override_mode">
            <a-radio value="off">关闭</a-radio>
            <a-radio value="merge">合并</a-radio>
            <a-radio value="replace">替换</a-radio>
          </a-radio-group>
        </div>
        <div v-if="form.body_override_mode !== 'off'" class="tf-item" style="align-items: flex-start"><span class="tf-k" style="padding-top: 5px">Body JSON</span>
          <div style="flex: 1">
            <a-textarea v-model:value="form.bodyText" :rows="3" placeholder='{ "stream": true }' />
            <div v-if="bodyBad" class="tf-warn">不是合法 JSON 对象</div>
          </div>
        </div>
      </div>
      <div class="tf-actions">
        <a-button @click="formOpen = false">取消</a-button>
        <a-button type="primary" :loading="saving" :disabled="headersBad > 0 || bodyBad" @click="saveTpl">保存</a-button>
      </div>
    </div>

    <!-- 列表 -->
    <a-table
      :columns="columns" :data-source="items" :loading="loading" row-key="id" size="small"
      :pagination="false" :scroll="{ y: 320 }"
    >
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'name'">
          <div class="tpl-name">{{ record.name }}</div>
          <div class="tpl-desc">{{ record.description || '—' }}</div>
        </template>
        <template v-else-if="column.key === 'pm'">
          <a-tag>{{ record.provider }}</a-tag><span class="tpl-mode">{{ record.api_mode }}</span>
        </template>
        <template v-else-if="column.key === 'ov'">
          <span class="tpl-mode">{{ record.body_override_mode }}<template v-if="record.body_override_mode !== 'off'">（{{ Object.keys(record.body_override || {}).length }} 项）</template></span>
        </template>
        <template v-else-if="column.key === 'assoc'">{{ record.associated_monitors }} 个</template>
        <template v-else-if="column.key === 'action'">
          <a-space>
            <a-button size="small" type="primary" :disabled="!record.associated_monitors" @click="openApply(record)">应用</a-button>
            <a-button size="small" @click="openForm(record)">编辑</a-button>
            <a-button size="small" danger @click="removeTpl(record)">删除</a-button>
          </a-space>
        </template>
      </template>
      <template #empty><span class="tpl-desc">暂无模板，点击「新建模板」创建</span></template>
    </a-table>
    <div class="tf-actions" style="justify-content: flex-start">
      <a-button type="primary" @click="openForm(null)">+ 新建模板</a-button>
      <a-button :loading="loading" @click="load">刷新</a-button>
    </div>

    <!-- 应用弹窗：勾选要刷新快照的关联监控 -->
    <a-modal v-model:open="applyOpen" :title="`应用模板：${applyTarget?.name || ''}`" width="480px" :footer="null">
      <div class="tpl-hint">勾选要覆盖快照的关联监控（拷贝当前模板内容，不随模板自动同步）。</div>
      <div v-if="!assocLoading" class="assoc-list">
        <label v-for="m in assocItems" :key="m.id" class="assoc-row">
          <input v-model="applyChecked" type="checkbox" :value="m.id" />
          <span>{{ m.name }}</span>
          <a-tag>{{ m.provider }}</a-tag>
          <span class="tpl-mode">{{ m.api_mode }}</span>
          <a-tag :color="m.enabled ? 'green' : 'default'">{{ m.enabled ? '启用' : '停用' }}</a-tag>
        </label>
      </div>
      <div v-else class="tpl-desc">加载关联监控中…</div>
      <div class="tf-actions">
        <span class="tpl-desc">已选 {{ applyChecked.length }} / {{ assocItems.length }}</span>
        <a-button type="primary" :disabled="!applyChecked.length" :loading="applying" @click="doApply">应用（{{ applyChecked.length }}）</a-button>
      </div>
    </a-modal>
  </a-modal>
</template>

<script setup>
/* 请求模板管理（旧 MonitorTemplateManagerDialog + ApplyPickerDialog 等价收敛）
 * 接口：GET/POST /admin/channel-monitor-templates、GET/PUT/DELETE :id、
 *       POST :id/apply、GET :id/monitors —— 契约见旧 channelMonitorTemplate.ts */
import { ref, computed, watch } from 'vue'
import { confirmAction } from '../../utils/confirmAction'
import { message } from 'ant-design-vue'
import { request } from '../../api'

const open = defineModel('open', { type: Boolean, default: false })
const emit = defineEmits(['changed'])

const PROVIDERS = [
  { value: 'openai', label: 'OpenAI' },
  { value: 'anthropic', label: 'Anthropic' },
  { value: 'gemini', label: 'Gemini' },
  { value: 'grok', label: 'Grok' },
]
const API_MODES = [
  { value: 'chat_completions', label: 'chat_completions' },
  { value: 'responses', label: 'responses' },
]

const columns = [
  { title: '名称 / 说明', key: 'name', ellipsis: true },
  { title: '平台 / 模式', key: 'pm', width: 190 },
  { title: 'Body 覆盖', key: 'ov', width: 130 },
  { title: '关联监控', key: 'assoc', width: 90 },
  { title: '操作', key: 'action', width: 200 },
]

const items = ref([])
const loading = ref(false)
const load = async () => {
  loading.value = true
  try {
    const res = await request('/admin/channel-monitor-templates', { method: 'GET' })
    items.value = res?.items || []
  } catch (e) { message.error(`模板加载失败：${e.message}`) } finally { loading.value = false }
}
watch(open, (o) => { if (o) load() })

// ===== 新建/编辑表单 =====
const formOpen = ref(false)
const saving = ref(false)
const blankForm = () => ({ id: 0, name: '', provider: 'openai', api_mode: 'chat_completions', description: '', headersText: '', body_override_mode: 'off', bodyText: '' })
const form = ref(blankForm())
const openForm = (r) => {
  if (r) {
    form.value = {
      id: r.id, name: r.name, provider: r.provider, api_mode: r.api_mode || 'chat_completions',
      description: r.description || '',
      headersText: Object.entries(r.extra_headers || {}).map(([k, v]) => `${k}: ${v}`).join('\n'),
      body_override_mode: r.body_override_mode || 'off',
      bodyText: r.body_override ? JSON.stringify(r.body_override, null, 2) : '',
    }
  } else form.value = blankForm()
  formOpen.value = true
}
// "Key: Value" 行解析（也容忍 Key=Value），返回 {解析结果, 坏行数}
const parseHeaders = () => {
  const out = {}
  let bad = 0
  for (const line of form.value.headersText.split('\n')) {
    const s = line.trim()
    if (!s) continue
    const i = s.search(/[:=]/)
    if (i <= 0) { bad++; continue }
    out[s.slice(0, i).trim()] = s.slice(i + 1).trim()
  }
  return { out, bad }
}
const headersBad = computed(() => parseHeaders().bad)
const bodyBad = computed(() => {
  if (form.value.body_override_mode === 'off' || !form.value.bodyText.trim()) return false
  try { const v = JSON.parse(form.value.bodyText); return !v || typeof v !== 'object' || Array.isArray(v) } catch { return true }
})
const saveTpl = async () => {
  if (!form.value.name.trim()) { message.error('模板名称必填'); return }
  saving.value = true
  try {
    const body = {
      name: form.value.name.trim(),
      provider: form.value.provider,
      api_mode: form.value.api_mode,
      description: form.value.description.trim(),
      extra_headers: parseHeaders().out,
      body_override_mode: form.value.body_override_mode,
    }
    if (body.body_override_mode !== 'off' && form.value.bodyText.trim()) body.body_override = JSON.parse(form.value.bodyText)
    if (form.value.id) await request(`/admin/channel-monitor-templates/${form.value.id}`, { method: 'PUT', body })
    else await request('/admin/channel-monitor-templates', { method: 'POST', body })
    message.success(form.value.id ? '模板已更新' : '模板已创建')
    formOpen.value = false
    await load()
    emit('changed')
  } catch (e) { message.error(`保存失败：${e.message}`) } finally { saving.value = false }
}
const removeTpl = async (r) => {
  if (!(await confirmAction({ title: '删除确认', content: `删除模板「${r.name}」？已拷贝快照的监控不受影响。`, okText: '删除', danger: true }))) return
  try {
    await request(`/admin/channel-monitor-templates/${r.id}`, { method: 'DELETE' })
    message.success('已删除')
    await load()
  } catch (e) { message.error(`删除失败：${e.message}`) }
}

// ===== 应用：选关联监控覆盖快照 =====
const applyOpen = ref(false)
const applyTarget = ref(null)
const assocItems = ref([])
const assocLoading = ref(false)
const applyChecked = ref([])
const applying = ref(false)
const openApply = async (r) => {
  applyTarget.value = r
  applyChecked.value = []
  applyOpen.value = true
  assocLoading.value = true
  try {
    const res = await request(`/admin/channel-monitor-templates/${r.id}/monitors`, { method: 'GET' })
    assocItems.value = res?.items || []
  } catch (e) { message.error(`关联监控加载失败：${e.message}`) } finally { assocLoading.value = false }
}
const doApply = async () => {
  applying.value = true
  try {
    const res = await request(`/admin/channel-monitor-templates/${applyTarget.value.id}/apply`, { method: 'POST', body: { monitor_ids: applyChecked.value } })
    message.success(`已应用 ${res?.affected ?? applyChecked.value.length} 个监控`)
    applyOpen.value = false
    await load()
  } catch (e) { message.error(`应用失败：${e.message}`) } finally { applying.value = false }
}
</script>

<style scoped>
.tpl-hint { font-size: 12px; color: var(--text2); margin-bottom: 10px; }
.tpl-form { border: 1px solid var(--hr); border-radius: 8px; padding: 12px 14px; margin-bottom: 12px; }
.tf-title { font-weight: 500; font-size: 13px; margin-bottom: 10px; }
.tf-grid { display: flex; flex-direction: column; gap: 8px; }
.tf-item { display: flex; align-items: center; gap: 12px; }
.tf-k { flex: none; width: 88px; font-size: 12px; color: var(--text2); text-align: right; }
.tf-warn { font-size: 12px; color: #c42b1c; margin-top: 4px; }
.tf-actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 12px; }
.tpl-name { font-size: 13px; font-weight: 500; }
.tpl-desc { font-size: 12px; color: var(--text2); }
.tpl-mode { font-size: 12px; color: var(--text2); margin-left: 4px; }
.assoc-list { display: flex; flex-direction: column; gap: 4px; max-height: 280px; overflow-y: auto; }
.assoc-row { display: flex; align-items: center; gap: 8px; padding: 6px 8px; border-radius: 6px; cursor: pointer; font-size: 13px; }
.assoc-row:hover { background: var(--hover, rgba(127,127,127,0.08)); }
</style>
