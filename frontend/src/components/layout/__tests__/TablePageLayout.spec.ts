import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import TablePageLayout from '../TablePageLayout.vue'

function setViewportWidth(width: number) {
  Object.defineProperty(window, 'innerWidth', { configurable: true, writable: true, value: width })
  window.dispatchEvent(new Event('resize'))
}

describe('TablePageLayout', () => {
  it('平板宽度保留桌面表格滚动容器，不进入 overflow visible 的移动模式', async () => {
    setViewportWidth(900)
    const wrapper = mount(TablePageLayout, { slots: { table: '<div class="table-wrapper"></div>' } })
    await wrapper.vm.$nextTick()

    expect(wrapper.find('.table-page-layout').classes()).not.toContain('mobile-mode')
  })

  it('仅在卡片式 DataTable 使用的 768px 以下进入移动模式', async () => {
    setViewportWidth(767)
    const wrapper = mount(TablePageLayout)
    await wrapper.vm.$nextTick()

    expect(wrapper.find('.table-page-layout').classes()).toContain('mobile-mode')
  })
})
