import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import DataTable from '../DataTable.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

const stubDesktopMatchMedia = () => {
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches: true,
      media: query,
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn()
    }))
  })
}

describe('DataTable', () => {
  beforeEach(() => {
    stubDesktopMatchMedia()
    localStorage.clear()
  })

  it('向读屏软件暴露排序方向并支持键盘切换', async () => {
    const wrapper = mount(DataTable, {
      props: {
        columns: [
          { key: 'name', label: 'Name', sortable: true },
          { key: 'created_at', label: 'Created', sortable: true }
        ],
        data: [
          { id: 1, name: 'Beta', created_at: '2026-01-02T00:00:00Z' },
          { id: 2, name: 'Alpha', created_at: '2026-01-01T00:00:00Z' }
        ],
        defaultSortKey: 'name',
        defaultSortOrder: 'asc'
      }
    })

    await wrapper.vm.$nextTick()

    const nameHeader = wrapper.findAll('th')[0]
    const createdHeader = wrapper.findAll('th')[1]
    expect(nameHeader.attributes('aria-sort')).toBe('ascending')
    expect(nameHeader.attributes('tabindex')).toBe('0')
    expect(createdHeader.attributes('aria-sort')).toBe('none')
    expect(nameHeader.findAll('svg')).toHaveLength(2)

    await nameHeader.trigger('keydown', { key: 'Enter' })
    await wrapper.vm.$nextTick()
    expect(nameHeader.attributes('aria-sort')).toBe('descending')

    await createdHeader.trigger('keydown', { key: ' ' })
    await wrapper.vm.$nextTick()
    expect(createdHeader.attributes('aria-sort')).toBe('ascending')
    expect(nameHeader.attributes('aria-sort')).toBe('none')
  })

  it('按稳定行主键缓存虚拟行高，排序后不会沿用位置索引', async () => {
    const data = [
      { id: 101, name: 'Beta' },
      { id: 102, name: 'Alpha' },
      { id: 103, name: 'Gamma' }
    ]
    const wrapper = mount(DataTable, {
      props: {
        columns: [{ key: 'name', label: 'Name', sortable: true }],
        data,
        rowKey: 'id'
      }
    })

    await wrapper.vm.$nextTick()

    const exposed = (wrapper.vm as any).virtualizer
    const instance = exposed?.value ?? exposed
    expect(instance.options.getItemKey(0)).toBe(101)

    const measureElementSpy = vi.spyOn(instance, 'measureElement')
    await wrapper.get('th').trigger('click')
    await wrapper.vm.$nextTick()

    expect(instance.options.getItemKey(0)).toBe(102)
    expect(measureElementSpy.mock.calls.some(([node]) => node === null)).toBe(false)
  })

  it('筛选或换页替换行身份集合时清理旧测量缓存', async () => {
    const firstPage = Array.from({ length: 12 }, (_, index) => ({
      id: index + 1,
      name: `First ${index + 1}`
    }))
    const secondPage = Array.from({ length: 12 }, (_, index) => ({
      id: index + 101,
      name: `Second ${index + 1}`
    }))
    const wrapper = mount(DataTable, {
      props: {
        columns: [{ key: 'name', label: 'Name' }],
        data: firstPage,
        rowKey: 'id'
      }
    })

    await wrapper.vm.$nextTick()

    const exposed = (wrapper.vm as any).virtualizer
    const instance = exposed?.value ?? exposed
    const measureElementSpy = vi.spyOn(instance, 'measureElement')
    const measureSpy = vi.spyOn(instance, 'measure')

    await wrapper.setProps({ data: secondPage })
    await wrapper.vm.$nextTick()

    expect(measureElementSpy.mock.calls.some(([node]) => node === null)).toBe(true)
    expect(measureSpy).toHaveBeenCalled()

    measureElementSpy.mockClear()
    measureSpy.mockClear()
    await wrapper.setProps({ data: secondPage.slice(0, 5) })
    await wrapper.vm.$nextTick()

    expect(measureElementSpy.mock.calls.some(([node]) => node === null)).toBe(true)
    expect(measureSpy).toHaveBeenCalled()
  })

  it('没有稳定主键的等长新页面也会保守清理缓存', async () => {
    const firstPage = Array.from({ length: 8 }, (_, index) => ({ name: `First ${index + 1}` }))
    const secondPage = Array.from({ length: 8 }, (_, index) => ({ name: `Second ${index + 1}` }))
    const wrapper = mount(DataTable, {
      props: {
        columns: [{ key: 'name', label: 'Name' }],
        data: firstPage
      }
    })

    await wrapper.vm.$nextTick()

    const exposed = (wrapper.vm as any).virtualizer
    const instance = exposed?.value ?? exposed
    const measureElementSpy = vi.spyOn(instance, 'measureElement')

    await wrapper.setProps({ data: secondPage })
    await wrapper.vm.$nextTick()

    expect(measureElementSpy.mock.calls.some(([node]) => node === null)).toBe(true)
  })

  it('固定布局使用列宽总和并让骨架行保持统一高度', async () => {
    const wrapper = mount(DataTable, {
      props: {
        columns: [
          { key: 'name', label: 'Name', width: 180 },
          { key: 'status', label: 'Status', width: 120 },
        ],
        data: [],
        loading: true,
        fixedLayout: true,
        rowMinHeight: 80,
      },
    })

    await wrapper.vm.$nextTick()

    const table = wrapper.get('table')
    expect(table.classes()).toContain('table-fixed')
    expect(table.classes()).not.toContain('min-w-max')
    expect(table.attributes('style')).toContain('width: 300px')
    expect(table.attributes('style')).toContain('min-width: 300px')
    expect(wrapper.findAll('col').map((col) => col.attributes('style'))).toEqual([
      'width: 180px;',
      'width: 120px;',
    ])
    expect(wrapper.get('tbody td').attributes('style')).toContain('height: 80px')
  })
})
