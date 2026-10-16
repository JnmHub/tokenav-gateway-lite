import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

import GrokBaseUrlPresets from '../GrokBaseUrlPresets.vue'
import { GROK_BASE_URL_PRESETS } from '../credentialsBuilder'

describe('GrokBaseUrlPresets', () => {
  it('renders all supported endpoint profiles and emits the exact selected URL', async () => {
    const wrapper = mount(GrokBaseUrlPresets)
    const buttons = wrapper.findAll('[data-testid="grok-base-url-preset"]')

    expect(buttons).toHaveLength(5)
    expect(GROK_BASE_URL_PRESETS.map((preset) => preset.url)).toEqual([
      'https://cli-chat-proxy.grok.com/v1',
      'https://api.x.ai/v1',
      'https://us-east-1.api.x.ai/v1',
      'https://us-west-2.api.x.ai/v1',
      'https://eu-west-1.api.x.ai/v1'
    ])

    await buttons[3].trigger('click')
    expect(wrapper.emitted('select')).toEqual([['https://us-west-2.api.x.ai/v1']])
  })
})
