<script setup>
import { computed } from 'vue'
import W12Button from '../design/W12Button.vue'
import { createPricingInterval } from './pricing.js'
const props = defineProps({ intervals: { type: Array, default: () => [] }, mode: { type: String, required: true } })
const emit = defineEmits(['update:intervals'])
const isToken = computed(() => props.mode === 'token')
const priceFields = [
  { key: 'input_price', label: '输入价格' }, { key: 'output_price', label: '输出价格' },
  { key: 'cache_write_price', label: '缓存写入' }, { key: 'cache_read_price', label: '缓存读取' },
]
const multiplierFields = [
  { key: 'input_multiplier', label: '输入倍率' }, { key: 'output_multiplier', label: '输出倍率' },
  { key: 'cache_write_multiplier', label: '缓存写入倍率' }, { key: 'cache_read_multiplier', label: '缓存读取倍率' },
]
const patch = (index, values) => emit('update:intervals', props.intervals.map((interval, row) => row === index ? { ...interval, ...values } : interval))
const add = (label = '') => {
  const last = props.intervals.at(-1)
  const interval = createPricingInterval(props.intervals.length, label)
  if (isToken.value && last?.max_tokens != null) interval.min_tokens = last.max_tokens
  emit('update:intervals', [...props.intervals, interval])
}
const remove = index => emit('update:intervals', props.intervals.filter((_interval, row) => row !== index))
</script>

<template>
  <section class="pricing-intervals">
    <div class="intervals-head">
      <span>{{ isToken ? '上下文区间价格（下界不含，上界包含）' : mode === 'video' ? '分辨率价格（美元 / 生成秒）' : '按层级价格' }}</span>
      <W12Button compact @click="add()">{{ isToken ? '添加区间' : '添加层级' }}</W12Button>
    </div>
    <div v-if="mode === 'image' || mode === 'video'" class="tier-presets">
      <W12Button v-for="label in mode === 'image' ? ['1K','2K','4K','HD'] : ['480p','720p','1080p']" :key="label" compact :disabled="intervals.some(interval => interval.tier_label?.toLowerCase() === label.toLowerCase())" @click="add(label)">{{ label }}</W12Button>
    </div>
    <div v-for="(interval, index) in intervals" :key="index" class="interval-card">
      <div class="intervals-head"><span>{{ isToken ? `区间 ${index + 1}` : interval.tier_label || `层级 ${index + 1}` }}</span><W12Button variant="danger" compact @click="remove(index)">删除</W12Button></div>
      <div class="interval-grid">
        <label v-if="!isToken"><span>{{ mode === 'image' || mode === 'video' ? '分辨率' : '层级名称' }}</span><a-input :value="interval.tier_label" placeholder="点击快捷值或输入层级" @update:value="value => patch(index, { tier_label: value })" /></label>
        <label><span>最小 Token 数（不含）</span><a-input-number :value="interval.min_tokens" :min="0" :precision="0" @change="value => patch(index, { min_tokens: value ?? 0 })" /></label>
        <label><span>最大 Token 数（含）</span><a-input-number :value="interval.max_tokens" :min="1" :precision="0" placeholder="无限制" @change="value => patch(index, { max_tokens: value })" /></label>
        <template v-if="isToken">
          <label v-for="field in priceFields" :key="field.key"><span>{{ field.label }}（美元 / 百万 Token）</span><a-input-number :value="interval[field.key]" :min="0" :step="0.01" placeholder="继承" @change="value => patch(index, { [field.key]: value })" /></label>
          <label v-for="field in multiplierFields" :key="field.key"><span>{{ field.label }}</span><a-input-number :value="interval[field.key]" :min="0.000001" :step="0.1" placeholder="继承" @change="value => patch(index, { [field.key]: value })" /></label>
        </template>
        <label v-else><span>{{ mode === 'video' ? '每秒价格（美元）' : mode === 'image' ? '每张价格（美元）' : '单次价格（美元）' }}</span><a-input-number :value="interval.per_request_price" :min="0" :step="0.001" @change="value => patch(index, { per_request_price: value })" /></label>
      </div>
    </div>
  </section>
</template>

<style scoped>
.pricing-intervals { display: grid; gap: 8px; font-size: 12px; }
.intervals-head { display: flex; justify-content: space-between; align-items: center; gap: 8px; }
.intervals-head > span { color: var(--w12-muted); }
.tier-presets { display: flex; flex-wrap: wrap; gap: 8px; }
.interval-card { padding: 12px; background: var(--w12-card); border-radius: 8px; display: grid; gap: 8px; }
.interval-grid { display: grid; grid-template-columns: repeat(auto-fit,minmax(min(180px,100%),1fr)); gap: 8px; }
label { min-width: 0; display: grid; gap: 4px; color: var(--w12-muted); }
label :deep(.ant-input-number) { width: 100%; }
@container w12-dialog (max-width: 460px) { .interval-grid { grid-template-columns: minmax(0,1fr); } }
</style>
