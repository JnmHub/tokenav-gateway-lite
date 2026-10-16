<script setup>
/* 分时计价编辑器（v0.2.5，对照官方 components/admin/channel/TimePricingSection.vue 移植）
 * 数据结构：{ timezone: 'Asia/Shanghai', weekdays_only: false, periods: [{start_time,end_time,multiplier}] }
 * 时段格式 HH:mm:ss（a-time-picker valueFormat 直接绑字符串）；倍率两位小数 > 0
 * 时区：IANA 名称，候选 COMMON_TIMEZONES 带当前 UTC 偏移展示，支持搜索与自输入（后端
 * loadChannelTimePricingLocation 用 time.LoadLocation 解析 IANA 名称） */
import { ref, computed } from 'vue'
import { PlusOutlined, DeleteOutlined } from '@ant-design/icons-vue'
import {
  COMMON_TIMEZONES, formatTimezoneOffset, DEFAULT_TIME_PRICING_TIMEZONE,
} from './pricing'

const props = defineProps({
  modelValue: { type: Object, required: true }, // TimePricingFormEntry
})
const emit = defineEmits(['update'])

// 折叠态：有已配置时段时默认展开，否则收起（「折叠区」交互要求）
const open = ref((props.modelValue?.periods?.length ?? 0) > 0)

const timezoneOptions = COMMON_TIMEZONES.map((value) => {
  const offset = formatTimezoneOffset(value)
  return { value, label: offset ? `${value}（${offset}）` : value }
})

// a-select 不接受布尔 value，用 0/1 数值承载（false=每天，true=仅工作日）
const dayScopeOptions = [
  { value: 0, label: '每天' },
  { value: 1, label: '仅工作日（周一至周五）' },
]

const enabled = computed(() => (props.modelValue?.periods?.length ?? 0) > 0)

/** 整体替换提交（保持父级 entry 不可变更新模式） */
function patch(part) {
  emit('update', { ...props.modelValue, ...part })
}

function updatePeriod(idx, field, value) {
  const periods = props.modelValue.periods.map((p, i) => (i === idx ? { ...p, [field]: value } : p))
  patch({ periods })
}

function addPeriod() {
  patch({ periods: [...(props.modelValue.periods || []), { start_time: '00:00:00', end_time: '00:00:00', multiplier: '1.00' }] })
  open.value = true
}

function removePeriod(idx) {
  patch({ periods: props.modelValue.periods.filter((_p, i) => i !== idx) })
}

/** 倍率失焦格式化：非法值回落 1.00（校验在提交时统一执行） */
function formatMultiplier(idx, value) {
  const n = Number(value)
  updatePeriod(idx, 'multiplier', Number.isFinite(n) && n > 0 ? n.toFixed(2) : '1.00')
}

/** 时区变更：清空输入回落默认值（a-select 允许清空） */
function changeTimezone(v) {
  patch({ timezone: v || DEFAULT_TIME_PRICING_TIMEZONE })
}
</script>

