import { beforeEach, describe, expect, it, vi } from 'vitest'

const { post } = vi.hoisted(() => ({ post: vi.fn() }))

vi.mock('@/api/client', () => ({ apiClient: { post } }))

import { duplicate } from '@/api/admin/channelMonitor'

describe('admin channel monitor duplicate', () => {
  beforeEach(() => {
    post.mockReset()
    localStorage.clear()
    sessionStorage.clear()
    localStorage.setItem('auth_user', JSON.stringify({ id: 77, role: 'admin' }))
  })

  it('只发送源监控 ID，失败重试复用管理员范围幂等键', async () => {
    post.mockRejectedValueOnce(new Error('network interrupted'))

    await expect(duplicate(42)).rejects.toThrow('network interrupted')

    const firstConfig = post.mock.calls[0][2] as { headers: Record<string, string> }
    const firstKey = firstConfig.headers['Idempotency-Key']
    expect(post.mock.calls[0][0]).toBe('/admin/channel-monitors/42/duplicate')
    expect(post.mock.calls[0][1]).toBeUndefined()
    expect(firstKey).toMatch(/^channel-monitor-duplicate-77-42-/)
    expect(sessionStorage.getItem('sub2api:admin:channel-monitor-duplicate:77:42')).toBe(firstKey)

    post.mockResolvedValueOnce({ data: { id: 43, name: 'Primary (Copy)', enabled: false, api_key_masked: '***' } })
    const result = await duplicate(42)

    const retryConfig = post.mock.calls[1][2] as { headers: Record<string, string> }
    expect(retryConfig.headers['Idempotency-Key']).toBe(firstKey)
    expect(result.enabled).toBe(false)
    expect(result.api_key_masked).toBe('***')
    expect(sessionStorage.getItem('sub2api:admin:channel-monitor-duplicate:77:42')).toBeNull()
  })

  it('不同管理员使用不同的幂等存储范围', async () => {
    post.mockRejectedValue(new Error('keep operation key'))

    await expect(duplicate(42)).rejects.toThrow()
    const firstKey = (post.mock.calls[0][2] as { headers: Record<string, string> }).headers['Idempotency-Key']

    localStorage.setItem('auth_user', JSON.stringify({ id: 88, role: 'admin' }))
    await expect(duplicate(42)).rejects.toThrow()
    const secondKey = (post.mock.calls[1][2] as { headers: Record<string, string> }).headers['Idempotency-Key']

    expect(firstKey).not.toBe(secondKey)
    expect(sessionStorage.getItem('sub2api:admin:channel-monitor-duplicate:77:42')).toBe(firstKey)
    expect(sessionStorage.getItem('sub2api:admin:channel-monitor-duplicate:88:42')).toBe(secondKey)
  })
})
