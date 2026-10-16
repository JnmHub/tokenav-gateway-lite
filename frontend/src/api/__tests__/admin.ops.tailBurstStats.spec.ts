import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get } = vi.hoisted(() => ({ get: vi.fn() }))

vi.mock('@/api/client', () => ({ apiClient: { get } }))

import { getCodexTailBurstStats } from '@/api/admin/ops'

describe('admin Codex Tail-Burst statistics API', () => {
  beforeEach(() => get.mockReset())

  it('serializes only configured filters and preserves the abort signal', async () => {
    const signal = new AbortController().signal
    const response = {
      data: {
        summary: {
          sessions: 1,
          active_sessions: 0,
          settled_sessions: 1,
          accounts: 1,
          requests: 3,
          tokens: 42,
          delta_percent: 1.5
        },
        trend: [],
        items: [],
        total: 0,
        page: 1,
        page_size: 20,
        pages: 1
      }
    }
    get.mockResolvedValueOnce(response)

    await expect(getCodexTailBurstStats({
      start_time: '2026-08-01T00:00:00.000Z',
      end_time: '2026-08-30T23:59:59.999Z',
      account_id: 42,
      window: '5h',
      reason: 'used_ratio',
      status: 'settled',
      page: 2,
      page_size: 50
    }, { signal })).resolves.toBe(response.data)

    expect(get).toHaveBeenCalledWith('/admin/ops/codex-tail-burst/stats', {
      params: {
        start_time: '2026-08-01T00:00:00.000Z',
        end_time: '2026-08-30T23:59:59.999Z',
        account_id: 42,
        window: '5h',
        reason: 'used_ratio',
        status: 'settled',
        page: 2,
        page_size: 50
      },
      signal
    })
  })

  it('does not send empty filters', async () => {
    get.mockResolvedValueOnce({ data: {} })

    await getCodexTailBurstStats()

    expect(get).toHaveBeenCalledWith('/admin/ops/codex-tail-burst/stats', {
      params: {},
      signal: undefined
    })
  })
})
