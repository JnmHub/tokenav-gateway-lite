import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { ref } from 'vue'

import DateRangePicker from '../DateRangePicker.vue'
import { formatDefaultTimeZoneDateInput } from '@/utils/timezone'

const messages: Record<string, string> = {
  'dates.today': 'Today',
  'dates.yesterday': 'Yesterday',
  'dates.last24Hours': 'Last 24 Hours',
  'dates.last7Days': 'Last 7 Days',
  'dates.last14Days': 'Last 14 Days',
  'dates.last30Days': 'Last 30 Days',
  'dates.thisMonth': 'This Month',
  'dates.lastMonth': 'Last Month',
  'dates.startDate': 'Start Date',
  'dates.endDate': 'End Date',
  'dates.apply': 'Apply',
  'dates.selectDateRange': 'Select date range'
}

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => messages[key] ?? key,
    locale: ref('en')
  })
}))

describe('DateRangePicker', () => {
  const originalTimeZone = process.env.TZ

  beforeEach(() => {
    process.env.TZ = 'America/Los_Angeles'
    vi.useFakeTimers()
    // 此时洛杉矶仍是 8 月 3 日，但上海已经是 8 月 4 日。
    vi.setSystemTime(new Date('2026-08-03T16:30:00.000Z'))
  })

  afterEach(() => {
    vi.useRealTimers()
    if (originalTimeZone === undefined) {
      delete process.env.TZ
    } else {
      process.env.TZ = originalTimeZone
    }
  })

  it('uses last 24 hours as the default recognized preset', () => {
    const now = new Date()
    const yesterday = new Date(now.getTime() - 24 * 60 * 60 * 1000)

    const wrapper = mount(DateRangePicker, {
      props: {
        startDate: formatDefaultTimeZoneDateInput(yesterday),
        endDate: formatDefaultTimeZoneDateInput(now)
      },
      global: {
        stubs: {
          Icon: true
        }
      }
    })

    expect(wrapper.text()).toContain('Last 24 Hours')
  })

  it('emits range updates with last24Hours preset when applied', async () => {
    const today = formatDefaultTimeZoneDateInput()

    const wrapper = mount(DateRangePicker, {
      props: {
        startDate: today,
        endDate: today
      },
      global: {
        stubs: {
          Icon: true
        }
      }
    })

    await wrapper.find('.date-picker-trigger').trigger('click')
    const presetButton = wrapper.findAll('.date-picker-preset').find((node) =>
      node.text().includes('Last 24 Hours')
    )
    expect(presetButton).toBeDefined()

    await presetButton!.trigger('click')
    await wrapper.find('.date-picker-apply').trigger('click')

    const expectedStart = '2026-08-03'
    const expectedEnd = '2026-08-04'

    expect(wrapper.emitted('update:startDate')?.[0]).toEqual([expectedStart])
    expect(wrapper.emitted('update:endDate')?.[0]).toEqual([expectedEnd])
    expect(wrapper.emitted('change')?.[0]).toEqual([
      {
        startDate: expectedStart,
        endDate: expectedEnd,
        preset: 'last24Hours'
      }
    ])
  })

  it('uses the Shanghai calendar day for today and yesterday in an American timezone boundary', async () => {
    const wrapper = mount(DateRangePicker, {
      props: {
        startDate: '2026-08-04',
        endDate: '2026-08-04'
      },
      global: {
        stubs: {
          Icon: true
        }
      }
    })

    await wrapper.find('.date-picker-trigger').trigger('click')
    const yesterdayButton = wrapper.findAll('.date-picker-preset').find((node) =>
      node.text().includes('Yesterday')
    )
    await yesterdayButton!.trigger('click')
    await wrapper.find('.date-picker-apply').trigger('click')

    expect(wrapper.emitted('change')?.[0]).toEqual([{
      startDate: '2026-08-03',
      endDate: '2026-08-03',
      preset: 'yesterday'
    }])
  })
})
