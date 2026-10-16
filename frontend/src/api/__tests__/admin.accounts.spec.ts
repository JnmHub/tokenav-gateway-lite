import { beforeEach, describe, expect, it, vi } from 'vitest'

const { post } = vi.hoisted(() => ({
  post: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: { post },
}))

import { duplicate, importCodexSession, syncFromCrs } from '@/api/admin/accounts'

describe('admin accounts long-running requests', () => {
  beforeEach(() => {
    post.mockReset()
    post.mockResolvedValue({ data: {} })
    sessionStorage.clear()
  })

  it('CRS 同步使用 180 秒超时', async () => {
    const payload = {
      base_url: 'https://crs.example.com',
      username: 'admin',
      password: 'secret',
    }

    await syncFromCrs(payload)

    expect(post).toHaveBeenCalledWith(
      '/admin/accounts/sync/crs',
      payload,
      { timeout: 180000 }
    )
  })

  it('Codex 会话导入使用 120 秒超时', async () => {
    const payload = { content: '{}' }

    await importCodexSession(payload)

    expect(post).toHaveBeenCalledWith(
      '/admin/accounts/import/codex-session',
      payload,
      { timeout: 120000 }
    )
  })

  it('账号复制不回传凭据，并在失败重试时复用同一个幂等键', async () => {
    post.mockRejectedValueOnce(new Error('network interrupted'))

    await expect(duplicate(42)).rejects.toThrow('network interrupted')

    const firstConfig = post.mock.calls[0][2] as { headers: Record<string, string> }
    const firstKey = firstConfig.headers['Idempotency-Key']
    expect(post.mock.calls[0][0]).toBe('/admin/accounts/42/duplicate')
    expect(post.mock.calls[0][1]).toBeUndefined()
    expect(firstKey).toMatch(/^account-duplicate-42-/)

    post.mockResolvedValueOnce({ data: { id: 43, name: 'source (Copy)' } })
    await duplicate(42)

    const retryConfig = post.mock.calls[1][2] as { headers: Record<string, string> }
    expect(retryConfig.headers['Idempotency-Key']).toBe(firstKey)
    expect(sessionStorage.getItem('sub2api:admin:account-duplicate:42')).toBeNull()
  })
})
