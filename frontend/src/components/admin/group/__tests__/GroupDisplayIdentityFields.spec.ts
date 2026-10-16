import { mount } from '@vue/test-utils'
import { defineComponent, ref } from 'vue'
import { describe, expect, it, vi } from 'vitest'

import GroupDisplayIdentityFields from '../GroupDisplayIdentityFields.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

const TestHost = defineComponent({
  components: { GroupDisplayIdentityFields },
  setup() {
    const displayBrand = ref('自定义类别')
    const displayIcon = ref('universal')
    return { displayBrand, displayIcon }
  },
  template: `
    <GroupDisplayIdentityFields
      v-model:display-brand="displayBrand"
      v-model:display-icon="displayIcon"
      platform="openai"
    />
  `
})

describe('GroupDisplayIdentityFields', () => {
  it('连续点击快捷类别时同步更新图标和输入框文字', async () => {
    const wrapper = mount(TestHost)
    const brandInput = wrapper.get('[data-testid="group-display-brand-input"]')

    expect((brandInput.element as HTMLInputElement).value).toBe('自定义类别')

    await wrapper.get('[data-testid="group-display-identity-kimi"]').trigger('click')
    expect((brandInput.element as HTMLInputElement).value).toBe('Kimi')

    await wrapper.get('[data-testid="group-display-identity-glm"]').trigger('click')
    expect((brandInput.element as HTMLInputElement).value).toBe('GLM')
    expect(wrapper.getComponent(GroupDisplayIdentityFields).props('displayIcon')).toBe('glm')

    await brandInput.setValue('自定义服务商')
    expect((brandInput.element as HTMLInputElement).value).toBe('自定义服务商')
    expect(wrapper.getComponent(GroupDisplayIdentityFields).props('displayBrand')).toBe('自定义服务商')
  })
})
