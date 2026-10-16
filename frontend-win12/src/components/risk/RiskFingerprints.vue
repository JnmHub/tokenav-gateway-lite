<script setup>
import { ref, onMounted, onUnmounted } from 'vue'
import { message } from 'ant-design-vue'
import { request, isCancellation } from '../../api/index.js'
import { createLatestRequest, runLatest } from '../../api/latestRequest.js'
import { useRequestLifetime } from '../../composables/useRequestLifetime.js'
import W12Button from '../design/W12Button.vue'
import W12Input from '../design/W12Input.vue'
import W12FormRow from '../design/W12FormRow.vue'
const count = ref(null), input = ref(''), loading = ref(false), error = ref('')
const lane = createLatestRequest()
const lifetime = useRequestLifetime(() => { lane.cancel(); count.value = null; input.value = ''; error.value = '' })
async function refresh() {
  loading.value = true
  await runLatest(lane, (signal) => request('/admin/risk-control/status', { ...lifetime, signal, method: 'GET' }), {
    apply: (value) => { count.value = Number.isFinite(value?.flagged_hash_count) ? value.flagged_hash_count : null; error.value = '' },
    error: (e) => { count.value = null; error.value = e.message },
    finish: () => { loading.value = false },
  })
}
async function remove(all = false) {
  if (loading.value || (!all && !input.value.trim())) return
  loading.value = true
  try {
    const value = await request(all ? '/admin/risk-control/hashes/all' : '/admin/risk-control/hashes', {
      ...lifetime, method: 'DELETE', ...(!all ? { body: { input_hash: input.value.trim() } } : {}),
    })
    // 全部清空返回数量，单条删除返回是否存在；缺字段不能被转换成成功删除零条。
    if (all) {
      if (!Number.isSafeInteger(value?.deleted) || value.deleted < 0) throw new Error('后台未返回有效清空数量')
      message.success(`已清空 ${value.deleted} 条指纹标记`)
    } else {
      if (typeof value?.deleted !== 'boolean') throw new Error('后台未返回有效删除结果')
      if (value.deleted) message.success('已删除该历史指纹记录')
      else message.info('该历史指纹已不存在，无需删除')
    }
    input.value = ''; await refresh()
  } catch (e) { if (!isCancellation(e)) { error.value = e.message; message.error(`删除失败：${e.message}`) } }
  finally { if (lifetime.isAlive()) loading.value = false }
}
defineExpose({ refresh })
onMounted(refresh)
onUnmounted(() => lane.dispose())
</script>
<template>
  <div class="card w12-design">
    <div class="card-title">历史指纹管理</div>
    <p class="card-sub">保留记录删除功能；后台固定关闭历史指纹前置捷径。删除记录不等同于允许纯 AI 下的违规内容。</p>
    <a-alert v-if="error" type="error" :message="error" show-icon />
    <W12FormRow label="当前标记数量">
      <span class="static">{{ count ?? '未知' }}</span><W12Button compact :loading="loading" @click="refresh">刷新</W12Button>
      <a-popconfirm title="确认清空全部被标记指纹？该操作不可恢复。" @confirm="remove(true)"><W12Button compact variant="danger" :disabled="!count || loading">全部清空</W12Button></a-popconfirm>
    </W12FormRow>
    <W12FormRow label="删除单条指纹"><W12Input v-model:value="input" class="fld" placeholder="SHA-256（内容摘要）" /><W12Button compact variant="danger" :disabled="!input.trim() || loading" @click="remove(false)">删除</W12Button></W12FormRow>
  </div>
</template>
<style scoped>
@import '../settings/settings.css';
</style>
