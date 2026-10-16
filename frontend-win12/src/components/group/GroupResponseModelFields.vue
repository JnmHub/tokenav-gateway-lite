<script setup>
/* 响应声明模型模式（models_list_config.response_model_*，二期 A）
 * 契约来源：旧 components/admin/group/GroupResponseModelFields.vue
 *  - mode ∈ request | upstream | custom（分组级无 inherit；后端校验同值域）
 *  - 只改变客户端响应中的 model 声明，不影响路由、计费和上游审计
 *  - custom 时必填自定义模型名（后端：response_model_custom is required）
 * 注意：models_list_config.enabled/models（/v1/models 白名单）由
 *   fields/ModelsListFields.vue 负责（二期 B），本组件只管 response_model 两个键。
 */
import { computed } from 'vue'

const props = defineProps({
  mode: { type: String, default: 'request' },
  custom: { type: String, default: '' },
})
const emit = defineEmits(['update:mode', 'update:custom'])

const options = [
  { value: 'request', label: '跟随请求', hint: '返回用户最初请求的模型名。', recommended: true },
  { value: 'upstream', label: '上游原样', hint: '保留上游真实返回的模型名。', recommended: false },
  { value: 'custom', label: '自定义', hint: '统一返回管理员设置的模型名。', recommended: false },
]
const showError = computed(() => props.mode === 'custom' && !props.custom.trim())
</script>

<template>
  <div class="rm">
    <div class="rm-t">响应声明模型</div>
    <div class="rm-d">只改变客户端响应中的 model 声明，不影响路由、计费和上游审计。</div>
    <div class="rm-cards">
      <button
        v-for="o in options" :key="o.value" type="button" class="rm-card"
        :class="{ on: mode === o.value }" @click="emit('update:mode', o.value)"
      >
        <span class="rm-ct">{{ o.label }}<i v-if="o.recommended">推荐</i></span>
        <span class="rm-cd">{{ o.hint }}</span>
      </button>
    </div>
    <div v-if="mode === 'custom'">
      <label>自定义模型名称</label>
      <a-input
        :value="custom" :maxlength="200" placeholder="例如：gpt-5.6-sol"
        @change="(e) => emit('update:custom', e.target.value)"
      />
      <div v-if="showError" class="rm-err">选择“自定义”时必须填写模型名称。</div>
    </div>
  </div>
</template>

<style scoped>
.rm { display: grid; gap: 8px; }
.rm-t { font-size: 13px; }
.rm-d { font-size: 12px; color: var(--text2); line-height: 1.6; }
.rm-cards { display: grid; grid-template-columns: repeat(3, 1fr); gap: 8px; }
.rm-card { text-align: left; border: 1px solid var(--hr); background: var(--card); border-radius: 8px; padding: 10px; cursor: pointer; display: grid; gap: 4px; }
.rm-card:hover { border-color: var(--text2); }
.rm-card.on { border-color: var(--accent); background: color-mix(in srgb, var(--accent) 10%, transparent); }
.rm-ct { font-size: 12.5px; font-weight: 500; display: flex; align-items: center; gap: 6px; }
.rm-ct i { font-style: normal; font-size: 10px; padding: 0 6px; border-radius: 999px; background: color-mix(in srgb, var(--accent) 14%, transparent); color: var(--accent); }
.rm-cd { font-size: 11.5px; color: var(--text2); line-height: 1.5; }
label { display: block; font-size: 11.5px; color: var(--text2); margin-bottom: 3px; }
.rm-err { font-size: 12px; color: #d13438; margin-top: 4px; }
@media (max-width: 640px) { .rm-cards { grid-template-columns: 1fr; } }
</style>
