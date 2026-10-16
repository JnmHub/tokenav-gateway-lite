import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const currentDir = dirname(fileURLToPath(import.meta.url))
const source = readFileSync(resolve(currentDir, '../SettingsView.vue'), 'utf8')

describe('SettingsView composite groups switch', () => {
  it('shows one click-only runtime switch with the disabled request consequence', () => {
    expect(source).toContain('data-testid="composite-groups-switch"')
    expect(source).toContain(':model-value="compositeGroupsForm.enabled"')
    expect(source).toContain(':disabled="!compositeGroupsLoaded || compositeGroupsSaving"')
    expect(source).toContain('@update:model-value="saveCompositeGroupsSwitch"')
    expect(source).toContain('聚合分组（多平台）')
    expect(source).toContain('已有聚合 Key 的新请求会返回 503')
  })

  it('keeps the switch in the feature tab gateway switch section', () => {
    const featureTab = source.indexOf(`activeTab === 'features'`)
    const featureSection = source.indexOf('网关功能开关', featureTab)
    const switchRow = source.indexOf('data-testid="composite-groups-switch-row"')
    const nextTab = source.indexOf(`activeTab === 'controlPlane'`, featureTab)

    expect(featureTab).toBeGreaterThanOrEqual(0)
    expect(featureSection).toBeGreaterThan(featureTab)
    expect(switchRow).toBeGreaterThan(featureSection)
    expect(switchRow).toBeLessThan(nextTab)
  })

  it('loads independently and restores the previous state after a failed save', () => {
    expect(source).toContain('loadCompositeGroupsSettings(),')
    expect(source).toContain('adminAPI.settings.getCompositeGroupsSettings()')
    expect(source).toContain('adminAPI.settings.updateCompositeGroupsSettings({ enabled })')
    expect(source).toContain('const previous: CompositeGroupsSettings = { ...compositeGroupsForm };')
    expect(source).toContain('Object.assign(compositeGroupsForm, previous);')
    expect(source).toContain('切换聚合分组失败，已恢复原状态')
    expect(source).toContain('!compositeGroupsLoaded.value || compositeGroupsSaving.value')
  })
})
