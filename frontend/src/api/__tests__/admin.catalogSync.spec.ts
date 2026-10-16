import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))

vi.mock('@/api/client', () => ({ apiClient: { get, post } }))

import { getSyncStatus, sync } from '@/api/admin/modelPrices'
import { getKeywordCatalogStatus, syncKeywordCatalog } from '@/api/admin/riskControl'

describe('admin remote catalog sync api', () => {
  beforeEach(() => {
    get.mockReset()
    post.mockReset()
  })

  it('loads and synchronizes the model price catalog', async () => {
    const status = { enabled: true, model_count: 420, source: 'remote', refreshing: false }
    get.mockResolvedValueOnce({ data: status })
    post.mockResolvedValueOnce({ data: status })

    await expect(getSyncStatus()).resolves.toEqual(status)
    expect(get).toHaveBeenCalledWith('/admin/model-prices/sync/status')
    await expect(sync()).resolves.toEqual(status)
    expect(post).toHaveBeenCalledWith('/admin/model-prices/sync')
  })

  it('loads and synchronizes the keyword catalog', async () => {
    const status = { enabled: true, catalog_version: '2026.08.22', source: 'remote', last_error: '' }
    get.mockResolvedValueOnce({ data: status })
    post.mockResolvedValueOnce({ data: status })

    await expect(getKeywordCatalogStatus()).resolves.toEqual(status)
    expect(get).toHaveBeenCalledWith('/admin/risk-control/keyword-catalog/status')
    await expect(syncKeywordCatalog()).resolves.toEqual(status)
    expect(post).toHaveBeenCalledWith('/admin/risk-control/keyword-catalog/sync')
  })
})
