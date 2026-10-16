import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const currentDir = dirname(fileURLToPath(import.meta.url))
const source = readFileSync(resolve(currentDir, '../SettingsView.vue'), 'utf8')

describe('SettingsView billing settlement switch', () => {
  it('offers one click-only switch and warns when legacy mode is active', () => {
    expect(source).toContain('data-testid="billing-settlement-switch"')
    expect(source).toContain(':model-value="billingSettlementForm.idempotent_enabled"')
    expect(source).toContain('@update:model-value="saveBillingSettlementSwitch"')
    expect(source).toContain('data-testid="billing-settlement-legacy-warning"')
    expect(source).toContain(
      'billingSettlementLoaded && !billingSettlementForm.idempotent_enabled && billingSettlementForm.legacy_warning',
    )
    expect(source).toContain('防重复结算（推荐）')
  })

  it('loads independently and restores the server state after a failed update', () => {
    expect(source).toContain('loadBillingSettlementSettings(),')
    expect(source).toContain('adminAPI.settings.getBillingSettlementSettings()')
    expect(source).toContain('adminAPI.settings.updateBillingSettlementSettings({')
    expect(source).toContain('const previous: BillingSettlementSettings = { ...billingSettlementForm };')
    expect(source).toContain('Object.assign(billingSettlementForm, previous);')
    expect(source).toContain('切换结算模式失败，已恢复原状态')
    expect(source).toContain('if (!billingSettlementLoaded.value || billingSettlementSaving.value) return;')
  })
})
