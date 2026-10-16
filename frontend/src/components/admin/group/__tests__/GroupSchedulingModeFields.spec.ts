import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { createI18n } from 'vue-i18n'
import GroupSchedulingModeFields from '../GroupSchedulingModeFields.vue'

const messages = {
  zh: {
    admin: {
      groups: {
        scheduling: {
          title: '账号调度规则', hint: '提示', default: '默认（负载感知）',
          defaultHint: '默认提示', strictPriority: '严格优先级',
          strictPriorityHint: '严格提示', random: '完全随机', randomHint: '随机提示',
        },
      },
    },
  },
}

describe('GroupSchedulingModeFields', () => {
  it('renders and emits every scheduling mode', async () => {
    const wrapper = mount(GroupSchedulingModeFields, {
      props: { modelValue: 'default', platform: 'openai' },
      global: { plugins: [createI18n({ legacy: false, locale: 'zh', messages })] },
    })

    const buttons = wrapper.findAll('[role="radio"]')
    expect(buttons).toHaveLength(3)
    expect(buttons[0].attributes('aria-checked')).toBe('true')
    await buttons[1].trigger('click')
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual(['strict_priority'])
    await buttons[2].trigger('click')
    expect(wrapper.emitted('update:modelValue')?.[1]).toEqual(['random'])
  })

  it('hides strict priority for platforms without the compatible scheduler', () => {
    const wrapper = mount(GroupSchedulingModeFields, {
      props: { modelValue: 'default', platform: 'anthropic' },
      global: { plugins: [createI18n({ legacy: false, locale: 'zh', messages })] },
    })

    expect(wrapper.findAll('[role="radio"]')).toHaveLength(2)
    expect(wrapper.text()).not.toContain('严格优先级')
  })

  it('resets strict priority when the platform changes to an unsupported scheduler', async () => {
    const wrapper = mount(GroupSchedulingModeFields, {
      props: { modelValue: 'strict_priority', platform: 'openai' },
      global: { plugins: [createI18n({ legacy: false, locale: 'zh', messages })] },
    })

    await wrapper.setProps({ platform: 'anthropic' })
    expect(wrapper.emitted('update:modelValue')).toContainEqual(['default'])
  })
})
