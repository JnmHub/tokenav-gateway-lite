/* 账号编辑器平台预设数据（对照旧 credentialsBuilder.ts，契约一致）
 * - Header 覆写：资格平台/官方 CLI 模板（后端 IsHeaderOverrideEligible 对齐）
 * - grok 上游端点预设：只影响转发，不影响 OAuth 授权地址
 * - 国产平台端点预设：mode+protocol+url 三者必须配套保存，
 *   账号模式决定套餐、协议决定请求格式，拆开会把密钥发错端点 */

export const PLATFORMS = ['openai', 'anthropic', 'gemini', 'grok', 'antigravity', 'kimi', 'zhipu', 'deepseek', 'opencode_go']

// 平台显示名（表格标签/筛选下拉用）：opencode_go 在界面上统一展示为 OpenCode
export const PLATFORM_LABELS = { opencode_go: 'OpenCode' }
export const platformLabel = (p) => PLATFORM_LABELS[p] || p

/* ===== OpenCode（opencode_go）预设（等价移植官方 credentialsBuilder.ts，契约一致） =====
 * - 接入模式 account_mode：zen（Zen 按量付费）/ go（Coding Plan 订阅网关），未设置按 go 兼容
 * - API 协议 api_protocol：adaptive（默认，按模型分流）/ chat_completions / anthropic / responses
 * - 协议规则 protocol_rules：[{pattern, protocol}]，adaptive 时按通配 pattern 匹配原生协议，
 *   未命中回退 chat_completions；后端最多 64 条、pattern ≤128 字符（小写规范化） */
export const OPENCODE_BASE_URLS = {
  zen_chat: 'https://opencode.ai/zen/v1',
  zen_anthropic: 'https://opencode.ai/zen',
  go_chat: 'https://opencode.ai/zen/go/v1',
  go_anthropic: 'https://opencode.ai/zen/go',
}
// zen/go 两套内置协议规则表（对齐 opencode.ai/docs/zen 端点表；与后端 DefaultOpenCode*ProtocolRules 一致）
export const OPENCODE_DEFAULT_RULES = {
  go: [
    { pattern: 'grok-*', protocol: 'responses' },
    { pattern: 'gpt-*', protocol: 'responses' },
    { pattern: 'muse-spark-*', protocol: 'responses' },
    { pattern: 'minimax-*', protocol: 'anthropic' },
    { pattern: 'qwen*', protocol: 'anthropic' },
  ],
  zen: [
    { pattern: 'grok-*', protocol: 'responses' },
    { pattern: 'gpt-*', protocol: 'responses' },
    { pattern: 'muse-spark-*', protocol: 'responses' },
    { pattern: 'claude-*', protocol: 'anthropic' },
    { pattern: 'qwen*', protocol: 'anthropic' },
  ],
}
// OpenCode 账号模式解析：仅 'zen' 是 Zen，其余（含空/历史 coding）一律按 go
export const resolveOpenCodeMode = (v) => (v === 'zen' ? 'zen' : 'go')
export const defaultOpenCodeRules = (mode) => (OPENCODE_DEFAULT_RULES[resolveOpenCodeMode(mode)] || OPENCODE_DEFAULT_RULES.go).map((r) => ({ ...r }))
// 模式+协议 → 默认 base_url（responses 与 chat_completions 共用 /v1 基址，anthropic 不含 /v1）
export const defaultOpenCodeBaseUrl = (mode, protocol) => {
  const zen = resolveOpenCodeMode(mode) === 'zen'
  if (protocol === 'anthropic') return zen ? OPENCODE_BASE_URLS.zen_anthropic : OPENCODE_BASE_URLS.go_anthropic
  return zen ? OPENCODE_BASE_URLS.zen_chat : OPENCODE_BASE_URLS.go_chat
}
// 协议规则 → credentials.protocol_rules（官方 applyOpenCodeGoProtocolRules 语义：
// create 只在有条目时写键；edit 始终写键——空数组即表示清空自定义规则回到内置默认表）
export const serializeOpenCodeRules = (rules) => (rules || [])
  .map((r) => ({ pattern: String(r.pattern || '').trim().toLowerCase(), protocol: r.protocol }))
  .filter((r) => r.pattern && ['chat_completions', 'anthropic', 'responses'].includes(r.protocol))
  .reduce((acc, r) => { if (!acc.some((x) => x.pattern === r.pattern)) acc.push(r); return acc }, [])
