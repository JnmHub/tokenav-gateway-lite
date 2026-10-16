<script setup>
/* 批量编辑弹窗——旧实现契约：frontend/src/components/account/BulkEditAccountModal.vue
 * 提交接口：POST /admin/accounts/bulk-update
 *   body: { account_ids: [...], ...updates }（updates 仅含打开了「修改」开关的字段）
 *   后端 BulkUpdateAccountsRequest：proxy_id/concurrency/priority/rate_multiplier/
 *   load_factor/status/schedulable/group_ids/credentials{}/extra{}/confirm_mixed_channel_risk
 * 字段 → 提交位置（全部对照旧 buildUpdatePayload 核实）：
 *   base_url / model_mapping / custom_error_codes(_enabled) / intercept_warmup_requests
 *     / compact_model_mapping            → credentials
 *   openai_passthrough / *_websockets_v2_mode(_enabled) / codex_cli_only(_allowed_clients)
 *     / codex_fingerprint_mode / codex_image_generation_* / openai_compact_mode
 *     / openai_long_context_billing_enabled / base_rpm / rpm_strategy / rpm_sticky_buffer
 *     / keyword_policy_mode / user_msg_queue_mode / openai_responses_flatten_namespaces
 *     / user_msg_queue_enabled           → extra
 * 混合平台预检（旧 canPreCheck）：开了分组 + 选了组 + 选中账号仅单一 anthropic/antigravity
 *   平台时先 POST /admin/accounts/check-mixed-channel 预检；多平台混合跳过预检由后端
 *   409 mixed_channel_warning 兜底，确认后带 confirm_mixed_channel_risk: true 重发。
 * 批量更新是 JSONB 增量合并：清除语义必须显式发 0/空串/null（对照旧注释核实）。 */
import { ref, computed, watch } from 'vue'
import { request, isCancellation } from '../../api'
import { useDialogRequestLifetime } from '../../composables/useDialogRequestLifetime'
import { useDiscardGuard } from '../../core/windowContext'
import W12Dialog from '../design/W12Dialog.vue'
import BulkHeaderFields from './BulkHeaderFields.vue'
import { buildBulkUpdates } from './bulkUpdates.js'
import { isHdrEligible } from './platformPresets.js'
import { message } from 'ant-design-vue'

const open = defineModel('open', { type: Boolean, default: false })
const props = defineProps({
  /** 选中的行对象数组（含 id/platform/type），由父组件传入 */
  rows: { type: Array, default: () => [] },
})
const emit = defineEmits(['updated'])

const saving = ref(false)
const mixedMsg = ref('')
const mixedConfirming = ref(false)
const pendingUpdates = ref(null)

// ===== 平台判定（对照旧 targetSelectedPlatforms / allOpenAI* 计算属性） =====
const platforms = computed(() => [...new Set(props.rows.map((r) => r.platform))])
const types = computed(() => [...new Set(props.rows.map((r) => r.type))])
const isMixedPlatform = computed(() => platforms.value.length > 1)
const onlyPlatform = (p) => platforms.value.length === 1 && platforms.value[0] === p
const allOpenAICapable = computed(() =>
  onlyPlatform('openai') && types.value.length > 0 && types.value.every((t) => ['oauth', 'setup-token', 'apikey'].includes(t))
)
const allOpenAIOAuth = computed(() =>
  onlyPlatform('openai') && types.value.length > 0 && types.value.every((t) => t === 'oauth' || t === 'setup-token')
)
const allOpenAIOAuthOnly = computed(() =>
  onlyPlatform('openai') && types.value.length > 0 && types.value.every((t) => t === 'oauth')
)
const allOpenAIAPIKey = computed(() =>
  onlyPlatform('openai') && types.value.length > 0 && types.value.every((t) => t === 'apikey')
)
const allAnthropicOAuth = computed(() =>
  onlyPlatform('anthropic') && types.value.length > 0 && types.value.every((t) => t === 'oauth' || t === 'setup-token')
)

// ===== 分组 / 代理选项（GET /admin/groups/all、GET /admin/proxies/all 裸数组） =====
const groupOptions = ref([])
const proxyOptions = ref([])
const loadOptions = async () => {
  const turn = dialog.capture()
  try {
    const [gs, ps] = await Promise.all([
      request('/admin/groups/all', { ...turn, method: 'GET' }),
      request('/admin/proxies/all', { ...turn, method: 'GET' }),
    ])
    turn.assertCurrent()
    groupOptions.value = gs || []
    proxyOptions.value = ps || []
  } catch (e) {
    if (turn.isCurrent() && !isCancellation(e)) message.warning(`分组/代理选项加载失败：${e.message}`)
  }
}

