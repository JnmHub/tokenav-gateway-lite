import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const currentDir = dirname(fileURLToPath(import.meta.url))
const settingsSource = readFileSync(resolve(currentDir, '../SettingsView.vue'), 'utf8')
const opsDialogSource = readFileSync(resolve(currentDir, '../ops/components/OpsSettingsDialog.vue'), 'utf8')
const opsApiSource = readFileSync(resolve(currentDir, '../../../api/admin/ops.ts'), 'utf8')

describe('Codex tail-burst settings persistence', () => {
  it('accepts an unset backend pointer and keeps the switch independent', () => {
    expect(opsApiSource).toContain('enabled?: boolean | null')
    expect(opsApiSource).toContain('codex_tail_burst?: OpsCodexTailBurstSettings')
    expect(opsApiSource).toContain("'/admin/ops/codex-tail-burst'")
    expect(settingsSource).toContain('settings.enabled ?? false')
    expect(settingsSource).toContain('loadCodexTailBurstSettings(),')
  })

  it('does not mutate the cached server object before a switch save succeeds', () => {
    expect(settingsSource).toContain('const updated = await adminAPI.ops.updateCodexTailBurstSettings(enabled);')
    expect(settingsSource).toContain('codexTailBurstForm.enabled = previous;')
    expect(settingsSource).not.toContain('codexTailBurstCached.codex_tail_burst.enabled = enabled;')
  })

  it('preserves the latest tail-burst field when the legacy ops dialog saves', () => {
    expect(opsDialogSource).toContain('const latest = await opsAPI.getAdvancedSettings()')
    expect(opsDialogSource).toContain('latest.codex_tail_burst ?? latestTailBurst')
    expect(opsDialogSource).toContain('codex_tail_burst: { ...latestTailBurst }')
    expect(opsDialogSource).not.toContain('advancedSettings.codex_tail_burst.enabled')
  })
})
