import { request, ApiError } from './index.js'

export const RISK_PATH = '/admin/risk-control'
// 只提交 handler 声明的可写字段，派生状态、掩码和能力声明不属于配置写入。
export const RISK_FIELDS = [
  'enabled', 'mode', 'base_url', 'model', 'timeout_ms', 'sample_rate', 'all_groups', 'group_ids',
  'record_non_hits', 'thresholds', 'worker_count', 'queue_size', 'block_status', 'block_message',
  'email_on_hit', 'auto_ban_enabled', 'ban_threshold', 'deduct_balance_on_block_enabled',
  'deduct_balance_on_block_amount', 'permanent_ban_enabled', 'permanent_ban_threshold',
  'violation_window_hours', 'cyber_policy_exclude_from_ban_count', 'retry_count', 'hit_retention_days',
  'non_hit_retention_days', 'blocked_keywords', 'keyword_blocking_mode', 'keyword_rule_packs',
  'model_filter', 'allowed_languages', 'scan_latest_user_input', 'scan_top_level_instructions',
  'scan_new_skill_content', 'scan_new_tool_output', 'keyword_group_routing_enabled',
]
export const AI_FIELDS = [
  'adapter', 'base_url', 'model', 'model_revision', 'external_data_consent', 'policy_version', 'policy',
  'penalties_enabled', 'image_review_enabled',
]
export const SYNTHETIC_CASES = [
  { value: 'allow', label: '安全问候', description: '合成日常问候，预期通过。' },
  { value: 'block', label: '违规请求', description: '合成暴力请求，预期违规。' },
  { value: 'injection', label: '指令注入', description: '合成伪造系统标签，检查审查策略是否被改变。' },
  { value: 'long_tail', label: '长文尾部违规', description: '合成安全长文与违规尾部，检查完整覆盖。' },
  { value: 'unsupported_media', label: '图片链接放行', description: '合成带 http 图片链接的请求，预期记录未覆盖并照常审查。' },
  { value: 'uncovered_field', label: '未知字段放行', description: '合成带未知扩展字段的请求，预期记录未覆盖并照常审查。' },
]
const clone = (value) => value === undefined ? undefined : JSON.parse(JSON.stringify(value))
const changed = (a, b) => JSON.stringify(a) !== JSON.stringify(b)

export function buildRiskConfigPatch(draft, saved, { apiKey = '', clearAPIKey = false, fields = RISK_FIELDS } = {}) {
  const body = {}
  for (const field of fields) {
    if (RISK_FIELDS.includes(field) && draft[field] !== undefined && changed(draft[field], saved?.[field])) body[field] = clone(draft[field])
  }
  if (fields === RISK_FIELDS && draft.ai) {
    const ai = {}
    for (const field of AI_FIELDS) {
      if (draft.ai[field] !== undefined && changed(draft.ai[field], saved?.ai?.[field])) ai[field] = clone(draft.ai[field])
    }
    // 留空密钥省略保留，删除必须显式勾选；掩码永远不充当秘密。
    if (clearAPIKey) ai.clear_api_key = true
    else if (apiKey.trim()) ai.api_key = apiKey.trim()
    if (Object.keys(ai).length) body.ai = ai
  }
  return body
}

export function mergeRiskConfigResponse(response, current, submitted, fields = RISK_FIELDS) {
  const next = fields === RISK_FIELDS ? clone(response) : clone(current)
  for (const field of fields) {
    if (!RISK_FIELDS.includes(field)) continue
    // 保存期间新编辑的值保持草稿；没再改过的字段采用后台规范化值。
    next[field] = clone(changed(current[field], submitted[field]) ? current[field] : response[field])
  }
  if (fields === RISK_FIELDS && response.ai) {
    for (const field of AI_FIELDS) {
      if (changed(current.ai?.[field], submitted.ai?.[field])) next.ai[field] = clone(current.ai?.[field])
    }
  }
  return next
}

export function assertRiskConfig(config) {
  if (!config || !['keyword_only', 'ai_only'].includes(config.keyword_blocking_mode) || !config.ai) {
    throw new ApiError('后台未返回完整审查配置，请确认后端版本后重试', { payload: { reason: 'RISK_CONFIG_INCOMPLETE' } })
  }
  return config
}
export async function readRiskConfig(options = {}) {
  return assertRiskConfig(await request(`${RISK_PATH}/config`, { ...options, method: 'GET' }))
}
export async function writeRiskConfig(body, options = {}) {
  return assertRiskConfig(await request(`${RISK_PATH}/config`, { ...options, method: 'PUT', body }))
}
export function testRiskAI(caseId, { configFingerprint, ...options } = {}) {
  if (!SYNTHETIC_CASES.some((item) => item.value === caseId)) throw new ApiError('请选择内置合成案例')
  const body = { case_id: caseId }
  if (configFingerprint) body.config_fingerprint = configFingerprint
  return request(`${RISK_PATH}/ai/test`, { ...options, method: 'POST', body })
}
export const readRiskMetrics = (options = {}) => request(`${RISK_PATH}/ai/metrics`, { ...options, method: 'GET' })
// 列表只取预览；管理员打开某条记录时才读取脱敏后的完整提取文本和实际送审文本。
export const readRiskLog = (id, options = {}) => request(`${RISK_PATH}/logs/${encodeURIComponent(id)}`, { ...options, method: 'GET' })

