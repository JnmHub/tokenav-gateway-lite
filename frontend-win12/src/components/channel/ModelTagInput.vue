<script setup>
/* 模型 Tag 输入框（对照旧 components/admin/channel/ModelTagInput.vue）
 * 交互：回车/Tab/失焦添加，粘贴按逗号/分号/换行批量去重导入，退格删除末尾 tag */
import { ref } from 'vue'
import { platformTagStyle } from './pricing'

const props = defineProps({
  models: { type: Array, default: () => [] },
  placeholder: { type: String, default: '输入模型名后按回车添加，支持通配符 *' },
  platform: { type: String, default: '' },
})
const emit = defineEmits(['update:models'])

const inputValue = ref('')

/** 添加单个模型：trim + 去重 */
function addModel() {
  const val = inputValue.value.trim()
  if (!val) return
  if (!props.models.includes(val)) {
    emit('update:models', [...props.models, val])
  }
  inputValue.value = ''
}

function removeModel(idx) {
  const next = [...props.models]
  next.splice(idx, 1)
  emit('update:models', next)
}

/** 输入为空时退格删除末尾 tag */
function handleBackspace() {
  if (inputValue.value === '' && props.models.length > 0) {
    removeModel(props.models.length - 1)
  }
}

/** 粘贴批量导入：按逗号/分号/换行切分，整体去重 */
function handlePaste(e) {
  e.preventDefault()
  const text = e.clipboardData?.getData('text') || ''
  const items = text.split(/[,;\n]+/).map((s) => s.trim()).filter(Boolean)
  if (items.length === 0) return
  emit('update:models', [...new Set([...props.models, ...items])])
  inputValue.value = ''
}
</script>

<template>
  <div class="mti">
    <div class="mti-box">
      <span
        v-for="(model, idx) in models"
        :key="idx"
        class="mti-tag"
        :style="platformTagStyle(platform)"
      >
        {{ model }}
        <button type="button" class="mti-x" @click="removeModel(idx)">✕</button>
      </span>
      <input
        v-model="inputValue"
        type="text"
        class="mti-input"
        :placeholder="models.length === 0 ? placeholder : ''"
        @keydown.enter.prevent="addModel"
        @keydown.tab.prevent="addModel"
        @keydown.delete="handleBackspace"
        @paste="handlePaste"
        @blur="addModel"
      />
    </div>
    <p class="mti-hint">回车或失焦添加；支持粘贴批量导入（逗号/换行分隔，自动去重）。</p>
  </div>
</template>

<style scoped>
.mti-box {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  min-height: 40px;
  padding: 6px 8px;
  border: 1px solid var(--hr);
  border-radius: 8px;
  background: rgba(255, 255, 255, 0.62);
  cursor: text;
}
:root.dark .mti-box { background: rgba(48, 48, 48, 0.6); }
.mti-box:focus-within { border-color: var(--theme-2); }
.mti-tag {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  height: 22px;
  padding: 0 4px 0 8px;
  border-radius: 4px;
  font-size: 12px;
  max-width: 100%;
  overflow: hidden;
}
.mti-x {
  border: 0;
  background: transparent;
  color: inherit;
  opacity: 0.6;
  font-size: 10px;
  cursor: pointer;
  padding: 0 3px;
  line-height: 1;
}
.mti-x:hover { opacity: 1; }
.mti-input {
  flex: 1;
  min-width: 120px;
  border: 0;
  outline: 0;
  background: transparent;
  color: inherit;
  font-size: 13px;
  font-family: inherit;
}
.mti-hint { font-size: 12px; color: var(--text2); margin: 4px 0 0; }
</style>
