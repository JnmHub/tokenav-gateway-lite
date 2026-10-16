import { describe, expect, it } from 'vitest'
import { resolvePrivacyResultMessage } from '../accountPrivacy'

describe('resolvePrivacyResultMessage', () => {
  it('区分 OpenAI 成功、Cloudflare 拦截和普通失败', () => {
    expect(resolvePrivacyResultMessage({
      platform: 'openai',
      extra: { privacy_mode: 'training_off' },
    })).toEqual({ type: 'success', key: 'admin.accounts.privacyTrainingOff' })

    expect(resolvePrivacyResultMessage({
      platform: 'openai',
      extra: { privacy_mode: 'training_set_cf_blocked' },
    })).toEqual({ type: 'error', key: 'admin.accounts.privacyCfBlocked' })

    expect(resolvePrivacyResultMessage({ platform: 'openai', extra: {} })).toEqual({
      type: 'error',
      key: 'admin.accounts.privacyFailed',
    })
  })

  it('只有 Antigravity privacy_set 视为成功', () => {
    expect(resolvePrivacyResultMessage({
      platform: 'antigravity',
      extra: { privacy_mode: 'privacy_set' },
    })).toEqual({ type: 'success', key: 'admin.accounts.privacyAntigravitySet' })

    expect(resolvePrivacyResultMessage({ platform: 'antigravity', extra: {} })).toEqual({
      type: 'error',
      key: 'admin.accounts.privacyAntigravityFailed',
    })
  })
})
