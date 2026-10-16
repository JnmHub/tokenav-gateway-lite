<script setup>
/* 模型映射编辑器（对照旧 ChannelsView.vue 372-418 的映射区块）
 * 结构：Record<源模型, 目标模型>；每平台一份（提交为 model_mapping[platform]）
 * 交互：+ 添加（自动命名 model-N 避免撞 key）、源 key 失焦重命名（重名/空名忽略）、目标就地编辑、删除
 * 源模式冲突（通配符重叠）在父级提交时用 findModelConflict 统一校验 */
import { computed } from 'vue'

const props = defineProps({
  /* 直接接收父级 reactive section 的 model_mapping 引用做就地变更，
     与旧版 renameMappingKey/mapping[srcModel] 赋值语义一致 */
  mapping: { type: Object, required: true },
})
const emit = defineEmits(['change'])

const entries = computed(() => Object.entries(props.mapping))

// script setup 组件默认封闭：显式暴露 addEntry 供父组件模板 $refs 调用
defineExpose({ addEntry })

/** 添加条目：model-1、model-2…跳过已存在 key（对照旧 addMappingEntry） */
function addEntry() {
  let key = ''
  let i = 1
  while (key === '' || key in props.mapping) {
    key = `model-${i}`
    i++
  }
  props.mapping[key] = ''
  emit('change')
}

function removeEntry(key) {
  delete props.mapping[key]
  emit('change')
}

/** 源 key 重命名：空名/重名直接忽略（对照旧 renameMappingKey 的静默容错） */
function renameKey(oldKey, e) {
  const newKey = e.target.value.trim()
  e.target.value = oldKey
  if (!newKey || newKey === oldKey || newKey in props.mapping) return
  const value = props.mapping[oldKey]
  delete props.mapping[oldKey]
  props.mapping[newKey] = value
  emit('change')
}

function setTarget(key, e) {
  props.mapping[key] = e.target.value
  emit('change')
}
</script>

<template>
  <div>
    <div v-if="entries.length === 0" class="empty-tip">暂无映射规则，点击「添加」创建一条 源模型 → 目标模型</div>
    <div v-else class="map-list">
      <div v-for="[src, dst] in entries" :key="src" class="map-row">
        <input class="map-in" :value="src" type="text" placeholder="源模型（支持 * 通配）" @change="renameKey(src, $event)" />
        <span class="arrow">→</span>
        <input class="map-in" :value="dst" type="text" placeholder="目标模型" @change="setTarget(src, $event)" />
        <button type="button" class="del" title="删除" @click="removeEntry(src)">✕</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.map-list { display: grid; gap: 6px; }
.map-row { display: flex; align-items: center; gap: 6px; }
.map-in {
  flex: 1; min-width: 0; height: 30px; padding: 0 8px;
  border: 1px solid var(--hr); border-radius: 6px;
  background: rgba(255, 255, 255, 0.62); color: inherit; font-size: 12px; font-family: inherit;
}
:root.dark .map-in { background: rgba(48, 48, 48, 0.6); }
.map-in:hover { border-color: var(--text2); }
.map-in:focus { outline: none; border-color: var(--theme-2); }
.arrow { color: var(--text2); font-size: 12px; flex: none; }
.del { border: 0; background: transparent; color: var(--text2); cursor: pointer; padding: 4px 6px; font-size: 12px; }
.del:hover { color: #d13438; }
.empty-tip {
  padding: 10px; text-align: center; font-size: 11.5px; color: var(--text2);
  border: 1px dashed var(--hr); border-radius: 8px;
}
</style>
