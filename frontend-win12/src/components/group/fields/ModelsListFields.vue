<script setup>
/* /v1/models 白名单（models_list_config.enabled / models，二期 B）
 * 契约来源：旧 GroupsView.vue modelsList 段（901-1002 / 2560-2660）+
 *   internal/service/group_models_list.go
 *  - 开启后 /v1/models 只返回白名单内模型，请求侧模型同规则被拦截（含通配）
 *  - 模型支持尾部 * 通配（如 gpt-5.6-*）；开启时至少一个模型（后端 400 前置校验）
 *  - 规范化与后端 normalizeGroupModelsListConfig 同语义：trim / 去空 / 去重
 *  - models_list_config 的 response_model_* 两键由 GroupResponseModelFields 管理
 *  - 一期此两键为快照透传防清空；本组件把初始值接进表单双向绑定，
 *    未改动的值仍原样回传，透传语义保留
 */
import { computed } from 'vue'

const props = defineProps({
  enabled: { type: Boolean, default: false },
  models: { type: Array, default: () => [] },
  candidates: { type: Array, default: () => [] }, // models-list-candidates 候选
  loading: { type: Boolean, default: false },
})
const emit = defineEmits(['update:enabled', 'update:models'])

const options = computed(() =>
  [...new Set(props.candidates.map((m) => String(m).trim()).filter(Boolean))].map((m) => ({ value: m })))

// 与 useGroupEditor 保存侧一致的规范化，保证 UI 状态即提交内容
const normalizeModels = (list) => {
  const seen = new Set()
  const out = []
  for (const raw of list ?? []) {
    const model = String(raw ?? '').trim()
    if (!model || seen.has(model)) continue
    seen.add(model)
    out.push(model)
  }
  return out
}
const onModelsChange = (list) => emit('update:models', normalizeModels(list))

const showEmptyError = computed(() => props.enabled && props.models.length === 0)
</script>

<template>
  <div class="mlf">
    <div class="frow">
      <div class="frow-main">
        <div class="frow-t">模型列表白名单</div>
        <div class="frow-d">开启后 /v1/models 只返回白名单内模型，请求白名单外模型也会被拒绝。</div>
      </div>
      <a-switch :checked="enabled" size="small" @change="(v) => emit('update:enabled', !!v)" />
    </div>

    <a-form-item v-if="enabled" label="白名单模型">
      <a-select
        mode="tags" :value="models" :options="options" :loading="loading"
        placeholder="输入模型名，或从候选中点选；支持尾部 * 通配"
        @change="onModelsChange"
      />
      <div class="hint">支持尾部 * 通配（如 gpt-5.6-*）；开启后至少保留一个模型。</div>
      <div v-if="showEmptyError" class="err-hint">开启白名单后至少保留一个模型。</div>
    </a-form-item>
  </div>
</template>

<style scoped>
.mlf { display: grid; gap: 8px; }
.frow { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 8px 10px; border-radius: 8px; background: var(--card); min-height: 40px; }
.frow-main { min-width: 0; }
.frow-t { font-size: 13px; }
.frow-d { font-size: 12px; color: var(--text2); margin-top: 1px; }
.hint { font-size: 12px; color: var(--text2); line-height: 1.6; margin-top: 4px; }
.err-hint { font-size: 12px; color: #d13438; margin-top: 4px; }
</style>
