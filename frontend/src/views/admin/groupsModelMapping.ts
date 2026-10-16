export interface WeightedTargetRow {
  model: string
  weight: number
}

export interface GroupModelMappingRow {
  from: string
  to: string
  // 智能分流行：true 时 targets 生效、to 被忽略；仅 openai 分组可用。
  split?: boolean
  targets?: WeightedTargetRow[]
}

export type GroupModelMappingValidationError =
  | 'incomplete'
  | 'duplicate'
  | 'sourceWildcard'
  | 'targetWildcard'
  | 'crossPlatform'
  | 'cycle'
  | 'splitTargetsIncomplete'
  | 'splitTargetsTooFew'
  | 'splitTargetsTooMany'
  | 'splitWeightRange'
  | 'splitDuplicateTarget'
  | 'splitChain'
  | 'splitPlatform'

export function groupModelMappingToRows(
  mapping?: Record<string, string> | null,
  weighted?: Record<string, { model: string; weight: number }[]> | null,
): GroupModelMappingRow[] {
  const rows: GroupModelMappingRow[] = []
  if (mapping) {
    for (const [from, to] of Object.entries(mapping)) {
      const source = from.trim()
      const target = String(to).trim()
      if (source && target) rows.push({ from: source, to: target })
    }
  }
  if (weighted) {
    for (const [from, targets] of Object.entries(weighted)) {
      const source = from.trim()
      const list = (targets ?? [])
        .map((target) => ({ model: String(target.model ?? '').trim(), weight: Number(target.weight) || 1 }))
        .filter((target) => target.model)
      if (source && list.length > 0) {
        rows.push({ from: source, to: '', split: true, targets: list })
      }
    }
  }
  return rows.sort((left, right) => left.from.localeCompare(right.from))
}

export function validateGroupModelMappingRows(
  rows: GroupModelMappingRow[],
  platform?: string,
): GroupModelMappingValidationError | null {
  const sources = new Set<string>()
  const exactMappings = new Map<string, string>()
  for (const row of rows) {
    const from = row.from.trim().toLowerCase()
    if (row.split) {
      // 智能分流行的 to 为空是正常状态，不能套用单目标的 incomplete 规则；
      // 这里只管源模型必填、禁止通配符键（后端要求精确键）和与单目标键互斥。
      if (!from) return 'incomplete'
      if (from.includes('*')) return 'sourceWildcard'
      if (sources.has(from)) return 'duplicate'
      sources.add(from)
      continue
    }
    const to = row.to.trim()
    if (!from && !to) continue
    if (!from || !to) return 'incomplete'
    const wildcardCount = [...from].filter((character) => character === '*').length
    if (wildcardCount > 1 || (wildcardCount === 1 && (!from.endsWith('*') || from === '*'))) {
      return 'sourceWildcard'
    }
    if (to.includes('*')) return 'targetWildcard'
    if (sources.has(from)) return 'duplicate'
    sources.add(from)
    const normalizedPlatform = platform?.trim().toLowerCase()
    if (normalizedPlatform && normalizedPlatform !== 'composite' && normalizedPlatform !== 'antigravity') {
      const sourcePlatform = detectModelPlatform(from.replace(/\*$/, ''))
      const targetPlatform = detectModelPlatform(to)
      if (targetPlatform && targetPlatform !== normalizedPlatform) return 'crossPlatform'
      if (sourcePlatform && targetPlatform && sourcePlatform !== targetPlatform) return 'crossPlatform'
    }
    if (!from.endsWith('*')) exactMappings.set(from, to.trim().toLowerCase())
  }

  // 只检查精确映射链；通配符只执行一次，不参与链式递归。
  for (const source of exactMappings.keys()) {
    const visited = new Set<string>()
    let current = source
    while (exactMappings.has(current)) {
      if (visited.has(current)) return 'cycle'
      visited.add(current)
      current = exactMappings.get(current)!
    }
  }
  return validateWeightedRows(rows, sources, platform)
}

