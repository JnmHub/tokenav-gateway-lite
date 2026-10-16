<script setup>
/* 定价区间/层级行（对照旧 components/admin/channel/IntervalRow.vue）
 * token 模式：(min, max] 上下文区间 + 输入/输出/缓存W/缓存R 价格（$/MTok）+ 4 倍率
 * per_request / image 模式：tier_label 层级 + 单次价格
 * 所有价格为空时整行红边提示（校验在提交时由 validateIntervals 统一执行） */
import { computed } from 'vue'

const props = defineProps({
  interval: { type: Object, required: true },
  mode: { type: String, default: 'token' },
  showMultipliers: { type: Boolean, default: false },
})
const emit = defineEmits(['update', 'remove'])

// 所有价格/倍率字段都为空时视为"未配置"，红边提示
const isEmpty = computed(() => {
  const iv = props.interval
  const blank = (v) => v == null || v === ''
  return (
    blank(iv.input_price) && blank(iv.output_price) &&
    blank(iv.cache_write_price) && blank(iv.cache_write_1h_price) &&
    blank(iv.cache_read_price) &&
    blank(iv.input_multiplier) && blank(iv.output_multiplier) &&
    blank(iv.cache_write_multiplier) && blank(iv.cache_read_multiplier) &&
    blank(iv.per_request_price)
  )
})

const multiplierFields = [
  { key: 'input_multiplier', label: '输入倍率' },
  { key: 'output_multiplier', label: '输出倍率' },
  { key: 'cache_write_multiplier', label: '缓存写倍率' },
  { key: 'cache_read_multiplier', label: '缓存读倍率' },
]

/** 通用字段更新：空串归一为 null（与旧 emitField 语义一致） */
function setField(field, e) {
  const v = e.target.value
  emit('update', { ...props.interval, [field]: v === '' ? null : v })
}

function toInt(e) {
  const n = parseInt(e.target.value, 10)
  emit('update', { ...props.interval, min_tokens: Number.isNaN(n) ? 0 : n })
}

function toIntOrNull(e) {
  if (e.target.value === '') {
    emit('update', { ...props.interval, max_tokens: null })
    return
  }
  const n = parseInt(e.target.value, 10)
  emit('update', { ...props.interval, max_tokens: Number.isNaN(n) ? null : n })
}
</script>

<template>
  <div class="ivrow" :class="{ empty: isEmpty }">
    <!-- token 模式：上下文区间 + 4 价格（$/MTok） -->
    <template v-if="mode === 'token'">
      <label class="fld w70">
        <span class="lb">Min</span>
        <input :value="interval.min_tokens" type="number" min="0" @input="toInt" />
      </label>
      <label class="fld w70">
        <span class="lb">Max（含）</span>
        <input :value="interval.max_tokens ?? ''" type="number" min="0" placeholder="∞" @input="toIntOrNull" />
      </label>
      <label class="fld">
        <span class="lb">输入 $/M <i v-if="isEmpty" class="req">*</i></span>
        <input :value="interval.input_price ?? ''" type="number" step="any" min="0" @input="setField('input_price', $event)" />
      </label>
      <label class="fld">
        <span class="lb">输出 $/M <i v-if="isEmpty" class="req">*</i></span>
        <input :value="interval.output_price ?? ''" type="number" step="any" min="0" @input="setField('output_price', $event)" />
      </label>
      <label class="fld">
        <span class="lb">缓存W $/M</span>
        <input :value="interval.cache_write_price ?? ''" type="number" step="any" min="0" @input="setField('cache_write_price', $event)" />
      </label>
      <!-- v0.2.5：1h 缓存写入价（Anthropic 1h 长缓存档） -->
      <label class="fld">
        <span class="lb">缓存W1h $/M</span>
        <input :value="interval.cache_write_1h_price ?? ''" type="number" step="any" min="0" @input="setField('cache_write_1h_price', $event)" />
      </label>
      <label class="fld">
        <span class="lb">缓存R $/M</span>
        <input :value="interval.cache_read_price ?? ''" type="number" step="any" min="0" @input="setField('cache_read_price', $event)" />
      </label>
      <!-- 区间倍率：可选，作用于该区间的价格放大 -->
      <div v-if="showMultipliers" class="multis">
        <label v-for="f in multiplierFields" :key="f.key" class="fld">
          <span class="lb">{{ f.label }}</span>
          <input :value="interval[f.key] ?? ''" type="number" step="any" min="0.000001" placeholder="×" @input="setField(f.key, $event)" />
        </label>
      </div>
    </template>

    <!-- per_request / image 模式：层级 label + 单次价格 -->
    <template v-else>
      <label class="fld w110">
        <span class="lb">{{ mode === 'image' ? '分辨率' : '层级' }}</span>
        <input :value="interval.tier_label" type="text" :placeholder="mode === 'image' ? '1K / 2K / 4K' : ''" @input="setField('tier_label', $event)" />
      </label>
      <label class="fld w70">
        <span class="lb">Min</span>
        <input :value="interval.min_tokens" type="number" min="0" @input="toInt" />
      </label>
      <label class="fld w70">
        <span class="lb">Max（含）</span>
        <input :value="interval.max_tokens ?? ''" type="number" min="0" placeholder="∞" @input="toIntOrNull" />
      </label>
      <label class="fld">
        <span class="lb">单次价格 $ <i v-if="isEmpty" class="req">*</i></span>
        <input :value="interval.per_request_price ?? ''" type="number" step="any" min="0" @input="setField('per_request_price', $event)" />
      </label>
    </template>

    <button type="button" class="del" title="删除" @click="emit('remove')">✕</button>
  </div>
</template>

<style scoped>
.ivrow {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: 8px;
  padding: 8px 10px;
  border: 1px solid var(--hr);
  border-radius: 8px;
  background: var(--card);
}
.ivrow.empty { border-color: rgba(209, 52, 56, 0.55); }
.fld { display: flex; flex-direction: column; gap: 3px; flex: 1; min-width: 96px; }
.fld.w70 { flex: 0 0 70px; min-width: 70px; }
.fld.w110 { flex: 0 0 110px; min-width: 110px; }
.lb { font-size: 11px; color: var(--text2); white-space: nowrap; }
.req { color: #d13438; font-style: normal; }
.ivrow input {
  height: 30px;
  width: 100%;
  padding: 0 8px;
  border: 1px solid var(--stroke-ctl);
  border-radius: var(--r-ctl);
  background: var(--surface-card);
  color: inherit;
  font-size: 12px;
  font-family: inherit;
}
:root.dark .ivrow input { background: rgba(48, 48, 48, 0.6); }
.ivrow input:hover { border-color: var(--accent); }
/* 聚焦统一用主题强调色（原先用 --theme-2，在五案主题下与 --accent 不是同一个色） */
.ivrow input:focus { outline: none; border-color: var(--accent); }
.multis {
  flex-basis: 100%;
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 8px;
  padding-top: 6px;
  border-top: 1px solid var(--hr);
}
.del {
  border: 0;
  background: transparent;
  color: var(--text2);
  cursor: pointer;
  padding: 4px 6px;
  font-size: 12px;
}
.del:hover { color: #d13438; }
</style>
