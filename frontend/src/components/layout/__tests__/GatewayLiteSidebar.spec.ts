import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const componentPath = resolve(dirname(fileURLToPath(import.meta.url)), '../GatewayLiteSidebar.vue')
const componentSource = readFileSync(componentPath, 'utf8')

describe('GatewayLiteSidebar', () => {
  it('Logo 和站点名称均可返回后台首页', () => {
    expect(componentSource.match(/to="\/admin\/dashboard"/g)).toHaveLength(2)
  })

  it('组件重建时保存并恢复菜单滚动位置', () => {
    expect(componentSource).toContain('ref="sidebarNavRef"')
    expect(componentSource).toContain('onBeforeUnmount')
    expect(componentSource).toContain('appStore.sidebarScrollTop = sidebarNavRef.value.scrollTop')
    expect(componentSource).toContain('sidebarNavRef.value.scrollTop = appStore.sidebarScrollTop')
    expect(componentSource).toContain('nextTick')
  })

  it('提供运行配置的独立只读入口', () => {
    expect(componentSource).toContain("path: '/admin/runtime'")
    expect(componentSource).toContain("t('nav.runtimeSummary')")
  })
})
