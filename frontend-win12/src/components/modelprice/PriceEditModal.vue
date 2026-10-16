<script setup>
/* 模型价格 新增/编辑弹窗（Win12 版，对照旧 frontend/src/views/admin/ModelPricesView.vue 契约）
 * - 字段名与后端 upsertModelPriceRequest（channel_handler.go）一一对应
 * - 保存前按计费方式归一化（移植旧 utils/modelPriceBilling + savePrice 规则）：
 *   1) 非 token 计费：清空全部 Token 价、长文阈值清 0、两个能力开关置 false
 *   2) 未勾选 Priority：清空 4 个优先价字段
 *   3) 长文阈值为 0：清空 4 个 >272k 长上下文档价
 *   4) 归一化后至少要有一个价格字段非空
 * - payload 只含 model/provider/mode/billing_mode + 价格字段 + 能力开关 + disabled，
 *   不携带 source/is_override/currency 等回显字段（旧 payload 同构）
 * - disabled（停用标志，后端 upsertModelPriceRequest.Disabled bool）必须始终携带：
 *   后端是非指针 bool，不传即视为 false，编辑保存时会把已停用覆盖回启用（防丢）
 * - 边界：disabled=true 时允许全部价格为空（后端 MODEL_PRICE_EMPTY 校验豁免），
 *   因此「至少一价」校验仅在未停用时生效 */
import { reactive, ref, watch, computed } from 'vue'
import { message } from 'ant-design-vue'
import { request } from '../../api'

const props = defineProps({
  open: { type: Boolean, default: false },
  price: { type: Object, default: null },
})
const emit = defineEmits(['update:open', 'saved'])

const BILLING_MODES = [
  { value: 'token', label: 'Token（按 token 计费）' },
  { value: 'image', label: '按图片' },
  { value: 'video', label: '按视频秒数' },
  { value: 'per_request', label: '按请求次数' },
]

// 价格字段清单（与旧 utils/modelPriceBilling 完全一致）
const TOKEN_PRICE_FIELDS = [
  'input_cost_per_1m_tokens', 'input_cost_per_1m_tokens_priority',
  'output_cost_per_1m_tokens', 'output_cost_per_1m_tokens_priority',
  'cache_creation_cost_per_1m_tokens', 'cache_creation_cost_per_1m_tokens_priority',
  'cache_read_cost_per_1m_tokens', 'cache_read_cost_per_1m_tokens_priority',
  'input_cost_per_1m_tokens_above_272k', 'output_cost_per_1m_tokens_above_272k',
  'cache_creation_cost_per_1m_tokens_above_272k', 'cache_read_cost_per_1m_tokens_above_272k',
]
const PRIORITY_FIELDS = [
  'input_cost_per_1m_tokens_priority', 'output_cost_per_1m_tokens_priority',
  'cache_creation_cost_per_1m_tokens_priority', 'cache_read_cost_per_1m_tokens_priority',
]
const ABOVE_FIELDS = [
  'input_cost_per_1m_tokens_above_272k', 'output_cost_per_1m_tokens_above_272k',
  'cache_creation_cost_per_1m_tokens_above_272k', 'cache_read_cost_per_1m_tokens_above_272k',
]
const IMAGE_PRICE_FIELDS = ['output_cost_per_image', 'output_cost_per_image_token', 'input_cost_per_image_token']
const VIDEO_PRICE_FIELDS = ['video_price_per_second_480p', 'video_price_per_second_720p', 'video_price_per_second_1080p']

const saving = ref(false)
const form = reactive(blankForm())

function blankForm() {
  return {
    model: '', provider: '', mode: 'chat', billing_mode: 'token',
    input_cost_per_1m_tokens: null, input_cost_per_1m_tokens_priority: null,
    output_cost_per_1m_tokens: null, output_cost_per_1m_tokens_priority: null,
    cache_creation_cost_per_1m_tokens: null, cache_creation_cost_per_1m_tokens_priority: null,
    cache_read_cost_per_1m_tokens: null, cache_read_cost_per_1m_tokens_priority: null,
    input_cost_per_1m_tokens_above_272k: null, output_cost_per_1m_tokens_above_272k: null,
    cache_creation_cost_per_1m_tokens_above_272k: null, cache_read_cost_per_1m_tokens_above_272k: null,
    long_context_input_token_threshold: null,
    output_cost_per_image: null, output_cost_per_image_token: null, input_cost_per_image_token: null,
    per_request_price: null,
    video_price_per_second_480p: null, video_price_per_second_720p: null, video_price_per_second_1080p: null,
    supports_prompt_caching: false, supports_service_tier: false,
    disabled: false,
  }
}

