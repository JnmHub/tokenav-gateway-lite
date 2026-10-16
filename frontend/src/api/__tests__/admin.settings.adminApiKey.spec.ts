import { beforeEach, describe, expect, it, vi } from 'vitest'

const { post } = vi.hoisted(() => ({ post: vi.fn() }))

vi.mock('@/api/client', () => ({ apiClient: { post } }))

import { revealAdminApiKey } from '@/api/admin/settings'

describe('admin settings API key reveal', () => {
  beforeEach(() => post.mockReset())

  it('uses an on-demand POST and returns only the response key', async () => {
    post.mockResolvedValueOnce({ data: { key: 'admin-test-value' } })

    await expect(revealAdminApiKey()).resolves.toEqual({ key: 'admin-test-value' })
    expect(post).toHaveBeenCalledWith('/admin/settings/admin-api-key/reveal')
  })
})
