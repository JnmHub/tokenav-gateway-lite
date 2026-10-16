<script setup>
import { computed, ref, onBeforeUnmount, watch } from 'vue'
import { request } from '../../api/index.js'
import { createLatestRequest } from '../../api/latestRequest.js'
import { CONCRETE_PLATFORMS, perTokenToMTok } from './pricing.js'
import GroupPricingIntervals from './GroupPricingIntervals.vue'
import W12Button from '../design/W12Button.vue'
import W12Status from '../design/W12Status.vue'
const props = defineProps({ entry: { type: Object, required: true }, platform: { type: String, required: true } })
const emit = defineEmits(['update', 'remove'])
const composite = computed(() => props.platform === 'composite')
const modes = [{ value: 'token', label: 'Token（词元）' }, { value: 'per_request', label: '按次' }, { value: 'image', label: '图片' }, { value: 'video', label: '视频' }]
const platformOptions = CONCRETE_PLATFORMS.map(value => ({ value, label: value }))
const tokenFields = [
  { key: 'input_price', label: '输入价格' }, { key: 'output_price', label: '输出价格' },
  { key: 'cache_write_price', label: '缓存写入' }, { key: 'cache_read_price', label: '缓存读取' },
  { key: 'image_input_price', label: '图片输入' }, { key: 'image_output_price', label: '图片输出' },
]
const tiersOpen = ref(!!props.entry.intervals?.length)
const patch = values => emit('update', { ...props.entry, ...values })
const modeLabel = computed(() => modes.find(mode => mode.value === props.entry.billing_mode)?.label || props.entry.billing_mode)
const summary = computed(() => props.entry.models.slice(0, 3).join('、') || '未添加模型')
const defaultPricing = createLatestRequest()
onBeforeUnmount(() => defaultPricing.dispose())
watch(() => [props.platform, props.entry.platform, props.entry.billing_mode], () => defaultPricing.cancel())
const changeModels = async models => {
  const previous = props.entry.models
  defaultPricing.cancel()
  patch({ models })
  const added = models.find(model => !previous.includes(model))
  if (!added || tokenFields.some(field => props.entry[field.key] != null)) return
  const turn = defaultPricing.begin()
  try {
    const price = await request('/admin/channels/model-pricing', { method: 'GET', query: { model: added }, signal: turn.signal })
    // 候选回填不能覆盖管理员在查询期间输入的价格，也不能复活已经移除的模型。
    if (!turn.isCurrent() || !price.found || JSON.stringify(props.entry.models) !== JSON.stringify(models)
      || tokenFields.some(field => props.entry[field.key] != null)) return
    patch(Object.fromEntries(tokenFields.map(field => [field.key, perTokenToMTok(price[field.key])])) )
  } catch { /* 默认价查询失败保留用户输入，不影响手工定价。 */ }
}
const changeMode = mode => { defaultPricing.cancel(); patch({ billing_mode: mode, intervals: [] }) }
</script>

<template>
  <article class="pricing-entry">
    <header><span class="entry-summary" :title="summary">{{ summary }}</span><W12Status tone="info">{{ modeLabel }}</W12Status><W12Button variant="danger" compact @click="emit('remove')">删除</W12Button></header>
    <div class="entry-fields">
      <label class="models"><span>模型（回车添加，支持末尾 * 通配符）</span><a-select :value="entry.models" mode="tags" :token-separators="[',', ' ']" placeholder="例如 claude-*" @change="changeModels" /></label>
      <label v-if="composite"><span>实际平台</span><a-select :value="entry.platform" :options="platformOptions" placeholder="选择真实目标平台" @change="value => patch({ platform: value })" /></label>
      <label><span>计费模式</span><a-select :value="entry.billing_mode" :options="modes" @change="changeMode" /></label>
      <template v-if="entry.billing_mode === 'token'">
        <label v-for="field in tokenFields" :key="field.key"><span>{{ field.label }}（美元 / 百万 Token）</span><a-input-number :value="entry[field.key]" :min="0" :step="0.01" placeholder="默认" @change="value => patch({ [field.key]: value })" /></label>
        <label><span>Fast（快速档）倍率</span><a-input-number :value="entry.fast_multiplier" :min="0.000001" :step="0.1" placeholder="沿用官方" @change="value => patch({ fast_multiplier: value })" /></label>
        <label><span>Flex（弹性档）倍率</span><a-input-number :value="entry.flex_multiplier" :min="0.000001" :step="0.1" placeholder="沿用官方" @change="value => patch({ flex_multiplier: value })" /></label>
        <div class="tier-actions"><W12Button compact @click="patch({ fast_multiplier: 2, flex_multiplier: 0.5 })">2× / 0.5×</W12Button><W12Button compact @click="patch({ fast_multiplier: null, flex_multiplier: null })">清空档位倍率</W12Button></div>
      </template>
      <label v-else><span>{{ entry.billing_mode === 'video' ? '默认视频价格（美元 / 秒）' : entry.billing_mode === 'image' ? '默认图片价格（美元 / 张）' : '默认单次价格（美元）' }}</span><a-input-number :value="entry.per_request_price" :min="0" :step="0.001" placeholder="未命中层级时使用" @change="value => patch({ per_request_price: value })" /></label>
    </div>
    <W12Button variant="text" compact :aria-expanded="tiersOpen" @click="tiersOpen = !tiersOpen">{{ tiersOpen ? '收起阶梯价格' : '设置阶梯价格' }}{{ entry.intervals?.length ? `（${entry.intervals.length}）` : '' }}</W12Button>
    <GroupPricingIntervals v-if="tiersOpen" :intervals="entry.intervals || []" :mode="entry.billing_mode" @update:intervals="intervals => patch({ intervals })" />
  </article>
</template>

<style scoped>
.pricing-entry { display: grid; gap: 12px; background: var(--w12-card); border-radius: 8px; padding: 12px; }
header { display: flex; align-items: center; gap: 8px; }
.entry-summary { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; color: var(--w12-muted); }
.entry-fields { display: grid; grid-template-columns: repeat(auto-fit,minmax(min(200px,100%),1fr)); gap: 8px; }
label { display: grid; gap: 4px; min-width: 0; color: var(--w12-muted); font-size: 12px; }
label :deep(.ant-input-number) { width: 100%; }
.models { grid-column: 1 / -1; }
.tier-actions { display: flex; align-items: flex-end; flex-wrap: wrap; gap: 8px; }
@container w12-dialog (max-width: 460px) { .entry-fields { grid-template-columns: minmax(0,1fr); } }
</style>
