export function applyInterceptWarmup(
  credentials: Record<string, unknown>,
  enabled: boolean,
  mode: 'create' | 'edit'
): void {
  if (enabled) {
    credentials.intercept_warmup_requests = true
  } else if (mode === 'edit') {
    delete credentials.intercept_warmup_requests
  }
}

export const ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY = 'antigravity_project_id'

export function applyAntigravityProjectID(
  credentials: Record<string, unknown>,
  projectId: string,
  mode: 'create' | 'edit'
): void {
  const trimmed = projectId.trim()
  if (trimmed) {
    credentials[ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY] = trimmed
  } else if (mode === 'edit') {
    delete credentials[ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY]
  }
}

// ========== 请求头覆写（Anthropic/OpenAI API Key + Grok API Key/OAuth） ==========

export const HEADER_OVERRIDE_ENABLED_CREDENTIAL_KEY = 'header_override_enabled'
export const HEADER_OVERRIDES_CREDENTIAL_KEY = 'header_overrides'

export interface HeaderOverrideRow {
  name: string
  value: string
}

/** 请求头覆写支持的平台×账号类型（与后端 IsHeaderOverrideEligible 保持一致） */
export function isHeaderOverridePlatform(platform: string, accountType = 'apikey'): boolean {
  if (platform === 'grok') return accountType === 'apikey' || accountType === 'oauth'
  return accountType === 'apikey' && (
    platform === 'anthropic' ||
    platform === 'openai' ||
    platform === 'kimi' ||
    platform === 'zhipu' ||
    platform === 'deepseek'
  )
}

/** 禁止覆写的请求头（与后端 headerOverrideBlockedNames 保持一致） */
const HEADER_OVERRIDE_BLOCKED_NAMES = new Set([
  'host',
  'content-length',
  'content-type',
  'transfer-encoding',
  'connection',
  'keep-alive',
  'proxy-authenticate',
  'proxy-authorization',
  'proxy-connection',
  'te',
  'trailer',
  'upgrade',
  'authorization',
  'x-api-key',
  'x-goog-api-key',
  'cookie',
  'accept-encoding',
  'sec-websocket-key',
  'sec-websocket-version',
  'sec-websocket-extensions',
  'sec-websocket-protocol',
  'sec-websocket-accept',
  'session_id',
  'conversation_id',
  'x-codex-turn-state',
  'x-codex-turn-metadata',
  'chatgpt-account-id',
  'x-claude-code-session-id',
  'x-client-request-id',
  'x-grok-conv-id'
])

/** RFC 7230 token：合法的 HTTP header 名称字符集 */
const HEADER_NAME_PATTERN = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/

function isValidHeaderOverrideName(name: string): boolean {
  return HEADER_NAME_PATTERN.test(name)
}

/** 模板：Claude Code CLI 标准客户端请求头（值留空由管理员填写） */
const ANTHROPIC_HEADER_OVERRIDE_TEMPLATE = [
  'user-agent',
  'x-app',
  'anthropic-beta',
  'anthropic-version',
  'anthropic-dangerous-direct-browser-access',
  'x-stainless-lang',
  'x-stainless-package-version',
  'x-stainless-os',
  'x-stainless-arch',
  'x-stainless-runtime',
  'x-stainless-runtime-version',
  'x-stainless-retry-count',
  'x-stainless-timeout'
]

/** 模板：Codex CLI 标准客户端请求头（值留空由管理员填写） */
const OPENAI_HEADER_OVERRIDE_TEMPLATE = [
  'user-agent',
  'originator',
  'openai-beta',
  'version',
  'accept',
  'accept-language'
]

const GROK_HEADER_OVERRIDE_TEMPLATE = ['user-agent', 'x-xai-token-auth', 'x-grok-client-version']

export function getHeaderOverrideTemplate(platform: string): HeaderOverrideRow[] {
  const names =
    platform === 'openai'
      ? OPENAI_HEADER_OVERRIDE_TEMPLATE
      : platform === 'grok'
        ? GROK_HEADER_OVERRIDE_TEMPLATE
        : ANTHROPIC_HEADER_OVERRIDE_TEMPLATE
  return names.map((name) => ({ name, value: '' }))
}

/** 与后端 maxHeaderOverride* 常量保持一致 */
const HEADER_OVERRIDE_MAX_ENTRIES = 64
const HEADER_OVERRIDE_MAX_NAME_LENGTH = 200
const HEADER_OVERRIDE_MAX_VALUE_LENGTH = 8192

