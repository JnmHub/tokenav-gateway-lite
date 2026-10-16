import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AuditLogView from '../AuditLogView.vue'

const { list } = vi.hoisted(() => ({
  list: vi.fn().mockResolvedValue({ items: [] })
}))

vi.mock('@/api/admin/audit', () => ({
  auditAPI: {
    list,
    getById: vi.fn(),
    clear: vi.fn()
  },
  default: { list, getById: vi.fn(), clear: vi.fn() }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() })
}))

vi.mock('@/composables/useStepUp', () => ({
  StepUpScope: { AuditClear: 'audit_clear' },
  isStepUpCancelled: () => false,
  useStepUp: () => ({ run: vi.fn() })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ locale: { value: 'zh-CN' } })
  }
})

describe('AuditLogView 上海时区筛选', () => {
  it('把 datetime-local 明确按中国上海时间转换成 RFC3339', async () => {
    const originalTimeZone = process.env.TZ
    process.env.TZ = 'America/Los_Angeles'
    try {
      const wrapper = mount(AuditLogView, {
        global: {
          stubs: {
            AppLayout: { template: '<div><slot /></div>' },
            BaseDialog: true,
            TotpStepUpDialog: true
          }
        }
      })
      await flushPromises()
      list.mockClear()

      const dateInputs = wrapper.findAll<HTMLInputElement>('input[type="datetime-local"]')
      await dateInputs[0].setValue('2026-08-04T00:30')
      await dateInputs[1].setValue('2026-08-04T01:30')
      await wrapper.get('form').trigger('submit')
      await flushPromises()

      expect(list).toHaveBeenCalledWith(expect.objectContaining({
        start_time: '2026-08-03T16:30:00.000Z',
        end_time: '2026-08-03T17:30:00.000Z'
      }))
      wrapper.unmount()
    } finally {
      if (originalTimeZone === undefined) delete process.env.TZ
      else process.env.TZ = originalTimeZone
    }
  })
})
