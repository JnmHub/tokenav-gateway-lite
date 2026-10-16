/* ============================================================
 * 分组模型映射（model_mapping / weighted_model_mapping）工具层
 * 契约来源：旧 views/admin/groupsModelMapping.ts（逐函数等价迁移），
 * 后端：internal/handler/admin/group_handler.go ModelMapping/WeightedModelMapping
 *  - model_mapping: { 源模型: 目标模型 }，源模型允许末尾一个 *
 *  - weighted_model_mapping: { 源模型: [{model, weight}] }，仅 openai 分组
 *  - 两个映射的键互斥；分流目标是终端上游模型，不再叠加映射
 * ============================================================ */

// UI 行：{ from, to, split?, targets? }；split=true 时走加权分流
// 分流目标模型为 { model, weight }，weight 为 1~100 整数
export const mappingToRows = (mapping, weighted) => {
  const rows = []
  if (mapping) {
    for (const [from, to] of Object.entries(mapping)) {
      const source = String(from).trim()
      const target = String(to ?? '').trim()
      if (source && target) rows.push({ from: source, to: target })
    }
  }
  if (weighted) {
    for (const [from, targets] of Object.entries(weighted)) {
      const source = String(from).trim()
      const list = (targets ?? [])
        .map((t) => ({ model: String(t.model ?? '').trim(), weight: Number(t.weight) || 1 }))
        .filter((t) => t.model)
      if (source && list.length > 0) rows.push({ from: source, to: '', split: true, targets: list })
    }
  }
  return rows.sort((a, b) => a.from.localeCompare(b.from))
}

export const rowsToMappingRecord = (rows) => {
  const mapping = {}
  for (const row of rows) {
    if (row.split) continue
    const from = row.from.trim()
    const to = row.to.trim()
    if (from && to) mapping[from] = to
  }
  return mapping
}

export const rowsToWeightedRecord = (rows) => {
  const mapping = {}
  for (const row of rows) {
    if (!row.split) continue
    const from = row.from.trim()
    const targets = (row.targets ?? [])
      .map((t) => ({ model: t.model.trim(), weight: t.weight }))
      .filter((t) => t.model)
    if (from && targets.length > 0) mapping[from] = targets
  }
  return mapping
}

