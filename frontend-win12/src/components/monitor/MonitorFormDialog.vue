<script setup>
/* 新增/编辑监控表单（对照旧 MonitorFormDialog.vue，字段与后端
 * internal/handler/admin/channel_monitor_handler.go 的 create/update 请求结构逐一核实）
 * 字段清单：name / provider(按钮组，默认 anthropic) / api_mode(仅 openai) / endpoint(origin) /
 *   api_key / primary_model / extra_models(标签输入) / group_name / interval_seconds(15-3600) /
 *   jitter_seconds(≤ interval-15) / enabled
 * 高级请求配置（请求模板 + extra_headers + body_override 三模式）：
 *   template_id(下拉，按平台过滤) / extra_headers(key-value 行) /
 *   body_override_mode(off|merge|replace) / body_override(JSON 对象)；
 *   更新时 template_id 为空用 clear_template=true 显式清空（后端指针语义）。
 * 提交语义：编辑时空 api_key = 不修改（省略字段）；端点必须为 https origin（后端
 * validateEndpoint 拒绝 path/query，客户端先做同规则校验减少往返）；
 * merge/replace 模式必须带非空 body（后端 validateBodyModeParams，客户端前置校验）。
 */
import { ref, reactive, computed, watch } from 'vue'
import { message } from 'ant-design-vue'
import { RightOutlined } from '@ant-design/icons-vue'
import {
  createMonitor, updateMonitor, listMonitorTemplates,
} from './api'
import MonitorKeyPickerDialog from './MonitorKeyPickerDialog.vue'
import MonitorAdvancedRequestConfig from './MonitorAdvancedRequestConfig.vue'
import ModelTagInput from '../channel/ModelTagInput.vue'
import {
  PROVIDERS, PROVIDER_LABELS,
  API_MODE_OPTIONS, API_MODE_CHAT_COMPLETIONS, API_MODE_RESPONSES,
  MONITOR_MIN_INTERVAL, MONITOR_MAX_INTERVAL, DEFAULT_INTERVAL_SECONDS,
  DEFAULT_GROK_ENDPOINT, DEFAULT_GROK_MODEL,
} from './constants'

const props = defineProps({
  open: { type: Boolean, default: false },
  monitor: { type: Object, default: null }, // null = 新建
  systemInterval: { type: Number, default: DEFAULT_INTERVAL_SECONDS }, // /settings/public 下发
})
const emit = defineEmits(['close', 'saved'])

const saving = ref(false)
const keyPickerOpen = ref(false)

const form = reactive({
  name: '',
  provider: 'anthropic',
  api_mode: API_MODE_CHAT_COMPLETIONS,
  endpoint: '',
  api_key: '',
  primary_model: '',
  extra_models: [],
  group_name: '',
  interval_seconds: props.systemInterval || DEFAULT_INTERVAL_SECONDS,
  jitter_seconds: 0,
  enabled: true,
  // 高级请求配置快照（请求模板 + 自定义 Header/Body）
  template_id: null, // number | null
  extra_headers: {},
  body_override_mode: 'off',
  body_override: null, // object | null
})

/* ---------- 高级请求配置：折叠区状态 ---------- */
const advOpen = ref(false)
const advRef = ref(null)
// 已配置 = 挂了模板或写了自定义 Header/Body（折叠头右侧的状态文字）
const advConfigured = computed(() =>
  form.template_id != null
  || Object.keys(form.extra_headers || {}).length > 0
  || (form.body_override_mode || 'off') !== 'off',
)

// 快照按平台/协议归属，切平台或切协议都整体清空（对照旧 clearRequestSnapshot）
function clearRequestSnapshot() {
  form.template_id = null
  form.extra_headers = {}
  form.body_override_mode = 'off'
  form.body_override = null
}

/* 后端校验链：interval ∈ [15,3600]；jitter ≥ 0 且 interval - jitter ≥ 15（防抖后间隔过短打爆上游） */
const maxJitter = computed(() => Math.max(0, (Number(form.interval_seconds) || 0) - MONITOR_MIN_INTERVAL))

/* provider 切换：清空 Key（避免跨平台误用）；非 openai 强制 chat_completions；
 * 切到 grok 自动填默认端点/模型（对照旧 watch + 后端 normalizeMonitorPrimaryModel）；
 * 同时清空请求快照 —— 模板与 extra_headers/body 都有平台归属，跨平台不通用。 */
