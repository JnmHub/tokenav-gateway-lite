import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, ref } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

import GatewayLiteLocalKeysView from '../GatewayLiteLocalKeysView.vue'

const { listKeys, revealKey, updateKey, getGroups, showError, showSuccess, copyToClipboard } = vi.hoisted(() => ({
  listKeys: vi.fn(),
  revealKey: vi.fn(),
  updateKey: vi.fn(),
  getGroups: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
  copyToClipboard: vi.fn(),
}))

vi.mock('@/api', () => ({
  adminAPI: {
    gatewayLiteLocalKeys: {
      list: listKeys,
      create: vi.fn(),
      reveal: revealKey,
      update: updateKey,
      remove: vi.fn(),
    },
    groups: {
      getAll: getGroups,
    },
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess }),
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copied: { value: false }, copyToClipboard }),
}))

vi.mock('@/utils/apiError', () => ({
  extractApiErrorMessage: (_error: unknown, fallback: string) => fallback,
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ locale: ref('zh-CN') }),
  }
})

const AppLayoutStub = { template: '<div><slot /></div>' }
const BaseDialogStub = defineComponent({
  props: { show: Boolean },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
})

const mountView = () => mount(GatewayLiteLocalKeysView, {
  global: {
    stubs: {
      AppLayout: AppLayoutStub,
      BaseDialog: BaseDialogStub,
      ConfirmDialog: true,
      Select: true,
      Toggle: true,
      Icon: true,
    },
  },
})

const rawKey = () => ({
  id: 1,
  name: '本地自动化',
  key_masked: 'gw_local_...test',
  group_id: 2,
  group_name: 'OpenAI',
  platform: 'openai',
  status: 'active',
  ip_whitelist: null,
  ip_blacklist: null,
  last_used_at: null,
  created_at: '2026-07-15T00:00:00Z',
  updated_at: '2026-07-15T00:00:00Z',
})

describe('GatewayLiteLocalKeysView null-safe states', () => {
  beforeEach(() => {
    localStorage.clear()
    listKeys.mockReset()
    revealKey.mockReset()
    updateKey.mockReset()
    getGroups.mockReset()
    showError.mockReset()
    showSuccess.mockReset()
    copyToClipboard.mockReset()

    listKeys.mockResolvedValue([rawKey()])
    revealKey.mockResolvedValue({ secret: 'glk_full-secret' })
    copyToClipboard.mockResolvedValue(true)
    getGroups.mockResolvedValue([{ id: 2, name: 'OpenAI', platform: 'openai', status: 'active' }])
    updateKey.mockImplementation(async (_id: number, payload: Record<string, unknown>) => ({
      ...rawKey(),
      ...payload,
      ip_whitelist: null,
      ip_blacklist: null,
    }))
  })

  it('renders and edits keys whose IP rule fields are null', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('白名单: 不限')
    expect(wrapper.text()).toContain('黑名单: 0')

    await wrapper.get('button[title="编辑"]').trigger('click')
    expect((wrapper.get('#gateway-local-key-allowlist').element as HTMLTextAreaElement).value).toBe('')
    expect((wrapper.get('#gateway-local-key-blocklist').element as HTMLTextAreaElement).value).toBe('')
  })

  it('shows the normal empty state for an empty key list', async () => {
    listKeys.mockResolvedValueOnce([])
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('还没有本地密钥')
    expect(wrapper.text()).not.toContain('本地密钥加载失败')
  })

  it('keeps the list usable when a status update response contains null rules', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('button[title="停用"]').trigger('click')
    await flushPromises()

    expect(updateKey).toHaveBeenCalledWith(1, { status: 'disabled' })
    expect(wrapper.text()).toContain('白名单: 不限')
    expect(wrapper.text()).toContain('黑名单: 0')
  })

  it('reveals one full key on demand, copies it, and does not persist it in the row', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('button[title="复制完整密钥"]').trigger('click')
    await flushPromises()

    expect(revealKey).toHaveBeenCalledWith(1)
    expect(copyToClipboard).toHaveBeenCalledWith('glk_full-secret', '完整密钥已复制')
    expect(wrapper.text()).not.toContain('glk_full-secret')
  })

  it('shows a stable bilingual-aware retry state when loading fails', async () => {
    listKeys.mockRejectedValueOnce(new Error('network down'))
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('本地密钥加载失败')
    expect(wrapper.text()).toContain('读取本地密钥失败，请稍后重试。')
    expect(wrapper.text()).toContain('重新加载')
    expect(showError).toHaveBeenCalled()
  })

  it('默认隐藏 ID，并可从列设置开启和持久化', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.findAll('th').map((header) => header.text())).not.toContain('ID')
    expect(localStorage.getItem('gateway-lite-local-keys-hidden-columns')).toBe(JSON.stringify(['id']))

    await wrapper.get('button[title="列设置"]').trigger('click')
    await wrapper.get('button[data-column-key="id"]').trigger('click')

    expect(wrapper.findAll('th').map((header) => header.text())).toContain('ID')
    expect(wrapper.text()).toContain('#1')
    expect(localStorage.getItem('gateway-lite-local-keys-hidden-columns')).toBe(JSON.stringify([]))
  })

  it('恢复已保存的 ID 列显示选择', async () => {
    localStorage.setItem('gateway-lite-local-keys-hidden-columns', JSON.stringify([]))

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.findAll('th').map((header) => header.text())).toContain('ID')
  })
})
