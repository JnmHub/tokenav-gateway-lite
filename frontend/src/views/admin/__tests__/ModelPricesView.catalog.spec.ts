import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

import ModelPricesView from '../ModelPricesView.vue'

const { list, getSyncStatus, sync, showError, showSuccess } = vi.hoisted(() => ({
  list: vi.fn(),
  getSyncStatus: vi.fn(),
  sync: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    modelPrices: {
      list,
      getSyncStatus,
      sync,
      upsert: vi.fn(),
      removeOverride: vi.fn(),
    },
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess }),
}))

vi.mock('@/utils/apiError', () => ({
  extractApiErrorMessage: (_error: unknown, fallback: string) => fallback,
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      locale: ref('zh'),
      t: (key: string) => key,
    }),
  }
})

describe('admin ModelPricesView catalog status', () => {
  beforeEach(() => {
    list.mockReset().mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20, pages: 0 })
    getSyncStatus.mockReset().mockResolvedValue({
      enabled: true,
      model_count: 420,
      source: 'remote',
      last_success_at: '2026-08-22T08:00:00Z',
      last_error: '',
      refreshing: false,
    })
    sync.mockReset().mockResolvedValue({
      enabled: true,
      model_count: 421,
      source: 'remote',
      last_success_at: '2026-08-23T08:00:00Z',
      last_error: '',
      refreshing: false,
    })
    showError.mockReset()
    showSuccess.mockReset()
  })

  it('renders catalog metadata and supports an immediate sync', async () => {
    const wrapper = mount(ModelPricesView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          TablePageLayout: { template: '<div><slot name="actions" /><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
          DataTable: { template: '<div><slot name="empty" /></div>' },
          Pagination: true,
          EmptyState: true,
          BaseDialog: true,
          ConfirmDialog: true,
          Icon: true,
        },
      },
    })
    await flushPromises()

    expect(getSyncStatus).toHaveBeenCalledOnce()
    expect(wrapper.get('[data-test="model-price-catalog-status"]').text()).toContain('420')

    await wrapper.get('[data-test="sync-model-price-catalog"]').trigger('click')
    await flushPromises()
    expect(sync).toHaveBeenCalledOnce()
    expect(wrapper.get('[data-test="model-price-catalog-status"]').text()).toContain('421')
    expect(showSuccess).toHaveBeenCalledWith('admin.modelPrices.catalogStatus.syncSuccess')
  })
})
