import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import GrokOAuthReconcileDialog from '../GrokOAuthReconcileDialog.vue'

const { reconcileOAuthAccounts, saveAs } = vi.hoisted(() => ({
  reconcileOAuthAccounts: vi.fn(),
  saveAs: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    grok: { reconcileOAuthAccounts }
  }
}))

vi.mock('file-saver', () => ({ saveAs }))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

const emptyPage = (overrides: Record<string, unknown> = {}) => ({
  dry_run: true,
  scanned: 0,
  actionable: 0,
  would_block: 0,
  would_refresh: 0,
  blocked: 0,
  refreshed: 0,
  skipped: 0,
  failed: 0,
  partial: 0,
  items: [],
  next_after_id: 0,
  has_more: false,
  preview_token: 'preview-default',
  ...overrides
})

function mountDialog() {
  return mount(GrokOAuthReconcileDialog, {
    props: { show: false },
    global: {
      stubs: {
        BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
        Icon: true
      }
    }
  })
}

describe('GrokOAuthReconcileDialog', () => {
  beforeEach(() => {
    reconcileOAuthAccounts.mockReset()
    saveAs.mockReset()
  })

  it('打开后自动扫描并聚合全部游标页', async () => {
    reconcileOAuthAccounts
      .mockResolvedValueOnce(emptyPage({ scanned: 2, actionable: 1, would_block: 1, has_more: true, next_after_id: 2 }))
      .mockResolvedValueOnce(emptyPage({ scanned: 1, actionable: 1, would_refresh: 1 }))
    const wrapper = mountDialog()

    await wrapper.setProps({ show: true })
    await flushPromises()

    expect(reconcileOAuthAccounts).toHaveBeenNthCalledWith(1, false, 0, 500)
    expect(reconcileOAuthAccounts).toHaveBeenNthCalledWith(2, false, 2, 500)
    expect(wrapper.text()).toContain('3')
  })

  it('修复必须二次点击确认并使用显式 apply 模式', async () => {
    reconcileOAuthAccounts
      .mockResolvedValueOnce(emptyPage({ actionable: 1, would_block: 1, items: [{ account_id: 7, reason: 'missing_refresh_token', action: 'block_account', outcome: 'planned' }] }))
      .mockResolvedValueOnce(emptyPage({ dry_run: false, blocked: 1 }))
    const wrapper = mountDialog()
    await wrapper.setProps({ show: true })
    await flushPromises()

    const repair = wrapper.findAll('button').find((button) => button.text() === 'admin.accounts.grokReconcile.repair')
    expect(repair).toBeTruthy()
    await repair!.trigger('click')
    expect(reconcileOAuthAccounts).toHaveBeenCalledTimes(1)

    const confirm = wrapper.findAll('button').find((button) => button.text() === 'admin.accounts.grokReconcile.confirmRepair')
    expect(confirm).toBeTruthy()
    await confirm!.trigger('click')
    await flushPromises()

    expect(reconcileOAuthAccounts).toHaveBeenLastCalledWith(true, 0, 500, 'preview-default', [7])
    expect(wrapper.emitted('repaired')).toHaveLength(1)
  })

  it('扫描失败时保留可重试错误状态', async () => {
    reconcileOAuthAccounts.mockRejectedValueOnce(new Error('network unavailable'))
    const wrapper = mountDialog()

    await wrapper.setProps({ show: true })
    await flushPromises()

    expect(wrapper.text()).toContain('network unavailable')
    expect(wrapper.text()).toContain('admin.accounts.grokReconcile.scanAgain')
  })

  it('支持只修复勾选账号并导出脱敏结果', async () => {
    reconcileOAuthAccounts
      .mockResolvedValueOnce(emptyPage({
        actionable: 2,
        would_block: 2,
        items: [
          { account_id: 7, reason: 'missing_refresh_token', action: 'block_account', outcome: 'planned' },
          { account_id: 8, reason: 'duplicate_stable_identity', action: 'block_account', outcome: 'planned' }
        ]
      }))
      .mockResolvedValueOnce(emptyPage({ dry_run: false, blocked: 1 }))
    const wrapper = mountDialog()
    await wrapper.setProps({ show: true })
    await flushPromises()

    const rowCheckboxes = wrapper.findAll('tbody input[type="checkbox"]')
    expect(rowCheckboxes).toHaveLength(2)
    await rowCheckboxes[0].trigger('change')

    const exportButton = wrapper.findAll('button').find((button) => button.text() === 'common.export')
    await exportButton!.trigger('click')
    expect(saveAs).toHaveBeenCalledTimes(1)

    await wrapper.findAll('button').find((button) => button.text() === 'admin.accounts.grokReconcile.repair')!.trigger('click')
    await wrapper.findAll('button').find((button) => button.text() === 'admin.accounts.grokReconcile.confirmRepair')!.trigger('click')
    await flushPromises()

    expect(reconcileOAuthAccounts).toHaveBeenLastCalledWith(true, 0, 500, 'preview-default', [8])
  })

  it('超过一百条时可翻到第二页勾选并只修复该账号', async () => {
    const items = Array.from({ length: 101 }, (_, index) => ({
      account_id: index + 1,
      reason: 'missing_refresh_token',
      action: 'block_account',
      outcome: 'planned'
    }))
    reconcileOAuthAccounts
      .mockResolvedValueOnce(emptyPage({ actionable: 101, would_block: 101, items }))
      .mockResolvedValueOnce(emptyPage({ dry_run: false, blocked: 1 }))
    const wrapper = mountDialog()
    await wrapper.setProps({ show: true })
    await flushPromises()

    expect(wrapper.findAll('tbody tr')).toHaveLength(100)
    await wrapper.findAll('button').find((button) => button.text() === 'common.clearSelection')!.trigger('click')
    await wrapper.findAll('button').find((button) => button.text() === 'pagination.next')!.trigger('click')
    expect(wrapper.findAll('tbody tr')).toHaveLength(1)
    expect(wrapper.text()).toContain('#101')

    await wrapper.find('tbody input[type="checkbox"]').trigger('change')
    await wrapper.findAll('button').find((button) => button.text() === 'admin.accounts.grokReconcile.repair')!.trigger('click')
    await wrapper.findAll('button').find((button) => button.text() === 'admin.accounts.grokReconcile.confirmRepair')!.trigger('click')
    await flushPromises()

    expect(reconcileOAuthAccounts).toHaveBeenLastCalledWith(true, 0, 500, 'preview-default', [101])
  })
})
