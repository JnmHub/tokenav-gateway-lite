<script setup>
import { DeleteOutlined, PlusOutlined } from '@ant-design/icons-vue'
import { defaultOpenCodeRules } from './platformPresets'

/* OpenCode 协议规则行编辑器（对照官方 OpenCodeGoProtocolRulesEditor.vue，功能字段等价）
 * - 行 = { pattern, protocol }：模型通配 pattern → 原生上游协议（adaptive 分流表）
 * - 未命中回退 chat_completions（后端 matchOpenCodeGoProtocolRules 语义，仅作提示展示）
 * - 「恢复默认」按当前接入模式（zen/go）重置为内置规则表 */
const props = defineProps({
  rows: { type: Array, default: () => [] },
  plan: { type: String, default: 'go' }, // zen | go
})
const emit = defineEmits(['update:rows'])

const set = (rows) => emit('update:rows', rows)
const addRow = () => set([...props.rows, { pattern: '', protocol: 'chat_completions' }])
const removeRow = (i) => set(props.rows.filter((_, idx) => idx !== i))
const restoreDefaults = () => set(defaultOpenCodeRules(props.plan))

const PROTOCOL_OPTIONS = [
  { value: 'chat_completions', label: 'Chat Completions' },
  { value: 'responses', label: 'Responses' },
  { value: 'anthropic', label: 'Anthropic' },
]
</script>

<template>
  <div class="oc-rules">
    <div class="oc-rules-head">
      <span class="oc-rules-title">协议规则（按模型分流）</span>
      <a-button type="link" size="small" @click="restoreDefaults">恢复默认</a-button>
    </div>
    <div class="oc-rules-hint">仅 API 协议为 adaptive（自适应）时生效：按通配符匹配模型选择原生协议，未命中回退 Chat Completions（fallback）。</div>
    <div v-for="(row, i) in rows" :key="i" class="oc-rules-row">
      <a-input v-model:value="row.pattern" placeholder="模型通配，如 gpt-*" style="flex: 1; font-family: Consolas, monospace" />
      <a-select v-model:value="row.protocol" :options="PROTOCOL_OPTIONS" style="width: 170px" />
      <a-button type="text" danger size="small" @click="removeRow(i)"><DeleteOutlined /></a-button>
    </div>
    <div class="oc-rules-fallback"><code>*</code><span>未命中规则时回退 Chat Completions 协议</span></div>
    <a-button block size="small" @click="addRow"><PlusOutlined /> 添加规则</a-button>
  </div>
</template>

<style scoped>
.oc-rules { display: flex; flex-direction: column; gap: 6px; }
.oc-rules-head { display: flex; align-items: center; justify-content: space-between; }
.oc-rules-title { font-size: 12px; color: var(--txt2, var(--text2, #5c5c5c)); }
.oc-rules-hint { font-size: 11px; color: var(--txt3, var(--text3, #8a8a8a)); line-height: 1.5; }
.oc-rules-row { display: flex; align-items: center; gap: 6px; }
.oc-rules-fallback { display: flex; align-items: center; gap: 10px; padding: 4px 10px; border: 1px dashed var(--stroke-card, var(--hr, #e0e0e0)); border-radius: 8px; font-size: 11px; color: var(--txt3, var(--text3, #8a8a8a)); background: var(--fill-subtle, transparent); }
.oc-rules-fallback code { font-family: Consolas, monospace; }
</style>
