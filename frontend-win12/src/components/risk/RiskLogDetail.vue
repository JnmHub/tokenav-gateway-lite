<script setup>
import { computed } from 'vue'
import { aiCallLabel, aiPhaseLabel, aiErrorLabel, presentAIDecision, presentAICoverage } from '../../api/riskControl.js'
import { isAILog, riskLogResult, riskLogCategories, riskLogInputText, riskLogReviewText, formatRiskTime, formatRiskPercent, formatRiskMS } from '../../api/riskLogPresentation.js'
import W12Button from '../design/W12Button.vue'
import W12FormRow from '../design/W12FormRow.vue'
import W12Status from '../design/W12Status.vue'
const props = defineProps({ row: Object, loading: Boolean, error: String, deleting: Boolean })
defineEmits(['retry', 'delete-fingerprint'])
const ai = computed(() => props.row?.ai_review_detail)
const decision = computed(() => presentAIDecision(ai.value))
const coverage = computed(() => presentAICoverage(ai.value))
const outcome = computed(() => riskLogResult(props.row || {}))
const categories = computed(() => riskLogCategories(props.row))
const evidenceRows = computed(() => {
  const evidence = props.row?.language_evidence
  return Object.entries(evidence?.language_counts || {})
    .map(([lang, count]) => ({ lang, count, ratio: `${((evidence.language_ratio_bps?.[lang] || 0) / 100).toFixed(1)}%` }))
    .sort((a, b) => b.count - a.count)
})
const privacy = computed(() => props.row?.action === 'language_block' || String(props.row?.language_evidence?.detector_version || '').trim() === 'v2')
// 只展示输入结构元信息，不读取片段文本、图片内容或外链，也不制造逐片判定。
const metadata = (item) => [
  ['parent', '所属字段'], ['role', '角色'], ['media_type', '媒体类型'], ['runes', '字符'], ['text_runes', '文本字符'], ['bytes', '字节'],
].filter(([key]) => item[key] !== undefined && item[key] !== null).map(([key, label]) => `${label}：${item[key]}`).join(' · ') || '未提供其他元信息'
</script>