// 保留导出：当前前端暂无调用方；后续与官方对齐 OpenCode 协议规则保存流程时复用，勿删
export const buildOpenCodeRulesCreds = (rules, mode) => {
  const serialized = serializeOpenCodeRules(rules).slice(0, 64)
  return { key: 'protocol_rules', value: serialized, write: serialized.length > 0 || mode === 'edit' }
}
// credentials.protocol_rules → 行数组（官方 parseOpenCodeGoProtocolRules 等价：非法条目跳过）
export const parseOpenCodeRules = (raw) => {
  if (raw == null) return null
  if (!Array.isArray(raw)) return defaultOpenCodeRules('go')
  const out = []
  for (const item of raw) {
    if (!item || typeof item !== 'object') continue
    const pattern = typeof item.pattern === 'string' ? item.pattern.trim() : ''
    if (!pattern || !['chat_completions', 'anthropic', 'responses'].includes(item.protocol)) continue
    out.push({ pattern, protocol: item.protocol })
  }
  return out
}

export const HEADER_PLATFORMS = ['anthropic', 'openai', 'grok', 'kimi', 'zhipu', 'deepseek']

const HDR_TEMPLATES = {
  openai: ['user-agent', 'originator', 'openai-beta', 'version', 'accept', 'accept-language'],
  grok: ['user-agent', 'x-xai-token-auth', 'x-grok-client-version'],
  anthropic: ['user-agent', 'x-app', 'anthropic-beta', 'anthropic-version', 'anthropic-dangerous-direct-browser-access', 'x-stainless-lang', 'x-stainless-package-version', 'x-stainless-os', 'x-stainless-arch', 'x-stainless-runtime', 'x-stainless-runtime-version', 'x-stainless-retry-count', 'x-stainless-timeout'],
}
// kimi/zhipu/deepseek 与 anthropic 共用 Claude CLI 模板（旧 getHeaderOverrideTemplate fallback）
export const hdrTemplateFor = (p) => (p === 'openai' ? HDR_TEMPLATES.openai : p === 'grok' ? HDR_TEMPLATES.grok : HDR_TEMPLATES.anthropic)

export const isHdrEligible = (p, t) => (p === 'grok' ? t === 'apikey' || t === 'oauth' : HEADER_PLATFORMS.includes(p) && t === 'apikey')

export const GROK_BASE_PRESETS = [
  { label: 'CLI', url: 'https://cli-chat-proxy.grok.com/v1' },
  { label: '官方', url: 'https://api.x.ai/v1' },
  { label: 'us-east-1', url: 'https://us-east-1.api.x.ai/v1' },
  { label: 'us-west-2', url: 'https://us-west-2.api.x.ai/v1' },
  { label: 'eu-west-1', url: 'https://eu-west-1.api.x.ai/v1' },
]

export const CN_PLATFORMS = ['kimi', 'zhipu', 'deepseek']

export const CN_BASE_PRESETS = {
  kimi: [
    { label: 'Moonshot', tag: '按量 · Chat', url: 'https://api.moonshot.cn/v1', mode: 'payg', protocol: 'chat_completions' },
    { label: 'Moonshot Anthropic', tag: '按量 · Claude', url: 'https://api.moonshot.cn/anthropic', mode: 'payg', protocol: 'anthropic' },
    { label: 'Kimi For Coding', tag: '套餐 · Chat', url: 'https://api.kimi.com/coding/v1', mode: 'coding', protocol: 'chat_completions' },
    { label: 'Kimi Coding Anthropic', tag: '套餐 · Claude', url: 'https://api.kimi.com/coding', mode: 'coding', protocol: 'anthropic' },
  ],
  zhipu: [
    { label: 'GLM PaaS', tag: '按量 · Chat', url: 'https://open.bigmodel.cn/api/paas/v4', mode: 'payg', protocol: 'chat_completions' },
    { label: 'GLM Anthropic', tag: '按量 · Claude', url: 'https://open.bigmodel.cn/api/anthropic', mode: 'payg', protocol: 'anthropic' },
    { label: 'GLM Coding', tag: '套餐 · Chat', url: 'https://open.bigmodel.cn/api/coding/paas/v4', mode: 'coding', protocol: 'chat_completions' },
    { label: 'GLM Coding Anthropic', tag: '套餐 · Claude', url: 'https://open.bigmodel.cn/api/anthropic', mode: 'coding', protocol: 'anthropic' },
  ],
  deepseek: [
    { label: 'DeepSeek', tag: '按量 · Chat', url: 'https://api.deepseek.com', mode: 'payg', protocol: 'chat_completions' },
    { label: 'DeepSeek Anthropic', tag: '按量 · Claude', url: 'https://api.deepseek.com/anthropic', mode: 'payg', protocol: 'anthropic' },
    { label: 'DeepSeek Responses', tag: '按量 · Responses', url: 'https://api.deepseek.com', mode: 'payg', protocol: 'responses' },
  ],
}

export const TIMEZONES = ['', 'Asia/Shanghai', 'UTC', 'America/Los_Angeles', 'America/New_York', 'Europe/London', 'Asia/Tokyo']

export const BEDROCK_REGIONS = ['us-east-1', 'us-west-2', 'eu-west-1', 'eu-central-1', 'ap-northeast-1', 'ap-southeast-1', 'global']
