<script setup>
import { riskModeLabel, aiErrorLabel } from '../../api/riskControl.js'
import AIReadinessSummary from './AIReadinessSummary.vue'
import W12Status from '../design/W12Status.vue'
import W12FormRow from '../design/W12FormRow.vue'
import W12Button from '../design/W12Button.vue'
defineProps({ status: Object, metrics: Object, saved: Object, error: String, refreshing: Boolean })
defineEmits(['refresh'])
const count = (v) => typeof v === 'number' ? v.toLocaleString('zh-CN') : '未知'
</script>
<template>
  <div class="sw w12-design">
    <AIReadinessSummary :saved="saved" :metrics="metrics" :error="error" />
    <div class="card">
      <div class="card-title">真实运行记录（当前进程）</div>
      <W12FormRow label="生产检查次数"><span class="static">{{ count(metrics?.production_checked) }}{{ metrics?.production_checked === 0 ? '（尚无生产 AI 检查记录）' : '' }}</span></W12FormRow>
      <W12FormRow label="生产放行 / 违规 / 错误"><span class="static">{{ count(metrics?.production_allowed) }} / {{ count(metrics?.production_blocked) }} / {{ count(metrics?.production_errors) }}</span></W12FormRow>
      <p class="card-sub">上方次数不含合成测试，也不等同于模型调用次数。放行次数包含无可审查输入的跳过；观察模式的有效违规只记录，未判定仍拒绝请求。每次审查仅发起一次模型调用。</p>
      <W12FormRow label="已保存运行模式"><span class="static">{{ riskModeLabel(status?.mode) }}</span></W12FormRow>
      <W12FormRow label="已保存引擎"><span class="static">{{ !saved ? '未知' : saved.keyword_blocking_mode === 'ai_only' ? '纯 AI' : '本地关键词' }}</span></W12FormRow>
      <W12FormRow label="专用请求路径"><span class="static mono">{{ metrics?.service_endpoint || '未知' }}</span></W12FormRow>
      <W12FormRow label="请求模型 / 策略"><span class="static">{{ metrics?.model || '未知' }} / {{ metrics?.policy_version || '未知' }}</span></W12FormRow>
      <W12FormRow label="专用密钥"><W12Status :tone="metrics?.configured === false ? 'warn' : ''">{{ metrics?.configured === true ? '已保存（不代表验证通过）' : metrics?.configured === false ? '未配置' : '未知' }}</W12Status></W12FormRow>
      <W12FormRow label="外部数据授权"><span class="static">{{ metrics?.external_data_consent === true ? '已授权' : metrics?.external_data_consent === false ? '未授权' : '未知' }}</span></W12FormRow>
      <W12FormRow label="处罚存储"><W12Status :tone="saved?.ai?.penalties_enabled && metrics?.store_ready !== true ? 'warn' : ''">{{ metrics?.store_ready === true ? '就绪' : metrics?.store_ready === false ? '不可用' : '未知' }}</W12Status><span class="static">{{ saved?.ai?.penalties_enabled ? '已开启 AI 处罚，需存储可用' : 'AI 处罚未开启，不影响模型审查' }}</span></W12FormRow>
      <W12FormRow label="状态共享范围"><span class="static">检查与调用计数保存在当前进程；处罚与补偿投递记录保存在数据库</span></W12FormRow>
      <W12FormRow label="含合成的放行 / 违规 / 错误"><span class="static">{{ count(metrics?.allowed) }} / {{ count(metrics?.blocked) }} / {{ count(metrics?.errors) }}</span></W12FormRow>
      <W12FormRow label="总检查 / 观察检查"><span class="static">{{ count(metrics?.checked) }} / {{ count(metrics?.shadow_checked) }}</span></W12FormRow>
      <W12FormRow label="正在调用 / 累计调用"><span class="static">{{ count(metrics?.active) }} / {{ count(metrics?.total_calls) }}（观察计数）</span></W12FormRow>
      <W12FormRow label="平均 / 最近审查耗时"><span class="static">{{ count(metrics?.avg_latency_ms) }} / {{ count(metrics?.last_latency_ms) }} ms（毫秒）</span></W12FormRow>
      <W12FormRow label="最近错误"><span class="static">{{ metrics ? (metrics.last_error_code ? aiErrorLabel(metrics.last_error_code) : '无记录') : '未知' }}</span></W12FormRow>
      <W12FormRow label="处罚待处理 / 失败"><span class="static">{{ count(metrics?.penalty_pending) }} / {{ count(metrics?.penalty_dead) }}</span></W12FormRow>
      <div class="savebar"><W12Button :loading="refreshing" @click="$emit('refresh')">刷新真实状态</W12Button></div>
    </div>
  </div>
</template>
<style scoped>
@import '../settings/settings.css';
</style>
