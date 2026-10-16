import { aiCategories, presentAIDecision } from './riskControl.js'

export const isAILog = (row) => !!row?.ai_review_detail || String(row?.action || '').startsWith('ai_')
export const formatRiskTime = (value) => value ? String(value).replace('T', ' ').slice(0, 19) : '—'
export const formatRiskPercent = (value) => Number.isFinite(value) ? `${(value * 100).toFixed(1)}%` : '—'
export const formatRiskMS = (value) => Number.isFinite(value) && value >= 0 ? `${value} ms` : '未知'

const ACTION_META = {
  block: { label: '已拦截 · 内容审计', color: 'red' },
  hash_block: { label: '已拦截 · 历史指纹', color: 'red' },
  keyword_block: { label: '已拦截 · 关键词', color: 'red' },
  language_block: { label: '已拦截 · 语种', color: 'red' },
  cyber_policy: { label: '已拦截 · 上游安全策略', color: 'red' },
  keyword_audit: { label: '已命中 · 仅审计', color: 'blue' },
  keyword_reroute: { label: '已命中 · 已切组放行', color: 'purple' },
  keyword_allow_original: { label: '已命中 · 原组放行', color: 'green' },
  error: { label: '异常', color: 'orange' },
}
export function riskLogResult(row) {
  if (isAILog(row)) {
    const decision = presentAIDecision(row.ai_review_detail)
    if (decision.label === '未判定') return { label: row.ai_review_detail?.verdict === 'error' || row.ai_review_detail?.error_code ? 'AI 未判定 · 请求拒绝' : 'AI 未判定 · 历史信息不足', color: 'orange' }
    if (row.ai_review_detail?.phase === 'skipped') return { label: 'AI 跳过审查 · 放行', color: 'blue' }
    if (String(row.action || '').startsWith('ai_shadow_')) return row.ai_review_detail?.verdict === 'block'
      ? { label: 'AI 观察 · 违规但未处罚', color: 'blue' } : { label: 'AI 观察 · 通过', color: 'green' }
    return row.ai_review_detail?.verdict === 'block' ? { label: 'AI 违规 · 已拦截', color: 'red' } : { label: 'AI 通过', color: 'green' }
  }
  if (row.action === 'allow') return { label: row.flagged ? '已命中 · 观察放行' : '未命中', color: row.flagged ? 'blue' : 'green' }
  if (ACTION_META[row.action]) return ACTION_META[row.action]
  if (row.error) return { label: '异常', color: 'orange' }
  return { label: row.flagged ? '已命中' : '未命中', color: row.flagged ? 'pink' : 'green' }
}

export function riskLogCategories(row) {
  // 整体 AI 判定的类别是标签；旧 category_scores 的 0/1 标记不代表概率。
  if (isAILog(row)) return aiCategories(row.ai_review_detail)
  return Object.keys(row?.category_scores || {})
}
export function riskLogInputText(row) {
  if (typeof row?.full_input_text !== 'string') return '该记录未保存完整提取文本'
  return row.full_input_text || '未提取到可审查文本'
}
export function riskLogReviewText(row) {
  if (row?.ai_review_detail?.provider_called === false) return '未调用模型，无实际送审文本'
  if (typeof row?.review_text_excerpt !== 'string' || !row.review_text_excerpt) return '该记录未保存实际送审文本'
  // review_text_excerpt 沿用接口键名，但值为完整送审文本；这里不能再截断或回退到错误信息。
  return row.review_text_excerpt
}