// 智能分流行校验：键互斥由外层 sources 保证；这里管目标数量、权重范围、
// 重复目标、自指（目标等于自身源等于没分流）与平台归属。分流目标是终端
// 上游模型（选中后直接按它选账号，不再叠加任何映射），所以目标允许与
// 其他映射的公开键同名，不做"防链式"拦截。
function validateWeightedRows(
  rows: GroupModelMappingRow[],
  _sources: Set<string>,
  platform?: string,
): GroupModelMappingValidationError | null {
  const normalizedPlatform = platform?.trim().toLowerCase()
  const splitEnabled = normalizedPlatform === 'openai'
  for (const row of rows) {
    if (!row.split) continue
    const from = row.from.trim().toLowerCase()
    if (!from) continue
    if (!splitEnabled) return 'splitPlatform'
    const targets = row.targets ?? []
    const filled = targets.filter((target) => target.model.trim())
    if (filled.length !== targets.length) return 'splitTargetsIncomplete'
    if (filled.length < 2) return 'splitTargetsTooFew'
    if (filled.length > 8) return 'splitTargetsTooMany'
    const seen = new Set<string>()
    for (const target of filled) {
      const model = target.model.trim()
      if (!Number.isInteger(target.weight) || target.weight < 1 || target.weight > 100) {
        return 'splitWeightRange'
      }
      const key = model.toLowerCase()
      if (seen.has(key)) return 'splitDuplicateTarget'
      seen.add(key)
      if (key === from) return 'splitChain'
      const targetPlatform = detectModelPlatform(model)
      if (targetPlatform && targetPlatform !== 'openai') return 'splitPlatform'
    }
  }
  return null
}

function detectModelPlatform(model: string): string | null {
  let normalized = model.trim().toLowerCase().replace(/^models\//, '')
  const slashIndex = normalized.indexOf('/')
  if (slashIndex > 0) {
    const provider = normalized.slice(0, slashIndex).trim()
    const rest = normalized.slice(slashIndex + 1).trim()
    if (['anthropic', 'claude'].includes(provider)) return 'anthropic'
    if (['openai', 'chatgpt'].includes(provider)) return 'openai'
    if (['google', 'google-ai-studio', 'gemini'].includes(provider)) return 'gemini'
    if (['xai', 'x-ai', 'grok'].includes(provider)) return 'grok'
    if (['kimi', 'moonshot'].includes(provider)) return 'kimi'
    if (['zhipu', 'glm', 'bigmodel'].includes(provider)) return 'zhipu'
    if (provider === 'deepseek') return 'deepseek'
    normalized = rest.replace(/^models\//, '')
  }
  if (normalized.startsWith('anthropic.claude-') || normalized.startsWith('claude-')) return 'anthropic'
  if (normalized.startsWith('gemini-') || normalized.startsWith('learnlm-')) return 'gemini'
  if (normalized === 'grok' || normalized.startsWith('grok-')) return 'grok'
  if (/^(gpt-|chatgpt-|codex-|text-embedding-|text-moderation-|omni-moderation-|dall-e-|gpt-image-|tts-|whisper-)/.test(normalized)) return 'openai'
  if (/^o[1345](?:-|$)/.test(normalized)) return 'openai'
  // 与后端 DetectModelPlatform 保持一致：glm-/deepseek-/kimi- 等前缀都能识别出
  // 具体厂商；漏掉会导致前端放行、后端 400，用户只能看到笼统的保存失败。
  if (normalized === 'k3' || normalized === 'k3-256k' || normalized.startsWith('kimi-') || normalized.startsWith('moonshot-')) return 'kimi'
  if (normalized.startsWith('glm-')) return 'zhipu'
  if (normalized.startsWith('deepseek-')) return 'deepseek'
  return null
}

export function groupModelMappingRowsToRecord(
  rows: GroupModelMappingRow[],
): Record<string, string> {
  const mapping: Record<string, string> = {}
  for (const row of rows) {
    if (row.split) continue
    const from = row.from.trim()
    const to = row.to.trim()
    if (from && to) mapping[from] = to
  }
  return mapping
}

export function groupWeightedMappingRowsToRecord(
  rows: GroupModelMappingRow[],
): Record<string, { model: string; weight: number }[]> {
  const mapping: Record<string, { model: string; weight: number }[]> = {}
  for (const row of rows) {
    if (!row.split) continue
    const from = row.from.trim()
    const targets = (row.targets ?? [])
      .map((target) => ({ model: target.model.trim(), weight: target.weight }))
      .filter((target) => target.model)
    if (from && targets.length > 0) mapping[from] = targets
  }
  return mapping
}