// 模型归属平台识别（对照旧 detectModelPlatform，与后端 DetectModelPlatform 前缀一致）
export const detectModelPlatform = (raw) => {
  let normalized = String(raw).trim().toLowerCase().replace(/^models\//, '')
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
  if (normalized === 'k3' || normalized === 'k3-256k' || normalized.startsWith('kimi-') || normalized.startsWith('moonshot-')) return 'kimi'
  if (normalized.startsWith('glm-')) return 'zhipu'
  if (normalized.startsWith('deepseek-')) return 'deepseek'
  return null
}

// 校验返回 null 或错误码；错误码→中文由 MAPPING_ERROR_MESSAGES 提供
export const validateMappingRows = (rows, platform) => {
  const sources = new Set()
  const exactMappings = new Map()
  for (const row of rows) {
    const from = row.from.trim().toLowerCase()
    if (row.split) {
      // 分流行的 to 为空是正常状态，只查：源必填、禁通配符键、键互斥
      if (!from) return 'incomplete'
      if (from.includes('*')) return 'sourceWildcard'
      if (sources.has(from)) return 'duplicate'
      sources.add(from)
      continue
    }
    const to = row.to.trim()
    if (!from && !to) continue
    if (!from || !to) return 'incomplete'
    const wildcardCount = [...from].filter((c) => c === '*').length
    if (wildcardCount > 1 || (wildcardCount === 1 && (!from.endsWith('*') || from === '*'))) return 'sourceWildcard'
    if (to.includes('*')) return 'targetWildcard'
    if (sources.has(from)) return 'duplicate'
    sources.add(from)
    const p = platform?.trim().toLowerCase()
    if (p && p !== 'composite' && p !== 'antigravity') {
      const sourcePlatform = detectModelPlatform(from.replace(/\*$/, ''))
      const targetPlatform = detectModelPlatform(to)
      if (targetPlatform && targetPlatform !== p) return 'crossPlatform'
      if (sourcePlatform && targetPlatform && sourcePlatform !== targetPlatform) return 'crossPlatform'
    }
    if (!from.endsWith('*')) exactMappings.set(from, to.trim().toLowerCase())
  }
  // 仅精确映射查环；通配符只执行一次，不参与链式递归
  for (const source of exactMappings.keys()) {
    const visited = new Set()
    let current = source
    while (exactMappings.has(current)) {
      if (visited.has(current)) return 'cycle'
      visited.add(current)
      current = exactMappings.get(current)
    }
  }
  return validateWeightedRows(rows, platform)
}

function validateWeightedRows(rows, platform) {
  const p = platform?.trim().toLowerCase()
  const splitEnabled = p === 'openai'
  for (const row of rows) {
    if (!row.split) continue
    const from = row.from.trim().toLowerCase()
    if (!from) continue
    if (!splitEnabled) return 'splitPlatform'
    const targets = row.targets ?? []
    const filled = targets.filter((t) => t.model.trim())
    if (filled.length !== targets.length) return 'splitTargetsIncomplete'
    if (filled.length < 2) return 'splitTargetsTooFew'
    if (filled.length > 8) return 'splitTargetsTooMany'
    const seen = new Set()
    for (const target of filled) {
      const model = target.model.trim()
      if (!Number.isInteger(target.weight) || target.weight < 1 || target.weight > 100) return 'splitWeightRange'
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

// 错误码 → 中文提示（对照旧 i18n admin.groups.modelMapping.*）
export const MAPPING_ERROR_MESSAGES = {
  incomplete: '模型映射的源模型和目标模型必须同时填写',
  duplicate: '同一个源模型只能配置一次映射（不区分大小写）',
  sourceWildcard: '源模型只允许在末尾使用一个 *，且 * 前必须有内容',
  targetWildcard: '目标模型必须是具体模型，不能包含 *',
  crossPlatform: '普通分组不能把模型映射到其他厂商，请使用同平台目标模型',
  cycle: '模型映射不能形成循环，请检查互相指向或指向自身的规则',
  splitTargetsIncomplete: '分流目标的模型必须全部填写完整',
  splitTargetsTooFew: '智能分流至少需要 2 个目标模型',
  splitTargetsTooMany: '智能分流最多 8 个目标模型',
  splitWeightRange: '分流权重必须是 1 到 100 的整数',
  splitDuplicateTarget: '同一个分流里目标模型不能重复',
  splitChain: '分流目标不能与自身的源模型相同：那等于没有分流',
  splitPlatform: '智能分流仅支持 openai 分组，目标必须是 openai 模型',
}

// 定价规则里的模型模式冲突检测（对照旧 channels/types.ts findModelConflict）
const toModelPattern = (model) => {
  const lower = model.toLowerCase()
  const wildcard = lower.endsWith('*')
  return { pattern: model, prefix: wildcard ? lower.slice(0, -1) : lower, wildcard }
}

export const findModelConflict = (models) => {
  const patterns = models.map(toModelPattern)
  for (let i = 0; i < patterns.length; i++) {
    for (let j = i + 1; j < patterns.length; j++) {
      const a = patterns[i]; const b = patterns[j]
      const hit = (!a.wildcard && !b.wildcard) ? a.prefix === b.prefix
        : (a.wildcard && !b.wildcard) ? b.prefix.startsWith(a.prefix)
        : (!a.wildcard && b.wildcard) ? a.prefix.startsWith(b.prefix)
        : a.prefix.startsWith(b.prefix) || b.prefix.startsWith(a.prefix)
      if (hit) return [a.pattern, b.pattern]
    }
  }
  return null
}
