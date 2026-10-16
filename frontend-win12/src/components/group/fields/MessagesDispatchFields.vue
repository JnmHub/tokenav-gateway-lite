<script setup>
/* OpenAI Messages 调度三件套（二期 B）
 * 字段：allow_messages_dispatch（开关）/ default_mapped_model（文本）/
 *   messages_dispatch_model_config（opus/sonnet/haiku 家族映射 + 精确映射行）
 * 契约来源：旧 GroupsView.vue openaiMessages 段（1571-1720 / 3348-3560）+
 *   groupsMessagesDispatch.ts；互转逻辑见 ../messagesDispatch.js
 *  - 开关关闭时其余字段仍随表单保存；后端按开关决定是否生效
 *  - 仅 openai 平台挂载（挂载点见 GroupEditorModal.vue）
 *  - 状态不在本组件持有：全部经 patch 上抛给 useGroupEditor 的 editForm
 */
import { computed } from 'vue'
import { PlusOutlined, DeleteOutlined } from '@ant-design/icons-vue'

const props = defineProps({
  form: { type: Object, required: true }, // useGroupEditor 的 editForm
})
const emit = defineEmits(['patch'])
const patch = (values) => emit('patch', values)

// —— 精确映射行（不可变更新） ——
const exactRows = computed(() => props.form.messages_exact_rows ?? [])
const addExactRow = () =>
  patch({ messages_exact_rows: [...exactRows.value, { claude_model: '', target_model: '' }] })
const removeExactRow = (index) =>
  patch({ messages_exact_rows: exactRows.value.filter((_, i) => i !== index) })
const updateExactRow = (index, key, value) =>
  patch({
    messages_exact_rows: exactRows.value.map((row, i) => (i === index ? { ...row, [key]: value } : row)),
  })
// 开关只切换布尔位；配置清空仅在切换平台时发生（useGroupEditor watch，对照旧行为）
</script>

<template>
  <div class="mdf">
    <div class="frow">
      <div class="frow-main">
        <div class="frow-t">允许 Messages 调度</div>
        <div class="frow-d">开启后本分组的 Anthropic /v1/messages 请求改写为 OpenAI/Codex 模型调度，用户侧同步展示 Claude Code 接入教程。</div>
      </div>
      <a-switch :checked="form.allow_messages_dispatch" size="small" @change="(v) => patch({ allow_messages_dispatch: !!v })" />
    </div>

    <template v-if="form.allow_messages_dispatch">
      <a-form-item label="默认映射模型">
        <a-input
          :value="form.default_mapped_model" :maxlength="100"
          placeholder="例如：gpt-5.4（留空关闭兜底）"
          @change="(e) => patch({ default_mapped_model: e.target.value })"
        />
        <div class="hint">账号映射未命中且请求是 Claude 系列模型时，改写为该模型；留空则保持原样交由调度判断。</div>
      </a-form-item>

      <div class="mdf-card">
        <div class="mdf-head">
          <div>
            <div class="mdf-t">Claude 家族映射</div>
            <div class="mdf-d">按 opus / sonnet / haiku 家族改写，留空使用内置默认（gpt-5.4 / gpt-5.3-codex / gpt-5.4-mini）。</div>
          </div>
        </div>
        <div class="mdf-grid">
          <div>
            <label>Opus 映射到</label>
            <a-input
              :value="form.opus_mapped_model" :maxlength="100"
              placeholder="gpt-5.4"
              @change="(e) => patch({ opus_mapped_model: e.target.value })"
            />
          </div>
          <div>
            <label>Sonnet 映射到</label>
            <a-input
              :value="form.sonnet_mapped_model" :maxlength="100"
              placeholder="gpt-5.3-codex"
              @change="(e) => patch({ sonnet_mapped_model: e.target.value })"
            />
          </div>
          <div>
            <label>Haiku 映射到</label>
            <a-input
              :value="form.haiku_mapped_model" :maxlength="100"
              placeholder="gpt-5.4-mini"
              @change="(e) => patch({ haiku_mapped_model: e.target.value })"
            />
          </div>
        </div>
      </div>

      <div class="mdf-card accent">
        <div class="mdf-head">
          <div>
            <div class="mdf-t">精确模型映射</div>
            <div class="mdf-d">为单个 Claude 模型指定目标模型，优先级高于家族映射。</div>
          </div>
          <a-button size="small" @click="addExactRow">
            <template #icon><PlusOutlined /></template>
            添加映射
          </a-button>
        </div>
        <div v-if="exactRows.length === 0" class="mdf-empty">未配置精确映射，全部请求按家族映射改写。</div>
        <div v-else class="mdf-rows">
          <div v-for="(row, i) in exactRows" :key="i" class="mdf-row">
            <a-input
              :value="row.claude_model" :maxlength="100"
              placeholder="Claude 模型，如 claude-sonnet-4-5"
              @change="(e) => updateExactRow(i, 'claude_model', e.target.value)"
            />
            <span class="arrow">→</span>
            <a-input
              :value="row.target_model" :maxlength="100"
              placeholder="实际调用模型，如 gpt-5.4-mini"
              @change="(e) => updateExactRow(i, 'target_model', e.target.value)"
            />
            <a-button size="small" type="text" danger title="删除该映射" @click="removeExactRow(i)">
              <template #icon><DeleteOutlined /></template>
            </a-button>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.mdf { display: grid; gap: 8px; }
.mdf-card { border: 1px solid var(--hr); background: var(--card); border-radius: 8px; padding: 10px; display: grid; gap: 8px; }
.mdf-card.accent { border-color: color-mix(in srgb, var(--accent) 35%, transparent); background: color-mix(in srgb, var(--accent) 6%, transparent); }
.mdf-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.mdf-t { font-size: 12.5px; font-weight: 500; }
.mdf-d { font-size: 11.5px; color: var(--text2); line-height: 1.6; margin-top: 2px; }
.mdf-grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 8px; }
label { display: block; font-size: 11.5px; color: var(--text2); margin-bottom: 3px; }
.mdf-empty { border: 1px dashed var(--hr); border-radius: 8px; padding: 12px; text-align: center; font-size: 12px; color: var(--text2); }
.mdf-rows { display: grid; gap: 8px; }
.mdf-row { display: grid; grid-template-columns: minmax(0, 1fr) auto minmax(0, 1fr) auto; gap: 8px; align-items: center; padding: 8px; border: 1px solid var(--hr); border-radius: 8px; background: var(--card); }
.arrow { color: var(--text2); }
/* 与 GroupEditorModal 的 frow 视觉一致（scoped 样式需本地定义） */
.frow { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 8px 10px; border-radius: 8px; background: var(--card); min-height: 40px; }
.frow-main { min-width: 0; }
.frow-t { font-size: 13px; }
.frow-d { font-size: 12px; color: var(--text2); margin-top: 1px; }
.hint { font-size: 12px; color: var(--text2); line-height: 1.6; margin-top: 4px; }
@media (max-width: 640px) {
  .mdf-grid { grid-template-columns: 1fr; }
  .mdf-row { grid-template-columns: 1fr auto; }
  .arrow { display: none; }
}
</style>
