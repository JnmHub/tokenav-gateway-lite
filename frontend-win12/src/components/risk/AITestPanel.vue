<script setup>
import { computed, ref, watch } from 'vue'
import { testRiskAI, SYNTHETIC_CASES, presentAIDecision, presentAICoverage, presentAITest, isCurrentAITestResult, aiErrorLabel, aiEndpoint, aiCallLabel, aiPhaseLabel, aiCategories } from '../../api/riskControl.js'
import { isCancellation } from '../../api/index.js'
import { useRequestLifetime } from '../../composables/useRequestLifetime.js'
import W12Button from '../design/W12Button.vue'
import W12Select from '../design/W12Select.vue'
import W12FormRow from '../design/W12FormRow.vue'
import W12Status from '../design/W12Status.vue'
const props = defineProps({ saved: Object, saving: Boolean, dirty: Boolean })
const emit = defineEmits(['tested'])
const caseId = ref('allow'), running = ref(false), result = ref(null), error = ref(''), stale = ref(false)
const lifetime = useRequestLifetime(() => { result.value = null; error.value = ''; stale.value = false; caseId.value = 'allow' })
const fingerprint = computed(() => props.saved?.ai?.config_fingerprint)
const decision = computed(() => presentAIDecision(result.value))
const coverage = computed(() => presentAICoverage(result.value))
const uncoveredFields = computed(() => result.value?.coverage?.uncovered_fields || [])
const test = computed(() => presentAITest(result.value?.test, fingerprint.value))
const selected = computed(() => SYNTHETIC_CASES.find((item) => item.value === caseId.value))
const categories = computed(() => aiCategories(result.value))
watch(fingerprint, () => { stale.value = running.value || !!result.value; result.value = null })
async function run() {
  if (running.value || props.saving || !fingerprint.value) return
  const requestedFingerprint = fingerprint.value
  running.value = true; result.value = null; error.value = ''; stale.value = false
  try {
    const value = await testRiskAI(caseId.value, { ...lifetime, configFingerprint: requestedFingerprint })
    if (!lifetime.isAlive()) return
    // A successful response from an older configuration cannot turn the new
    // saved state green; the server independently performs the same check.
    if (requestedFingerprint !== fingerprint.value || !isCurrentAITestResult(value, fingerprint.value)) {
      stale.value = true
      if (value?.error_code === 'ai_test_config_changed') error.value = aiErrorLabel(value.error_code)
    } else result.value = value
    emit('tested')
  } catch (e) { if (!isCancellation(e) && lifetime.isAlive()) error.value = '测试接口请求失败，请重新读取配置或稍后重试。' }
  finally { if (lifetime.isAlive()) running.value = false }
}
defineExpose({ run })
</script>
<template>
  <div class="sw w12-design">
    <div class="card">
      <div class="card-title">AI 合成审查测试</div>
      <p class="card-sub">合成测试是可选验证，不影响生产审查启用。测试使用后台已保存的 AI 地址、模型、密钥与策略，一次调用整体判定内置合成案例。调用可能产生供应商费用；不读取客户原文、不调用业务上游、不触发处罚。测试通过不会自动开启生产审查或外部数据授权。</p>
      <W12FormRow label="已保存请求路径"><span class="static mono">{{ aiEndpoint(saved?.ai) || '尚未读取' }}</span></W12FormRow>
      <W12FormRow label="已保存请求模型"><span class="static">{{ saved?.ai?.model || '尚未读取' }}</span></W12FormRow>
      <a-alert v-if="dirty" type="warning" show-icon message="当前有未保存草稿。本次测试仍使用上方已保存的地址与模型，请先保存希望验证的修改。" />
      <W12FormRow label="合成案例"><W12Select v-model:value="caseId" class="fld" :options="SYNTHETIC_CASES" :disabled="running" /></W12FormRow>
      <p class="hint">{{ selected?.description }}</p>
      <p v-if="!fingerprint" class="hint">后台尚未返回已保存配置指纹，请先重新读取配置。</p>
      <div class="savebar"><W12Button variant="primary" :loading="running" :disabled="saving || !fingerprint" @click="run">测试已保存配置</W12Button></div>
      <a-alert v-if="error" type="error" show-icon :message="error" />
      <a-alert v-if="stale" type="warning" show-icon message="配置已变化或测试响应已失效；旧结果不适用于当前配置，可按需重新读取并测试。" />
    </div>
    <div v-if="result" class="card">
      <div class="card-title">合成测试状态 <W12Status :tone="test.tone">{{ test.label }}</W12Status></div>
      <p v-if="result.test?.error_code" class="card-sub">{{ aiErrorLabel(result.test.error_code, result.test.provider_http_status) }}</p>
      <W12FormRow label="整体判定"><W12Status :tone="decision.tone">{{ decision.label }}</W12Status><span class="static">{{ decision.reason }}</span></W12FormRow>
      <W12FormRow label="模型调用"><W12Status :tone="result.provider_called === true ? '' : 'warn'">{{ aiCallLabel(result.provider_called) }}</W12Status></W12FormRow>
      <W12FormRow label="处理阶段"><span class="static">{{ aiPhaseLabel(result.phase) }}</span></W12FormRow>
      <W12FormRow v-if="result.error_code" label="失败原因"><span class="static">{{ aiErrorLabel(result.error_code, result.provider_http_status) }} · {{ result.error_code }}</span></W12FormRow>
      <W12FormRow label="专用服务 HTTP 状态"><span class="static">{{ result.provider_http_status || (result.provider_called === false ? '未调用' : '未收到响应状态') }}</span></W12FormRow>
      <W12FormRow label="判定理由"><span class="static">{{ result.reason || '未提供' }}</span></W12FormRow>
      <W12FormRow label="分类标签"><a-tag v-for="category in categories" :key="category">{{ category }}</a-tag><span v-if="!categories.length" class="static">{{ decision.label === '未判定' ? '未判定' : '无分类标签' }}</span></W12FormRow>
      <W12FormRow label="已选内容覆盖"><W12Status :tone="coverage.tone">{{ coverage.label }}</W12Status></W12FormRow>
      <W12FormRow label="文本字符 / 图片数"><span class="static">{{ result.coverage?.text_runes ?? '未知' }} / {{ result.coverage?.image_count ?? '未知' }}</span></W12FormRow>
      <W12FormRow label="未覆盖字段"><span class="static">{{ uncoveredFields.join('、') || '无记录' }}</span></W12FormRow>
      <W12FormRow label="AI 总耗时"><span class="static">{{ result.latency_ms ?? '未知' }} ms（毫秒）</span></W12FormRow>
      <W12FormRow label="模型调用耗时"><span class="static">{{ result.provider_called === false ? '未调用' : `${result.provider_latency_ms ?? '未知'} ms（毫秒）` }}</span></W12FormRow>
      <W12FormRow label="处罚"><span class="static">合成测试不处罚客户</span></W12FormRow>
    </div>
  </div>
</template>
<style scoped>
@import '../settings/settings.css';
</style>