// ===== 字段「是否修改」开关 + 值（与旧 enableX 开关一一对应） =====
const en = ref({})
const v = ref({})
const resetForm = () => {
  en.value = {}
  v.value = {
    status: 'active',
    group_ids: [],
    proxy_id: null,
    concurrency: 1,
    load_factor: null,
    priority: 1,
    rate_multiplier: 1,
    base_url: '',
    model_mode: 'whitelist',
    model_whitelist: [],
    model_mappings: [],
    error_codes: [],
    custom_code: null,
    hdr_on: false,
    hdr_rows: [],
    intercept_warmup: false,
    keyword_policy_mode: 'inherit',
    umq_mode: '',
    openai_passthrough: false,
    openai_flatten: false,
    long_context_billing: false,
    oauth_ws_mode: 'off',
    apikey_ws_mode: 'off',
    codex_cli_only: false,
    codex_cli_claude_code: false,
    codex_fingerprint: 'off',
    codex_image_tool: 'inherit',
    compact_mode: 'auto',
    compact_mappings: [],
    rpm_enabled: false,
    base_rpm: null,
    rpm_strategy: 'tiered',
    rpm_sticky_buffer: null,
  }
}
const dialog = useDialogRequestLifetime(() => open.value, () => {
  en.value = {}; v.value = {}; groupOptions.value = []; proxyOptions.value = []
  mixedMsg.value = ''; mixedConfirming.value = false; pendingUpdates.value = null; saving.value = false
})
const canClose = useDiscardGuard(() => open.value && Object.values(en.value).some(Boolean), () => open.value && saving.value)
const close = async () => { if (await canClose()) open.value = false }

const E = (k) => !!en.value[k]
const toggle = (k, checked) => { en.value = { ...en.value, [k]: checked } }

const statusOptions = [
  { value: 'active', label: 'active（启用）' },
  { value: 'inactive', label: 'inactive（停用）' },
]
const proxySelectOptions = computed(() => [
  { value: 0, label: '直连（清除代理）' },
  ...proxyOptions.value.map((p) => ({ value: p.id, label: `${p.name} (${p.protocol}://${p.host}:${p.port})` })),
])
const groupSelectOptions = computed(() => groupOptions.value.map((g) => ({ value: g.id, label: `${g.name} (#${g.id})` })))
const WS_MODES = [
  { value: 'off', label: '关闭' },
  { value: 'ctx_pool', label: '上下文池' },
  { value: 'passthrough', label: '透传' },
]
const ERROR_CODE_PRESETS = [
  { value: 401, label: '401 Unauthorized' },
  { value: 403, label: '403 Forbidden' },
  { value: 429, label: '429 Rate Limit' },
  { value: 500, label: '500 Server Error' },
  { value: 502, label: '502 Bad Gateway' },
  { value: 503, label: '503 Unavailable' },
  { value: 529, label: '529 Overloaded' },
]
const KEYWORD_MODES = [
  { value: 'inherit', label: '跟随全局' },
  { value: 'protect', label: '拦截（protect）' },
  { value: 'allow', label: '放行（allow）' },
]
const UMQ_MODES = [
  { value: '', label: '清除覆盖（跟随全局）' },
  { value: 'throttle', label: '节流' },
  { value: 'serialize', label: '串行' },
]
const FINGERPRINT_MODES = [
  { value: 'off', label: '关闭' },
  { value: 'device', label: '设备' },
  { value: 'session', label: '会话' },
  { value: 'full', label: '完整' },
]
const CODEX_IMAGE_MODES = [
  { value: 'inherit', label: '继承' },
  { value: 'enabled', label: '启用桥接' },
  { value: 'disabled', label: '禁用桥接' },
  { value: 'block', label: '拦截工具' },
]
const COMPACT_MODES = [
  { value: 'auto', label: '自动' },
  { value: 'force_on', label: '强制开启' },
  { value: 'force_off', label: '强制关闭' },
]
const RPM_STRATEGIES = [
  { value: 'tiered', label: '分层（tiered）' },
  { value: 'sticky_exempt', label: '粘性豁免（sticky_exempt）' },
]

watch(open, (o) => {
  if (o) {
    resetForm()
    if (!groupOptions.value.length) loadOptions()
  }
}, { flush: 'sync' })