/** header value 不允许包含控制字符（与后端 httpguts.ValidHeaderFieldValue 对齐） */
// eslint-disable-next-line no-control-regex
const HEADER_VALUE_INVALID_PATTERN = /[\x00-\x08\x0a-\x1f\x7f]/

/** 长度限制按 UTF-8 字节计（与后端 Go len() 对齐，避免多字节值前端放行后端 400） */
const HEADER_TEXT_ENCODER = new TextEncoder()
function utf8ByteLength(value: string): number {
  return HEADER_TEXT_ENCODER.encode(value).length
}

/**
 * 校验请求头覆写行，返回首个错误的 i18n key（无错误返回 null）。
 * 名称为空但值非空 → invalidName；名称非法 → invalidName；
 * 禁止覆写 → blockedName；大小写不敏感重名 → duplicateName；
 * 值含控制字符或超长 → invalidValue；条目过多 → tooManyEntries。
 */
export function validateHeaderOverrideRows(
  rows: HeaderOverrideRow[]
): 'invalidName' | 'blockedName' | 'duplicateName' | 'invalidValue' | 'tooManyEntries' | null {
  const seen = new Set<string>()
  for (const row of rows) {
    const name = row.name.trim()
    const value = row.value.trim()
    if (!name) {
      if (value) return 'invalidName'
      continue
    }
    if (!isValidHeaderOverrideName(name) || name.length > HEADER_OVERRIDE_MAX_NAME_LENGTH) {
      return 'invalidName'
    }
    const lower = name.toLowerCase()
    if (HEADER_OVERRIDE_BLOCKED_NAMES.has(lower)) return 'blockedName'
    if (seen.has(lower)) return 'duplicateName'
    if (
      HEADER_VALUE_INVALID_PATTERN.test(value) ||
      utf8ByteLength(value) > HEADER_OVERRIDE_MAX_VALUE_LENGTH
    ) {
      return 'invalidValue'
    }
    seen.add(lower)
  }
  if (seen.size > HEADER_OVERRIDE_MAX_ENTRIES) return 'tooManyEntries'
  return null
}

/** 行数组 → credentials 存储对象（名称小写化，丢弃空行） */
export function buildHeaderOverridesObject(rows: HeaderOverrideRow[]): Record<string, string> {
  const result: Record<string, string> = {}
  for (const row of rows) {
    const name = row.name.trim().toLowerCase()
    if (!name) continue
    result[name] = row.value.trim()
  }
  return result
}

/** credentials 存储对象 → 行数组（按名称排序保证稳定展示） */
export function splitHeaderOverridesObject(record: unknown): HeaderOverrideRow[] {
  if (!record || typeof record !== 'object' || Array.isArray(record)) return []
  return Object.entries(record as Record<string, unknown>)
    .filter(([, value]) => typeof value === 'string')
    .map(([name, value]) => ({ name, value: value as string }))
    .sort((a, b) => a.name.localeCompare(b.name))
}

/**
 * 将请求头覆写写入 credentials。
 * create 模式：关闭时不写入任何字段；edit 模式：关闭时删除字段（全量替换语义）。
 */
export function applyHeaderOverride(
  credentials: Record<string, unknown>,
  enabled: boolean,
  rows: HeaderOverrideRow[],
  mode: 'create' | 'edit'
): void {
  if (enabled) {
    credentials[HEADER_OVERRIDE_ENABLED_CREDENTIAL_KEY] = true
    credentials[HEADER_OVERRIDES_CREDENTIAL_KEY] = buildHeaderOverridesObject(rows)
  } else if (mode === 'edit') {
    delete credentials[HEADER_OVERRIDE_ENABLED_CREDENTIAL_KEY]
    delete credentials[HEADER_OVERRIDES_CREDENTIAL_KEY]
  }
}

// ========== Grok 上游端点（只影响转发，不影响 OAuth 授权/刷新地址） ==========

const GROK_DEFAULT_GATEWAY_HOST = 'cli-chat-proxy.grok.com'

export function isCustomGrokBaseUrl(value: unknown): boolean {
  if (typeof value !== 'string') return false
  const trimmed = value.trim()
  if (!trimmed) return false
  try {
    return new URL(trimmed).hostname.toLowerCase() !== GROK_DEFAULT_GATEWAY_HOST
  } catch {
    return false
  }
}

export interface GrokBaseUrlPreset {
  labelKey?: 'cli' | 'official'
  label?: string
  url: string
}

