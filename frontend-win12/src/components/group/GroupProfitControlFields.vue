<script setup>
/* 分组利润控制字段组（对照官方 v0.2.5 GroupsView.vue 2860-2913）
 * 表单存小数（profit_min_margin / profit_safety_buffer），本组件内部
 * 持有百分比输入状态，双向换算后经 @patch 写回 decimals（换算见 profitControl.js）。
 * 仅五个支持平台显示；开关关闭或平台不支持时隐藏数字输入。
 */
import { reactive, watch } from 'vue'
import { InfoCircleOutlined } from '@ant-design/icons-vue'
import { profitDecimalToPercent, profitPercentToDecimal, isProfitControlPlatform } from './profitControl'

const props = defineProps({
  form: { type: Object, required: true }, // editForm（含 3 个小数字段）
})
const emit = defineEmits(['patch'])

// 内部百分比输入状态（显示层），初始由 decimals 换算
const percent = reactive({ margin: 0, buffer: 0 })
const syncFromForm = () => {
  percent.margin = profitDecimalToPercent(props.form.profit_min_margin)
  percent.buffer = profitDecimalToPercent(props.form.profit_safety_buffer)
}
syncFromForm()
// 弹窗回填/切组时把表单 decimals 重新换算进输入框
watch(() => [props.form.profit_min_margin, props.form.profit_safety_buffer], syncFromForm)

// 百分比输入 → 小数写回表单（精度与边界处理统一走助手）
const onMarginChange = (v) => emit('patch', { profit_min_margin: profitPercentToDecimal(v) })
const onBufferChange = (v) => emit('patch', { profit_safety_buffer: profitPercentToDecimal(v) })
</script>

<template>
  <div v-if="isProfitControlPlatform(form.platform)" class="pc-block">
    <div class="frow">
      <div class="frow-main">
        <div class="frow-t">
          启用利润控制
          <a-tooltip title="调度时仅允许「账号倍率 ≤ 请求实际下游倍率 ×（1 − 最低毛利率 − 安全缓冲）」的账号进入候选池；图片/视频调度暂不参与。">
            <InfoCircleOutlined class="tip-icon" />
          </a-tooltip>
        </div>
        <div class="frow-d">{{ form.profit_control_enabled
          ? '已启用 — 高于下游核价线的账号将被排除，避免亏损请求'
          : '未启用 — 不做利润过滤，账号倍率高于下游倍率也可能被选中（可能亏损）' }}</div>
      </div>
      <a-switch :checked="form.profit_control_enabled" size="small" @change="(v) => $emit('patch', { profit_control_enabled: v })" />
    </div>
    <div v-if="form.profit_control_enabled" class="grid2">
      <a-form-item label="最低毛利率（%）">
        <a-input-number
          :value="percent.margin" :min="0" :max="99.99" :step="0.1"
          style="width: 100%" placeholder="0" @change="onMarginChange"
        />
        <div class="hint">百分比输入，如 30 表示 30%；后端按小数存储。</div>
      </a-form-item>
      <a-form-item label="安全缓冲（%）">
        <a-input-number
          :value="percent.buffer" :min="0" :max="99.99" :step="0.1"
          style="width: 100%" placeholder="0" @change="onBufferChange"
        />
        <div class="hint">与最低毛利率相加后从下游倍率中扣除，默认 0。</div>
      </a-form-item>
    </div>
  </div>
</template>

<style scoped>
.pc-block { margin-bottom: 8px; }
.frow {
  display: flex; align-items: center; justify-content: space-between; gap: 16px;
  padding: 8px 10px; border-radius: 8px; min-height: 40px; margin-bottom: 8px;
  background: var(--card);
}
.frow:hover { filter: brightness(1.03); }
.frow-main { min-width: 0; }
.frow-t { font-size: 13px; display: flex; align-items: center; gap: 6px; }
.frow-d { font-size: 12px; color: var(--text2); margin-top: 1px; }
.tip-icon { font-size: 12px; color: var(--text2); cursor: help; }
.grid2 { display: grid; grid-template-columns: 1fr 1fr; gap: 0 16px; }
.hint { font-size: 12px; color: var(--text2); line-height: 1.6; margin-top: 4px; }
</style>
