<script setup>
import { computed } from 'vue'
import { presentAIReadiness, presentAITest } from '../../api/riskControl.js'
import W12Status from '../design/W12Status.vue'
import W12FormRow from '../design/W12FormRow.vue'
const props = defineProps({ saved: Object, metrics: Object, dirty: Boolean, error: String })
const readiness = computed(() => presentAIReadiness(props.metrics, props.saved))
const test = computed(() => readiness.value.known ? presentAITest(props.metrics?.test, props.saved?.ai?.config_fingerprint) : { tone: 'warn', label: '当前配置测试状态未知' })
</script>
<template>
  <div class="card w12-design">
    <div class="card-title">保存、测试与生产状态</div>
    <a-alert v-if="error" type="warning" show-icon message="状态读取失败，请刷新真实状态；暂不能确认生产条件。" />
    <W12FormRow label="配置保存"><W12Status :tone="dirty ? 'warn' : ''">{{ dirty ? '有未保存草稿，测试只使用已保存配置' : metrics?.config_saved === true && readiness.known ? '后台已保存' : readiness.known ? '默认配置尚未保存' : '保存状态待读取' }}</W12Status></W12FormRow>
    <W12FormRow label="合成测试（可选）"><W12Status :tone="test.tone">{{ test.label }}</W12Status></W12FormRow>
    <W12FormRow label="生产运行条件"><W12Status :tone="readiness.tone">{{ readiness.label }}</W12Status></W12FormRow>
    <p class="card-sub">合成测试是可选验证，不影响生产审查启用；通过只证明当前合成案例的整体判定。生产请求还需命中已保存的分组与业务模型范围，并沿用原业务请求的鉴权、额度和取消生命周期。观察模式下，有效判定不拦截、不处罚；未判定仍拒绝本次请求。无可审查输入则跳过并放行。</p>
    <ul v-if="readiness.missing.length" class="hint"><li v-for="item in readiness.missing" :key="item.code">{{ item.message }}</li></ul>
    <p class="hint">测试证据保存在当前进程，服务重启仅清空测试记录，配置变化后旧证据过期，可按需重新测试；模型修订备注不是测试结果。</p>
  </div>
</template>
<style scoped>
@import '../settings/settings.css';
</style>
