import { beforeEach, describe, expect, it, vi } from 'vitest'

const { post } = vi.hoisted(() => ({ post: vi.fn() }))

vi.mock('@/api/client', () => ({ apiClient: { post } }))

import { reveal } from '@/api/admin/gatewayLiteLocalKeys'

describe('gateway-lite local key reveal', () => {
  beforeEach(() => post.mockReset())

  it('requests one key on demand without adding it to list responses', async () => {
    post.mockResolvedValueOnce({ data: { secret: 'glk_test-secret' } })

    await expect(reveal(19)).resolves.toEqual({ secret: 'glk_test-secret' })
    expect(post).toHaveBeenCalledWith('/admin/gateway-lite/local-api-keys/19/reveal')
  })
})
