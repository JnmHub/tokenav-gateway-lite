export type PrivacyResultMessage = {
  type: 'success' | 'error'
  key: string
}

type PrivacyAccount = {
  platform?: string
  extra?: Record<string, unknown> | null
}

export function resolvePrivacyResultMessage(account: PrivacyAccount): PrivacyResultMessage {
  const mode = typeof account.extra?.privacy_mode === 'string'
    ? account.extra.privacy_mode
    : ''

  if (account.platform === 'openai') {
    if (mode === 'training_off') {
      return { type: 'success', key: 'admin.accounts.privacyTrainingOff' }
    }
    if (mode === 'training_set_cf_blocked') {
      return { type: 'error', key: 'admin.accounts.privacyCfBlocked' }
    }
    return { type: 'error', key: 'admin.accounts.privacyFailed' }
  }

  if (account.platform === 'antigravity') {
    if (mode === 'privacy_set') {
      return { type: 'success', key: 'admin.accounts.privacyAntigravitySet' }
    }
    return { type: 'error', key: 'admin.accounts.privacyAntigravityFailed' }
  }

  return { type: 'error', key: 'admin.accounts.privacyFailed' }
}
