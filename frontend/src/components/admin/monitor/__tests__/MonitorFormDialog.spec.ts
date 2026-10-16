import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

import MonitorFormDialog from '../MonitorFormDialog.vue'

const { createMonitor, listTemplates, showError, showSuccess } = vi.hoisted(() => ({
  createMonitor: vi.fn(),
  listTemplates: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    channelMonitor: {
      create: createMonitor,
      update: vi.fn(),
    },
    channelMonitorTemplate: {
      list: listTemplates,
    },
  },
}))

vi.mock('@/api/keys', () => ({
  keysAPI: { list: vi.fn() },
}))

vi.mock('@/api/groups', () => ({
  userGroupsAPI: { getUserGroupRates: vi.fn() },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    cachedPublicSettings: null,
    showError,
    showSuccess,
  }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

const BaseDialogStub = defineComponent({
  props: { show: Boolean },
  template: '<section v-if="show"><slot /><slot name="footer" /></section>',
})

function mountDialog() {
  return mount(MonitorFormDialog, {
    props: {
      show: true,
      monitor: null,
    },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        Toggle: true,
        Select: true,
        ModelTagInput: true,
        MonitorKeyPickerDialog: true,
        MonitorAdvancedRequestConfig: true,
        ProviderIcon: {
          props: ['provider'],
          template: '<span :data-provider-icon="provider" />',
        },
      },
    },
  })
}

describe('MonitorFormDialog Grok defaults', () => {
  beforeEach(() => {
    createMonitor.mockReset()
    listTemplates.mockReset()
    showError.mockReset()
    showSuccess.mockReset()

    listTemplates.mockResolvedValue({ items: [] })
    createMonitor.mockResolvedValue({})
  })

  it('fills the xAI endpoint and grok-4.5 when Grok is selected', async () => {
    const wrapper = mountDialog()
    await flushPromises()

    await wrapper.get('button[data-provider="grok"]').trigger('click')

    expect((wrapper.get('#channel-monitor-endpoint').element as HTMLInputElement).value)
      .toBe('https://api.x.ai')
    expect((wrapper.get('#channel-monitor-primary-model').element as HTMLInputElement).value)
      .toBe('grok-4.5')
    expect(wrapper.get('[data-provider-icon="grok"]').exists()).toBe(true)
  })

  it('submits Grok through Chat Completions with the default endpoint and model', async () => {
    const wrapper = mountDialog()
    await flushPromises()

    await wrapper.get('button[data-provider="grok"]').trigger('click')
    await wrapper.get('input[placeholder="admin.channelMonitor.form.namePlaceholder"]')
      .setValue('Grok production')
    await wrapper.get('input[placeholder="admin.channelMonitor.form.apiKeyPlaceholder"]')
      .setValue('xai-test-secret')
    await wrapper.get('#channel-monitor-form').trigger('submit')
    await flushPromises()

    expect(createMonitor).toHaveBeenCalledWith(expect.objectContaining({
      name: 'Grok production',
      provider: 'grok',
      api_mode: 'chat_completions',
      endpoint: 'https://api.x.ai',
      api_key: 'xai-test-secret',
      primary_model: 'grok-4.5',
    }))
  })
})
