import { apiClient } from './client'

export type MonitorRange = '90m' | '24h' | '7d' | '30d'
export type HealthState = 'unknown' | 'healthy' | 'warning' | 'critical'

export interface MonitorFilter {
  range: MonitorRange
  platforms?: string[]
  groupIds?: number[]
  models?: string[]
}

export interface LatencyMetric {
  sample_count: number
  p50_ms: number | null
  p90_ms?: number | null
  p95_ms: number | null
  avg_ms: number | null
}

export interface MonitorMetric {
  success_requests: number
  error_requests: number
  request_count: number
  token_count: number
  rpm: number
  tpm: number
  error_rate: number
  success_rate: number
  cache_rate: number
  ttft: LatencyMetric
  duration: LatencyMetric
}

export interface MonitorHealth {
  overall: HealthState
  error_rate: HealthState
  ttft: HealthState
  cache: HealthState
}

export interface MonitorConfig {
  version: number
  enabled: boolean
  refresh_interval_seconds: 60 | 300
  platforms: Array<{ platform: string; enabled: boolean; models: string[] }>
  group_ids: number[]
  ignored_error_categories: string[]
  health_thresholds: Record<string, number>
}

export interface MonitorCoverage {
  data_through: string
  aggregation_lag_seconds: number
  coverage_complete: boolean
  bootstrap?: { active: boolean; progress_percent: number } | null
}

export interface MonitorSnapshot {
  config: MonitorConfig
  coverage: MonitorCoverage
  metrics: MonitorMetric
  health: MonitorHealth
  trend: Array<{ bucket_start: string; metrics: MonitorMetric; health: MonitorHealth }>
}

export interface MonitorModelRow {
  platform: string
  model: string
  metrics: MonitorMetric
  health: MonitorHealth
}

export interface MonitorErrorRow {
  category: string
  count: number
  rate: number
  ignored?: boolean
}

export interface MonitorUserRow {
  user_id?: number
  rank: number
  email?: string
  username?: string
  display_label: string
  metrics: MonitorMetric
}

function requestConfig(filter: MonitorFilter, signal?: AbortSignal) {
  return {
    params: {
      range: filter.range,
      platform: filter.platforms?.length ? filter.platforms : undefined,
      group_id: filter.groupIds?.length ? filter.groupIds : undefined,
      model: filter.models?.length ? filter.models : undefined,
    },
    paramsSerializer: {
      serialize(values: Record<string, unknown>) {
        const search = new URLSearchParams()
        for (const [key, rawValue] of Object.entries(values)) {
          if (rawValue == null || rawValue === '') continue
          for (const value of Array.isArray(rawValue) ? rawValue : [rawValue]) {
            search.append(key, String(value))
          }
        }
        return search.toString()
      },
    },
    signal,
  }
}

export async function getConfig(): Promise<MonitorConfig> {
  const { data } = await apiClient.get<MonitorConfig>('/admin/channel-monitor-v2/config')
  return data
}

export async function updateConfig(config: MonitorConfig): Promise<MonitorConfig> {
  const { data } = await apiClient.put<MonitorConfig>('/admin/channel-monitor-v2/config', config)
  return data
}

export async function getSnapshot(filter: MonitorFilter, signal?: AbortSignal): Promise<MonitorSnapshot> {
  const { data } = await apiClient.get<MonitorSnapshot>('/admin/channel-monitor-v2/snapshot', requestConfig(filter, signal))
  return data
}

export async function getModels(filter: MonitorFilter, signal?: AbortSignal): Promise<MonitorModelRow[]> {
  const { data } = await apiClient.get<{ items: MonitorModelRow[] }>('/admin/channel-monitor-v2/models', requestConfig(filter, signal))
  return data.items || []
}

export async function getErrors(filter: MonitorFilter, signal?: AbortSignal): Promise<MonitorErrorRow[]> {
  const { data } = await apiClient.get<{ items: MonitorErrorRow[] }>('/admin/channel-monitor-v2/errors', requestConfig(filter, signal))
  return data.items || []
}

export async function getUsers(filter: MonitorFilter, signal?: AbortSignal): Promise<MonitorUserRow[]> {
  const { data } = await apiClient.get<{ items: MonitorUserRow[] }>('/admin/channel-monitor-v2/users', requestConfig(filter, signal))
  return data.items || []
}
