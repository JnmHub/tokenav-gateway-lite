<script>
// 与旧 constants/groupDisplay.ts 一一对应（资源位于 public/models/）
export const ICON_OPTIONS = [
  { key: 'openai', label: 'OpenAI', src: '/models/GPT.svg' },
  { key: 'claude', label: 'Claude', src: '/models/Anthropic.svg' },
  { key: 'gemini', label: 'Gemini', src: '/models/Gemini.svg' },
  { key: 'grok', label: 'Grok', src: '/models/Grok.svg' },
  { key: 'kimi', label: 'Kimi', src: '/models/Kimi.svg' },
  { key: 'glm', label: 'GLM', src: '/models/Glm.svg' },
  { key: 'deepseek', label: 'DeepSeek', src: '/models/Deepseek.svg' },
  { key: 'qwen', label: 'Qwen', src: '/models/Qwen.svg' },
  { key: 'doubao', label: 'Doubao', src: '/models/Doubao.svg' },
  { key: 'minimax', label: 'MiniMax', src: '/models/MiniMax.svg' },
  { key: 'universal', label: '通用模型', src: '/models/Universal.png' },
  { key: 'composite', label: '聚合模型', src: '/models/Composite.png' },
]
</script>

<script setup>
/* 分组展示身份字段（对照旧 GroupDisplayIdentityFields.vue + constants/groupDisplay.ts）
 * - display_brand：对外品牌名（用户侧展示，留空回退分组名）
 * - display_icon：品牌图标（12 个预设，选中描边高亮；用户点名补齐的 logo 设置）
 * - 切平台时若品牌/图标为空，展示平台默认建议（defaultGroupDisplayIdentity） */
import { computed } from 'vue'

const props = defineProps({
  platform: { type: String, default: '' },
  brand: { type: String, default: '' },
  icon: { type: String, default: '' },
})
const emit = defineEmits(['update:brand', 'update:icon'])

// 平台默认品牌/图标（旧 defaultGroupDisplayIdentity 语义）
const DEFAULTS = {
  openai: { brand: 'OpenAI', icon: 'openai' },
  anthropic: { brand: 'Claude', icon: 'claude' },
  gemini: { brand: 'Gemini', icon: 'gemini' },
  grok: { brand: 'Grok', icon: 'grok' },
  kimi: { brand: 'Kimi', icon: 'kimi' },
  zhipu: { brand: '智谱 GLM', icon: 'glm' },
  deepseek: { brand: 'DeepSeek', icon: 'deepseek' },
  composite: { brand: '聚合模型', icon: 'composite' },
}
const defaultIdentity = computed(() => DEFAULTS[String(props.platform || '').toLowerCase()] || { brand: '通用模型', icon: 'universal' })
const effectiveIcon = computed(() => props.icon || defaultIdentity.value.icon)
</script>

<template>
  <div class="gdi">
    <div class="gdi-brand">
      <a-input :value="brand" :placeholder="`留空则显示分组名；平台默认建议：${defaultIdentity.brand}`"
        @update:value="(v) => emit('update:brand', v)" />
    </div>
    <div class="gdi-icons">
      <button v-for="opt in ICON_OPTIONS" :key="opt.key" type="button" class="gdi-icon"
        :class="{ on: effectiveIcon === opt.key }" :title="opt.label"
        :aria-pressed="effectiveIcon === opt.key"
        @click="emit('update:icon', opt.key)">
        <img :src="opt.src" :alt="opt.label" loading="lazy" />
      </button>
    </div>
  </div>
</template>

<style scoped>
.gdi { display: flex; flex-direction: column; gap: 8px; }
.gdi-icons { display: flex; flex-wrap: wrap; gap: 6px; }
.gdi-icon { width: 40px; height: 40px; border-radius: 8px; border: 1px solid var(--stroke-card); background: var(--card); display: flex; align-items: center; justify-content: center; cursor: pointer; padding: 6px; transition: border-color 0.12s, background 0.12s; }
.gdi-icon:hover { background: var(--fill-subtle); }
.gdi-icon.on { border-color: var(--accent, #3b91d8); background: var(--accent-soft, #3b91d81a); }
.gdi-icon img { width: 100%; height: 100%; object-fit: contain; }
</style>
