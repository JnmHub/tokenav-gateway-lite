import { beforeEach, describe, expect, it, vi } from 'vitest'

const { post } = vi.hoisted(() => ({ post: vi.fn() }))

vi.mock('@/api/client', () => ({ apiClient: { post } }))

import { createFromSSO, getGrokSSOImportTimeout, reconcileOAuthAccounts } from '@/api/admin/grok'

describe('admin Grok SSO import', () => {
  beforeEach(() => {
    post.mockReset()
    post.mockResolvedValue({ data: { created: [], failed: [] } })
  })

  it('scales timeout by three-worker batches with a ten-minute cap', () => {
    expect(getGrokSSOImportTimeout(1)).toBe(120_000)
    expect(getGrokSSOImportTimeout(4)).toBe(210_000)
    expect(getGrokSSOImportTimeout(100)).toBe(600_000)
  })

  it('sends routing and header override settings with the batch request', async () => {
    const payload = {
      sso_tokens: ['sso-one', 'sso-two'],
      credentials: {
        base_url: 'https://us-west-2.api.x.ai/v1',
        header_override_enabled: true,
        header_overrides: { 'x-tenant': 'tenant-a' }
      }
    }
    await createFromSSO(payload)
    expect(post).toHaveBeenCalledWith('/admin/grok/sso-to-oauth', payload, {
      timeout: getGrokSSOImportTimeout(2)
    })
  })

  it('binds apply to the preview fingerprint and selected accounts', async () => {
    post.mockResolvedValueOnce({ data: { dry_run: false, items: [], preview_token: 'next' } })

    await reconcileOAuthAccounts(true, 500, 500, 'preview-500', [17, 19])

    expect(post).toHaveBeenCalledWith('/admin/grok/oauth/reconcile', {
      dry_run: false,
      apply: true,
      after_id: 500,
      limit: 500,
      preview_token: 'preview-500',
      account_ids: [17, 19]
    }, { timeout: 120_000 })
  })
})
