import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, put } = vi.hoisted(() => ({
  get: vi.fn(),
  put: vi.fn(),
}))

vi.mock('@/api/client', () => ({ apiClient: { get, put } }))

import {
  getBillingSettlementSettings,
  updateBillingSettlementSettings,
} from '@/api/admin/settings'

describe('admin billing settlement settings API', () => {
  beforeEach(() => {
    get.mockReset()
    put.mockReset()
  })

  it('reads the current gateway settlement mode', async () => {
    const response = {
      idempotent_enabled: false,
      mode: 'legacy',
      legacy_warning: true,
    }
    get.mockResolvedValueOnce({ data: response })

    await expect(getBillingSettlementSettings()).resolves.toEqual(response)
    expect(get).toHaveBeenCalledWith('/admin/settings/billing-settlement')
  })

  it('updates only the duplicate-safe settlement switch', async () => {
    const response = {
      idempotent_enabled: true,
      mode: 'idempotent_v2',
      legacy_warning: false,
    }
    put.mockResolvedValueOnce({ data: response })

    await expect(
      updateBillingSettlementSettings({ idempotent_enabled: true }),
    ).resolves.toEqual(response)
    expect(put).toHaveBeenCalledWith('/admin/settings/billing-settlement', {
      idempotent_enabled: true,
    })
  })
})