// 跳过无可审查输入可以放行，但没有模型调用与完整覆盖，不能冒充审查通过。
const isAIReviewComplete = (result) => ['allow', 'block'].includes(result?.verdict) && result.phase === 'completed' && result.provider_called === true && result.coverage?.complete === true && (!result.error_code || result.verdict === 'block')
export function presentAIDecision(result) {
  if (!result || !['allow', 'block', 'error'].includes(result.verdict)) return { tone: 'warn', label: '未判定', reason: '后台未返回有效判定' }
  // 已完成的 block 仍可能附带处罚投递错误，不能把有效违规改报成未判定。
  if (result.verdict === 'block' && isAIReviewComplete(result)) return { tone: 'bad', label: '违规', reason: result.error_code ? '模型整体判定违规；后续处置异常，处罚结果需核实' : '模型整体判定违规' }
  if (result.verdict === 'error' || result.error_code) return { tone: 'warn', label: '未判定', reason: aiErrorLabel(result.error_code, result.provider_http_status) }
  if (result.verdict === 'allow' && result.provider_called === false && result.phase === 'skipped') return { tone: '', label: '跳过审查 · 放行', reason: '没有可审查的文本或图片，未调用模型' }
  if (!isAIReviewComplete(result)) return { tone: 'warn', label: '未判定', reason: '尚无完整的模型审查结果' }
  return { tone: 'good', label: '通过', reason: '模型已完成整体判定' }
}
export function presentAICoverage(result) {
  if (result?.phase === 'skipped' || result?.provider_called === false) return { tone: '', label: '未送审' }
  return isAIReviewComplete(result) ? { tone: '', label: '已选内容已完成审查' } : { tone: 'warn', label: '未完成 / 未知' }
}
export const aiCallLabel = (called) => called === true ? '已调用模型' : called === false ? '未调用模型' : '调用状态未知'
export const aiPhaseLabel = (phase) => ({ submission: '提交检查', preparation: '准备送审内容', provider: '调用模型服务', completed: '处理完成', skipped: '无可审查输入，已跳过' }[phase] || (phase ? `其他阶段（${phase}）` : '阶段未知'))
export const aiCategories = (result) => Array.isArray(result?.categories) ? result.categories.filter((item) => typeof item === 'string' && item.trim()) : []
const AI_ERRORS = {
  ai_not_configured: '未配置专用审查密钥', ai_external_data_not_authorized: '未授权向外部模型发送审查内容',
  ai_deadline: '原请求已超时，审查已停止', ai_canceled: '原请求已取消，审查已停止', ai_cancelled: '原请求已取消，审查已停止',
  ai_transport: '无法连接专用审查服务，请检查地址和网络',
  ai_unsupported_protocol: '当前协议不受支持', ai_unsupported_media: '媒体内容未覆盖',
  ai_invalid_config: '审查配置无效', ai_invalid_endpoint: '专用服务地址无效',
  ai_provider_invalid_credential: '专用服务拒绝了密钥，请重新核实密钥', ai_provider_forbidden: '专用服务拒绝访问，请核实账户或模型权限',
  ai_provider_not_found: '专用接口或模型未找到，请核对当前请求路径与模型；此结果不能证明密钥无效',
  ai_provider_invalid_request: '专用服务不接受当前请求，请核对模型与协议能力', ai_provider_rate_limit: '专用服务请求受限，请检查额度或稍后重试',
  ai_provider_http_error: '专用服务返回错误，请根据 HTTP 状态检查服务',
  ai_runtime_unavailable: '审查处理暂时不可用，请核对运行状态', ai_request_encoding: '送审内容构造失败',
  ai_penalty_store_unavailable: '处罚存储不可用，处罚结果需核实', ai_penalty_identity_conflict: '处罚事件信息冲突，处罚结果需核实',
  ai_penalty_subject_unsupported: '当前请求没有可用于处罚的主站接口密钥身份',
  ai_response_incomplete_or_action: '模型响应不完整或包含不允许的工具 / 动作', ai_response_choices: '模型响应数量不符合审查要求',
  ai_response_shape: '专用服务响应格式不符合要求', ai_response_read: '模型响应读取失败',
  ai_invalid_result_json: '模型未返回有效的结构化审查结果', ai_result_schema: '审查结果须符合四字段结构且策略版本一致',
  ai_result_category: '审查结果包含无效类别', ai_result_inconsistent: '审查结果前后矛盾',
  ai_test_config_changed: '后台配置已变化，本次未调用服务；请重新读取并测试', ai_test_unexpected_verdict: '合成案例判定未达到预期，请核实审查策略与模型',
}
export function aiErrorLabel(code, httpStatus) {
  const label = AI_ERRORS[code] || '审查暂时不可用，请核对配置或稍后重试'
  return Number.isInteger(httpStatus) && httpStatus >= 100 && httpStatus <= 599 ? `${label}（HTTP ${httpStatus}）` : label
}

