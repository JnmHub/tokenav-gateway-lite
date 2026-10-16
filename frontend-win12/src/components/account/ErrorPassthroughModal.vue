<script setup>
/* 错误透传规则弹窗（三期）——旧实现契约：frontend/src/components/admin/ErrorPassthroughRulesModal.vue
 * + frontend/src/api/admin/errorPassthrough.ts
 * 接口：GET/POST /admin/error-passthrough-rules，PUT/DELETE /admin/error-passthrough-rules/:id
 *   （启停 toggle = PUT {enabled}，旧 toggleEnabled 即 update 的语义）
 * 字段：name/enabled/priority/error_codes[]/keywords[]/match_mode(any|all)/platforms[]/
 *       passthrough_code/response_code/passthrough_body/custom_message/skip_monitoring/description */
import { ref, reactive, watch } from 'vue'
import { confirmAction } from '../../utils/confirmAction'
import { request } from '../../api'
import { message } from 'ant-design-vue'

const open = defineModel('open', { type: Boolean, default: false })

const rules = ref([])
const loading = ref(false)
const showForm = ref(false)
const editingId = ref(null)
const saving = ref(false)
const errorCodesInput = ref('')
const keywordsInput = ref('')
const form = reactive({
  name: '', enabled: true, priority: 0, match_mode: 'any', platforms: [],
  passthrough_code: true, response_code: null, passthrough_body: true,
  custom_message: null, skip_monitoring: false, description: null,
})
// 平台选项（旧 CONCRETE_PLATFORM_OPTIONS）
const PLATFORMS = [
  { value: 'anthropic', label: 'Anthropic' }, { value: 'openai', label: 'OpenAI' },
  { value: 'gemini', label: 'Gemini' }, { value: 'antigravity', label: 'Antigravity' },
  { value: 'grok', label: 'Grok' }, { value: 'kimi', label: 'Kimi' },
  { value: 'zhipu', label: '智谱 GLM' }, { value: 'deepseek', label: 'DeepSeek' },
]

const URL_BASE = '/admin/error-passthrough-rules'
const load = async () => {
  loading.value = true
  try { rules.value = (await request(URL_BASE, { method: 'GET' })) || [] }
  catch (e) { message.error(`规则读取失败：${e.message}`) }
  finally { loading.value = false }
}
watch(open, (o) => { if (o) load() })

const resetForm = () => {
  Object.assign(form, { name: '', enabled: true, priority: 0, match_mode: 'any', platforms: [], passthrough_code: true, response_code: null, passthrough_body: true, custom_message: null, skip_monitoring: false, description: null })
  errorCodesInput.value = ''
  keywordsInput.value = ''
}
const startCreate = () => { editingId.value = null; resetForm(); showForm.value = true }
const startEdit = (r) => {
  editingId.value = r.id
  Object.assign(form, {
    name: r.name, enabled: r.enabled, priority: r.priority, match_mode: r.match_mode,
    platforms: [...(r.platforms || [])], passthrough_code: r.passthrough_code, response_code: r.response_code,
    passthrough_body: r.passthrough_body, custom_message: r.custom_message,
    skip_monitoring: r.skip_monitoring, description: r.description,
  })
  errorCodesInput.value = (r.error_codes || []).join(', ')
  keywordsInput.value = (r.keywords || []).join('\n')
  showForm.value = true
}

// 旧 parseErrorCodes / parseKeywords：逗号或空白分隔数字；换行分隔关键词
const parseCodes = () => errorCodesInput.value.split(/[,\s]+/).map((s) => parseInt(s, 10)).filter((n) => !Number.isNaN(n) && n > 0)
const parseKeywords = () => keywordsInput.value.split('\n').map((s) => s.trim()).filter(Boolean)

const submit = async () => {
  if (!form.name.trim()) { message.error('规则名称必填'); return }
  const codes = parseCodes()
  const kws = parseKeywords()
  if (!codes.length && !kws.length) { message.error('至少配置一个错误码或关键词匹配条件'); return }
  saving.value = true
  try {
    const body = {
      name: form.name.trim(), enabled: form.enabled, priority: Number(form.priority) || 0,
      error_codes: codes, keywords: kws, match_mode: form.match_mode, platforms: form.platforms,
      passthrough_code: form.passthrough_code,
      response_code: form.passthrough_code ? null : (Number(form.response_code) || null),
      passthrough_body: form.passthrough_body,
      custom_message: form.passthrough_body ? null : (form.custom_message || null),
      skip_monitoring: form.skip_monitoring,
      description: form.description?.trim() || null,
    }
    if (editingId.value) await request(`${URL_BASE}/${editingId.value}`, { method: 'PUT', body })
    else await request(URL_BASE, { method: 'POST', body })
    message.success(editingId.value ? '规则已更新' : '规则已创建')
    showForm.value = false
    await load()
  } catch (e) {
    message.error(`保存失败：${e.message}`)
  } finally {
    saving.value = false
  }
}

const toggleEnabled = async (r) => {
  try {
    const updated = await request(`${URL_BASE}/${r.id}`, { method: 'PUT', body: { enabled: !r.enabled } })
    const i = rules.value.findIndex((x) => x.id === r.id)
    if (i !== -1) rules.value[i] = updated
  } catch (e) { message.error(`切换失败：${e.message}`) }
}

const remove = async (r) => {
  if (!(await confirmAction({ title: '删除确认', content: `确认删除规则「${r.name}」？`, okText: '删除', danger: true }))) return
  try {
    await request(`${URL_BASE}/${r.id}`, { method: 'DELETE' })
    message.success('规则已删除')
    await load()
  } catch (e) { message.error(`删除失败：${e.message}`) }
}
</script>