let suppressWatcher = false
watch(() => form.provider, () => {
  if (suppressWatcher) return
  form.api_key = ''
  if (form.provider !== 'openai') form.api_mode = API_MODE_CHAT_COMPLETIONS
  if (form.provider === 'grok') {
    form.endpoint = DEFAULT_GROK_ENDPOINT
    form.primary_model = DEFAULT_GROK_MODEL
  }
  clearRequestSnapshot()
})

/* openai 协议切换同样清空快照：模板与 body 覆盖都按 chat_completions/responses 区分 */
watch(() => form.api_mode, () => {
  if (suppressWatcher) return
  if (form.provider === 'openai') clearRequestSnapshot()
})

/* ---------- 请求模板（GET /admin/channel-monitor-templates，按平台/协议过滤） ---------- */
const templates = ref([])
const templatesLoading = ref(false)
async function loadTemplates() {
  templatesLoading.value = true
  try {
    const res = await listMonitorTemplates()
    templates.value = res?.items || []
  } catch {
    templates.value = [] // 模板拉取失败不阻塞表单，用户可以不选模板（对照旧实现）
  } finally {
    templatesLoading.value = false
  }
}

const templateSelectValue = computed(() => (form.template_id == null ? '' : String(form.template_id)))

const templateOptions = computed(() => {
  const items = templates.value.filter((t) => {
    if (t.provider !== form.provider) return false
    if (form.provider !== 'openai') return true
    // openai 模板再按协议过滤：responses 模板不进 chat_completions 表单（对照旧过滤）
    return (t.api_mode === 'responses' ? API_MODE_RESPONSES : API_MODE_CHAT_COMPLETIONS) === form.api_mode
  })
  return [
    { value: '', label: '不使用模板' },
    ...items.map((t) => ({ value: String(t.id), label: templateOptionLabel(t) })),
  ]
})

function templateOptionLabel(t) {
  if (t.provider !== 'openai') return t.name
  const modeLabel = API_MODE_OPTIONS.find((o) => o.value === (t.api_mode === 'responses' ? API_MODE_RESPONSES : API_MODE_CHAT_COMPLETIONS))?.label
  return modeLabel ? `${t.name} · ${modeLabel}` : t.name
}

/** 选中模板 = 把模板的 api_mode/headers/body 拷贝成本监控快照（不随模板自动同步） */
function onTemplateChange(raw) {
  if (raw === '' || raw == null) { form.template_id = null; return }
  const id = Number(raw)
  if (!Number.isFinite(id)) return
  form.template_id = id
  const tpl = templates.value.find((t) => t.id === id)
  if (!tpl) return
  suppressWatcher = true
  form.api_mode = tpl.api_mode === 'responses' ? API_MODE_RESPONSES : API_MODE_CHAT_COMPLETIONS
  form.template_id = id
  form.extra_headers = { ...(tpl.extra_headers || {}) }
  form.body_override_mode = tpl.body_override_mode || 'off'
  form.body_override = tpl.body_override ? { ...tpl.body_override } : null
  suppressWatcher = false
}

watch(() => props.open, (open) => {
  if (!open) return
  const m = props.monitor
  suppressWatcher = true
  if (m) {
    Object.assign(form, {
      name: m.name || '',
      provider: m.provider || 'anthropic',
      api_mode: m.api_mode === 'responses' ? 'responses' : API_MODE_CHAT_COMPLETIONS,
      endpoint: m.endpoint || '',
      api_key: '', // 后端只回掩码；留空 = 保留原 Key
      primary_model: m.primary_model || '',
      extra_models: [...(m.extra_models || [])],
      group_name: m.group_name || '',
      interval_seconds: m.interval_seconds || props.systemInterval || DEFAULT_INTERVAL_SECONDS,
      jitter_seconds: m.jitter_seconds || 0,
      enabled: m.enabled !== false,
      // 高级请求配置回填（响应带 template_id/extra_headers/body_override_mode/body_override）
      template_id: m.template_id ?? null,
      extra_headers: { ...(m.extra_headers || {}) },
      body_override_mode: m.body_override_mode || 'off',
      body_override: m.body_override ? { ...m.body_override } : null,
    })
  } else {
    Object.assign(form, {
      name: '', provider: 'anthropic', api_mode: API_MODE_CHAT_COMPLETIONS,
      endpoint: '', api_key: '', primary_model: '', extra_models: [], group_name: '',
      interval_seconds: props.systemInterval || DEFAULT_INTERVAL_SECONDS,
      jitter_seconds: 0, enabled: true,
      template_id: null, extra_headers: {}, body_override_mode: 'off', body_override: null,
    })
  }
  suppressWatcher = false
  // 编辑已配置过高级项时默认展开；新建默认收起（保持表单简约）
  advOpen.value = m ? advConfigured.value : false
  loadTemplates()
})