// 空串/NaN 归一为 null，0 保留（0 是合法价格）
function orNull(v) {
  if (v === null || v === undefined || v === '') return null
  const n = Number(v)
  return Number.isNaN(n) ? null : n
}

// 回显：与旧 fillForm 相同的兜底（provider 缺省 custom、mode 缺省 chat）
function fillForm(p) {
  Object.assign(form, blankForm())
  if (!p) return
  form.model = p.model
  form.provider = p.provider || 'custom'
  form.mode = p.mode || 'chat'
  form.billing_mode = ['image', 'video', 'per_request'].includes(p.billing_mode) ? p.billing_mode : 'token'
  for (const k of TOKEN_PRICE_FIELDS) form[k] = p[k] || null
  form.long_context_input_token_threshold = p.long_context_input_token_threshold || null
  for (const k of IMAGE_PRICE_FIELDS) form[k] = p[k] ?? null
  form.per_request_price = p.per_request_price || null
  for (const k of VIDEO_PRICE_FIELDS) form[k] = p[k] || null
  form.supports_prompt_caching = !!p.supports_prompt_caching
  form.supports_service_tier = !!p.supports_service_tier
  // 停用标志回显（列表行 modelPriceCatalogResponse.disabled）
  form.disabled = !!p.disabled
}

watch(() => props.open, (v) => { if (v) fillForm(props.price) })

const close = () => emit('update:open', false)

// 开关 UI 语义是「启用」：开 = 启用（disabled=false），关 = 停用（disabled=true）
const enabled = computed({
  get: () => !form.disabled,
  set: (v) => { form.disabled = !v },
})

// 构造 PUT payload：按计费方式剔除无关字段（语义等同旧 normalizeModelPricePayloadForBillingMode）
function buildPayload() {
  const billing = ['image', 'video', 'per_request'].includes(form.billing_mode) ? form.billing_mode : 'token'
  const isToken = billing === 'token'
  const p = {
    model: form.model.trim(),
    provider: (form.provider || '').trim(),
    mode: (form.mode || '').trim(),
    billing_mode: billing,
    // disabled 始终携带：后端是非指针 bool，漏传会被当成 false，把已停用覆盖回启用
    disabled: !!form.disabled,
  }
  for (const k of TOKEN_PRICE_FIELDS) p[k] = isToken ? orNull(form[k]) : null
  p.long_context_input_token_threshold = isToken ? (orNull(form.long_context_input_token_threshold) || 0) : 0
  p.supports_prompt_caching = isToken && !!form.supports_prompt_caching
  p.supports_service_tier = isToken && !!form.supports_service_tier
  // Priority 未勾选 → 4 个优先价清空提交（后端按 null 覆盖）
  if (!p.supports_service_tier) for (const k of PRIORITY_FIELDS) p[k] = null
  // 长文阈值为 0 → 4 个 >272k 档价清空提交
  if (!p.long_context_input_token_threshold) {
    p.long_context_input_token_threshold = 0
    for (const k of ABOVE_FIELDS) p[k] = null
  }
  for (const k of IMAGE_PRICE_FIELDS) p[k] = billing === 'image' ? orNull(form[k]) : null
  for (const k of VIDEO_PRICE_FIELDS) p[k] = billing === 'video' ? orNull(form[k]) : null
  p.per_request_price = billing === 'per_request' ? orNull(form.per_request_price) : null
  return p
}