<template>
  <div class="tps">
    <!-- 折叠头：时段数 + 时区摘要 -->
    <button type="button" class="tps-head" :aria-expanded="open" @click="open = !open">
      <span class="chev" :class="{ on: open }">›</span>
      <span class="tps-title">分时计价（可选）</span>
      <span class="tps-sum">
        {{ enabled ? `${modelValue.periods.length} 个时段 · ${modelValue.timezone}` : '未配置，按基础价格全天计费' }}
      </span>
    </button>

    <div v-show="open" class="tps-body">
      <p class="hint">配置时段后，落在时段内的请求整单费用乘对应倍率；未命中时段按基础价格计费。</p>
      <div class="tps-cfg">
        <label class="fld">
          <span class="lb">时区</span>
          <a-select
            :value="modelValue.timezone || DEFAULT_TIME_PRICING_TIMEZONE"
            :options="timezoneOptions" show-search size="small" style="width: 100%"
            :filter-option="(input, opt) => opt.label.toLowerCase().includes(input.toLowerCase())"
            @change="changeTimezone"
          />
        </label>
        <label class="fld">
          <span class="lb">生效日</span>
          <a-select
            :value="modelValue.weekdays_only === true ? 1 : 0"
            :options="dayScopeOptions" size="small" style="width: 100%"
            @change="(v) => patch({ weekdays_only: v === 1 })"
          />
        </label>
        <button type="button" class="mini-btn add" @click="addPeriod">
          <PlusOutlined /> 添加时段
        </button>
      </div>

      <div v-if="(modelValue.periods || []).length" class="tps-rows">
        <div v-for="(period, idx) in modelValue.periods" :key="idx" class="tps-row">
          <label class="fld">
            <span class="lb">开始时间</span>
            <a-time-picker
              :value="period.start_time || null" value-format="HH:mm:ss" format="HH:mm:ss"
              size="small" style="width: 100%" placeholder="HH:mm:ss"
              @update:value="(v) => updatePeriod(idx, 'start_time', v || '')"
            />
          </label>
          <label class="fld">
            <span class="lb">结束时间</span>
            <a-time-picker
              :value="period.end_time || null" value-format="HH:mm:ss" format="HH:mm:ss"
              size="small" style="width: 100%" placeholder="HH:mm:ss"
              @update:value="(v) => updatePeriod(idx, 'end_time', v || '')"
            />
          </label>
          <label class="fld">
            <span class="lb">倍率</span>
            <a-input-number
              :value="period.multiplier === '' ? null : Number(period.multiplier)"
              :min="0.01" :step="0.01" size="small" style="width: 100%" placeholder="1.00"
              @update:value="(v) => updatePeriod(idx, 'multiplier', v == null ? '' : String(v))"
              @blur="formatMultiplier(idx, period.multiplier)"
            />
          </label>
          <button type="button" class="del" title="删除时段" @click="removePeriod(idx)">
            <DeleteOutlined />
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.tps {
  margin-top: 12px; padding: 10px 12px; border: 1px solid var(--hr);
  border-radius: 8px; background: var(--surface);
}
.tps-head {
  display: flex; align-items: center; gap: 6px; width: 100%;
  border: 0; background: transparent; color: inherit; cursor: pointer;
  padding: 2px 0; text-align: left; font-family: inherit;
}
.chev { display: inline-block; transition: transform 150ms; color: var(--text2); font-size: 14px; }
.chev.on { transform: rotate(90deg); }
.tps-title { font-size: 12px; color: var(--text); font-weight: 500; }
.tps-sum { font-size: 11.5px; color: var(--text2); margin-left: 8px; }
.tps-body { margin-top: 8px; }
.hint { font-size: 11.5px; color: var(--text2); margin: 0 0 8px; line-height: 1.5; }
.tps-cfg { display: flex; align-items: flex-end; gap: 8px; flex-wrap: wrap; }
.tps-cfg .fld { width: 200px; }
.fld { display: flex; flex-direction: column; gap: 3px; }
.lb { font-size: 11px; color: var(--text2); white-space: nowrap; }
.mini-btn {
  height: 26px; padding: 0 10px; border: 1px solid var(--hr); border-radius: 6px;
  background: transparent; color: var(--text2); font-size: 11.5px; cursor: pointer;
  font-family: inherit; display: inline-flex; align-items: center; gap: 4px;
}
.mini-btn:hover { border-color: var(--theme-2); color: var(--theme-2); }
.tps-rows { display: grid; gap: 8px; margin-top: 8px; }
.tps-row {
  display: flex; align-items: flex-end; gap: 8px;
  padding: 8px 10px; border: 1px solid var(--hr); border-radius: 8px; background: var(--card);
}
.tps-row .fld { flex: 1; min-width: 110px; }
.del { border: 0; background: transparent; color: var(--text2); cursor: pointer; padding: 6px 6px; font-size: 12px; }
.del:hover { color: #d13438; }
@media (max-width: 640px) {
  .tps-cfg .fld { width: 100%; }
  .tps-row { flex-wrap: wrap; }
}
</style>
