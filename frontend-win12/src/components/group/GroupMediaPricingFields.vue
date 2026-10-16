<script setup>
/* 媒体与按次计价字段（二期 A：一期遗留小字段集中区）
 * 契约来源：旧 GroupsView.vue 1095-1347（image/video/webSearch）、2665-2713（grok voice/search）
 * 字段：allow_image_generation、image_rate_independent、image_rate_multiplier、
 *   image_price_1k/2k/4k、video_rate_independent、video_rate_multiplier、
 *   video_price_480p/720p/1080p、video_model_prices、web_search_price_per_call（openai）、
 *   search_price_per_1k（grok）、audio_realtime_price_per_min、
 *   audio_tts_price_per_million_chars、audio_stt_price_per_hour（grok voice）
 * 价格语义：留空 = 使用默认/目录价；填 0 = 免费；实际扣费叠加分组有效倍率。
 * 最终价预览照抄旧 buildImageFinalPricePreview / buildVideoFinalPricePreview /
 *   buildWebSearchFinalPricePreview / buildGrokSearchFinalPricePreview。
 */
import { computed } from 'vue'
import { GROK_VIDEO_TIERS, grokVideoPriceFamilyRows } from './pricing'

const props = defineProps({
  form: { type: Object, required: true },  // Groups.vue 的 editForm
})
const emit = defineEmits(['patch'])
const patch = (values) => emit('patch', values)

const showImagePricing = computed(() =>
  ['antigravity', 'gemini', 'openai', 'grok', 'composite'].includes(props.form.platform))
const isGrok = computed(() => props.form.platform === 'grok')

// 允许生图文案：grok 为「图片和视频」，其余为「生图」（对照旧 imagePricing.allow*）
const allowLabel = computed(() => (isGrok.value ? '允许当前分组生成图片和视频' : '允许当前分组生图'))

const imageTiers = computed(() => {
  const tiers = [
    { key: 'image_price_1k', label: '1K ($)', placeholder: '0.134' },
    { key: 'image_price_2k', label: '2K ($)', placeholder: '0.201' },
    { key: 'image_price_4k', label: '4K ($)', placeholder: '0.268' },
  ]
  return isGrok.value ? tiers.filter((t) => t.key !== 'image_price_4k') : tiers
})

// —— 最终价预览（照抄旧实现） ——
const normNum = (v, fallback) => {
  if (v === null || v === undefined || v === '') return fallback
  const n = Number(v)
  return Number.isFinite(n) ? n : fallback
}
const fmtPrice = (v) => {
  if (v === null || v === undefined || v === '') return '未配置'
  const n = Number(v)
  if (!Number.isFinite(n) || n < 0) return '未配置'
  return `$${n.toFixed(6).replace(/0+$/, '').replace(/\.$/, '')}`
}
const fmtVideoPrice = (v) => {
  if (v === null || v === undefined || v === '') return '使用模型目录价'
  const n = Number(v)
  if (!Number.isFinite(n) || n < 0) return '使用模型目录价'
  return `$${n.toFixed(6).replace(/0+$/, '').replace(/\.$/, '')}`
}
const imageMultiplier = computed(() =>
  props.form.image_rate_independent ? normNum(props.form.image_rate_multiplier, 1) : normNum(props.form.rate_multiplier, 1))
const imagePreview = computed(() => imageTiers.value.map((tier) => {
  const base = normNum(props.form[tier.key])
  return { label: tier.label, value: base > 0 ? fmtPrice(base * imageMultiplier.value) : '未配置' }
}))
const videoMultiplier = computed(() =>
  props.form.video_rate_independent ? normNum(props.form.video_rate_multiplier, 1) : normNum(props.form.rate_multiplier, 1))
const videoPreview = computed(() => ['video_price_480p', 'video_price_720p', 'video_price_1080p'].map((key) => {
  const raw = props.form[key]
  if (raw === null || raw === undefined || raw === '') return { label: key.replace('video_price_', ''), value: '使用模型目录价' }
  const n = Number(raw)
  return { label: key.replace('video_price_', ''), value: Number.isFinite(n) && n >= 0 ? fmtVideoPrice(n * videoMultiplier.value) : '使用模型目录价' }
}))
// 留空使用默认价：web 搜索 0.01/次、grok 搜索 10/千次（旧 DEFAULT_* 常量）
const webSearchPreview = computed(() =>
  fmtPrice(normNum(props.form.web_search_price_per_call, 0.01) * normNum(props.form.rate_multiplier, 1)))