const save = async () => {
  if (!form.model.trim()) { message.error('请填写模型名称'); return }
  const payload = buildPayload()
  // 归一化后至少一价校验（字段集与旧 savePrice 一致）；
  // 停用行豁免——后端在 disabled=true 时允许全部价格为空（仅作停用标记保存）
  const priceKeys = [...TOKEN_PRICE_FIELDS, ...IMAGE_PRICE_FIELDS, ...VIDEO_PRICE_FIELDS, 'per_request_price']
  if (!payload.disabled && priceKeys.every((k) => payload[k] === null)) { message.error('请至少填写一个价格字段'); return }
  saving.value = true
  try {
    await request('/admin/model-prices', { method: 'PUT', body: payload })
    message.success('模型价格已保存')
    close()
    emit('saved')
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
    :title="price ? `编辑模型价格：${price.model}` : '新增覆盖价'"
    :confirm-loading="saving"
    ok-text="保存" cancel-text="取消" width="640px"
    @cancel="close" @ok="save"
  >
    <a-form layout="vertical" size="small" class="pe-form">
      <!-- 启用/停用：开关语义 = 启用；停用后网关调度跳过该模型，价格字段可留空 -->
      <div class="status-row">
        <span class="st-label">启用状态</span>
        <span class="st-main">
          <a-switch v-model:checked="enabled" size="small" />
          <span class="st-text" :class="{ off: form.disabled }">{{ form.disabled ? '已停用' : '启用中' }}</span>
        </span>
      </div>
      <!-- 基本信息：编辑时模型名是主键不可改（旧版同规则） -->
      <div class="two">
        <a-form-item label="模型" required>
          <a-input v-model:value="form.model" :disabled="!!price" placeholder="mimo-v2.5-pro" />
        </a-form-item>
        <a-form-item label="供应商">
          <a-input v-model:value="form.provider" placeholder="xiaomi / openai / custom" />
        </a-form-item>
        <a-form-item label="模式（mode）">
          <a-input v-model:value="form.mode" placeholder="chat" />
        </a-form-item>
        <a-form-item label="计费方式">
          <a-select v-model:value="form.billing_mode" :options="BILLING_MODES" />
        </a-form-item>
      </div>

      <template v-if="form.billing_mode === 'token'">
        <div class="checks">
          <a-checkbox v-model:checked="form.supports_prompt_caching">支持缓存计费</a-checkbox>
          <a-checkbox v-model:checked="form.supports_service_tier">支持 Priority（优先处理）</a-checkbox>
        </div>

        <div class="sect">标准价格（USD / 1M Token）</div>
        <div class="two">
          <a-form-item label="输入"><a-input-number v-model:value="form.input_cost_per_1m_tokens" :min="0" :step="0.000001" placeholder="0" style="width: 100%" /></a-form-item>
          <a-form-item label="输出"><a-input-number v-model:value="form.output_cost_per_1m_tokens" :min="0" :step="0.000001" placeholder="0" style="width: 100%" /></a-form-item>
          <a-form-item label="缓存写入"><a-input-number v-model:value="form.cache_creation_cost_per_1m_tokens" :min="0" :step="0.000001" placeholder="0" style="width: 100%" /></a-form-item>
          <a-form-item label="缓存读取"><a-input-number v-model:value="form.cache_read_cost_per_1m_tokens" :min="0" :step="0.000001" placeholder="0" style="width: 100%" /></a-form-item>
        </div>

        <template v-if="form.supports_service_tier">
          <div class="sect pri">Priority（优先处理）价格</div>
          <div class="two">
            <a-form-item label="优先输入"><a-input-number v-model:value="form.input_cost_per_1m_tokens_priority" :min="0" :step="0.000001" placeholder="0" style="width: 100%" /></a-form-item>
            <a-form-item label="优先输出"><a-input-number v-model:value="form.output_cost_per_1m_tokens_priority" :min="0" :step="0.000001" placeholder="0" style="width: 100%" /></a-form-item>
            <a-form-item label="优先缓存读取"><a-input-number v-model:value="form.cache_read_cost_per_1m_tokens_priority" :min="0" :step="0.000001" placeholder="0" style="width: 100%" /></a-form-item>
            <a-form-item label="优先缓存写入"><a-input-number v-model:value="form.cache_creation_cost_per_1m_tokens_priority" :min="0" :step="0.000001" placeholder="0" style="width: 100%" /></a-form-item>
          </div>
        </template>

        <div class="sect lc">长上下文</div>
        <a-form-item label="长上下文阈值（输入 Token，超过后启用下方档价，0 = 不启用）">
          <a-input-number v-model:value="form.long_context_input_token_threshold" :min="0" :step="1000" :precision="0" placeholder="272000" style="width: 220px" />
        </a-form-item>
        <div v-if="Number(form.long_context_input_token_threshold || 0) > 0" class="two">
          <a-form-item label="长上下文输入"><a-input-number v-model:value="form.input_cost_per_1m_tokens_above_272k" :min="0" :step="0.000001" placeholder="0" style="width: 100%" /></a-form-item>
          <a-form-item label="长上下文输出"><a-input-number v-model:value="form.output_cost_per_1m_tokens_above_272k" :min="0" :step="0.000001" placeholder="0" style="width: 100%" /></a-form-item>
          <a-form-item label="长上下文缓存读取"><a-input-number v-model:value="form.cache_read_cost_per_1m_tokens_above_272k" :min="0" :step="0.000001" placeholder="0" style="width: 100%" /></a-form-item>
          <a-form-item label="长上下文缓存写入"><a-input-number v-model:value="form.cache_creation_cost_per_1m_tokens_above_272k" :min="0" :step="0.000001" placeholder="0" style="width: 100%" /></a-form-item>
          <p v-if="form.supports_service_tier" class="hint">Priority 暂不支持长上下文档位，因此不提供长上下文优先价。</p>
        </div>
      </template>

      <!-- 非 Token 计费：仅展示对应计费方式的字段，保存时其余自动剔除 -->
      <template v-else-if="form.billing_mode === 'image'">
        <div class="sect">图片价格</div>
        <div class="two">
          <a-form-item label="每张（USD / 张）"><a-input-number v-model:value="form.output_cost_per_image" :min="0" :step="0.000001" placeholder="0" style="width: 100%" /></a-form-item>
          <a-form-item label="图片输入 Token 单价"><a-input-number v-model:value="form.input_cost_per_image_token" :min="0" :step="0.000001" placeholder="0" style="width: 100%" /></a-form-item>
          <a-form-item label="图片输出 Token 单价"><a-input-number v-model:value="form.output_cost_per_image_token" :min="0" :step="0.000001" placeholder="0" style="width: 100%" /></a-form-item>
        </div>
      </template>
      <template v-else-if="form.billing_mode === 'video'">
        <div class="sect">视频价格（USD / 秒）</div>
        <div class="two">
          <a-form-item label="480p 每秒"><a-input-number v-model:value="form.video_price_per_second_480p" :min="0" :step="0.000001" placeholder="0" style="width: 100%" /></a-form-item>
          <a-form-item label="720p 每秒"><a-input-number v-model:value="form.video_price_per_second_720p" :min="0" :step="0.000001" placeholder="0" style="width: 100%" /></a-form-item>
          <a-form-item label="1080p 每秒"><a-input-number v-model:value="form.video_price_per_second_1080p" :min="0" :step="0.000001" placeholder="0" style="width: 100%" /></a-form-item>
        </div>
      </template>
      <template v-else>
        <div class="sect">按次价格</div>
        <a-form-item label="按次单价（USD / 次）">
          <a-input-number v-model:value="form.per_request_price" :min="0" :step="0.000001" placeholder="0" style="width: 220px" />
        </a-form-item>
      </template>
    </a-form>
  </a-modal>
</template>

<style scoped>
.pe-form { max-height: 62vh; overflow-y: auto; padding-right: 4px; }
/* 启用状态行：开关与同行文字垂直居中（Win12 表单规范） */
.status-row { display: flex; align-items: center; gap: 12px; padding: 4px 0 10px; border-bottom: 1px solid var(--hr); margin-bottom: 10px; }
.st-label { font-size: 12px; color: var(--text2); }
.st-main { display: flex; align-items: center; gap: 8px; }
.st-text { font-size: 12px; color: var(--text2); }
.st-text.off { opacity: 0.7; }
.two { display: grid; grid-template-columns: 1fr 1fr; gap: 0 12px; }
.sect { font-size: 13px; font-weight: 600; margin: 4px 0 8px; padding-top: 10px; border-top: 1px solid var(--hr); }
.sect.pri { color: #8b5cf6; }
.sect.lc { color: #0e7490; }
.checks { display: flex; gap: 20px; margin: 2px 0 8px; }
.hint { grid-column: 1 / -1; font-size: 12px; color: var(--text2); margin: 0; }
</style>
