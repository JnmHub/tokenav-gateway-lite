import { describe, it, expect, vi, beforeEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ImportDataModal from '@/components/admin/account/ImportDataModal.vue'
import { adminAPI } from '@/api/admin'

const showError = vi.fn()
const showSuccess = vi.fn()
const showWarning = vi.fn()

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError,
    showSuccess,
    showWarning
  })
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      importData: vi.fn()
    }
  }
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

describe('ImportDataModal', () => {
  beforeEach(() => {
    showError.mockReset()
    showSuccess.mockReset()
    showWarning.mockReset()
    vi.mocked(adminAPI.accounts.importData).mockReset()
  })

  const mountModal = () =>
    mount(ImportDataModal, {
      props: { show: true },
      global: {
        stubs: {
          BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' }
        }
      }
    })

  const setFiles = async (wrapper: ReturnType<typeof mount>, files: File[]) => {
    const input = wrapper.find('input[type="file"]')
    Object.defineProperty(input.element, 'files', {
      configurable: true,
      value: files
    })
    await input.trigger('change')
    await flushPromises()
  }

  const jsonFile = (name: string, payload: unknown) => {
    const content = typeof payload === 'string' ? payload : JSON.stringify(payload)
    const file = new File([content], name, { type: 'application/json' })
    Object.defineProperty(file, 'text', {
      value: () => Promise.resolve(content)
    })
    return file
  }

  it('未选择文件时提示错误', async () => {
    const wrapper = mountModal()

    await wrapper.find('form').trigger('submit')
    expect(showError).toHaveBeenCalledWith('admin.accounts.dataImportSelectFile')
  })

  it('无效 JSON 时提示解析失败', async () => {
    const wrapper = mountModal()
    await setFiles(wrapper, [jsonFile('broken.json', 'invalid json')])
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(wrapper.text()).toContain('broken.json')
    expect(wrapper.text()).toContain('admin.accounts.dataImportParseFailed')
    expect(showError).toHaveBeenCalledWith('admin.accounts.dataImportNoValidFiles')
  })

  it('有效 JSON 会生成预览并提交解析后的 payload', async () => {
    vi.mocked(adminAPI.accounts.importData).mockResolvedValue({
      proxy_created: 0,
      proxy_reused: 0,
      proxy_failed: 0,
      account_created: 1,
      account_failed: 0
    })

    const wrapper = mountModal()

    const payload = {
      type: 'sub2api-data',
      version: 1,
      exported_at: '2026-07-08T00:00:00Z',
      proxies: [],
      accounts: [
        {
          name: 'acc-1',
          platform: 'openai',
          type: 'apikey',
          credentials: { api_key: 'sk-test' },
          concurrency: 1,
          priority: 1
        }
      ]
    }

    await setFiles(wrapper, [jsonFile('data.json', payload)])

    expect(wrapper.text()).toContain('admin.accounts.dataImportPreview')
    expect(wrapper.text()).toContain('v1')

    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(adminAPI.accounts.importData).toHaveBeenCalledWith({
      data: payload,
      skip_default_group_bind: true
    })
    expect(showSuccess).toHaveBeenCalledWith('admin.accounts.dataImportSuccess')
  })

  it('Grok OAuth 旧格式缺少可选字段时仍保留路由和请求头覆写', async () => {
    vi.mocked(adminAPI.accounts.importData).mockResolvedValue({
      proxy_created: 0,
      proxy_reused: 0,
      proxy_failed: 0,
      account_created: 1,
      account_failed: 0
    })
    const wrapper = mountModal()
    const account = {
      name: 'grok-regional',
      platform: 'grok',
      type: 'oauth',
      credentials: {
        refresh_token: 'grok-refresh',
        base_url: 'https://us-east-1.api.x.ai/v1',
        header_override_enabled: true,
        header_overrides: { 'x-tenant': 'tenant-a' }
      }
    }
    const payload = {
      proxies: [],
      accounts: [account]
    }

    await setFiles(wrapper, [jsonFile('grok-legacy.json', payload)])
    expect(wrapper.text()).toContain('admin.accounts.dataImportPreviewLegacy')

    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(adminAPI.accounts.importData).toHaveBeenCalledWith({
      data: expect.objectContaining({
        accounts: [account],
        proxies: []
      }),
      skip_default_group_bind: true
    })
    expect(showError).not.toHaveBeenCalled()
  })

  it('结构错误的 JSON 不会提交到后端', async () => {
    const wrapper = mountModal()
    await setFiles(wrapper, [jsonFile('invalid-shape.json', { accounts: {}, proxies: [] })])
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(adminAPI.accounts.importData).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('admin.accounts.dataImportInvalidShape')
    expect(showError).toHaveBeenCalledWith('admin.accounts.dataImportNoValidFiles')
  })

  it('拖拽 JSON 文件后会生成导入预览', async () => {
    const wrapper = mountModal()

    const payload = {
      exported_at: '2026-07-08T00:00:00Z',
      proxies: [{ proxy_key: 'http|127.0.0.1|8080||', name: 'p1', protocol: 'http', host: '127.0.0.1', port: 8080, status: 'active' }],
      accounts: []
    }
    const file = new File([JSON.stringify(payload)], 'data.json', { type: 'application/json' })
    Object.defineProperty(file, 'text', {
      value: () => Promise.resolve(JSON.stringify(payload))
    })

    await wrapper.find('[data-testid="account-import-dropzone"]').trigger('drop', {
      dataTransfer: { files: [file] }
    })
    await flushPromises()

    expect(wrapper.text()).toContain('admin.accounts.dataImportPreview')
    expect(wrapper.text()).toContain('admin.accounts.dataImportPreviewLegacy')
  })

  it('多个有效文件会合并账号与代理后一次提交', async () => {
    vi.mocked(adminAPI.accounts.importData).mockResolvedValue({
      proxy_created: 1,
      proxy_reused: 0,
      proxy_failed: 0,
      account_created: 1,
      account_failed: 0
    })
    const wrapper = mountModal()
    const account = {
      name: 'acc-1',
      platform: 'openai',
      type: 'apikey',
      credentials: { api_key: 'sk-test' },
      concurrency: 1,
      priority: 1
    }
    const proxy = {
      proxy_key: 'http|127.0.0.1|8080||',
      name: 'proxy-1',
      protocol: 'http',
      host: '127.0.0.1',
      port: 8080,
      status: 'active'
    }

    await setFiles(wrapper, [
      jsonFile('accounts.json', {
        type: 'sub2api-data',
        version: 1,
        exported_at: '2026-07-08T00:00:00Z',
        proxies: [],
        accounts: [account]
      }),
      jsonFile('proxies.json', {
        type: 'sub2api-bundle',
        version: 1,
        exported_at: '2026-07-08T00:00:00Z',
        proxies: [proxy],
        accounts: []
      })
    ])

    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(adminAPI.accounts.importData).toHaveBeenCalledWith({
      data: expect.objectContaining({
        version: 1,
        accounts: [account],
        proxies: [proxy]
      }),
      skip_default_group_bind: true
    })
  })

  it('有效文件与损坏文件混合时跳过损坏文件并继续导入', async () => {
    vi.mocked(adminAPI.accounts.importData).mockResolvedValue({
      proxy_created: 0,
      proxy_reused: 0,
      proxy_failed: 0,
      account_created: 0,
      account_failed: 0
    })
    const wrapper = mountModal()
    const validPayload = {
      type: 'sub2api-data',
      version: 1,
      exported_at: '2026-07-08T00:00:00Z',
      proxies: [],
      accounts: []
    }

    await setFiles(wrapper, [jsonFile('valid.json', validPayload), jsonFile('broken.json', '{')])

    expect(wrapper.text()).toContain('broken.json')
    expect(showWarning).toHaveBeenCalledWith(
      'admin.accounts.dataImportSkippedInvalidFiles'
    )

    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(adminAPI.accounts.importData).toHaveBeenCalledWith({
      data: validPayload,
      skip_default_group_bind: true
    })
  })

  it.each([
    ['wrong-type.json', { type: 'other-data', version: 1, accounts: [], proxies: [] }, 'admin.accounts.dataImportUnsupportedType'],
    ['wrong-version.json', { type: 'sub2api-data', version: 2, accounts: [], proxies: [] }, 'admin.accounts.dataImportUnsupportedVersion']
  ])('拒绝错误类型或版本的文件 %s', async (name, payload, message) => {
    const wrapper = mountModal()
    await setFiles(wrapper, [jsonFile(name, payload)])

    expect(wrapper.text()).toContain(name)
    expect(wrapper.text()).toContain(message)
    expect(adminAPI.accounts.importData).not.toHaveBeenCalled()
  })

  it('后端部分成功时保留结果，关闭后刷新账号与代理列表', async () => {
    vi.mocked(adminAPI.accounts.importData).mockResolvedValue({
      proxy_created: 0,
      proxy_reused: 1,
      proxy_failed: 0,
      account_created: 1,
      account_failed: 1,
      errors: [
        { kind: 'account', name: 'duplicate-account', message: 'already exists' }
      ]
    })
    const wrapper = mountModal()
    await setFiles(wrapper, [
      jsonFile('mixed.json', {
        type: 'sub2api-data',
        version: 1,
        exported_at: '2026-07-08T00:00:00Z',
        proxies: [],
        accounts: []
      })
    ])

    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('admin.accounts.dataImportCompletedWithErrors')
    expect(wrapper.text()).toContain('duplicate-account')
    expect(wrapper.emitted('imported')).toBeUndefined()

    const cancelButton = wrapper.findAll('button').find((button) => button.text() === 'common.cancel')
    await cancelButton?.trigger('click')
    expect(wrapper.emitted('imported')).toHaveLength(1)
    expect(wrapper.emitted('close')).toHaveLength(1)
  })
})