<template>
  <div class="detail w12-design">
    <p v-if="loading" role="status">正在读取日志详情与完整文本…</p>
    <div v-else-if="error" class="detail-error" role="alert"><a-alert type="error" show-icon :message="error" /><W12Button @click="$emit('retry')">重新读取详情</W12Button></div>
    <template v-else-if="row">
      <div class="d-grid">
        <div class="d-box"><div class="d-k">时间</div><div>{{ formatRiskTime(row.created_at) }}</div></div>
        <div class="d-box"><div class="d-k">用户</div><div class="wrap">{{ row.user_email || `UID ${row.user_id ?? '—'}` }}</div></div>
        <div class="d-box"><div class="d-k">请求结果</div><a-tag :color="outcome.color">{{ outcome.label }}</a-tag></div>
        <div class="d-box"><div class="d-k">接口密钥名称</div><div class="wrap">{{ row.api_key_name || '—' }}</div></div>
        <div class="d-box"><div class="d-k">请求标识（request_id）</div><div class="wrap mono">{{ row.request_id || '—' }}</div></div>
        <div class="d-box"><div class="d-k">邮件通知</div><div>{{ row.email_sent ? '已发送' : '未发送' }}</div></div>
      </div>
      <div v-if="row.content_fingerprint" class="d-fp">
        <div class="wrap"><div class="d-k">内容指纹（历史记录）</div><div class="mono">{{ row.content_fingerprint }}</div></div>
        <W12Button size="small" variant="danger" :loading="deleting" @click="$emit('delete-fingerprint')">删除该指纹</W12Button>
      </div>
      <div v-if="ai" class="d-sec">
        <div class="d-head">AI 整体审查</div>
        <W12FormRow label="模型判定"><W12Status :tone="decision.tone">{{ decision.label }}</W12Status></W12FormRow>
        <W12FormRow label="模型调用"><W12Status :tone="ai.provider_called === true ? '' : 'warn'">{{ aiCallLabel(ai.provider_called) }}</W12Status></W12FormRow>
        <W12FormRow label="处理阶段"><span class="static">{{ aiPhaseLabel(ai.phase) }}</span></W12FormRow>
        <W12FormRow label="服务 HTTP 状态"><span class="static">{{ ai.provider_http_status || (ai.provider_called === false ? '未调用' : '未收到响应状态') }}</span></W12FormRow>
        <W12FormRow label="请求模型"><span class="static wrap">{{ ai.model || '未知' }}</span></W12FormRow>
        <W12FormRow label="适配协议"><span class="static">{{ ({ deepseek_chat: 'Chat Completions（对话接口）', deepseek_responses: 'Responses（响应接口）' })[ai.adapter] || ai.adapter || '未知' }}</span></W12FormRow>
        <W12FormRow label="策略版本"><span class="static wrap">{{ ai.policy_version || '未知' }}</span></W12FormRow>
        <W12FormRow label="AI 总耗时"><span class="static">{{ formatRiskMS(ai.latency_ms) }}</span></W12FormRow>
        <W12FormRow label="模型调用耗时"><span class="static">{{ ai.provider_called === false ? '未调用' : formatRiskMS(ai.provider_latency_ms) }}</span></W12FormRow>
        <W12FormRow label="分类标签"><div class="tags"><a-tag v-for="category in categories" :key="category">{{ category }}</a-tag><span v-if="!categories.length" class="static">{{ decision.label === '未判定' ? '未判定' : '无分类标签' }}</span></div></W12FormRow>
        <W12FormRow label="判定理由"><span class="static wrap">{{ ai.reason || (decision.label === '未判定' ? '未形成有效判定' : decision.reason) }}</span></W12FormRow>
        <W12FormRow v-if="ai.error_code" :label="decision.label === '违规' ? '处置异常' : '失败原因'"><span class="static wrap">{{ decision.label === '违规' ? decision.reason : aiErrorLabel(ai.error_code, ai.provider_http_status) }} · {{ ai.error_code }}</span></W12FormRow>
        <div class="d-coverage">
          <span>已选内容：{{ coverage.label }}</span>
          <span>输入字段 {{ ai.coverage?.fragment_count ?? '未知' }}</span>
          <span>图片 {{ ai.coverage?.image_count ?? '未知' }}</span>
          <span>文本字符 {{ ai.coverage?.text_runes ?? '未知' }}</span>
        </div>
        <p class="sub">覆盖完成仅指已选送审内容，不代表下方未覆盖字段或外链图片已审查。</p>
        <div v-if="ai.fragments?.length" class="d-sub-sec">
          <div class="d-k">输入字段元信息（整体判定，无逐片结论）</div>
          <div class="metadata-list"><div v-for="(item, index) in ai.fragments" :key="item.id || index" class="metadata-row"><span class="mono">{{ item.id || `字段 ${index + 1}` }}</span><span>{{ metadata(item) }}</span></div></div>
        </div>
        <div v-if="ai.images?.length" class="d-sub-sec">
          <div class="d-k">图片元信息（不展示图片内容）</div>
          <div class="metadata-list"><div v-for="(item, index) in ai.images" :key="item.id || index" class="metadata-row"><span class="mono">{{ item.id || `图片 ${index + 1}` }}</span><span>{{ metadata(item) }}</span></div></div>
        </div>
        <div v-if="ai.uncovered_fields?.length" class="d-sub-sec">
          <div class="d-k">未覆盖内容（仅记录，不因这些字段拒绝请求）</div>
          <div class="tags"><a-tag v-for="field in ai.uncovered_fields" :key="field" color="orange">{{ field }}</a-tag></div>
        </div>
      </div>
      <a-alert v-else-if="isAILog(row)" type="warning" show-icon message="该历史记录没有保存 AI 详情，判定和调用状态未知。" />
      <div v-if="!isAILog(row)" class="d-sec">
        <div class="d-head">分类与语种证据</div>
        <div>{{ row.highest_category || '无分类' }} · {{ formatRiskPercent(row.highest_score) }}</div>
        <div v-if="row.matched_keyword" class="sub">命中：{{ row.matched_keyword }}</div>
        <div class="tags"><a-tag v-for="(score, category) in row.category_scores || {}" :key="category">{{ category }} · {{ formatRiskPercent(score) }}</a-tag></div>
        <div v-if="evidenceRows.length" class="d-grid"><div v-for="item in evidenceRows" :key="item.lang" class="d-box"><div class="d-k">{{ item.lang }}</div><div>{{ item.count }} · {{ item.ratio }}</div></div></div>
      </div>
      <div v-if="row.error && !ai?.error_code" class="d-sec"><div class="d-head">错误信息</div><p class="wrap">{{ row.error }}</p></div>
      <div class="d-sec">
        <div class="d-head">完整提取文本（已脱敏）</div>
        <p class="sub wrap">{{ row.endpoint || '—' }} · {{ row.provider || '—' }} / {{ row.model || '—' }} · {{ row.group_name || '—' }}</p>
        <pre class="d-pre">{{ riskLogInputText(row) }}</pre>
        <p v-if="privacy" class="sub">语种隐私记录仅保存字符统计和带随机盐的内容指纹，不保存客户请求原文。</p>
      </div>
      <div v-if="!row.full_input_text && row.input_excerpt && !privacy" class="d-sec">
        <div class="d-head">内容短预览（该记录无完整文本）</div>
        <pre class="d-pre">{{ row.input_excerpt }}</pre>
      </div>
      <div v-if="isAILog(row)" class="d-sec">
        <div class="d-head">完整实际送审文本（已脱敏）</div>
        <pre class="d-pre">{{ riskLogReviewText(row) }}</pre>
      </div>
      <p v-if="row.queue_delay_ms !== null && row.queue_delay_ms !== undefined" class="sub">日志落库延迟 {{ formatRiskMS(row.queue_delay_ms) }}（不是 AI 等待队列）</p>
    </template>
  </div>
