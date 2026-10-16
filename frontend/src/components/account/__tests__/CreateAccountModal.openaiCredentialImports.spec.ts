import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const source = readFileSync(
  resolve(process.cwd(), 'src/components/account/CreateAccountModal.vue'),
  'utf8'
)

describe('CreateAccountModal OpenAI credential imports', () => {
  it('wires Agent Identity and PAT to their dedicated server endpoints', () => {
    expect(source).toContain(':show-agent-identity-import-option="form.platform === \'openai\'"')
    expect(source).toContain(':show-personal-access-token-option="form.platform === \'openai\'"')
    expect(source).toContain('@import-agent-identity="handleOpenAIImportAgentIdentity"')
    expect(source).toContain('@import-personal-access-token="handleOpenAIImportPersonalAccessToken"')
    expect(source).toContain('adminAPI.accounts.importCodexAgentIdentity({')
    expect(source).toContain('adminAPI.accounts.importCodexPersonalAccessToken({')
  })

  it('reuses the first-step model and scheduling configuration', () => {
    expect(source).toContain('const credentialExtras = buildOpenAICodexImportCredentialExtras()')
    expect(source).toContain('credential_extras: Object.keys(credentialExtras).length > 0 ? credentialExtras : undefined')
    expect(source).toContain('confirm_mixed_channel_risk: mixedScheduling.value')
  })
})
