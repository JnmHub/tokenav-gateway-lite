import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import UsageCleanupDialog from '../UsageCleanupDialog.vue'

const { createCleanupTask, listCleanupTasks } = vi.hoisted(() => ({
  createCleanupTask: vi.fn().mockResolvedValue({ id: 1, status: 'pending' }),
  listCleanupTasks: vi.fn().mockResolvedValue({ items: [], total: 0, page: 1, page_size: 5 })
}))

vi.mock('@/api/admin/usage', () => ({
  adminUsageAPI: {
    createCleanupTask,
    listCleanupTasks,
    cancelCleanupTask: vi.fn()
  },
  default: {
    createCleanupTask,
    listCleanupTasks,
    cancelCleanupTask: vi.fn()
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

describe('UsageCleanupDialog 上海时区清理范围', () => {
  it('无论浏览器在哪都固定提交 Asia/Shanghai', async () => {
    const originalTimeZone = process.env.TZ
    process.env.TZ = 'America/Los_Angeles'
    try {
      const wrapper = mount(UsageCleanupDialog, {
        props: {
          show: false,
          filters: {},
          startDate: '2026-08-03',
          endDate: '2026-08-04'
        },
        global: {
          stubs: {
            BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
            ConfirmDialog: {
              props: ['show'],
              emits: ['confirm', 'cancel'],
              template: '<button v-if="show" data-test="confirm-cleanup" @click="$emit(\'confirm\')">confirm</button>'
            },
            Pagination: true,
            UsageFilters: true
          }
        }
      })

      await wrapper.setProps({ show: true })
      await flushPromises()
      await wrapper.get('.btn-danger').trigger('click')
      await wrapper.get('[data-test="confirm-cleanup"]').trigger('click')
      await flushPromises()

      expect(createCleanupTask).toHaveBeenCalledWith(expect.objectContaining({
        start_date: '2026-08-03',
        end_date: '2026-08-04',
        timezone: 'Asia/Shanghai'
      }))
      wrapper.unmount()
    } finally {
      if (originalTimeZone === undefined) delete process.env.TZ
      else process.env.TZ = originalTimeZone
    }
  })
})
