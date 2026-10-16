/**
 * 国产供应商（Kimi / 智谱 GLM / DeepSeek）管理端探测接口。
 */

import { apiClient } from '../client'

export interface CNQuotaTier {
  window: '5h' | 'weekly'
  used_percent: number
  reset_at?: string
}

export interface CNProviderQuotaProbeResult {
  provider: string
  source?: string
  success: boolean
  credential_valid: boolean
  tiers?: CNQuotaTier[]
  plan_level?: string
  status_code?: number
  fetched_at: number
  persisted: boolean
  error?: string
}

export interface CNProviderBalanceEntry {
  currency: string
  balance: number
}

export interface CNProviderBalanceResult {
  provider: string
  success: boolean
  balance: number
  currency?: string
  balances?: CNProviderBalanceEntry[]
  available: boolean
  status_code?: number
  fetched_at: number
  persisted: boolean
  error?: string
}

export async function queryQuota(id: number): Promise<CNProviderQuotaProbeResult> {
  const { data } = await apiClient.get<CNProviderQuotaProbeResult>(
    `/admin/cn-providers/accounts/${id}/quota`
  )
  return data
}

export async function queryBalance(id: number): Promise<CNProviderBalanceResult> {
  const { data } = await apiClient.get<CNProviderBalanceResult>(
    `/admin/cn-providers/accounts/${id}/balance`
  )
  return data
}

export default { queryQuota, queryBalance }
