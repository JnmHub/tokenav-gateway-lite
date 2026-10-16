<script setup>
import W12Button from '../design/W12Button.vue'
import W12Input from '../design/W12Input.vue'
import { hdrTemplateFor } from './platformPresets.js'

const modify = defineModel('modify', { type: Boolean, default: false })
const enabled = defineModel('enabled', { type: Boolean, default: false })
const rows = defineModel('rows', { type: Array, default: () => [] })
const props = defineProps({ platform: { type: String, default: '' } })
const add = () => { rows.value = [...rows.value, { name: '', value: '' }] }
const remove = index => { rows.value = rows.value.filter((_, i) => i !== index) }
// 模板仅补充缺少的名称，避免管理员已填写的值被一键替换。
const applyTemplate = () => {
  const existing = new Set(rows.value.map(row => row.name.trim().toLowerCase()))
  rows.value = [...rows.value, ...hdrTemplateFor(props.platform).filter(name => !existing.has(name)).map(name => ({ name, value: '' }))]
}
</script>

<template>
  <div class="bulk-headers w12-design">
    <div class="header-control">
      <a-checkbox v-model:checked="modify">修改请求头覆写</a-checkbox>
      <a-switch v-model:checked="enabled" :disabled="!modify" size="small" />
      <span>{{ enabled ? '开启并替换所选账号的覆写配置' : '关闭并清空所选账号的覆写配置' }}</span>
    </div>
    <template v-if="modify && enabled">
      <div class="header-control">
        <W12Button :disabled="rows.length >= 64" @click="add">添加请求头</W12Button>
        <W12Button @click="applyTemplate">补充客户端模板</W12Button>
      </div>
      <div v-for="(row, index) in rows" :key="index" class="header-row">
        <W12Input v-model:value="row.name" placeholder="请求头名称" :maxlength="200" aria-label="请求头名称" />
        <W12Input v-model:value="row.value" placeholder="请求头值（可留空）" aria-label="请求头值" autocomplete="off" />
        <W12Button variant="danger" @click="remove(index)">删除</W12Button>
      </div>
      <div class="header-hint">最多64条；不能覆写认证、会话路由或传输请求头。开启时至少填写一条，清空配置请关闭开关。</div>
    </template>
  </div>
</template>

<style scoped>
.bulk-headers { margin: 8px 0; }
.header-control, .header-row { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; }
.header-control { flex-wrap: wrap; }
.header-control span, .header-hint { color: var(--text2); font-size: 12px; }
.header-row > :nth-child(1) { flex: 1; min-width: 0; }
.header-row > :nth-child(2) { flex: 2; min-width: 0; }
@media (max-width: 480px) {
  .header-row { flex-wrap: wrap; }
  .header-row > :nth-child(1), .header-row > :nth-child(2) { flex: 1 1 100%; }
}
</style>
