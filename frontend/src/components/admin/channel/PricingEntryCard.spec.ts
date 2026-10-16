import { mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { describe, expect, it, vi } from 'vitest'

import PricingEntryCard from './PricingEntryCard.vue'
import type { PricingFormEntry } from './types'

vi.mock('vue-i18n', async importOriginal => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({ t: (_key: string, fallback?: string) => fallback || _key }),
  }
})

const SelectStub = defineComponent({
  name: 'PricingSelectStub',
  props: {
    modelValue: { type: [String, Number, Boolean], default: null },
    options: { type: Array, default: () => [] },
  },
  emits: ['update:modelValue'],
  template: `
    <select :value="modelValue" @change="$emit('update:modelValue', $event.target.value)">
      <option v-for="option in options" :key="String(option.value)" :value="option.value">{{ option.label }}</option>
    </select>
  `,
})

function videoEntry(): PricingFormEntry {
  return {
    platform: undefined,
    models: [],
    billing_mode: 'video',
    input_price: null,
    output_price: null,
    cache_write_price: null,
    cache_read_price: null,
    image_input_price: null,
    image_output_price: null,
    per_request_price: null,
    intervals: [],
  }
}

function mountCard(entry = videoEntry()) {
  return mount(PricingEntryCard, {
    props: { entry, platform: 'composite', compact: true },
    global: {
      stubs: {
        Select: SelectStub,
        Icon: true,
        ModelTagInput: true,
        IntervalRow: true,
      },
    },
  })
}

describe('PricingEntryCard video pricing', () => {
  it('uses a semantic disclosure button linked to the collapsible content', async () => {
    const entry = videoEntry()
    entry.models = ['gpt-5.6-sol']
    const wrapper = mountCard(entry)
    const disclosure = wrapper.get('button[aria-controls]')
    const contentId = disclosure.attributes('aria-controls')

    expect(disclosure.attributes('aria-expanded')).toBe('false')
    expect(wrapper.get(`#${contentId}`).exists()).toBe(true)

    await disclosure.trigger('keydown.enter')
    await disclosure.trigger('click')
    expect(disclosure.attributes('aria-expanded')).toBe('true')
  })

  it('edits fixed 480p, 720p, and 1080p per-second prices', async () => {
    const wrapper = mountCard()

    expect(wrapper.get('[data-testid="video-price-480p"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="video-price-720p"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="video-price-1080p"]').exists()).toBe(true)

    await wrapper.get('[data-testid="video-price-720p"]').setValue('0.14')
    const updated = wrapper.emitted('update')?.at(-1)?.[0] as PricingFormEntry
    expect(updated.intervals).toEqual([
      expect.objectContaining({ tier_label: '720p', per_request_price: '0.14', sort_order: 1 }),
    ])
  })

  it('requires the operator to select a concrete platform for composite pricing', async () => {
    const wrapper = mountCard()
    await wrapper.get('[data-testid="pricing-platform-select"]').setValue('grok')

    const updated = wrapper.emitted('update')?.at(-1)?.[0] as PricingFormEntry
    expect(updated.platform).toBe('grok')
  })
})
