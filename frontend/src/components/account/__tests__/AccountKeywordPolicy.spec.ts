import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

function read(relativePath: string) {
  return readFileSync(resolve(process.cwd(), relativePath), 'utf8')
}

describe('account keyword policy', () => {
  it('defaults all account creation paths to inherit and injects the shared extra field', () => {
    const source = read('src/components/account/CreateAccountModal.vue')

    expect(source).toContain("const keywordPolicyMode = ref<KeywordPolicyMode>('inherit')")
    expect(source).toContain('keyword_policy_mode: keywordPolicyMode.value')
    expect(source).toContain('adminAPI.accounts.create(withAntigravityConfirmFlag(payload))')
    expect(source).toContain('extra: withKeywordPolicyExtra(extra)')
    expect(source).toContain('extra: withKeywordPolicyExtra(buildOpenAIExtra())')
  })

  it('provides complete Chinese and English labels for the three click choices', () => {
    const zh = read('src/i18n/locales/zh.ts')
    const en = read('src/i18n/locales/en.ts')

    for (const key of ['inherit', 'protect', 'allow']) {
      expect(zh).toMatch(new RegExp(`${key}:\\s*'[^']+'`))
      expect(en).toMatch(new RegExp(`${key}:\\s*'[^']+'`))
    }
    expect(zh).toContain("title: '账号关键词策略'")
    expect(en).toContain("title: 'Account Keyword Policy'")
  })

  it('does not restore the retired global account-filter moderation UI', () => {
    const riskControl = read('src/views/admin/RiskControlView.vue')
    expect(riskControl).not.toContain('account_filter')
  })
})
