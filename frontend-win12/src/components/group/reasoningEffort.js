/* ============================================================
 * 推理力度策略（max_reasoning_effort / reasoning_effort_mappings）助手（二期 B）
 * 契约来源：internal/service/openai_reasoning_effort_policy.go
 *  - 枚举固定：minimal / low / medium / high / xhigh / max，仅 openai 与
 *    composite 平台支持；空字符串 = 不设上限（跟随客户端）
 *  - 映射行：把客户端显式提交的某个档位精确改写为另一档位；from 唯一，
 *    后端上限 64 条（超限 400），保存前在前端做同规则前置校验
 * ============================================================ */

export const REASONING_EFFORT_VALUES = ['minimal', 'low', 'medium', 'high', 'xhigh', 'max']
export const MAX_REASONING_EFFORT_MAPPINGS = 64

// 下拉选项：值 + 中文注记（保持后端原值作为提交内容）
export const REASONING_EFFORT_OPTIONS = [
  { value: 'minimal', label: 'minimal（最省）' },
  { value: 'low', label: 'low（低）' },
  { value: 'medium', label: 'medium（中）' },
  { value: 'high', label: 'high（高）' },
  { value: 'xhigh', label: 'xhigh（超高）' },
  { value: 'max', label: 'max（顶格）' },
]

export const supportsReasoningEffort = (platform) =>
  platform === 'openai' || platform === 'composite'

// 规范化：不支持的值一律归空（与后端 normalizeMaxReasoningEffortForPlatform 同语义）
export const normalizeReasoningEffort = (platform, value) => {
  const v = String(value ?? '').trim().toLowerCase()
  return supportsReasoningEffort(platform) && REASONING_EFFORT_VALUES.includes(v) ? v : ''
}

let rowSeq = 0
export const createEffortRow = (from = '', to = '') => ({
  id: `reasoning-effort-row-${++rowSeq}`,
  from,
  to,
})

// 档位值规范化：仅按枚举判断（行数据只在 openai/composite 表单里出现）
const normalizeEffortValue = (value) => {
  const v = String(value ?? '').trim().toLowerCase()
  return REASONING_EFFORT_VALUES.includes(v) ? v : ''
}

// API [{from,to}] → UI 行：无效档位行直接丢弃（对照旧 reasoningEffortMappingsToRows）
export const reasoningEffortToRows = (mappings) =>
  (mappings ?? [])
    .map((m) => createEffortRow(normalizeEffortValue(m?.from), normalizeEffortValue(m?.to)))
    .filter((row) => row.from && row.to)

// UI 行 → API：仅 trim，平台过滤交给 buildBody（空数组 = 清空全部映射）
export const reasoningEffortRowsToApi = (rows) =>
  (rows ?? []).map((row) => ({ from: row.from.trim(), to: row.to.trim() }))

// 保存前置校验：from/to 必填且源档位唯一；返回 null 或错误文案
export const validateReasoningEffortRows = (rows) => {
  const seen = new Set()
  for (let i = 0; i < (rows ?? []).length; i++) {
    const from = (rows[i].from ?? '').trim()
    const to = (rows[i].to ?? '').trim()
    if (!from) return `第 ${i + 1} 行的客户端档位不能为空`
    if (!to) return `第 ${i + 1} 行的改写档位不能为空`
    if (seen.has(from)) return `第 ${i + 1} 行的客户端档位「${from}」重复，每个档位只能映射一次`
    seen.add(from)
  }
  return (rows ?? []).length > MAX_REASONING_EFFORT_MAPPINGS
    ? `映射条数不能超过 ${MAX_REASONING_EFFORT_MAPPINGS} 条`
    : null
}
