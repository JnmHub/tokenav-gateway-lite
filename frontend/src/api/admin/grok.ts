/**
 * Admin Grok/xAI API endpoints
 * Handles xAI OAuth flows for administrators.
 */

import { apiClient } from '../client'
import type { GrokBillingSummary, WindowStats } from '@/types'

export interface GrokAuthUrlResponse {
  auth_url: string
  session_id: string
  state: string
}

export interface GrokAuthUrlRequest {
  proxy_id?: number
  redirect_uri?: string
}

export interface GrokExchangeCodeRequest {
  session_id: string
  state: string
  code: string
  proxy_id?: number
  redirect_uri?: string
}

export interface GrokTokenInfo {
  access_token?: string
  refresh_token?: string
  token_type?: string
  id_token?: string
  expires_at?: number | string
  expires_in?: number
  scope?: string
  client_id?: string
  email?: string
  subscription_tier?: string
  entitlement_status?: string
  [key: string]: unknown
}

export interface GrokQuotaWindow {
  limit?: number | null
  remaining?: number | null
  reset_unix?: number | null
  reset_at?: string | null
}

export interface GrokQuotaSnapshot {
  requests?: GrokQuotaWindow | null
  tokens?: GrokQuotaWindow | null
  retry_after_seconds?: number | null
  subscription_tier?: string
  entitlement_status?: string
  status_code?: number
  headers?: Record<string, string>
  updated_at: string
}

export interface GrokQuotaProbeResult {
  source: 'active_probe' | 'billing_probe' | 'hybrid_probe'
  model?: string
  billing?: GrokBillingSummary | null
  snapshot?: GrokQuotaSnapshot | null
  local_usage_24h?: WindowStats | null
  local_usage_7d?: WindowStats | null
  local_usage_monthly?: WindowStats | null
  status_code?: number
  headers_observed: boolean
  reset_supported: boolean
  fetched_at: number
  persisted: boolean
  probe_error?: string
}

export interface GrokQuotaResetResult {
  supported: boolean
  code: string
  message: string
}

export type GrokOAuthReconcileReason =
  | 'missing_refresh_token'
  | 'missing_access_token'
  | 'missing_expiry'
  | 'invalid_expiry'
  | 'near_expiry'
  | 'missing_stable_identity'
  | 'owner_mismatch'
  | 'owner_changed'
  | 'duplicate_stable_identity'
  | 'refresh_failed'

export interface GrokOAuthReconcileItem {
  account_id: number
  reason: GrokOAuthReconcileReason
  action: 'block_account' | 'refresh_credentials'
  outcome: 'planned' | 'applied' | 'skipped' | 'failed' | 'partial'
  owner_account_id?: number
}

export interface GrokOAuthReconcileResult {
  dry_run: boolean
  scanned: number
  actionable: number
  would_block: number
  would_refresh: number
  blocked: number
  refreshed: number
  skipped: number
  failed: number
  partial: number
  items: GrokOAuthReconcileItem[]
  next_after_id: number
  has_more: boolean
  preview_token: string
}

export interface GrokSSOToOAuthRequest {
  sso_tokens: string[]
  name?: string
  notes?: string | null
  proxy_id?: number | null
  group_ids?: number[]
  credentials?: Record<string, unknown>
  extra?: Record<string, unknown>
  concurrency?: number
  load_factor?: number | null
  priority?: number
  rate_multiplier?: number
  expires_at?: number | null
  auto_pause_on_expired?: boolean
}

export interface GrokSSOToOAuthItemResult {
  index: number
  name?: string
  email?: string
  account?: unknown
  error?: string
}

export interface GrokSSOToOAuthResponse {
  created: GrokSSOToOAuthItemResult[]
  failed: GrokSSOToOAuthItemResult[]
}

export function getGrokSSOImportTimeout(keyCount: number): number {
  const count = Math.max(1, Math.min(100, Math.trunc(keyCount || 1)))
  return Math.min(10 * 60_000, 30_000 + Math.ceil(count / 3) * 90_000)
}

export async function generateAuthUrl(
  payload: GrokAuthUrlRequest
): Promise<GrokAuthUrlResponse> {
  const { data } = await apiClient.post<GrokAuthUrlResponse>(
    '/admin/grok/oauth/auth-url',
    payload
  )
  return data
}

export async function exchangeCode(payload: GrokExchangeCodeRequest): Promise<GrokTokenInfo> {
  const { data } = await apiClient.post<GrokTokenInfo>(
    '/admin/grok/oauth/exchange-code',
    payload
  )
  return data
}

export async function refreshGrokToken(
  refreshToken: string,
  proxyId?: number | null
): Promise<GrokTokenInfo> {
  const payload: Record<string, unknown> = { refresh_token: refreshToken }
  if (proxyId) payload.proxy_id = proxyId

  const { data } = await apiClient.post<GrokTokenInfo>(
    '/admin/grok/oauth/refresh-token',
    payload
  )
  return data
}

export async function queryQuota(id: number): Promise<GrokQuotaProbeResult> {
  const { data } = await apiClient.get<GrokQuotaProbeResult>(`/admin/grok/accounts/${id}/quota`)
  return data
}

export async function resetQuota(id: number): Promise<GrokQuotaResetResult> {
  const { data } = await apiClient.post<GrokQuotaResetResult>(`/admin/grok/accounts/${id}/reset-quota`)
  return data
}

export async function createFromSSO(payload: GrokSSOToOAuthRequest): Promise<GrokSSOToOAuthResponse> {
  const { data } = await apiClient.post<GrokSSOToOAuthResponse>(
    '/admin/grok/sso-to-oauth',
    payload,
    { timeout: getGrokSSOImportTimeout(payload.sso_tokens.length) }
  )
  return data
}

export async function reconcileOAuthAccounts(
  apply = false,
  afterId = 0,
  limit = 500,
  previewToken = '',
  accountIds: number[] = []
): Promise<GrokOAuthReconcileResult> {
  const { data } = await apiClient.post<GrokOAuthReconcileResult>(
    '/admin/grok/oauth/reconcile',
    {
      dry_run: !apply,
      apply,
      after_id: afterId,
      limit,
      ...(apply ? { preview_token: previewToken, account_ids: accountIds } : {})
    },
    { timeout: apply ? 120_000 : 30_000 }
  )
  return data
}

export default { generateAuthUrl, exchangeCode, refreshGrokToken, queryQuota, resetQuota, createFromSSO, reconcileOAuthAccounts }
