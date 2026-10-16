import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import OpsSystemLogTable from '../OpsSystemLogTable.vue'

const mocks = vi.hoisted(() => ({
  listSystemLogs: vi.fn(),
  cleanupSystemLogs: vi.fn()
}))

vi.mock('@/api/admin/ops', () => ({
  opsAPI: {
    listSystemLogs: mocks.listSystemLogs,
    cleanupSystemLogs: mocks.cleanupSystemLogs,
    getSystemLogSinkHealth: vi.fn().mockResolvedValue({
      queue_depth: 0,
      queue_capacity: 100,
      dropped_count: 0,
      write_failed_count: 0,
      written_count: 0,
      avg_write_delay_ms: 0
    }),
    getRuntimeLogConfig: vi.fn().mockResolvedValue({
      level: 'info',
      enable_sampling: false,
      sampling_initial: 100,
      sampling_thereafter: 100,
      caller: true,
      stacktrace_level: 'error',
      retention_days: 30
    }),
    updateRuntimeLogConfig: vi.fn(),
    resetRuntimeLogConfig: vi.fn()
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showSuccess: vi.fn(),
    showError: vi.fn()
  })
}))

const SelectStub = {
  props: ['modelValue', 'options'],
  emits: ['update:modelValue'],
  template: '<select :value="modelValue" @change="$emit(\'update:modelValue\', $event.target.value)"><option v-for="option in options" :key="option.value" :value="option.value">{{ option.label }}</option></select>'
}

const PaginationStub = {
  props: ['total', 'page', 'pageSize'],
  template: '<div class="pagination-stub" />'
}

async function mountTable() {
  mocks.listSystemLogs.mockResolvedValue({ items: [], total: 0 })
  mocks.cleanupSystemLogs.mockResolvedValue({ deleted: 1 })

  const wrapper = mount(OpsSystemLogTable, {
    global: {
      stubs: {
        Select: SelectStub,
        Pagination: PaginationStub
      }
    }
  })
  await flushPromises()
  return wrapper
}

function inputByLabel(wrapper: ReturnType<typeof mount>, labelText: string) {
  const label = wrapper.findAll('label').find((item) => item.text().includes(labelText))
  expect(label, `label ${labelText} should exist`).toBeTruthy()
  return label!.find('input')
}

describe('OpsSystemLogTable api_key_id filter', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    window.confirm = vi.fn().mockReturnValue(true)
  })

  it('sends api_key_id when querying system logs', async () => {
    const wrapper = await mountTable()

    mocks.listSystemLogs.mockClear()
    await inputByLabel(wrapper, 'api_key_id').setValue('42')
    await wrapper.findAll('button').find((button) => button.text() === '查询')!.trigger('click')
    await flushPromises()

    expect(mocks.listSystemLogs).toHaveBeenCalledWith(expect.objectContaining({ api_key_id: 42 }))
  })

  it('sends api_key_id when cleaning up current filters', async () => {
    const wrapper = await mountTable()

    await inputByLabel(wrapper, 'api_key_id').setValue('42')
    await wrapper.findAll('button').find((button) => button.text() === '按当前筛选清理')!.trigger('click')
    await flushPromises()

    expect(mocks.cleanupSystemLogs).toHaveBeenCalledWith(expect.objectContaining({ api_key_id: 42 }))
  })

  it('interprets datetime-local filters as China Shanghai time', async () => {
    const originalTimeZone = process.env.TZ
    process.env.TZ = 'America/Los_Angeles'
    try {
      const wrapper = await mountTable()
      mocks.listSystemLogs.mockClear()

      await inputByLabel(wrapper, '开始时间').setValue('2026-08-04T00:30')
      await inputByLabel(wrapper, '结束时间').setValue('2026-08-04T01:30')
      await wrapper.findAll('button').find((button) => button.text() === '查询')!.trigger('click')
      await flushPromises()

      expect(mocks.listSystemLogs).toHaveBeenCalledWith(expect.objectContaining({
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