const useCurrentDomain = () => { form.endpoint = window.location.origin }

/* 端点客户端校验：镜像后端 validateEndpoint —— https、仅 origin（无 path/query/fragment） */
function validateEndpoint() {
  const raw = (form.endpoint || '').trim()
  if (!raw) return '请输入上游地址'
  let u
  try { u = new URL(raw) } catch { return '上游地址必须是合法的 https URL' }
  if (u.protocol !== 'https:') return '上游地址必须使用 https'
  if (u.pathname && u.pathname !== '/') return '上游地址只需填 origin（不带路径），例如 https://api.anthropic.com'
  if (u.search || u.hash) return '上游地址不允许携带查询参数或锚点'
  return ''
}

function validate() {
  // 高级请求配置先提交未失焦的编辑（Header 行 / Body JSON），有错误则阻断保存
  const advErr = advRef.value?.validate?.()
  if (advErr) return advErr
  if (!form.name.trim()) return '请输入监控名称'
  const epErr = validateEndpoint()
  if (epErr) return epErr
  if (!form.primary_model.trim()) return '请输入主模型'
  const itv = Number(form.interval_seconds)
  if (!Number.isFinite(itv) || itv < MONITOR_MIN_INTERVAL || itv > MONITOR_MAX_INTERVAL) {
    return `检测间隔需在 ${MONITOR_MIN_INTERVAL} - ${MONITOR_MAX_INTERVAL} 秒之间`
  }
  const jit = Number(form.jitter_seconds) || 0
  if (jit < 0 || itv - jit < MONITOR_MIN_INTERVAL) {
    return `抖动不能超过 检测间隔 - ${MONITOR_MIN_INTERVAL} 秒`
  }
  if (!props.monitor && !form.api_key.trim()) return '请输入 API Key（新建必填）'
  return ''
}

function buildPayload() {
  const body = {
    name: form.name.trim(),
    provider: form.provider,
    // responses 仅 openai 有意义；其余 provider 统一回 chat_completions（对照旧 buildPayload）
    api_mode: form.provider === 'openai' ? form.api_mode : API_MODE_CHAT_COMPLETIONS,
    endpoint: form.endpoint.trim(),
    primary_model: form.primary_model.trim(),
    extra_models: [...form.extra_models],
    group_name: form.group_name.trim(),
    enabled: form.enabled,
    interval_seconds: Number(form.interval_seconds),
    jitter_seconds: Number(form.jitter_seconds) || 0,
    // 高级请求配置：headers 总是下发（空对象 = 后端清空）；off 模式不带 body
    extra_headers: { ...form.extra_headers },
    body_override_mode: form.body_override_mode,
    body_override: form.body_override_mode === 'off' ? null : form.body_override,
  }
  if (props.monitor) {
    // 更新：template_id 为空时用 clear_template=true 显式清空（后端指针语义）
    if (form.template_id == null) body.clear_template = true
    else body.template_id = form.template_id
  } else {
    body.template_id = form.template_id // 新建可直接传 null
  }
  // 编辑时空串 = 不修改 Key（省略字段）；新建必填已在校验兜底
  if (form.api_key.trim()) body.api_key = form.api_key.trim()
  return body
}

