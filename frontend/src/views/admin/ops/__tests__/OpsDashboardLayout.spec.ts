import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const componentPath = resolve(dirname(fileURLToPath(import.meta.url)), '../OpsDashboard.vue')
const componentSource = readFileSync(componentPath, 'utf8')

describe('OpsDashboard chart layout', () => {
  it('切换率和吞吐趋势图使用固定高度，避免数据变化撑高页面', () => {
    expect(componentSource).toContain('class="h-[360px] lg:col-span-1"')
    expect(componentSource).toContain('class="h-[360px] lg:col-span-2"')
    expect(componentSource).toContain('class="min-h-[360px] lg:col-span-1"')
  })

  it('运维仪表盘接入入口保护独立组件并跟随统一刷新', () => {
    expect(componentSource).toContain("import OpsIngressProtectionCard from './components/OpsIngressProtectionCard.vue'")
    expect(componentSource).toContain('<OpsIngressProtectionCard')
    expect(componentSource).toContain(':refresh-token="dashboardRefreshToken"')
  })
})