export const riskModeLabel = (mode) => ({ off: '关闭', observe: '观察（只记录）', shadow: '观察（有效判定不拦截，审查失败拒绝）', pre_block: '同步拦截' }[mode] || '未知模式')

export const AI_SERVICE_PRESETS = [
  { value: 'deepseek_chat', label: 'DeepSeek · 对话接口' },
  { value: 'deepseek_responses', label: 'DeepSeek · 响应接口' },
  { value: 'custom', label: '自定义服务' },
]
export const AI_MODEL_OPTIONS = [
  { value: 'deepseek-flash', label: 'deepseek-flash（默认快速模型）' },
  { value: 'deepseek-v4-pro', label: 'deepseek-v4-pro（专业模型）' },
  { value: 'custom', label: '自定义请求模型名' },
]
export function applyDeepSeekPreset(ai, adapter) {
  // 预设只填协议、地址和请求模型；账户权限与外发授权仍需管理员核实。
  Object.assign(ai, { adapter, base_url: 'https://api.deepseek.com', model: 'deepseek-flash', model_revision: 'unverified' })
}
export function aiEndpoint(ai) {
  if (!ai?.base_url) return ''
  try {
    const url = new URL(ai.base_url)
    if (!['http:', 'https:'].includes(url.protocol) || !url.hostname || url.username || url.password || url.search || url.hash) return ''
    url.pathname = `${url.pathname.replace(/\/+$/, '')}/${ai.adapter === 'deepseek_responses' ? 'responses' : 'chat/completions'}`
    return url.toString()
  } catch { return '' }
}

export function presentAITest(test, fingerprint) {
  if (!test || !fingerprint) return { tone: 'warn', label: '测试状态未知' }
  if (test.config_fingerprint !== fingerprint || test.status === 'stale') return { tone: 'warn', label: '配置已变化，旧测试已过期（可选重测）' }
  return ({
    not_tested: { tone: '', label: '已保存，尚未测试' }, running: { tone: '', label: '正在测试已保存配置' },
    passed: { tone: 'good', label: '当前配置合成测试通过' }, failed: { tone: 'warn', label: '当前配置测试未通过' },
  })[test.status] || { tone: 'warn', label: '测试状态未知' }
}
export function presentAIReadiness(metrics, saved) {
  const fingerprint = saved?.ai?.config_fingerprint
  if (!metrics || !fingerprint || metrics.config_fingerprint !== fingerprint) return { known: false, tone: 'warn', label: '当前配置状态待重新读取', missing: [] }
  if (!metrics.production || !Array.isArray(metrics.production.checks)) return { known: false, tone: 'warn', label: '后台未提供生产运行条件', missing: [] }
  return { known: true, tone: metrics.production.ready ? 'good' : 'warn', label: metrics.production.ready ? '生产条件已满足，实际请求仍检查作用范围' : '生产运行条件未满足', missing: metrics.production.checks.filter((item) => item.ready !== true) }
}
export function isCurrentAITestResult(result, fingerprint) {
  return !!fingerprint && result?.config_fingerprint === fingerprint && result?.test?.config_fingerprint === fingerprint && result.test.status !== 'stale'
}
export const legacyKeyStatusLabel = (status) => ({ available: '可用', ok: '可用', frozen: '暂时冷却（本进程）', unknown: '未测试', unavailable: '暂不可用' }[status] || '状态未知')
export function legacyKeyErrorLabel(item) {
  const code = item?.last_http_status
  if (code === 404) return '旧 /v1/moderations 接口未找到；不能用于判定专用 AI 能力'
  if (code === 401) return '旧审核接口拒绝密钥，请核实旧密钥'
  if (code === 403) return '旧审核接口拒绝访问，请核实权限'
  if (code === 429 || code === 529) return '旧审核接口限流或额度不足'
  if (code >= 500) return '旧审核服务暂时不可用'
  if (code >= 300) return `旧审核接口返回 HTTP ${code}`
  return item?.last_error ? '旧审核请求失败，请检查配置或网络' : '无错误记录'
}