export const GROK_BASE_URL_PRESETS: GrokBaseUrlPreset[] = [
  { labelKey: 'cli', url: 'https://cli-chat-proxy.grok.com/v1' },
  { labelKey: 'official', url: 'https://api.x.ai/v1' },
  { label: 'us-east-1', url: 'https://us-east-1.api.x.ai/v1' },
  { label: 'us-west-2', url: 'https://us-west-2.api.x.ai/v1' },
  { label: 'eu-west-1', url: 'https://eu-west-1.api.x.ai/v1' }
]

// ========== 国产供应商（Kimi / Zhipu / DeepSeek）端点预设 ==========

export type CnAccountMode = 'payg' | 'coding'
export type CnApiProtocol = 'adaptive' | 'chat_completions' | 'anthropic' | 'responses'
export type CnNativeApiProtocol = Exclude<CnApiProtocol, 'adaptive'>

export interface CnBaseUrlPreset {
  mode: CnAccountMode
  protocol: CnApiProtocol
  label: string
  url: string
}

/**
 * 账号模式决定上游套餐，协议决定实际请求格式；两者必须分开保存，
 * 否则切换 Claude Code / Codex 客户端时会把同一密钥错误发往旧端点。
 */
export const CN_BASE_URL_PRESETS: Record<'kimi' | 'zhipu' | 'deepseek', CnBaseUrlPreset[]> = {
  kimi: [
    { mode: 'payg', protocol: 'chat_completions', label: 'Moonshot', url: 'https://api.moonshot.cn/v1' },
    { mode: 'payg', protocol: 'anthropic', label: 'Moonshot Anthropic', url: 'https://api.moonshot.cn/anthropic' },
    { mode: 'coding', protocol: 'chat_completions', label: 'Kimi For Coding', url: 'https://api.kimi.com/coding/v1' },
    { mode: 'coding', protocol: 'anthropic', label: 'Kimi Coding Anthropic', url: 'https://api.kimi.com/coding' }
  ],
  zhipu: [
    { mode: 'payg', protocol: 'chat_completions', label: 'GLM PaaS', url: 'https://open.bigmodel.cn/api/paas/v4' },
    { mode: 'payg', protocol: 'anthropic', label: 'GLM Anthropic', url: 'https://open.bigmodel.cn/api/anthropic' },
    { mode: 'coding', protocol: 'chat_completions', label: 'GLM Coding', url: 'https://open.bigmodel.cn/api/coding/paas/v4' },
    { mode: 'coding', protocol: 'anthropic', label: 'GLM Coding Anthropic', url: 'https://open.bigmodel.cn/api/anthropic' }
  ],
  deepseek: [
    { mode: 'payg', protocol: 'chat_completions', label: 'DeepSeek', url: 'https://api.deepseek.com' },
    { mode: 'payg', protocol: 'anthropic', label: 'DeepSeek Anthropic', url: 'https://api.deepseek.com/anthropic' },
    { mode: 'payg', protocol: 'responses', label: 'DeepSeek Responses', url: 'https://api.deepseek.com' }
  ]
}

export function defaultCNBaseUrl(
  platform: string,
  mode: CnAccountMode,
  protocol: CnApiProtocol = 'chat_completions'
): string {
  if (protocol === 'anthropic') {
    switch (platform) {
      case 'kimi':
        return mode === 'coding' ? 'https://api.kimi.com/coding' : 'https://api.moonshot.cn/anthropic'
      case 'zhipu':
        return 'https://open.bigmodel.cn/api/anthropic'
      case 'deepseek':
        return 'https://api.deepseek.com/anthropic'
      default:
        return ''
    }
  }
  switch (platform) {
    case 'kimi':
      return mode === 'coding' ? 'https://api.kimi.com/coding/v1' : 'https://api.moonshot.cn/v1'
    case 'zhipu':
      return mode === 'coding'
        ? 'https://open.bigmodel.cn/api/coding/paas/v4'
        : 'https://open.bigmodel.cn/api/paas/v4'
    case 'deepseek':
      return 'https://api.deepseek.com'
    default:
      return ''
  }
}

export function defaultCNAdaptiveBaseUrls(
  platform: 'kimi' | 'zhipu' | 'deepseek',
  mode: CnAccountMode
): Record<CnNativeApiProtocol, string> {
  return {
    chat_completions: defaultCNBaseUrl(platform, mode, 'chat_completions'),
    anthropic: defaultCNBaseUrl(platform, mode, 'anthropic'),
    responses: platform === 'deepseek' ? defaultCNBaseUrl(platform, mode, 'responses') : ''
  }
}

