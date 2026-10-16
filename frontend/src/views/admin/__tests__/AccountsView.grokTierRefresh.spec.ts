import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'
import PlatformTypeBadge from '@/components/common/PlatformTypeBadge.vue'

const {
  listAccounts,
  listWithEtag,
  getBatchTodayStats,
  getAllProxies,
  getAllGroups
} = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  listWithEtag: vi.fn(),
  getBatchTodayStats: vi.fn(),
  getAllProxies: vi.fn(),
  getAllGroups: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      listWithEtag,
      getBatchTodayStats,
      delete: vi.fn(),
      batchClearError: vi.fn(),
      batchRefresh: vi.fn(),
      toggleSchedulable: vi.fn()
    },
    proxies: { getAll: getAllProxies },
    groups: { getAll: getAllGroups }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn() })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ token: 'test-token' })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

const mountView = () => mount(AccountsView, {
  global: {
    stubs: {
      AppLayout: { template: '<div><slot /></div>' },
      TablePageLayout: {
        template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>'
      },
      DataTable: {
        props: ['data'],
        template: '<div><div v-for="row in data" :key="row.id"><slot name="cell-platform_type" :row="row" /></div></div>'
      },
      Pagination: true,
      ConfirmDialog: true,
      AccountTableActions: true,
      AccountTableFilters: true,
      AccountBulkActionsBar: true,
      AccountActionMenu: true,
      ImportDataModal: true,
      ReAuthAccountModal: true,
      AccountTestModal: true,
      AccountStatsModal: true,
      ScheduledTestsPanel: true,
      SyncFromCrsModal: true,
      TempUnschedStatusModal: true,
      ErrorPassthroughRulesModal: true,
      TLSFingerprintProfilesModal: true,
      GrokOAuthReconcileDialog: true,
      TotpStepUpDialog: true,
      CreateAccountModal: true,
      EditAccountModal: true,
      BulkEditAccountModal: true,
      PlatformTypeBadge: true,
      AccountCapacityCell: true,
      AccountStatusIndicator: true,
      AccountTodayStatsCell: true,
      AccountGroupsCell: true,
      AccountUsageCell: true,
      Icon: true
    }
  }
})

describe('admin AccountsView — Grok 实时档位', () => {
  beforeEach(() => {
    localStorage.clear()
    for (const mock of [listAccounts, listWithEtag, getBatchTodayStats, getAllProxies, getAllGroups]) {
      mock.mockReset()
    }
    listWithEtag.mockResolvedValue({ notModified: true, etag: null, data: null })
    getBatchTodayStats.mockResolvedValue({ stats: {} })
    getAllProxies.mockResolvedValue([])
    getAllGroups.mockResolvedValue([])
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('徽章优先展示规范用量快照，避免旧凭据档位覆盖', async () => {
    listAccounts.mockResolvedValue({
      items: [{
        id: 201,
        name: 'live-tier',
        platform: 'grok',
        type: 'oauth',
        credentials: { subscription_tier: 'free', plan_type: 'free' },
        extra: {
          grok_usage_snapshot: { subscription_tier: 'supergrok_heavy' },
          grok_billing_snapshot: { plan: 'SuperGrok' }
        }
      }],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1
    })

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.findComponent(PlatformTypeBadge).props('planType')).toBe('supergrok_heavy')
    wrapper.unmount()
  })

  it('自动刷新收到新规范快照时替换账号行和徽章', async () => {
    vi.useFakeTimers()
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    localStorage.setItem('account-auto-refresh', JSON.stringify({ enabled: true, interval_seconds: 5 }))

    const initialAccount = {
      id: 202,
      name: 'refresh-tier',
      platform: 'grok',
      type: 'oauth',
      credentials: { subscription_tier: 'free' },
      extra: { grok_usage_snapshot: { subscription_tier: 'free', status_code: 200 } }
    }
    const refreshedAccount = {
      ...initialAccount,
      extra: { grok_usage_snapshot: { subscription_tier: 'supergrok', status_code: 200 } }
    }
    listAccounts.mockResolvedValue({ items: [initialAccount], total: 1, page: 1, page_size: 20, pages: 1 })
    listWithEtag.mockResolvedValueOnce({
      notModified: false,
      etag: 'grok-snapshot-2',
      data: { items: [refreshedAccount], total: 1, page: 1, page_size: 20, pages: 1 }
    })

    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.findComponent(PlatformTypeBadge).props('planType')).toBe('free')

    await vi.advanceTimersByTimeAsync(6000)
    await flushPromises()

    expect(listWithEtag).toHaveBeenCalledTimes(1)
    expect(wrapper.findComponent(PlatformTypeBadge).props('planType')).toBe('supergrok')
    wrapper.unmount()
  })
})
