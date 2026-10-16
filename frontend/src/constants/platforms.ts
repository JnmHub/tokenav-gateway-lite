import type { AccountPlatform, GroupPlatform } from '@/types'

export interface PlatformOption<T extends string = string> {
  value: T
  label: string
}

// 所有管理入口共用同一份目录，避免后端已支持的平台在某个筛选或表单里消失。
export const CONCRETE_PLATFORM_OPTIONS = [
  { value: 'anthropic', label: 'Anthropic' },
  { value: 'openai', label: 'OpenAI' },
  { value: 'gemini', label: 'Gemini' },
  { value: 'antigravity', label: 'Antigravity' },
  { value: 'grok', label: 'Grok' },
  { value: 'kimi', label: 'Kimi' },
  { value: 'zhipu', label: '智谱 GLM' },
  { value: 'deepseek', label: 'DeepSeek' },
] as const satisfies readonly PlatformOption<AccountPlatform>[]

export const GROUP_PLATFORM_OPTIONS = [
  ...CONCRETE_PLATFORM_OPTIONS,
  { value: 'composite', label: '聚合模型（多平台）' },
] as const satisfies readonly PlatformOption<GroupPlatform>[]