const grokSearchPreview = computed(() =>
  fmtPrice(normNum(props.form.search_price_per_1k, 10) * normNum(props.form.rate_multiplier, 1)))


// 矩阵行 = 官方两个规范族 + 后端已存在的其他族（未来新增族不再被隐藏/覆盖丢失）
const videoFamilyRows = computed(() => grokVideoPriceFamilyRows(props.form.video_model_prices))
// 官方媒体默认价（对照 API.md「Grok 官方媒体默认价格」，用于运营快速判断是否要覆盖）
const VIDEO_FAMILY_DEFAULTS = [
  { family: 'grok-imagine-video', price: '$0.05 / 生成秒' },
  { family: 'grok-imagine-video-1.5', price: '$0.08 / 生成秒' },
]
const videoFamilyDefault = (family) =>
  VIDEO_FAMILY_DEFAULTS.find((item) => item.family === family)?.price || '按模型目录价'
const matrixValue = (family, tier) => props.form.video_model_prices?.[family]?.[tier] ?? null
const setMatrixValue = (family, tier, v) => {
  const matrix = { ...(props.form.video_model_prices || {}) }
  matrix[family] = { ...(matrix[family] || {}) }
  const n = v === null || v === '' ? null : Number(v)
  matrix[family][tier] = n !== null && Number.isFinite(n) ? n : null
  patch({ video_model_prices: matrix })
}
</script>