// 国产供应商用量单元格共用同一组可见性规则，避免列表占位和子组件分支漂移。
export function cnQuotaCellVisible(platform: string, accountMode: string): boolean {
  return (platform === 'kimi' || platform === 'zhipu') && accountMode === 'coding'
}

export function cnBalanceCellVisible(platform: string, accountMode: string): boolean {
  return (platform === 'kimi' || platform === 'deepseek') && accountMode !== 'coding'
}

// ===== OpenAI plan_type（ChatGPT 订阅档位）手动纠正 =====

export interface PlanTypeOption {
  value: string
  label: string
  [key: string]: unknown
}

/** 将 OpenAI 返回的 canonical（规范）档位值转换为后台可读标签。 */
export function planTypeDisplayLabel(value: string): string {
  switch (value.trim().toLowerCase()) {
    case 'plus':
      return 'Plus'
    case 'pro':
    case 'chatgptpro':
      return 'Pro'
    case 'free':
      return 'Free'
    case 'team':
      return 'Team'
    default:
      return value
  }
}

/** 脏数据不能进入选择器，只有字符串 plan_type 才视为有效。 */
export function readPlanType(credentials: Record<string, unknown> | undefined | null): string {
  const value = credentials?.plan_type
  return typeof value === 'string' ? value : ''
}

/**
 * 保留上游真实 canonical 值，同时为 Plus、Pro、Free 提供快捷纠正项。
 * 例如 chatgptpro 仍原样保存，但界面显示为 Pro，避免保存一次就改坏上游档位值。
 */
export function buildPlanTypeOptions(current: string, clearLabel: string): PlanTypeOption[] {
  const normalizedCurrent = (current || '').trim()
  const currentLabel = normalizedCurrent ? planTypeDisplayLabel(normalizedCurrent) : ''
  const presets: PlanTypeOption[] = [
    { value: 'plus', label: 'Plus' },
    { value: 'pro', label: 'Pro' },
    { value: 'free', label: 'Free' }
  ]
  const options: PlanTypeOption[] = [{ value: '', label: clearLabel }]

  for (const preset of presets) {
    if (
      normalizedCurrent &&
      preset.value !== normalizedCurrent.toLowerCase() &&
      preset.label === currentLabel
    ) {
      options.push({ value: normalizedCurrent, label: preset.label })
    } else {
      options.push(preset)
    }
  }

  if (
    normalizedCurrent &&
    !options.some((option) => option.value.toLowerCase() === normalizedCurrent.toLowerCase())
  ) {
    options.push({
      value: normalizedCurrent,
      label: planTypeDisplayLabel(normalizedCurrent)
    })
  }
  return options
}

/** 空值表示恢复自动识别；非空值只覆盖 plan_type，不触碰其他 OAuth 凭据。 */
export function applyPlanType(
  credentials: Record<string, unknown>,
  planType: string
): Record<string, unknown> {
  const normalized = (planType || '').trim()
  if (normalized) {
    credentials.plan_type = normalized
  } else {
    delete credentials.plan_type
  }
  return credentials
}

// ========== 出口时区（OpenAI API Key + OAuth） ==========

export const UPSTREAM_TIMEZONE_CREDENTIAL_KEY = 'upstream_timezone'

/** 转发热路径可用的高频出口时区（与网关出口 IP 地理对齐，消除降智风控矛盾） */
export const UPSTREAM_TIMEZONE_OPTIONS = [
  'America/Los_Angeles',
  'America/New_York',
  'America/Chicago',
  'Europe/London',
  'Europe/Frankfurt',
  'Asia/Singapore',
  'Asia/Tokyo',
  'Asia/Hong_Kong',
  'Asia/Shanghai',
  'Australia/Sydney'
] as const

/**
 * applyUpstreamTimezone 写入/清除账号出口时区。
 * 空值表示「自动探测」：删除该键，交由后台出口探测服务决定。
 */
export function applyUpstreamTimezone(
  credentials: Record<string, unknown>,
  timezone: string,
  mode: 'create' | 'edit'
): void {
  const trimmed = timezone.trim()
  if (trimmed) {
    credentials[UPSTREAM_TIMEZONE_CREDENTIAL_KEY] = trimmed
  } else if (mode === 'edit') {
    delete credentials[UPSTREAM_TIMEZONE_CREDENTIAL_KEY]
  }
}
