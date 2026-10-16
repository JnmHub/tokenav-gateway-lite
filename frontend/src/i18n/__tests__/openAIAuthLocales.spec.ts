import { describe, expect, it } from 'vitest'
import en from '../locales/en'
import zh from '../locales/zh'

describe('OpenAI account import locale keys', () => {
  it('provides Mobile RT and Access Token labels in both languages', () => {
    expect(zh.admin.accounts.oauth.openai.mobileRefreshTokenAuth).toBe('手动输入 Mobile RT')
    expect(zh.admin.accounts.oauth.openai.accessTokenAuth).toBe('手动输入 Access Token')
    expect(en.admin.accounts.oauth.openai.mobileRefreshTokenAuth).toBe('Manual Mobile RT Input')
    expect(en.admin.accounts.oauth.openai.accessTokenAuth).toBe('Manual Access Token Input')
  })

  it('provides separate Agent Identity and PAT labels in both languages', () => {
    expect(zh.admin.accounts.oauth.openai.agentIdentityAuth).toBe('Agent Identity auth.json')
    expect(zh.admin.accounts.oauth.openai.personalAccessTokenAuth).toBe('Codex Personal Access Token')
    expect(en.admin.accounts.oauth.openai.agentIdentityAuth).toBe('Agent Identity auth.json')
    expect(en.admin.accounts.oauth.openai.personalAccessTokenAuth).toBe('Codex Personal Access Token')
  })
})