// ===== 组装提交体（逐字段对照旧 buildUpdatePayload） =====
const headerEligible = computed(() => props.rows.length > 0 && props.rows.every(row => isHdrEligible(row.platform, row.type)) && (onlyPlatform('grok') || types.value.every(type => type === 'apikey')))
const buildUpdates = () => buildBulkUpdates(en.value, v.value, { allOpenAIOAuthOnly: allOpenAIOAuthOnly.value, headerEligible: headerEligible.value })

// ===== 混合渠道预检（旧 canPreCheck + checkMixedChannelRisk） =====
const canPreCheck = () =>
  E('group_ids') && (v.value.group_ids || []).length > 0 &&
  platforms.value.length === 1 && ['antigravity', 'anthropic'].includes(platforms.value[0])

const doSubmit = async (updates, turn = dialog.capture()) => {
  if (!turn.isCurrent()) return
  saving.value = true
  try {
    const body = { account_ids: props.rows.map((r) => r.id), ...updates }
    if (mixedConfirming.value) body.confirm_mixed_channel_risk = true
    const res2 = await request('/admin/accounts/bulk-update', { ...turn, method: 'POST', body })
    turn.assertCurrent()
    const ok = res2?.success ?? 0
    const fail = res2?.failed ?? 0
    if (ok > 0 && fail === 0) message.success(`批量编辑完成：成功 ${ok} 个`)
    else if (ok > 0) message.warning(`部分成功：成功 ${ok}，失败 ${fail}`)
    else { message.error('批量编辑失败：0 个成功'); return }
    emit('updated')
    open.value = false
  } catch (e) {
    if (!turn.isCurrent() || isCancellation(e)) return
    if (e.status === 409 && e.payload?.error === 'mixed_channel_warning') {
      pendingUpdates.value = structuredClone(updates)
      mixedMsg.value = e.payload.message || '所选分组存在混合渠道风险'; return
    }
    message.error(`批量编辑失败：${e.message}`)
  } finally {
    if (turn.isCurrent()) { saving.value = false; mixedConfirming.value = false }
  }
}

const submit = async () => {
  if (saving.value || !open.value) return
  const turn = dialog.capture()
  if (!props.rows.length) { message.error('未选择账号'); return }
  let updates
  try { updates = buildUpdates() } catch (e) { message.error(e.message); return }
  if (!updates) { message.error('请至少打开一个「修改」开关'); return }
  if (E('base_url') && v.value.base_url.trim()) {
    try {
      const u = new URL(v.value.base_url.trim())
      if (!['http:', 'https:'].includes(u.protocol) || u.username || u.password || u.search || u.hash) throw new Error('bad')
    } catch {
      message.error('Base URL 无效（需 http/https，不含用户信息/查询串）')
      return
    }
  }
  if (canPreCheck() && !mixedConfirming.value) {
    saving.value = true
    try {
      const res = await request('/admin/accounts/check-mixed-channel', {
        ...turn, method: 'POST',
        body: { platform: platforms.value[0], group_ids: (v.value.group_ids || []).map(Number) },
      })
      turn.assertCurrent()
      if (res?.has_risk) {
        pendingUpdates.value = structuredClone(updates)
        mixedMsg.value = res.message || '所选分组存在混合渠道风险'
        return // 弹确认框，用户确认后 mixedConfirming=true 再提交
      }
    } catch (e) {
      if (turn.isCurrent() && !isCancellation(e)) message.error(`预检失败：${e.message}`)
      return
    } finally { if (turn.isCurrent()) saving.value = false }
  }
  if (turn.isCurrent()) await doSubmit(updates, turn)
}

const confirmMixed = async () => {
  if (saving.value || !open.value) return
  mixedConfirming.value = true
  mixedMsg.value = ''
  // 风险确认只重发触发确认时的提交体，避免确认期间的编辑绕过校验。
  const updates = pendingUpdates.value
  if (updates) await doSubmit(updates)
}
</script>

