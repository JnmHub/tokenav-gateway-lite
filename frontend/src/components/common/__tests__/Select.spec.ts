import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import Select from '../Select.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => ({
      'common.selectOption': '请选择',
      'common.clearSelection': '清空选择',
      'common.searchPlaceholder': '搜索...',
      'common.noOptionsFound': '无匹配选项',
    })[key] ?? key,
  }),
}))

describe('Select accessibility labels', () => {
  it('uses localized labels for the trigger and clear action', () => {
    const wrapper = mount(Select, {
      props: {
        modelValue: 'a',
        options: [{ value: 'a', label: 'A' }],
        clearable: true,
      },
      global: {
        stubs: {
          Icon: true,
        },
      },
    })

    expect(wrapper.get('button.select-trigger').attributes('aria-label')).toBe('请选择')
    expect(wrapper.get('[role="button"]').attributes('aria-label')).toBe('清空选择')
  })
})
