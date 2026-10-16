import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

function readSource(path: string): string {
  return readFileSync(resolve(path), 'utf8')
}

describe('admin platform selectors', () => {
  it('uses the shared catalog in group and channel management', () => {
    expect(readSource('src/views/admin/GroupsView.vue')).toContain('GROUP_PLATFORM_OPTIONS')
    expect(readSource('src/views/admin/ChannelsView.vue')).toContain('CONCRETE_PLATFORM_OPTIONS')
  })

  it('uses the concrete catalog in account and policy filters', () => {
    for (const path of [
      'src/components/admin/account/AccountTableFilters.vue',
      'src/components/admin/ErrorPassthroughRulesModal.vue',
      'src/views/admin/ops/components/OpsDashboardHeader.vue',
      'src/components/admin/channel/PricingEntryCard.vue',
      'src/components/admin/group/CompositeRoutesModal.vue',
    ]) {
      expect(readSource(path)).toContain('CONCRETE_PLATFORM_OPTIONS')
    }
  })
})
