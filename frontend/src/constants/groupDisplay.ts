import type { GroupPlatform } from '@/types'

export type GroupDisplayIconKey =
  | 'openai'
  | 'claude'
  | 'gemini'
  | 'grok'
  | 'kimi'
  | 'glm'
  | 'deepseek'
  | 'qwen'
  | 'doubao'
  | 'minimax'
  | 'universal'
  | 'composite'

export interface GroupDisplayIconOption {
  key: GroupDisplayIconKey
  label: string
  src: string
}

export const groupDisplayIconOptions: GroupDisplayIconOption[] = [
  { key: 'openai', label: 'OpenAI', src: '/models/GPT.svg' },
  { key: 'claude', label: 'Claude', src: '/models/Anthropic.svg' },
  { key: 'gemini', label: 'Gemini', src: '/models/Gemini.svg' },
  { key: 'grok', label: 'Grok', src: '/models/Grok.svg' },
  { key: 'kimi', label: 'Kimi', src: '/models/Kimi.svg' },
  { key: 'glm', label: 'GLM', src: '/models/Glm.svg' },
  { key: 'deepseek', label: 'DeepSeek', src: '/models/Deepseek.svg' },
  { key: 'qwen', label: 'Qwen', src: '/models/Qwen.svg' },
  { key: 'doubao', label: 'Doubao', src: '/models/Doubao.svg' },
  { key: 'minimax', label: 'MiniMax', src: '/models/MiniMax.svg' },
  { key: 'universal', label: '通用模型', src: '/models/Universal.png' },
  { key: 'composite', label: '聚合模型', src: '/models/Composite.png' }
]

export function defaultGroupDisplayIdentity(platform: GroupPlatform | string) {
  switch (String(platform || '').toLowerCase()) {
    case 'openai':
      return { brand: 'OpenAI', icon: 'openai' as GroupDisplayIconKey }
    case 'anthropic':
      return { brand: 'Claude', icon: 'claude' as GroupDisplayIconKey }
    case 'gemini':
      return { brand: 'Gemini', icon: 'gemini' as GroupDisplayIconKey }
    case 'grok':
      return { brand: 'Grok', icon: 'grok' as GroupDisplayIconKey }
    case 'kimi':
      return { brand: 'Kimi', icon: 'kimi' as GroupDisplayIconKey }
    case 'zhipu':
      return { brand: '智谱 GLM', icon: 'glm' as GroupDisplayIconKey }
    case 'deepseek':
      return { brand: 'DeepSeek', icon: 'deepseek' as GroupDisplayIconKey }
    case 'composite':
      return { brand: '聚合模型', icon: 'composite' as GroupDisplayIconKey }
    default:
      return { brand: '通用模型', icon: 'universal' as GroupDisplayIconKey }
  }
}
