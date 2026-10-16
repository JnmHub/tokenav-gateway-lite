import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import KeywordPolicyModeSelector from '../KeywordPolicyModeSelector.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

describe('KeywordPolicyModeSelector', () => {
  it('offers three accessible click choices and emits the selected mode', async () => {
    const wrapper = mount(KeywordPolicyModeSelector, {
      props: { modelValue: 'inherit', testIdPrefix: 'test-account' },
      global: { stubs: { Icon: true } }
    })

    expect(wrapper.get('[role="radiogroup"]').attributes('aria-label'))
      .toBe('admin.accounts.keywordPolicy.title')
    expect(wrapper.findAll('[role="radio"]')).toHaveLength(3)
    expect(wrapper.get('[data-testid="test-account-keyword-policy-inherit"]').attributes('aria-checked'))
      .toBe('true')

    await wrapper.get('[data-testid="test-account-keyword-policy-protect"]').trigger('click')
    expect(wrapper.emitted('update:modelValue')).toEqual([['protect']])
  })
})