async function save() {
  if (saving.value) return
  const bad = validate()
  if (bad) { message.error(bad); return }
  saving.value = true
  try {
    const body = buildPayload()
    if (props.monitor) await updateMonitor(props.monitor.id, body)
    else await createMonitor(body)
    message.success(props.monitor ? '监控更新成功' : '监控创建成功')
    emit('saved')
    emit('close')
  } catch (e) {
    message.error(`保存失败：${e.message}`)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <a-modal
    :open="open"
    :title="monitor ? '编辑渠道监控' : '新增渠道监控'"
    width="640px"
    :confirm-loading="saving"
    ok-text="保存"
    cancel-text="取消"
    wrap-class-name="monitor-form-modal"
    @ok="save"
    @cancel="emit('close')"
  >
    <div class="mform">
      <!-- 名称 -->
      <a-form-item label="名称" required>
        <a-input v-model:value="form.name" placeholder="输入监控名称" />
      </a-form-item>

      <!-- 平台按钮组（默认 anthropic；点击切换，样式延续 Win12 低饱和描边选中态） -->
      <a-form-item label="平台" required>
        <div class="prov-group">
          <button
            v-for="p in PROVIDERS" :key="p" type="button"
            class="prov-btn" :class="{ on: form.provider === p }"
            @click="form.provider = p"
          >
            {{ PROVIDER_LABELS[p] }}
          </button>
        </div>
      </a-form-item>

      <!-- OpenAI 协议二选一（仅 openai 显示；responses 仅 openai 支持） -->
      <a-form-item v-if="form.provider === 'openai'" label="OpenAI 协议">
        <div class="mode-group">
          <button
            v-for="o in API_MODE_OPTIONS" :key="o.value" type="button"
            class="mode-btn" :class="{ on: form.api_mode === o.value }"
            @click="form.api_mode = o.value"
          >
            <span class="mode-t">{{ o.label }}</span>
            <span class="mode-d">{{ o.hint }}</span>
          </button>
        </div>
      </a-form-item>

      <!-- 上游地址：仅 origin；快捷使用当前域名 -->
      <a-form-item label="上游地址" required>
        <div class="with-btn">
          <a-input v-model:value="form.endpoint" placeholder="https://api.anthropic.com" />
          <a-button @click="useCurrentDomain">使用当前服务</a-button>
        </div>
        <div class="hint">只填 origin（不含路径），例如 https://api.anthropic.com。</div>
      </a-form-item>

      <!-- API Key：新建必填；编辑留空 = 保留原值；支持从「我的 Key」选择 -->
      <a-form-item :label="monitor ? 'API Key（留空保留原值）' : 'API Key'" :required="!monitor">
        <div class="with-btn">
          <a-input-password v-model:value="form.api_key" :placeholder="monitor ? '留空表示不修改' : '请输入 API Key'" />
          <a-button @click="keyPickerOpen = true">使用我的 Key</a-button>
        </div>
        <div v-if="monitor && monitor.api_key_masked" class="hint">当前 Key：{{ monitor.api_key_masked }}</div>
      </a-form-item>

      <!-- 主模型 / 分组名（修复旧骨架缺分组名导致新建永远空分组的 bug） -->
      <div class="grid2">
        <a-form-item label="主模型" required>
          <a-input v-model:value="form.primary_model" placeholder="gpt-4o-mini" />
        </a-form-item>
        <a-form-item label="分组名称">
          <a-input v-model:value="form.group_name" placeholder="可选，用于在用户视图中聚合显示" />
        </a-form-item>
      </div>

      <!-- 附加模型标签输入（回车添加、粘贴批量、去重） -->
      <a-form-item label="附加模型">
        <ModelTagInput v-model:models="form.extra_models" :platform="form.provider" placeholder="回车添加附加模型" />
      </a-form-item>

      <!-- 检测间隔 / 随机抖动 -->
      <div class="grid2">
        <a-form-item label="检测间隔（秒）" required>
          <a-input-number
            v-model:value="form.interval_seconds"
            :min="MONITOR_MIN_INTERVAL" :max="MONITOR_MAX_INTERVAL" :precision="0" style="width: 100%"
          />
          <div class="hint">范围 15 - 3600 秒；系统默认 {{ systemInterval }} 秒。</div>
        </a-form-item>
        <a-form-item label="随机抖动（± 秒）">
          <a-input-number v-model:value="form.jitter_seconds" :min="0" :max="maxJitter" :precision="0" style="width: 100%" />
          <div class="hint">0 = 固定间隔；需满足 间隔 - 抖动 ≥ 15 秒。</div>
        </a-form-item>
      </div>

      <!-- 启用开关 -->
      <div class="frow">
        <div class="frow-main">
          <div class="frow-t">启用监控</div>
          <div class="frow-d">关闭后暂停定时检测，配置保留。</div>
        </div>
        <a-switch v-model:checked="form.enabled" />
      </div>

      <!-- 高级请求配置：请求模板 + 自定义 Header / Body 覆盖
           （弹窗内分组，1px 浅灰分割线；默认收起保持表单简约，已配置时自动展开） -->
      <div class="adv-divider"></div>
      <button type="button" class="adv-head" @click="advOpen = !advOpen">
        <RightOutlined class="adv-caret" :class="{ open: advOpen }" />
        <span class="adv-title">高级请求配置</span>
        <span class="adv-state">{{ advConfigured ? '已配置' : '未配置' }}</span>
      </button>
      <div v-show="advOpen" class="adv-body">
        <a-form-item label="请求模板">
          <a-select
            :value="templateSelectValue"
            :options="templateOptions"
            :loading="templatesLoading"
            placeholder="选择一个模板（按当前平台过滤）"
            allow-clear
            @change="onTemplateChange"
          />
          <div class="hint">选中模板会把它的请求头与请求体拷贝到本监控（快照），后续模板变动不自动同步。</div>
        </a-form-item>
        <MonitorAdvancedRequestConfig
          ref="advRef"
          :provider="form.provider"
          :api-mode="form.api_mode"
          :extra-headers="form.extra_headers"
          :body-override-mode="form.body_override_mode"
          :body-override="form.body_override"
          @update:extra-headers="form.extra_headers = $event"
          @update:body-override-mode="form.body_override_mode = $event"
          @update:body-override="form.body_override = $event"
        />
      </div>
    </div>

    <MonitorKeyPickerDialog
      :open="keyPickerOpen"
      :provider="form.provider"
      @close="keyPickerOpen = false"
      @pick="(secret) => { form.api_key = secret; keyPickerOpen = false }"
    />
  </a-modal>
</template>

<style scoped>
.mform :deep(.ant-form-item) { margin-bottom: 12px; }
.mform :deep(.ant-form-item-label) { padding-bottom: 2px; }
.grid2 { display: grid; grid-template-columns: 1fr 1fr; gap: 0 16px; }
.hint { font-size: 12px; color: var(--text2); line-height: 1.6; margin-top: 4px; }

/* 平台按钮组 */
.prov-group { display: flex; gap: 8px; flex-wrap: wrap; }
.prov-btn {
  min-width: 96px; height: 32px; padding: 0 16px; border-radius: 8px;
  border: 1px solid var(--hr); background: var(--card); color: var(--text2);
  font-size: 13px; cursor: pointer; transition: border-color 100ms, background 100ms, color 100ms;
}
.prov-btn:hover { background: var(--hover-b); color: inherit; }
.prov-btn.on {
  border-color: var(--theme-2); color: var(--theme-2);
  background: color-mix(in srgb, var(--theme-2) 12%, transparent);
}

/* 协议二选一卡片 */
.mode-group { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }
.mode-btn {
  text-align: left; border: 1px solid var(--hr); background: var(--card);
  border-radius: 8px; padding: 8px 10px; cursor: pointer; display: grid; gap: 3px; font-family: inherit;
}
.mode-btn:hover { border-color: var(--text2); }
.mode-btn.on { border-color: var(--theme-2); background: color-mix(in srgb, var(--theme-2) 12%, transparent); }
.mode-t { font-size: 12.5px; font-weight: 500; }
.mode-d { font-size: 11.5px; color: var(--text2); line-height: 1.5; }

/* 输入框 + 快捷按钮 */
.with-btn { display: flex; gap: 8px; }

/* 开关行 */
.frow {
  display: flex; align-items: center; justify-content: space-between; gap: 16px;
  padding: 8px 10px; border-radius: 8px; min-height: 40px; background: var(--card);
}
.frow:hover { filter: brightness(1.03); }
.frow-t { font-size: 13px; }
.frow-d { font-size: 12px; color: var(--text2); margin-top: 1px; }

/* 高级请求配置折叠区（Win12 表单规范：区块间 1px 浅灰分割线） */
.adv-divider { border-top: 1px solid var(--hr); margin: 16px 0 4px; }
.adv-head {
  display: flex; align-items: center; gap: 8px; width: 100%;
  padding: 7px 10px; border: none; border-radius: 8px; background: transparent;
  cursor: pointer; font-family: inherit; text-align: left;
  transition: background 100ms;
}
.adv-head:hover { background: var(--hover-b); }
.adv-caret { font-size: 10px; color: var(--text2); transition: transform 120ms; }
.adv-caret.open { transform: rotate(90deg); }
.adv-title { font-size: 13px; font-weight: 500; }
.adv-state { font-size: 12px; color: var(--text2); margin-left: auto; }
.adv-body { padding: 8px 10px 0; }

@media (max-width: 640px) {
  .grid2, .mode-group { grid-template-columns: 1fr; }
}
</style>

<style>
/* a-modal 挂 body 下，scoped 覆盖不到：限高滚动防长表单撑出屏幕 */
.monitor-form-modal .ant-modal-body { max-height: 68vh; overflow-y: auto; }
</style>
