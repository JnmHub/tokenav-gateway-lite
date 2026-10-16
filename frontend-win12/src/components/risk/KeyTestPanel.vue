<script setup>
/* This panel is only for the legacy /v1/moderations chain. It must never imply
 * verification of the dedicated AI service or expose provider error bodies. */
import { ref, computed, watch } from 'vue'
import { request, isCancellation } from '../../api/index.js'
import { legacyKeyStatusLabel, legacyKeyErrorLabel } from '../../api/riskControl.js'
import { useRequestLifetime } from '../../composables/useRequestLifetime.js'
import W12Button from '../design/W12Button.vue'
import W12Input from '../design/W12Input.vue'
import W12FormRow from '../design/W12FormRow.vue'
import W12Status from '../design/W12Status.vue'
import W12Table from '../design/W12Table.vue'
const props = defineProps({ cfg: { type: Object, default: null } })
const keysText = ref(''), baseUrl = ref(''), model = ref(''), timeoutMs = ref(10000)
const prompt = ref('测试内容：请判断这条文本是否需要拦截。'), imagesText = ref('')
const running = ref(false), result = ref(null), err = ref('')
const lifetime = useRequestLifetime(() => {
  keysText.value = ''; prompt.value = ''; imagesText.value = ''; baseUrl.value = ''; model.value = ''; result.value = null; err.value = ''
})
const prefill = () => {
  const c = props.cfg || {}
  if (!baseUrl.value) baseUrl.value = c.base_url || ''
  if (!model.value) model.value = c.model || ''
  if (c.timeout_ms) timeoutMs.value = c.timeout_ms
}
watch(() => props.cfg, prefill, { immediate: true })
const splitLines = (s) => String(s || '').split('\n').map((x) => x.trim()).filter(Boolean)
const keyItems = computed(() => result.value?.items || [])
const audit = computed(() => result.value?.audit_result || null)
const endpoint = computed(() => `${baseUrl.value.trim().replace(/\/+$/, '')}/v1/moderations`)
const columns = [
  { title: '#', dataIndex: 'index', key: 'index', width: 56 }, { title: '旧密钥（掩码）', key: 'masked', width: 180 },
  { title: '本进程状态', key: 'status', width: 180 }, { title: '成功 / 失败', key: 'counts', width: 110 },
  { title: 'HTTP', dataIndex: 'last_http_status', key: 'http', width: 80 }, { title: '延迟（毫秒）', dataIndex: 'last_latency_ms', key: 'lat', width: 100 },
  { title: '错误说明', key: 'err' },
]
async function run() {
  if (running.value || !lifetime.isAlive()) return
  prefill(); running.value = true; err.value = ''; result.value = null
  try {
    const value = await request('/admin/risk-control/api-keys/test', {
      ...lifetime, method: 'POST', body: { api_keys: splitLines(keysText.value), base_url: baseUrl.value.trim(), model: model.value.trim(), timeout_ms: Number(timeoutMs.value) || 0, prompt: prompt.value, images: splitLines(imagesText.value) },
    })
    if (lifetime.isAlive()) result.value = value
  } catch (e) { if (!isCancellation(e) && lifetime.isAlive()) err.value = '旧审核接口测试请求失败，请核对旧配置后重试。' }
  finally { if (lifetime.isAlive()) running.value = false }
}
</script>
<template>
  <div class="sw w12-design">
    <div class="card">
      <div class="card-title">旧 Moderations（审核接口）密钥测试</div>
      <p class="card-sub">固定请求旧 /v1/moderations，使用顶层旧审核配置，与专用 AI 的对话 / 响应接口独立。留空旧密钥时测试后台保存的旧密钥集合，专用 AI 密钥不会参与。此操作会调用旧审核服务。</p>
      <W12FormRow label="待测旧密钥" hint="每行一个；留空仅测试已保存的旧密钥。"><a-textarea v-model:value="keysText" :rows="3" class="fld-full" :disabled="running" /></W12FormRow>
      <W12FormRow label="旧审核服务地址"><W12Input v-model:value="baseUrl" class="fld" :disabled="running" /></W12FormRow>
      <W12FormRow label="旧审核请求路径"><span class="static mono">{{ endpoint }}</span></W12FormRow>
      <W12FormRow label="旧审核模型"><W12Input v-model:value="model" class="fld" :disabled="running" /></W12FormRow>
      <W12FormRow label="超时（毫秒）"><a-input-number v-model:value="timeoutMs" :min="100" :max="120000" :step="500" class="fld-n" :disabled="running" /></W12FormRow>
      <W12FormRow label="测试文本"><a-textarea v-model:value="prompt" :rows="2" class="fld-full" :disabled="running" /></W12FormRow>
      <W12FormRow label="旧审核测试图片" hint="可选图片 URL（网址），每行一个。"><a-textarea v-model:value="imagesText" :rows="2" class="fld-full" :disabled="running" /></W12FormRow>
      <div class="savebar"><W12Button variant="primary" :loading="running" @click="run">测试旧审核接口</W12Button></div>
      <a-alert v-if="err" type="error" show-icon :message="err" />
    </div>
    <div v-if="result" class="card">
      <div class="card-title">旧审核结果</div>
      <p class="card-sub">暂时冷却是网关本进程的保护状态，不代表供应商冻结账户或密钥，也不会控制专用 AI 审查。</p>
      <W12Table :columns="columns" :data-source="keyItems" row-key="index" :pagination="false">
        <template #bodyCell="{ column, record }">
          <code v-if="column.key === 'masked'" class="mono">{{ record.masked || '—' }}</code>
          <W12Status v-else-if="column.key === 'status'" :tone="record.status === 'available' || record.status === 'ok' ? '' : 'warn'">{{ legacyKeyStatusLabel(record.status) }}</W12Status>
          <span v-else-if="column.key === 'counts'">{{ record.success_count ?? 0 }} / {{ record.failure_count ?? 0 }}</span>
          <span v-else-if="column.key === 'err'">{{ legacyKeyErrorLabel(record) }}</span>
        </template>
      </W12Table>
      <template v-if="audit">
        <W12FormRow label="旧审核判定"><W12Status :tone="audit.flagged ? 'bad' : ''">{{ audit.flagged ? '命中' : '未命中' }}</W12Status></W12FormRow>
        <W12FormRow label="最高类别 / 分数"><span class="static">{{ audit.highest_category || '—' }} / {{ audit.highest_score ?? '—' }}</span></W12FormRow>
        <W12FormRow label="综合分数"><span class="static">{{ audit.composite_score ?? '—' }}</span></W12FormRow>
        <W12FormRow v-for="(value, key) in audit.category_scores || {}" :key="key" :label="key"><span class="static">{{ value }}</span></W12FormRow>
      </template>
    </div>
  </div>
</template>
<style scoped>
@import '../settings/settings.css';
</style>
