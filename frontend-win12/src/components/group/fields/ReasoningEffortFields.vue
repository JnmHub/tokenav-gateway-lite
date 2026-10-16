<script setup>
/* 推理力度策略字段（max_reasoning_effort + reasoning_effort_mappings，二期 B）
 * 契约来源：旧 components/admin/group/ReasoningEffortPolicyFields.vue +
 *   views/admin/groupsReasoningEffort.ts；枚举与校验规则见 ../reasoningEffort.js
 * （与 internal/service/openai_reasoning_effort_policy.go 完全一致）
 *  - 上限：客户端显式提交更高 effort 时压回上限；留空 = 不设上限
 *  - 映射：先按精确映射改写档位，再受上限约束；from 唯一（后端同样校验）
 *  - 仅 openai / composite 平台挂载（挂载点见 GroupEditorModal.vue）
 */
import { computed } from 'vue'
import { PlusOutlined, DeleteOutlined } from '@ant-design/icons-vue'
import { REASONING_EFFORT_OPTIONS, createEffortRow } from '../reasoningEffort'

const props = defineProps({
  platform: { type: String, default: '' },
  maxEffort: { type: String, default: '' },
  mappings: { type: Array, default: () => [] }, // [{id, from, to}]
})
const emit = defineEmits(['update:maxEffort', 'update:mappings'])

// a-select 允许清空：清空 = 不设上限
const onMaxEffortChange = (value) => emit('update:maxEffort', value || '')

const writeRows = (rows) => emit('update:mappings', rows)
const addRow = () => writeRows([...props.mappings, createEffortRow()])
const removeRow = (id) => writeRows(props.mappings.filter((row) => row.id !== id))
const updateRow = (id, key, value) =>
  writeRows(props.mappings.map((row) => (row.id === id ? { ...row, [key]: value || '' } : row)))

// 源档位重复的行内提示（保存时还有兜底校验，见 useGroupEditor.save）
const duplicateIndex = computed(() => {
  const seen = new Set()
  return props.mappings.map((row) => {
    const key = row.from.trim()
    if (key && seen.has(key)) return true
    if (key) seen.add(key)
    return false
  })
})
</script>

<template>
  <div class="ref">
    <div class="ref-t">推理力度策略</div>
    <div class="ref-d">限制客户端可请求的最高推理档位，并支持把某个档位精确改写为另一档位。</div>

    <a-form-item label="推理力度上限">
      <a-select
        :value="maxEffort || undefined" :options="REASONING_EFFORT_OPTIONS"
        allow-clear placeholder="不设上限（跟随客户端）"
        @change="onMaxEffortChange"
      />
      <div class="hint">客户端提交高于上限的 effort 时压回上限；留空表示分组不做干预。</div>
    </a-form-item>

    <div class="ref-head">
      <div>
        <div class="ref-st">档位精确映射</div>
        <div class="ref-d">把客户端提交的某个推理档位改写为另一档位，先改写再限幅。</div>
      </div>
      <a-button size="small" @click="addRow">
        <template #icon><PlusOutlined /></template>
        添加映射
      </a-button>
    </div>

    <div v-if="mappings.length === 0" class="ref-empty">
      未配置映射：客户端档位原样透传（仍受上限约束）。
    </div>
    <div v-else class="ref-rows">
      <div v-for="(row, i) in mappings" :key="row.id" class="ref-row">
        <div class="ref-cell">
          <a-select
            :value="row.from || undefined" :options="REASONING_EFFORT_OPTIONS"
            allow-clear placeholder="客户端档位" @change="(v) => updateRow(row.id, 'from', v)"
          />
          <div v-if="duplicateIndex[i]" class="ref-err">该档位已有一条映射</div>
        </div>
        <span class="arrow">→</span>
        <div class="ref-cell">
          <a-select
            :value="row.to || undefined" :options="REASONING_EFFORT_OPTIONS"
            allow-clear placeholder="改写为" @change="(v) => updateRow(row.id, 'to', v)"
          />
          <div v-if="!row.to.trim()" class="ref-err">请选择改写档位</div>
        </div>
        <a-button size="small" type="text" danger title="删除该映射" @click="removeRow(row.id)">
          <template #icon><DeleteOutlined /></template>
        </a-button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.ref { display: grid; gap: 8px; }
.ref-t { font-size: 13px; }
.ref-d { font-size: 12px; color: var(--text2); line-height: 1.6; }
.ref-st { font-size: 12.5px; font-weight: 500; }
.ref-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; margin-top: 4px; }
.ref-rows { display: grid; gap: 8px; }
.ref-row { display: grid; grid-template-columns: minmax(0, 1fr) auto minmax(0, 1fr) auto; gap: 8px; align-items: center; padding: 8px; border: 1px solid var(--hr); border-radius: 8px; background: var(--card); }
.ref-cell { display: grid; gap: 3px; }
.arrow { color: var(--text2); }
.ref-err { font-size: 11.5px; color: #d13438; }
.ref-empty { border: 1px dashed var(--hr); border-radius: 8px; padding: 12px; text-align: center; font-size: 12px; color: var(--text2); }
.hint { font-size: 12px; color: var(--text2); line-height: 1.6; margin-top: 4px; }
@media (max-width: 640px) {
  .ref-row { grid-template-columns: 1fr auto; }
  .arrow { display: none; }
}
</style>
