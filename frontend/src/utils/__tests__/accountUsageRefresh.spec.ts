import { describe, expect, it } from 'vitest'
import { buildGrokUsageRefreshKey, buildOpenAIUsageRefreshKey } from '../accountUsageRefresh'

describe('buildOpenAIUsageRefreshKey', () => {
  it('会在 codex 快照变化时生成不同 key', () => {
    const base = {
      id: 1,
      platform: 'openai',
      type: 'oauth',
      updated_at: '2026-03-07T10:00:00Z',
      last_used_at: '2026-03-07T09:59:00Z',
      extra: {
        codex_usage_updated_at: '2026-03-07T10:00:00Z',
        codex_5h_used_percent: 0,
        codex_7d_used_percent: 0
      }
    } as any

    const next = {
      ...base,
      extra: {
        ...base.extra,
        codex_usage_updated_at: '2026-03-07T10:01:00Z',
        codex_5h_used_percent: 100
      }
    }

    expect(buildOpenAIUsageRefreshKey(base)).not.toBe(buildOpenAIUsageRefreshKey(next))
  })

  it('会在 last_used_at 变化时生成不同 key', () => {
    const base = {
      id: 3,
      platform: 'openai',
      type: 'oauth',
      updated_at: '2026-03-07T10:00:00Z',
      last_used_at: '2026-03-07T10:00:00Z',
      extra: {
        codex_usage_updated_at: '2026-03-07T10:00:00Z',
        codex_5h_used_percent: 12,
        codex_7d_used_percent: 24
      }
    } as any

    const next = {
      ...base,
      last_used_at: '2026-03-07T10:02:00Z'
    }

    expect(buildOpenAIUsageRefreshKey(base)).not.toBe(buildOpenAIUsageRefreshKey(next))
  })

  it('非 OpenAI OAuth 账号返回空 key', () => {
    expect(buildOpenAIUsageRefreshKey({
      id: 2,
      platform: 'anthropic',
      type: 'oauth',
      updated_at: '2026-03-07T10:00:00Z',
      last_used_at: '2026-03-07T10:00:00Z',
      extra: {}
    } as any)).toBe('')
  })
})

describe('buildGrokUsageRefreshKey', () => {
  it('规范用量快照发生变化时生成不同 key', () => {
    const base = {
      platform: 'grok',
      extra: {
        grok_usage_snapshot: { subscription_tier: 'free', status_code: 200 }
      }
    } as any

    expect(buildGrokUsageRefreshKey(base)).not.toBe(buildGrokUsageRefreshKey({
      ...base,
      extra: {
        grok_usage_snapshot: { subscription_tier: 'supergrok', status_code: 200 }
      }
    }))
  })

  it('对象字段顺序变化不会制造无效刷新', () => {
    const first = {
      platform: 'grok',
      extra: {
        grok_usage_snapshot: {
          status_code: 200,
          subscription_tier: 'supergrok',
          requests: { remaining: 8, limit: 10 }
        }
      }
    } as any
    const reordered = {
      platform: 'grok',
      extra: {
        grok_usage_snapshot: {
          requests: { limit: 10, remaining: 8 },
          subscription_tier: 'supergrok',
          status_code: 200
        }
      }
    } as any

    expect(buildGrokUsageRefreshKey(first)).toBe(buildGrokUsageRefreshKey(reordered))
  })

  it('规范快照已有档位时忽略旧别名，缺少档位时继续跟踪旧别名', () => {
    const canonical = {
      platform: 'grok',
      extra: {
        grok_usage_snapshot: { subscription_tier: 'supergrok' },
        grok_quota_snapshot: { subscription_tier: 'free' }
      }
    } as any
    expect(buildGrokUsageRefreshKey(canonical)).toBe(buildGrokUsageRefreshKey({
      ...canonical,
      extra: {
        ...canonical.extra,
        grok_quota_snapshot: { subscription_tier: 'supergrok_heavy' }
      }
    }))

    const legacy = {
      platform: 'grok',
      extra: {
        grok_usage_snapshot: { status_code: 200 },
        grok_quota_snapshot: { subscription_tier: 'free' }
      }
    } as any
    expect(buildGrokUsageRefreshKey(legacy)).not.toBe(buildGrokUsageRefreshKey({
      ...legacy,
      extra: {
        ...legacy.extra,
        grok_quota_snapshot: { subscription_tier: 'supergrok' }
      }
    }))
  })

  it('非 Grok 账号返回空 key', () => {
    expect(buildGrokUsageRefreshKey({
      platform: 'openai',
      extra: { grok_usage_snapshot: { subscription_tier: 'supergrok' } }
    } as any)).toBe('')
  })
})