<template>
  <W12Dialog :open="open" :busy="saving" title="批量编辑账号" width="680px" :confirm-loading="saving" ok-text="提交" cancel-text="取消" @ok="submit" @cancel="close">
    <div class="binfo">将修改选中的 {{ rows.length }} 个账号；只提交打开了「修改」开关的字段，其余保持原值。</div>
    <a-alert v-if="isMixedPlatform" type="warning" show-icon class="bmixed">
      <template #message>所选账号包含多个平台（{{ platforms.join('、') }}），平台专属字段已隐藏，仅通用字段可用。</template>
    </a-alert>

    <!-- 通用字段 -->
    <div class="bsec">基础</div>
    <div class="brow">
      <a-checkbox :checked="E('status')" @change="(e) => toggle('status', e.target.checked)" />
      <span class="blab">状态</span>
      <a-select v-model:value="v.status" :disabled="!E('status')" :options="statusOptions" style="width: 200px" />
    </div>
    <div class="brow">
      <a-checkbox :checked="E('group_ids')" @change="(e) => toggle('group_ids', e.target.checked)" />
      <span class="blab">分组（整组替换）</span>
      <a-select
        v-model:value="v.group_ids" mode="multiple" :disabled="!E('group_ids')" :options="groupSelectOptions"
        :max-tag-count="3" placeholder="留空 = 全部未分组" style="flex: 1" show-search option-filter-prop="label"
      />
    </div>
    <div class="brow">
      <a-checkbox :checked="E('proxy_id')" @change="(e) => toggle('proxy_id', e.target.checked)" />
      <span class="blab">代理</span>
      <a-select v-model:value="v.proxy_id" :disabled="!E('proxy_id')" :options="proxySelectOptions" style="width: 240px" show-search option-filter-prop="label" />
    </div>
    <div class="bgrid4">
      <div class="brow">
        <a-checkbox :checked="E('concurrency')" @change="(e) => toggle('concurrency', e.target.checked)" />
        <span class="blab">并发上限</span>
        <a-input-number v-model:value="v.concurrency" :min="1" :disabled="!E('concurrency')" style="width: 100%" />
      </div>
      <div class="brow">
        <a-checkbox :checked="E('load_factor')" @change="(e) => toggle('load_factor', e.target.checked)" />
        <span class="blab">负载因子</span>
        <a-input-number v-model:value="v.load_factor" :min="1" :max="10000" placeholder="空=清除" :disabled="!E('load_factor')" style="width: 100%" />
      </div>
      <div class="brow">
        <a-checkbox :checked="E('priority')" @change="(e) => toggle('priority', e.target.checked)" />
        <span class="blab">优先级</span>
        <a-input-number v-model:value="v.priority" :disabled="!E('priority')" style="width: 100%" />
      </div>
      <div class="brow">
        <a-checkbox :checked="E('rate_multiplier')" @change="(e) => toggle('rate_multiplier', e.target.checked)" />
        <span class="blab">计费倍率</span>
        <a-input-number v-model:value="v.rate_multiplier" :min="0" :step="0.1" :disabled="!E('rate_multiplier')" style="width: 100%" />
      </div>
    </div>

    <div class="bsec">凭据</div>
    <div class="brow">
      <a-checkbox :checked="E('base_url')" @change="(e) => toggle('base_url', e.target.checked)" />
      <span class="blab">Base URL（apikey 账号）</span>
      <a-input v-model:value="v.base_url" :disabled="!E('base_url')" placeholder="https://（留空则不改）" style="flex: 1" />
    </div>
    <BulkHeaderFields v-if="headerEligible" v-model:modify="en.header_override" v-model:enabled="v.hdr_on" v-model:rows="v.hdr_rows" :platform="platforms[0]" />
    <div class="brow" style="align-items: flex-start">
      <a-checkbox :checked="E('model_restrict')" @change="(e) => toggle('model_restrict', e.target.checked)" />
      <span class="blab">模型限制</span>
      <div v-if="E('model_restrict')" style="flex: 1">
        <a-radio-group v-model:value="v.model_mode" size="small" style="margin-bottom: 8px">
          <a-radio-button value="whitelist">白名单</a-radio-button>
          <a-radio-button value="mapping">映射</a-radio-button>
        </a-radio-group>
        <a-select
          v-if="v.model_mode === 'whitelist'" v-model:value="v.model_whitelist" mode="tags" :token-separators="[',']"
          :max-tag-count="4" placeholder="留空 = 支持所有模型（提交空映射清除限制）" style="width: 100%"
        />
        <template v-else>
          <div v-for="(m, i) in v.model_mappings" :key="i" class="bmap">
            <a-input v-model:value="m.from" placeholder="请求模型" />
            <span class="bmap-arrow">→</span>
            <a-input v-model:value="m.to" placeholder="实际模型" />
            <a-button size="small" danger @click="v.model_mappings.splice(i, 1)">删</a-button>
          </div>
          <a-button size="small" block @click="v.model_mappings.push({ from: '', to: '' })">+ 添加映射（留空 = 支持所有模型）</a-button>
        </template>
      </div>
      <span v-else class="bhint">未开启</span>
    </div>
    <div class="brow">
      <a-checkbox :checked="E('error_codes')" @change="(e) => toggle('error_codes', e.target.checked)" />
      <span class="blab">自定义错误码</span>
      <a-select v-if="E('error_codes')" v-model:value="v.error_codes" mode="multiple" :options="ERROR_CODE_PRESETS" :max-tag-count="4" placeholder="选择错误码" style="flex: 1" />
      <span v-else class="bhint">未开启</span>
    </div>
    <div class="brow">
      <a-checkbox :checked="E('intercept_warmup')" @change="(e) => toggle('intercept_warmup', e.target.checked)" />
      <span class="blab">拦截预热请求</span>
      <a-switch v-model:checked="v.intercept_warmup" :disabled="!E('intercept_warmup')" size="small" />
    </div>
    <div class="brow">
      <a-checkbox :checked="E('keyword_policy_mode')" @change="(e) => toggle('keyword_policy_mode', e.target.checked)" />
      <span class="blab">关键词策略</span>
      <a-select v-model:value="v.keyword_policy_mode" :disabled="!E('keyword_policy_mode')" :options="KEYWORD_MODES" style="width: 200px" />
    </div>
    <div class="brow">
      <a-checkbox :checked="E('umq_mode')" @change="(e) => toggle('umq_mode', e.target.checked)" />
      <span class="blab">用户消息排队</span>
      <a-select v-model:value="v.umq_mode" :disabled="!E('umq_mode')" :options="UMQ_MODES" style="width: 200px" />
    </div>

    <!-- OpenAI 专属（仅所选全部为 openai 时显示，混合平台自动隐藏——旧版同逻辑） -->
    <template v-if="allOpenAICapable">
      <div class="bsec">OpenAI 专属</div>
      <div class="brow">
        <a-checkbox :checked="E('openai_passthrough')" @change="(e) => toggle('openai_passthrough', e.target.checked)" />
        <span class="blab">透传模式</span>
        <a-switch v-model:checked="v.openai_passthrough" :disabled="!E('openai_passthrough')" size="small" />
      </div>
      <div class="brow">
        <a-checkbox :checked="E('codex_image_tool')" @change="(e) => toggle('codex_image_tool', e.target.checked)" />
        <span class="blab">Codex 图像工具</span>
        <a-select v-model:value="v.codex_image_tool" :disabled="!E('codex_image_tool')" :options="CODEX_IMAGE_MODES" style="width: 200px" />
      </div>
      <div class="brow">
        <a-checkbox :checked="E('compact_mode')" @change="(e) => toggle('compact_mode', e.target.checked)" />
        <span class="blab">Compact 模式</span>
        <a-select v-model:value="v.compact_mode" :disabled="!E('compact_mode')" :options="COMPACT_MODES" style="width: 200px" />
      </div>
      <div class="brow">
        <a-checkbox :checked="E('compact_mappings')" @change="(e) => toggle('compact_mappings', e.target.checked)" />
        <span class="blab">Compact 模型映射</span>
        <div v-if="E('compact_mappings')" style="flex: 1">
          <div v-for="(m, i) in v.compact_mappings" :key="i" class="bmap">
            <a-input v-model:value="m.from" placeholder="请求模型" />
            <span class="bmap-arrow">→</span>
            <a-input v-model:value="m.to" placeholder="实际模型" />
            <a-button size="small" danger @click="v.compact_mappings.splice(i, 1)">删</a-button>
          </div>
          <a-button size="small" block @click="v.compact_mappings.push({ from: '', to: '' })">+ 添加映射</a-button>
        </div>
        <span v-else class="bhint">未开启</span>
      </div>
      <div class="brow">
        <a-checkbox :checked="E('long_context_billing')" @change="(e) => toggle('long_context_billing', e.target.checked)" />
        <span class="blab">长上下文计费</span>
        <a-switch v-model:checked="v.long_context_billing" :disabled="!E('long_context_billing')" size="small" />
      </div>
    </template>
    <template v-if="allOpenAIOAuth">
      <div class="bsec">OpenAI OAuth / Setup Token</div>
      <div class="brow">
        <a-checkbox :checked="E('codex_cli_only')" @change="(e) => toggle('codex_cli_only', e.target.checked)" />
        <span class="blab">仅 Codex CLI</span>
        <a-switch v-model:checked="v.codex_cli_only" :disabled="!E('codex_cli_only')" size="small" />
      </div>
      <div class="brow">
        <a-checkbox :checked="E('codex_cli_claude_code')" @change="(e) => toggle('codex_cli_claude_code', e.target.checked)" />
        <span class="blab">额外放行 Claude Code 客户端</span>
        <a-switch v-model:checked="v.codex_cli_claude_code" :disabled="!E('codex_cli_claude_code')" size="small" />
      </div>
      <div class="brow">
        <a-checkbox :checked="E('codex_fingerprint')" @change="(e) => toggle('codex_fingerprint', e.target.checked)" />
        <span class="blab">TLS 指纹模式</span>
        <a-select v-model:value="v.codex_fingerprint" :disabled="!E('codex_fingerprint')" :options="FINGERPRINT_MODES" style="width: 200px" />
      </div>
    </template>
    <template v-if="allOpenAIOAuthOnly">
      <div class="bsec">OpenAI OAuth 专属</div>
      <div class="brow">
        <a-checkbox :checked="E('openai_flatten')" @change="(e) => toggle('openai_flatten', e.target.checked)" />
        <span class="blab">Namespace 工具摊平</span>
        <a-switch v-model:checked="v.openai_flatten" :disabled="!E('openai_flatten')" size="small" />
      </div>
    </template>
    <template v-if="allOpenAIOAuth">
      <div class="brow">
        <a-checkbox :checked="E('oauth_ws_mode')" @change="(e) => toggle('oauth_ws_mode', e.target.checked)" />
        <span class="blab">OAuth WebSocket 模式</span>
        <a-select v-model:value="v.oauth_ws_mode" :disabled="!E('oauth_ws_mode')" :options="WS_MODES" style="width: 200px" />
      </div>
    </template>
    <template v-if="allOpenAIAPIKey">
      <div class="brow">
        <a-checkbox :checked="E('apikey_ws_mode')" @change="(e) => toggle('apikey_ws_mode', e.target.checked)" />
        <span class="blab">API Key WebSocket 模式</span>
        <a-select v-model:value="v.apikey_ws_mode" :disabled="!E('apikey_ws_mode')" :options="WS_MODES" style="width: 200px" />
      </div>
    </template>
    <template v-if="allAnthropicOAuth">
      <div class="bsec">Anthropic OAuth / Setup Token</div>
      <div class="brow">
        <a-checkbox :checked="E('rpm')" @change="(e) => toggle('rpm', e.target.checked)" />
        <span class="blab">RPM 限流</span>
        <template v-if="E('rpm')">
          <a-switch v-model:checked="v.rpm_enabled" size="small" style="margin-right: 8px" />
          <a-input-number v-model:value="v.base_rpm" :min="1" :max="10000" placeholder="Base RPM" :disabled="!v.rpm_enabled" style="width: 120px" />
          <a-select v-model:value="v.rpm_strategy" :options="RPM_STRATEGIES" :disabled="!v.rpm_enabled" style="width: 170px" />
          <a-input-number v-model:value="v.rpm_sticky_buffer" :min="1" placeholder="粘性缓冲" :disabled="!v.rpm_enabled" style="width: 110px" />
        </template>
        <span v-else class="bhint">未开启</span>
      </div>
    </template>

    <!-- 混合渠道风险确认（预检命中 or 后端 409 兜底，均走此确认） -->
    <W12Dialog :open="!!mixedMsg" :busy="saving" title="混合渠道风险确认" :z-index="1100" ok-text="仍要继续" cancel-text="取消" @ok="confirmMixed" @cancel="mixedMsg = ''">
      <p>{{ mixedMsg }}</p>
      <p class="bhint">确认后本次提交将携带 confirm_mixed_channel_risk 标记跳过该检查。</p>
    </W12Dialog>
  </W12Dialog>
</template>

<style scoped>
.binfo { font-size: 12px; color: var(--text2); margin-bottom: 10px; }
.bmixed { margin-bottom: 10px; }
.bsec { font-weight: 500; font-size: 13px; margin: 14px 0 6px; padding-top: 10px; border-top: 1px solid var(--hr); }
.brow { display: flex; align-items: center; gap: 8px; min-height: 36px; font-size: 13px; }
.brow .blab { width: 168px; flex: none; color: var(--text2); }
.brow .bhint { color: var(--text2); font-size: 12px; }
.bgrid4 { display: grid; grid-template-columns: 1fr 1fr; gap: 0 16px; }
.bmap { display: flex; align-items: center; gap: 6px; margin-bottom: 6px; }
.bmap-arrow { color: var(--text2); }
</style>