</template>

<style scoped>
@import '../settings/settings.css';
.detail, .detail-error { display: flex; flex-direction: column; gap: 16px; min-width: 0; }
.d-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(160px, 1fr)); gap: 8px; }
.d-box, .d-sec, .d-fp { border-radius: 8px; background: var(--bg-1, rgba(127, 127, 127, 0.05)); padding: 12px; min-width: 0; }
.d-box { font-size: 13px; }
.d-fp { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
.d-fp :deep(button) { flex-shrink: 0; }
.d-k, .sub { font-size: 12px; color: var(--text2); }
.d-k { margin-bottom: 4px; }
.d-head { font-size: 14px; font-weight: 500; margin-bottom: 8px; }
.wrap { overflow-wrap: anywhere; min-width: 0; }
.mono, .d-pre { font-family: Consolas, monospace; font-size: 12px; }
.d-pre { margin: 8px 0 0; max-height: 360px; overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; line-height: 1.6; background: rgba(127, 127, 127, 0.06); border-radius: 8px; padding: 12px; }
.d-coverage, .tags { display: flex; flex-wrap: wrap; gap: 8px; }
.d-coverage { margin-top: 16px; color: var(--text2); font-size: 12px; }
.tags :deep(.ant-tag) { margin: 0; }
.d-sub-sec { margin-top: 16px; }
.metadata-list { display: flex; flex-direction: column; max-height: 240px; overflow: auto; }
.metadata-row { display: flex; flex-wrap: wrap; gap: 8px; padding: 8px 0; font-size: 12px; overflow-wrap: anywhere; }
.metadata-row:hover { background: rgba(127, 127, 127, 0.06); }
@media (max-width: 600px) {
  .d-fp { align-items: flex-start; flex-direction: column; }
  .d-grid { grid-template-columns: 1fr; }
  .d-pre { max-height: 280px; }
}
</style>
