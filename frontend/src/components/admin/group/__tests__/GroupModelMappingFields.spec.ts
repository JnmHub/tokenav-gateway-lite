import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import GroupModelMappingFields from '../GroupModelMappingFields.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

describe('GroupModelMappingFields', () => {
  it('adds and removes mapping rows without requiring keyboard input', async () => {
    const wrapper = mount(GroupModelMappingFields, {
      props: { modelValue: [], candidates: ['gpt-5.6-sol'], loading: false },
      global: { stubs: { Select: true, Icon: true } }
    })

    await wrapper.get('button').trigger('click')
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([[{ from: '', to: '' }]])

    await wrapper.setProps({ modelValue: [{ from: 'gpt-public', to: 'gpt-5.6-sol' }] })
    expect(wrapper.text()).toContain('gpt-public → gpt-5.6-sol')
    const removeButton = wrapper.get('button[aria-label="admin.groups.modelMapping.remove"]')
    await removeButton.trigger('click')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([[]])
  })
})

describe('GroupModelMappingFields weighted split', () => {
  it('converts a row to weighted split and back without keyboard input', async () => {
    const wrapper = mount(GroupModelMappingFields, {
      props: {
        modelValue: [{ from: 'gpt-public', to: 'gpt-5.6-sol' }],
        candidates: ['gpt-5.6-sol', 'gpt-5.6-mini'],
        loading: false,
        platform: 'openai'
      },
      global: { stubs: { Select: true, Icon: true } }
    })

    await wrapper.get('button[aria-label="admin.groups.modelMapping.toSplit"]').trigger('click')
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([[
      { from: 'gpt-public', to: '', split: true, targets: [
        { model: 'gpt-5.6-sol', weight: 70 },
        { model: '', weight: 30 }
      ] }
    ]])

    await wrapper.setProps({
      modelValue: [{ from: 'gpt-public', to: '', split: true, targets: [
        { model: 'gpt-5.6-sol', weight: 70 },
        { model: 'gpt-5.6-mini', weight: 30 }
      ] }]
    })
    await wrapper.get('button[aria-label="admin.groups.modelMapping.weightUp"]').trigger('click')
    const rows = wrapper.emitted('update:modelValue')?.at(-1)?.[0] as Array<{ targets?: Array<{ weight: number }> }>
    expect(rows[0].targets?.[0].weight).toBe(80)

    await wrapper.setProps({
      modelValue: [{ from: 'gpt-public', to: '', split: true, targets: [
        { model: 'gpt-5.6-sol', weight: 80 },
        { model: 'gpt-5.6-mini', weight: 30 }
      ] }]
    })
    await wrapper.get('button[aria-label="admin.groups.modelMapping.toSingle"]').trigger('click')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([[{ from: 'gpt-public', to: 'gpt-5.6-sol' }]])
  })

  it('clamps weight step buttons to the 1-100 range', async () => {
    const wrapper = mount(GroupModelMappingFields, {
      props: {
        modelValue: [{ from: 'gpt-public', to: '', split: true, targets: [
          { model: 'a', weight: 95 },
          { model: 'b', weight: 5 }
        ] }],
        candidates: ['a', 'b'],
        loading: false,
        platform: 'openai'
      },
      global: { stubs: { Select: true, Icon: true } }
    })

    await wrapper.get('button[aria-label="admin.groups.modelMapping.weightUp"]').trigger('click')
    let rows = wrapper.emitted('update:modelValue')?.at(-1)?.[0] as Array<{ targets?: Array<{ weight: number }> }>
    expect(rows[0].targets?.[0].weight).toBe(100)

    await wrapper.setProps({
      modelValue: [{ from: 'gpt-public', to: '', split: true, targets: [
        { model: 'a', weight: 100 },
        { model: 'b', weight: 5 }
      ] }]
    })
    const downButtons = wrapper.findAll('button[aria-label="admin.groups.modelMapping.weightDown"]')
    await downButtons[1].trigger('click')
    rows = wrapper.emitted('update:modelValue')?.at(-1)?.[0] as Array<{ targets?: Array<{ weight: number }> }>
    expect(rows[0].targets?.[1].weight).toBe(1)
  })

  it('hides the split toggle for non-openai platforms', () => {
    const wrapper = mount(GroupModelMappingFields, {
      props: {
        modelValue: [{ from: 'claude-x', to: 'claude-y' }],
        candidates: ['claude-y'],
        loading: false,
        platform: 'anthropic'
      },
      global: { stubs: { Select: true, Icon: true } }
    })
    expect(wrapper.find('button[aria-label="admin.groups.modelMapping.toSplit"]').exists()).toBe(false)
  })
})
