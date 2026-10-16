import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const source = readFileSync(
  resolve(process.cwd(), 'src/components/account/CreateAccountModal.vue'),
  'utf8'
)

describe('CreateAccountModal Grok account types', () => {
  it('offers API-key setup alongside OAuth with the official xAI default', () => {
    expect(source).toContain('data-testid="grok-account-type-api-key"')
    expect(source).toContain("@click=\"accountCategory = 'apikey'\"")
    expect(source).toContain("newPlatform === 'grok'")
    expect(source).toContain("? 'https://api.x.ai/v1'")
    expect(source).toContain("form.platform === 'grok'")
    expect(source).toContain("? 'xai-...'")
  })

  it('wires OAuth routing, header overrides, and SSO batch import into account creation', () => {
    expect(source).toContain('<GrokBaseUrlPresets @select="grokOAuthBaseUrl = $event" />')
    expect(source).toContain('applyGrokOAuthOptions(credentials)')
    expect(source).toContain("applyHeaderOverride(credentials, headerOverrideEnabled.value")
    expect(source).toContain(':show-grok-sso-option="form.platform === \'grok\'"')
    expect(source).toContain('@validate-grok-sso="handleGrokSSOImport"')
    expect(source).toContain('const result = await createFromSSO({')
  })
})
