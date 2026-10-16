import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'

const { listAccounts, listWithEtag, getBatchTodayStats, getAllProxies, getAllGroups } = vi.hoisted(() => ({
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
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showInfo: vi.fn()
  })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ token: 'test-token', isSimpleMode: false })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

const DataTableStub = {
  props: ['columns', 'data'],
  template: `
    <div>
      <span data-test="priority-heading">
        {{ columns.find((column) => column.key === 'priority')?.label }}
      </span>
      <div v-for="row in data" :key="row.id" data-test="priority-value">
        <slot name="cell-priority" :row="row" :value="row.priority" />
      </div>
    </div>
  `
}

const AccountTableFiltersStub = {
  emits: ['update:filters'],
  template: `
    <div>
      <button data-test="select-group" @click="$emit('update:filters', { group: '12' })">group</button>
      <button data-test="select-ungrouped" @click="$emit('update:filters', { group: 'ungrouped' })">ungrouped</button>
    </div>
  `
}

const account = {
  id: 7,
  name: 'priority-account',
  platform: 'openai',
  type: 'oauth',
  priority: 50,
  account_groups: [
    { account_id: 7, group_id: 12, priority: 3 },
    { account_id: 7, group_id: 13, priority: 8 }
  ],
  status: 'active',
  schedulable: true
}

const mountView = () => mount(AccountsView, {
  global: {
    stubs: {
      AppLayout: { template: '<div><slot /></div>' },
      TablePageLayout: {
        template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>'
      },
      DataTable: DataTableStub,
      AccountTableFilters: AccountTableFiltersStub,
      Pagination: true,
      ConfirmDialog: true,
      AccountTableActions: { template: '<div><slot name="beforeCreate" /><slot name="after" /></div>' },
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
      HelpTooltip: true,
      Icon: true
    }
  }
})

describe('admin AccountsView priority scope', () => {
  beforeEach(() => {
    localStorage.clear()
    listAccounts.mockReset().mockResolvedValue({
      items: [account],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1
    })
    listWithEtag.mockReset().mockResolvedValue({ notModified: true, etag: null, data: null })
    getBatchTodayStats.mockReset().mockResolvedValue({ stats: {} })
    getAllProxies.mockReset().mockResolvedValue([])
    getAllGroups.mockReset().mockResolvedValue([])
  })

  it('shows account priority when no concrete group is selected', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-test="priority-heading"]').text()).toBe('admin.accounts.columns.priority')
    expect(wrapper.get('[data-test="priority-value"]').text()).toBe('50')
  })

  it('keeps showing account priority when a concrete group is selected', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-test="select-group"]').trigger('click')

    expect(wrapper.get('[data-test="priority-heading"]').text()).toBe('admin.accounts.columns.priority')
    expect(wrapper.get('[data-test="priority-value"]').text()).toBe('50')
  })

  it('keeps ungrouped filtering on account priority', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-test="select-ungrouped"]').trigger('click')

    expect(wrapper.get('[data-test="priority-heading"]').text()).toBe('admin.accounts.columns.priority')
    expect(wrapper.get('[data-test="priority-value"]').text()).toBe('50')
  })
})
