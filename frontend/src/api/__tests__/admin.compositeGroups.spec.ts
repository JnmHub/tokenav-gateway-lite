import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, put } = vi.hoisted(() => ({ get: vi.fn(), put: vi.fn() }))

vi.mock('@/api/client', () => ({ apiClient: { get, put } }))

import {
  getCompositeGroupsSettings,
  updateCompositeGroupsSettings,
} from '@/api/admin/settings'

describe('admin composite groups settings api', () => {
  beforeEach(() => {
    get.mockReset()
    put.mockReset()
  })

  it('reads and updates the isolated runtime switch endpoint', async () => {
    get.mockResolvedValueOnce({ data: { enabled: false } })
    await expect(getCompositeGroupsSettings()).resolves.toEqual({ enabled: false })
    expect(get).toHaveBeenCalledWith('/admin/settings/composite-groups')

    put.mockResolvedValueOnce({ data: { enabled: true } })
    await expect(updateCompositeGroupsSettings({ enabled: true })).resolves.toEqual({ enabled: true })
    expect(put).toHaveBeenCalledWith('/admin/settings/composite-groups', { enabled: true })
  })
})
