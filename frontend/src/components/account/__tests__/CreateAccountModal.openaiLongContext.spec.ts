import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const source = readFileSync(
  resolve(process.cwd(), 'src/components/account/CreateAccountModal.vue'),
  'utf8'
)

describe('CreateAccountModal OpenAI long-context billing', () => {
  it('exposes the provider-cost toggle and writes the explicit default into account extra', () => {
    expect(source).toContain('data-testid="openai-long-context-billing-toggle"')
    expect(source).toContain('const openAILongContextBillingEnabled = ref(false)')
    expect(source).toContain(
      'extra.openai_long_context_billing_enabled = openAILongContextBillingEnabled.value'
    )
  })
})