<template>
  <div class="mp">
    <!-- 图片生成计费：antigravity/gemini/openai/grok/composite -->
    <template v-if="showImagePricing">
      <div class="mp-t">图片生成计费</div>
      <div class="mp-d">配置图片生成能力和图片基础单价，留空则使用默认价格。</div>
      <div class="mp-switches">
        <div class="frow">
          <div class="frow-main">
            <div class="frow-t">{{ allowLabel }}</div>
            <div class="frow-d">关闭后本分组的图片生成请求将被拒绝。</div>
          </div>
          <a-switch :checked="form.allow_image_generation" size="small" @change="(v) => patch({ allow_image_generation: v })" />
        </div>
        <div class="frow">
          <div class="frow-main">
            <div class="frow-t">生图倍率独立</div>
            <div class="frow-d">开启后图片费用使用独立倍率，不叠加分组倍率。</div>
          </div>
          <a-switch :checked="form.image_rate_independent" size="small" @change="(v) => patch({ image_rate_independent: v })" />
        </div>
      </div>
      <div v-if="form.image_rate_independent" class="mp-grid one">
        <div>
          <label>生图独立倍率</label>
          <a-input-number :value="form.image_rate_multiplier" :min="0" :step="0.0001" style="width: 100%" placeholder="1" @change="(v) => patch({ image_rate_multiplier: v })" />
        </div>
      </div>
      <div class="mp-grid" :class="{ two: isGrok }">
        <div v-for="tier in imageTiers" :key="tier.key">
          <label>{{ tier.label }}</label>
          <a-input-number :value="form[tier.key]" :min="0" :step="0.001" style="width: 100%" :placeholder="tier.placeholder" @change="(v) => patch({ [tier.key]: v })" />
        </div>
      </div>
      <div class="mp-d">默认关闭独立倍率时，图片费用 = 图片价格 × 当前分组有效倍率；开启独立倍率后，图片费用 = 图片价格 × 生图独立倍率。</div>
      <div class="mp-preview">
        <div class="mp-pt">最终单张价格预览</div>
        <div class="mp-pv">
          <span v-for="item in imagePreview" :key="item.label">{{ item.label }}: {{ item.value }}</span>
        </div>
      </div>
    </template>

    <!-- Grok 视频生成按秒计费 -->
    <template v-if="isGrok">
      <div class="mp-t">视频生成计费</div>
      <div class="mp-d">按分辨率配置每秒基础单价；留空时使用对应 Grok 视频模型的目录价格。</div>
      <div class="mp-switches">
        <div class="frow">
          <div class="frow-main">
            <div class="frow-t">视频倍率独立</div>
            <div class="frow-d">开启后视频费用使用独立倍率，不叠加分组倍率。</div>
          </div>
          <a-switch :checked="form.video_rate_independent" size="small" @change="(v) => patch({ video_rate_independent: v })" />
        </div>
      </div>
      <div v-if="form.video_rate_independent" class="mp-grid one">
        <div>
          <label>视频独立倍率</label>
          <a-input-number :value="form.video_rate_multiplier" :min="0" :step="0.0001" style="width: 100%" placeholder="1" @change="(v) => patch({ video_rate_multiplier: v })" />
        </div>
      </div>
      <div class="mp-grid three">
        <div><label>480p ($ / s)</label><a-input-number :value="form.video_price_480p" :min="0" :step="0.001" style="width: 100%" placeholder="目录价" @change="(v) => patch({ video_price_480p: v })" /></div>
        <div><label>720p ($ / s)</label><a-input-number :value="form.video_price_720p" :min="0" :step="0.001" style="width: 100%" placeholder="目录价" @change="(v) => patch({ video_price_720p: v })" /></div>
        <div><label>1080p ($ / s)</label><a-input-number :value="form.video_price_1080p" :min="0" :step="0.001" style="width: 100%" placeholder="目录价" @change="(v) => patch({ video_price_1080p: v })" /></div>
      </div>
      <!-- 模型族 × 分辨率 每秒价格矩阵（含后端已有的其他模型族，避免未来新增族不可见） -->
      <div class="matrix">
        <div class="mx-row mx-head">
          <div>视频模型族</div>
          <div v-for="tier in GROK_VIDEO_TIERS" :key="tier">{{ tier }} ($ / s)</div>
        </div>
        <div v-for="family in videoFamilyRows" :key="family" class="mx-row">
          <div class="mx-family">
            <code>{{ family }}</code>
            <span class="mx-default" :title="`未填价时使用官方默认价：${videoFamilyDefault(family)}`">{{ videoFamilyDefault(family) }}</span>
          </div>
          <div v-for="tier in GROK_VIDEO_TIERS" :key="tier">
            <a-input-number :value="matrixValue(family, tier)" :min="0" :step="0.001" size="small" style="width: 100%" placeholder="目录价" @change="(v) => setMatrixValue(family, tier, v)" />
          </div>
        </div>
      </div>
      <div class="mp-d">留空表示使用该族默认价（见每行标注）；填 0 表示免费。</div>
      <div class="mp-d">基础 <code>grok-imagine-video</code> 不支持 1080p：这一档保持 0 表示「能力不支持」，而不是免费；1080p 请求会被上游拒绝。</div>
      <div class="mp-d">关闭独立倍率时，视频费用 = 每秒价格 × 视频时长 × 数量 × 当前分组有效倍率；开启后使用视频独立倍率。</div>
      <div class="mp-preview">
        <div class="mp-pt">最终每秒价格预览</div>
        <div class="mp-pv">
          <span v-for="item in videoPreview" :key="item.label">{{ item.label }}: {{ item.value }}</span>
        </div>
      </div>

      <!-- Grok Voice 语音计费 -->
      <div class="mp-t">Grok Voice 语音计费</div>
      <div class="mp-d">留空使用默认价格，填 0 表示免费；实际费用仍叠加当前分组有效倍率。</div>
      <div class="mp-grid three">
        <div><label>Realtime 实时语音（$/分钟）</label><a-input-number :value="form.audio_realtime_price_per_min" :min="0" :step="0.001" style="width: 100%" placeholder="0.10" @change="(v) => patch({ audio_realtime_price_per_min: v })" /></div>
        <div><label>TTS 文字转语音（$/百万字符）</label><a-input-number :value="form.audio_tts_price_per_million_chars" :min="0" :step="0.01" style="width: 100%" placeholder="15" @change="(v) => patch({ audio_tts_price_per_million_chars: v })" /></div>
        <div><label>STT 语音转文字（$/小时）</label><a-input-number :value="form.audio_stt_price_per_hour" :min="0" :step="0.001" style="width: 100%" placeholder="0.36" @change="(v) => patch({ audio_stt_price_per_hour: v })" /></div>
      </div>

      <!-- Grok 搜索计费 -->
      <div class="mp-t">Grok 搜索计费</div>
      <div class="mp-grid one">
        <div>
          <label>搜索价格（$/千次）</label>
          <a-input-number :value="form.search_price_per_1k" :min="0" :step="0.01" style="width: 100%" placeholder="10" @change="(v) => patch({ search_price_per_1k: v })" />
          <div class="mp-d">用于 Grok 原生搜索和对话内搜索附加费。留空使用默认价 10 美元/千次，填 0 表示免费；实际扣费会叠加分组倍率。</div>
        </div>
      </div>
      <div class="mp-preview">应用当前分组倍率后的每千次价格：{{ grokSearchPreview }}</div>
    </template>

    <!-- Codex 网页搜索计费（openai） -->
    <template v-if="form.platform === 'openai'">
      <div class="mp-t">Codex 网页搜索计费</div>
      <div class="mp-grid one">
        <div>
          <label>搜索单次价格（美元/次）</label>
          <a-input-number :value="form.web_search_price_per_call" :min="0" :step="0.001" style="width: 100%" placeholder="0.01" @change="(v) => patch({ web_search_price_per_call: v })" />
          <div class="mp-d">仅用于 Codex alpha/search 独立搜索。留空使用默认价 0.01 美元/次，填 0 表示免费；实际扣费会叠加分组倍率。</div>
        </div>
      </div>
      <div class="mp-preview">应用当前分组倍率后的单次价格：{{ webSearchPreview }}</div>
    </template>
  </div>
