import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AccountTestModal from '../AccountTestModal.vue'

const { getAvailableModels, copyToClipboard } = vi.hoisted(() => ({
  getAvailableModels: vi.fn(),
  copyToClipboard: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      getAvailableModels
    }
  }
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({
    copyToClipboard
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  const messages: Record<string, string> = {
    'admin.accounts.imagePromptDefault': 'Generate a cute orange cat astronaut sticker on a clean pastel background.',
    'admin.accounts.videoPromptDefault': 'Generate a short video.',
    'admin.accounts.grok.ttsTextDefault': 'Hello from Tokenav.'
  }
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, string | number>) => {
        if (key === 'admin.accounts.imageReceived' && params?.count) {
          return `received-${params.count}`
        }
        return messages[key] || key
      }
    })
  }
})

function createStreamResponse(lines: string[]) {
  const encoder = new TextEncoder()
  const chunks = lines.map((line) => encoder.encode(line))
  let index = 0

  return {
    ok: true,
    body: {
      getReader: () => ({
        read: vi.fn().mockImplementation(async () => {
          if (index < chunks.length) {
            return { done: false, value: chunks[index++] }
          }
          return { done: true, value: undefined }
        })
      })
    }
  } as Response
}

function mountModal(account: Partial<any> = {}) {
  return mount(AccountTestModal, {
    props: {
      show: false,
      account: {
        id: 42,
        name: 'Gemini Image Test',
        platform: 'gemini',
        type: 'apikey',
        status: 'active',
        ...account
      }
    } as any,
    global: {
      stubs: {
        BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
        Select: {
          props: ['modelValue', 'options', 'valueKey', 'labelKey'],
          emits: ['update:modelValue'],
          template: `
            <select
              class="select-stub"
              :value="modelValue"
              @change="$emit('update:modelValue', $event.target.value)"
            >
              <option
                v-for="option in options"
                :key="valueKey ? option[valueKey] : option.value"
                :value="valueKey ? option[valueKey] : option.value"
              >
                {{ labelKey ? option[labelKey] : option.label }}
              </option>
            </select>
          `
        },
        TextArea: {
          props: ['modelValue'],
          emits: ['update:modelValue'],
          template: '<textarea class="textarea-stub" :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />'
        },
        Icon: true
      }
    }
  })
}

