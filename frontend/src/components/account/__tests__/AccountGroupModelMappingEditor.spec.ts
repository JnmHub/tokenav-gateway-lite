import { defineComponent, ref } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import type { AdminGroup } from '@/types'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

import AccountGroupModelMappingEditor from '../AccountGroupModelMappingEditor.vue'

const group = {
  id: 7,
  name: '专属组',
  platform: 'openai',
  model_mapping: {}
} as AdminGroup

describe('AccountGroupModelMappingEditor', () => {
  it('keeps a newly added blank row when the parent receives the empty mapping', async () => {
    const Host = defineComponent({
      components: { AccountGroupModelMappingEditor },
      setup() {
        const model = ref<Record<string, Record<string, string>>>({})
        return { model, selectedGroupIds: [7], groups: [group] }
      },
      template: `
        <AccountGroupModelMappingEditor
          v-model="model"
          :selected-group-ids="selectedGroupIds"
          :groups="groups"
        />
      `
    })

    const wrapper = mount(Host)
    const editor = wrapper.findComponent(AccountGroupModelMappingEditor)
    await editor.get('button').trigger('click')
    await flushPromises()

    expect(editor.findAll('input')).toHaveLength(2)

    await editor.findAll('input')[0].setValue('gpt-5.6-sol')
    await editor.findAll('input')[1].setValue('gpt-5.6-luna')
    await flushPromises()

    expect(editor.findAll('input')[0].element).toHaveProperty('value', 'gpt-5.6-sol')
    expect(editor.findAll('input')[1].element).toHaveProperty('value', 'gpt-5.6-luna')
    expect(editor.emitted('update:modelValue')?.at(-1)?.[0]).toEqual({
      '7': { 'gpt-5.6-sol': 'gpt-5.6-luna' }
    })
  })
})