</template>

<style scoped>
.mp { display: grid; gap: 8px; }
.mp-t { font-size: 13px; margin-top: 6px; }
.mp-t:first-child { margin-top: 0; }
.mp-d { font-size: 12px; color: var(--text2); line-height: 1.6; }
.mp-switches { display: grid; gap: 8px; }
.mp-grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 8px; }
.mp-grid.two { grid-template-columns: repeat(2, 1fr); }
.mp-grid.one { grid-template-columns: 1fr; }
.mp-grid.three { grid-template-columns: repeat(3, 1fr); }
label { display: block; font-size: 11.5px; color: var(--text2); margin-bottom: 3px; }
.mp-preview { font-size: 12px; color: inherit; border-radius: 8px; background: var(--card); padding: 8px 10px; }
.mp-pt { font-size: 12px; font-weight: 500; margin-bottom: 4px; }
.mp-pv { display: flex; flex-wrap: wrap; gap: 4px 16px; color: var(--text2); }
.matrix { border: 1px solid var(--hr); border-radius: 8px; overflow: hidden; }
.mx-row { display: grid; grid-template-columns: minmax(120px, 1.4fr) repeat(3, 1fr); gap: 8px; align-items: center; padding: 6px 10px; }
.mx-row + .mx-row { border-top: 1px solid var(--hr); }
.mx-head { font-size: 12px; font-weight: 500; color: var(--text2); text-align: center; background: var(--card); }
.mx-family { display: grid; gap: 2px; min-width: 0; }
.mx-default { font-size: 11px; color: var(--text2); }
@media (max-width: 640px) {
  /* 窄屏：族名独占一行，三档价格各占一格，避免默认价标注把输入框挤没 */
  .mx-row { grid-template-columns: repeat(3, 1fr); row-gap: 6px; padding: 8px 10px; }
  .mx-family { grid-column: 1 / -1; }
  .mx-head div:first-child { grid-column: 1 / -1; }
}
.mx-head div:first-child { text-align: left; }
.mx-row code { font-size: 11.5px; color: var(--text2); }
.frow { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 8px 10px; border-radius: 8px; background: var(--card); min-height: 40px; }
.frow-main { min-width: 0; }
.frow-t { font-size: 13px; }
.frow-d { font-size: 12px; color: var(--text2); margin-top: 1px; }
@media (max-width: 640px) {
  .mp-grid { grid-template-columns: 1fr 1fr; }
}
</style>