describe('AccountTestModal', () => {
  beforeEach(() => {
    getAvailableModels.mockResolvedValue([
      { id: 'gemini-2.0-flash', display_name: 'Gemini 2.0 Flash' },
      { id: 'gemini-2.5-flash-image', display_name: 'Gemini 2.5 Flash Image' },
      { id: 'gemini-3.1-flash-image', display_name: 'Gemini 3.1 Flash Image' }
    ])
    copyToClipboard.mockReset()
    Object.defineProperty(globalThis, 'localStorage', {
      value: {
        getItem: vi.fn((key: string) => (key === 'auth_token' ? 'test-token' : null)),
        setItem: vi.fn(),
        removeItem: vi.fn(),
        clear: vi.fn()
      },
      configurable: true
    })
    global.fetch = vi.fn().mockResolvedValue(
      createStreamResponse([
        'data: {"type":"test_start","model":"gemini-2.5-flash-image"}\n',
        'data: {"type":"image","image_url":"data:image/png;base64,QUJD","mime_type":"image/png"}\n',
        'data: {"type":"test_complete","success":true}\n'
      ])
    ) as any
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('gemini 图片模型测试会携带提示词并渲染图片预览', async () => {
    const wrapper = mountModal()
    await wrapper.setProps({ show: true })
    await flushPromises()

    const promptInput = wrapper.find('textarea.textarea-stub')
    expect(promptInput.exists()).toBe(true)
    await promptInput.setValue('draw a tiny orange cat astronaut')

    const buttons = wrapper.findAll('button')
    const startButton = buttons.find((button) => button.text().includes('admin.accounts.startTest'))
    expect(startButton).toBeTruthy()

    await startButton!.trigger('click')
    await flushPromises()
    await flushPromises()

    expect(global.fetch).toHaveBeenCalledTimes(1)
    const [, request] = (global.fetch as any).mock.calls[0]
    expect(JSON.parse(request.body)).toEqual({
      model_id: 'gemini-3.1-flash-image',
      prompt: 'draw a tiny orange cat astronaut'
    })

    const preview = wrapper.find('img[alt="test-image-1"]')
    expect(preview.exists()).toBe(true)
    expect(preview.attributes('src')).toBe('data:image/png;base64,QUJD')
  })

  it('OpenAI compact 探测模式会写入测试请求体', async () => {
    getAvailableModels.mockResolvedValue([
      { id: 'gpt-5.4-mini', display_name: 'gpt-5.4-mini' }
    ])
    global.fetch = vi.fn().mockResolvedValue(
      createStreamResponse([
        'data: {"type":"test_start","model":"gpt-5.4-mini"}\n',
        'data: {"type":"test_complete","success":true}\n'
      ])
    ) as any

    const wrapper = mountModal({
      name: 'OpenAI Test',
      platform: 'openai',
      type: 'oauth'
    })
    await wrapper.setProps({ show: true })
    await flushPromises()

    const selects = wrapper.findAll('select.select-stub')
    expect(selects.length).toBeGreaterThanOrEqual(2)
    await selects[1].setValue('compact')

    const startButton = wrapper.findAll('button').find((button) => button.text().includes('admin.accounts.startTest'))
    expect(startButton).toBeTruthy()
    await startButton!.trigger('click')
    await flushPromises()
    await flushPromises()

    expect(global.fetch).toHaveBeenCalledTimes(1)
    const [, request] = (global.fetch as any).mock.calls[0]
    expect(JSON.parse(request.body)).toEqual({
      model_id: 'gpt-5.4-mini',
      prompt: '',
      mode: 'compact'
    })
  })

  it('Grok TTS 测试会发送模式并渲染音频预览', async () => {
    getAvailableModels.mockResolvedValue([{ id: 'grok-4.5', display_name: 'grok-4.5' }])
    global.fetch = vi.fn().mockResolvedValue(
      createStreamResponse([
        'data: {"type":"test_start"}\n',
        'data: {"type":"audio","audio_url":"data:audio/mpeg;base64,QUJD","mime_type":"audio/mpeg"}\n',
        'data: {"type":"test_complete","success":true}\n'
      ])
    ) as any

    const wrapper = mountModal({ name: 'Grok Test', platform: 'grok' })
    await wrapper.setProps({ show: true })
    await flushPromises()

    const selects = wrapper.findAll('select.select-stub')
    await selects[1].setValue('tts')
    await flushPromises()
    await wrapper.find('textarea.textarea-stub').setValue('hello from Tokenav')

    const startButton = wrapper.findAll('button').find((button) => button.text().includes('admin.accounts.startTest'))
    await startButton!.trigger('click')
    await flushPromises()
    await flushPromises()

    const [, request] = (global.fetch as any).mock.calls[0]
    expect(JSON.parse(request.body)).toEqual({ model_id: '', prompt: 'hello from Tokenav', mode: 'tts' })
    expect(wrapper.find('audio').attributes('src')).toBe('data:audio/mpeg;base64,QUJD')
  })

  it('Grok 视频测试会渲染视频预览', async () => {
    getAvailableModels.mockResolvedValue([{ id: 'grok-imagine-video', display_name: 'grok-imagine-video' }])
    global.fetch = vi.fn().mockResolvedValue(
      createStreamResponse([
        'data: {"type":"test_start","model":"grok-imagine-video"}\n',
        'data: {"type":"video","video_url":"https://cdn.example.com/demo.mp4","mime_type":"video/mp4"}\n',
        'data: {"type":"test_complete","success":true}\n'
      ])
    ) as any

    const wrapper = mountModal({ name: 'Grok Test', platform: 'grok' })
    await wrapper.setProps({ show: true })
    await flushPromises()

    const selects = wrapper.findAll('select.select-stub')
    await selects[1].setValue('video')
    await flushPromises()
    await wrapper.find('textarea.textarea-stub').setValue('a short launch video')

    const startButton = wrapper.findAll('button').find((button) => button.text().includes('admin.accounts.startTest'))
    await startButton!.trigger('click')
    await flushPromises()
    await flushPromises()

    const [, request] = (global.fetch as any).mock.calls[0]
    expect(JSON.parse(request.body)).toEqual({
      model_id: 'grok-imagine-video',
      prompt: 'a short launch video',
      mode: 'video'
    })
    expect(wrapper.find('video').attributes('src')).toBe('https://cdn.example.com/demo.mp4')
  })
})