<template>
  <a-modal v-model:open="open" title="错误透传规则" width="760px" :footer="null">
    <div class="head">
      <span class="dim">命中规则的上游错误按规则原样透传给用户（错误码/响应体可透传或改写）。</span>
      <a-button type="primary" size="small" @click="startCreate">新建规则</a-button>
    </div>
    <a-spin :spinning="loading">
      <div v-if="!rules.length && !loading" class="empty">暂无规则</div>
      <div v-for="r in rules" :key="r.id" class="rule">
        <div class="rrow1">
          <span class="prio">{{ r.priority }}</span>
          <b>{{ r.name }}</b>
          <a-tag :color="r.match_mode === 'all' ? 'purple' : 'blue'">{{ r.match_mode === 'all' ? '全部匹配' : '任一匹配' }}</a-tag>
          <span class="flex1" />
          <a-switch :checked="!!r.enabled" size="small" @change="() => toggleEnabled(r)" />
          <a-button size="small" @click="startEdit(r)">编辑</a-button>
          <a-button size="small" danger @click="remove(r)">删除</a-button>
        </div>
        <div class="rrow2">
          <a-tag v-for="c in (r.error_codes || []).slice(0, 4)" :key="c" color="red">{{ c }}</a-tag>
          <span v-if="(r.error_codes || []).length > 4" class="dim">+{{ r.error_codes.length - 4 }}</span>
          <a-tag v-for="k in (r.keywords || []).slice(0, 2)" :key="k">"{{ k.slice(0, 12) }}{{ k.length > 12 ? '…' : '' }}"</a-tag>
          <span v-if="(r.keywords || []).length > 2" class="dim">+{{ r.keywords.length - 2 }}</span>
          <span class="dim">平台：{{ (r.platforms || []).length ? r.platforms.join(' / ') : '全部' }}</span>
        </div>
        <div class="rrow3 dim">
          状态码：{{ r.passthrough_code ? '透传' : `改写为 ${r.response_code ?? '-'}` }}
          · 响应体：{{ r.passthrough_body ? '透传' : '自定义' }}
          <template v-if="r.skip_monitoring"> · 不计入监控</template>
        </div>
      </div>
    </a-spin>

    <!-- 新建/编辑表单 -->
    <a-modal :open="showForm" :title="editingId ? '编辑规则' : '新建规则'" width="560px" :footer="null" @cancel="showForm = false">
      <a-form layout="vertical" size="small">
        <div class="two">
          <a-form-item label="规则名称" required><a-input v-model:value="form.name" placeholder="如：透传 429 限流" /></a-form-item>
          <a-form-item label="优先级（小者优先）"><a-input-number v-model:value="form.priority" :min="0" style="width: 100%" /></a-form-item>
        </div>
        <a-form-item label="描述"><a-input v-model:value="form.description" /></a-form-item>
        <div class="two">
          <a-form-item label="错误码（逗号/空格分隔，如 429, 529）"><a-input v-model:value="errorCodesInput" placeholder="429, 503" /></a-form-item>
          <a-form-item label="关键词（每行一个，匹配响应体）">
            <a-textarea v-model:value="keywordsInput" :rows="2" placeholder="quota exceeded" />
          </a-form-item>
        </div>
        <a-form-item label="匹配模式">
          <a-radio-group v-model:value="form.match_mode">
            <a-radio value="any">任一命中（错误码或关键词）</a-radio>
            <a-radio value="all">全部命中（需同时满足）</a-radio>
          </a-radio-group>
        </a-form-item>
        <a-form-item label="生效平台（不选=全部）">
          <a-checkbox-group v-model:value="form.platforms" :options="PLATFORMS" />
        </a-form-item>
        <div class="two">
          <a-form-item>
            <a-checkbox v-model:checked="form.passthrough_code">透传原始状态码</a-checkbox>
            <a-input-number v-if="!form.passthrough_code" v-model:value="form.response_code" :min="100" :max="599" placeholder="422" style="width: 100%; margin-top: 4px" />
          </a-form-item>
          <a-form-item>
            <a-checkbox v-model:checked="form.passthrough_body">透传原始响应体</a-checkbox>
            <a-input v-if="!form.passthrough_body" v-model:value="form.custom_message" placeholder="自定义错误消息" style="margin-top: 4px" />
          </a-form-item>
        </div>
        <a-form-item>
          <a-checkbox v-model:checked="form.skip_monitoring">命中后不计入渠道监控统计</a-checkbox>
        </a-form-item>
        <a-form-item><a-checkbox v-model:checked="form.enabled">规则启用</a-checkbox></a-form-item>
        <div class="foot">
          <a-button @click="showForm = false">取消</a-button>
          <a-button type="primary" :loading="saving" @click="submit">{{ editingId ? '保存' : '创建' }}</a-button>
        </div>
      </a-form>
    </a-modal>
  </a-modal>
</template>

<style scoped>
.head { display: flex; align-items: center; justify-content: space-between; gap: 10px; margin-bottom: 10px; }
.dim { color: var(--text2); font-size: 12px; }
.empty { color: var(--text2); font-size: 13px; padding: 16px 0; text-align: center; }
.rule { padding: 8px 10px; border: 1px solid var(--hr); border-radius: 8px; margin-bottom: 8px; }
.rrow1 { display: flex; align-items: center; gap: 8px; }
.rrow2 { display: flex; align-items: center; gap: 4px; flex-wrap: wrap; margin-top: 6px; }
.rrow3 { margin-top: 4px; }
.prio { display: inline-flex; align-items: center; justify-content: center; width: 20px; height: 20px; border-radius: 4px; background: var(--card); font-size: 11px; }
.two { display: grid; grid-template-columns: 1fr 1fr; gap: 0 12px; }
.foot { display: flex; justify-content: flex-end; gap: 8px; }
.flex1 { flex: 1; }
</style>
