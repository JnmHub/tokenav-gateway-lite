import { describe, expect, it } from 'vitest'
import {
  cnBalanceCellVisible,
  cnQuotaCellVisible,
  defaultCNAdaptiveBaseUrls,
  defaultCNBaseUrl,
  isHeaderOverridePlatform
} from '../credentialsBuilder'

describe('国产供应商账号凭据默认值', () => {
  it('按账号模式生成 Kimi 自适应端点', () => {
    expect(defaultCNAdaptiveBaseUrls('kimi', 'coding')).toEqual({
      chat_completions: 'https://api.kimi.com/coding/v1',
      anthropic: 'https://api.kimi.com/coding',
      responses: ''
    })
  })

  it('DeepSeek 自适应模式包含 Responses 端点', () => {
    expect(defaultCNAdaptiveBaseUrls('deepseek', 'payg')).toEqual({
      chat_completions: 'https://api.deepseek.com',
      anthropic: 'https://api.deepseek.com/anthropic',
      responses: 'https://api.deepseek.com'
    })
  })

  it('智谱编程套餐使用 coding 地址', () => {
    expect(defaultCNBaseUrl('zhipu', 'coding', 'chat_completions')).toBe(
      'https://open.bigmodel.cn/api/coding/paas/v4'
    )
  })

  it('原生国产 API Key 支持安全请求头覆写', () => {
    for (const platform of ['kimi', 'zhipu', 'deepseek']) {
      expect(isHeaderOverridePlatform(platform, 'apikey')).toBe(true)
      expect(isHeaderOverridePlatform(platform, 'oauth')).toBe(false)
    }
  })

  it('按平台和账号模式区分套餐额度与按量余额', () => {
    expect(cnQuotaCellVisible('kimi', 'coding')).toBe(true)
    expect(cnQuotaCellVisible('zhipu', 'coding')).toBe(true)
    expect(cnQuotaCellVisible('deepseek', 'coding')).toBe(false)
    expect(cnBalanceCellVisible('kimi', 'payg')).toBe(true)
    expect(cnBalanceCellVisible('deepseek', 'payg')).toBe(true)
    expect(cnBalanceCellVisible('zhipu', 'payg')).toBe(false)
  })
})
