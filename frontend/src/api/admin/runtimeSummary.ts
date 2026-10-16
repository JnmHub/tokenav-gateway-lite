import { apiClient } from '../client'

export interface GatewayRuntimeSummary {
  service: {
    name: string
    version: string
    runtime: string
    started_at: string
    uptime_seconds: number
    source: string
  }
  database: {
    provider: string
    max_open: number
    max_idle: number
    max_lifetime_minutes: number
    max_idle_time_minutes: number
    quota_flusher_enabled: boolean
    quota_flush_interval_ms: number
    quota_flush_batch_size: number
    source: string
  }
  cache: {
    provider: string
    pool_size: number
    min_idle: number
    dial_timeout_seconds: number
    read_timeout_seconds: number
    write_timeout_seconds: number
    tls_enabled: boolean
    source: string
  }
  billing: {
    preset: string
    revision: number
    critical_workers: number
    critical_active_workers: number
    critical_capacity: number
    critical_pending: number
    critical_full_total: number
    critical_fallback_total: number
    critical_fallback_fail_total: number
    best_effort_workers: number
    best_effort_active_workers: number
    best_effort_capacity: number
    best_effort_pending: number
    best_effort_deferred: number
    best_effort_merged_total: number
    best_effort_fallback_total: number
    best_effort_fallback_failed: number
    best_effort_dropped_total: number
    ordering_wait_total: number
    reload_total: number
    write_timeout_ms: number
    source: string
  }
  usage: {
    local_workers: number
    local_running: number
    local_capacity: number
    local_pending: number
    local_completed: number
    local_failed: number
    local_dropped: number
    report_workers: number
    report_batch_size: number
    local_overflow_mode: string
    source: string
  }
  timeouts: {
    control_plane_ms: number
    response_header_seconds: number
    openai_response_header_seconds: number
    openai_first_output_seconds: number
    openai_high_reasoning_output_seconds: number
    stream_data_interval_seconds: number
    image_stream_data_interval_seconds: number
    source: string
  }
  websocket: {
    configured_enabled: boolean
    force_http: boolean
    effective_enabled: boolean
    mode_router_v2: boolean
    ingress_mode: string
    client_first_message_seconds: number
    inter_turn_idle_seconds: number
    max_connections_per_account: number
    source: string
  }
  scheduling: {
    advanced_enabled: boolean
    advanced_setting_available: boolean
    sticky_escape_enabled: boolean
    sticky_escape_ttft_ms: number
    sticky_escape_error_rate: number
    fallback_queue_enabled: boolean
    fallback_max_waiting: number
    fallback_wait_ms: number
    fallback_selection_mode: string
    load_batch_enabled: boolean
    load_batch_cache_ttl_ms: number
    source: string
  }
  composite_groups_enabled: boolean
  read_only: boolean
}

export type OpenAISchedulerPolicyPreset = 'safe_default' | 'conservative' | 'balanced' | 'low_latency' | 'custom'

export interface OpenAISchedulerRuntimePolicy {
  revision: number
  preset: OpenAISchedulerPolicyPreset
  advanced_enabled: boolean
  sticky_soft_scoring_enabled: boolean
  top_k: number
  prefer_subscription_pool: boolean
  priority_weight: number
  load_weight: number
  queue_weight: number
  error_rate_weight: number
  ttft_weight: number
  prefer_soonest_reset_weight: number
  quota_headroom_weight: number
  upstream_cost_enabled: boolean
  upstream_cost_weight: number
  session_sticky_weight: number
  previous_response_mode: 'hard'
  sales_billing_isolated: true
  updated_at: string
  source: string
}

export interface UpdateOpenAISchedulerRuntimePolicy {
  expected_revision: number
  preset: OpenAISchedulerPolicyPreset
  advanced_enabled: boolean
  sticky_soft_scoring_enabled: boolean
  top_k: number
  prefer_subscription_pool: boolean
  priority_weight: number
  load_weight: number
  queue_weight: number
  error_rate_weight: number
  ttft_weight: number
  prefer_soonest_reset_weight: number
  quota_headroom_weight: number
  upstream_cost_enabled: boolean
  upstream_cost_weight: number
  session_sticky_weight: number
}

export type BillingCachePreset = 'low' | 'standard' | 'high'

export interface BillingCacheRuntimeConfig {
  revision: number
  preset: BillingCachePreset
  available_presets: BillingCachePreset[]
  critical_worker_count: number
  critical_buffer_size: number
  critical_queue_length: number
  critical_queue_capacity: number
  best_effort_worker_count: number
  best_effort_buffer_size: number
  best_effort_queue_length: number
  best_effort_queue_capacity: number
  timeout_milliseconds: number
  critical_active_workers: number
  best_effort_active_workers: number
  best_effort_deferred_length: number
  best_effort_deferred_capacity: number
}

export interface UpdateBillingCacheRuntimeConfig {
  expected_revision: number
  preset: BillingCachePreset
}

export async function getGatewayRuntimeSummary(): Promise<GatewayRuntimeSummary> {
  const response = await apiClient.get<GatewayRuntimeSummary>('/admin/ops/runtime/summary')
  return response.data
}

export async function getOpenAISchedulerRuntimePolicy(): Promise<OpenAISchedulerRuntimePolicy> {
  const response = await apiClient.get<OpenAISchedulerRuntimePolicy>('/admin/ops/runtime/openai-scheduler')
  return response.data
}

export async function updateOpenAISchedulerRuntimePolicy(input: UpdateOpenAISchedulerRuntimePolicy): Promise<OpenAISchedulerRuntimePolicy> {
  const response = await apiClient.put<OpenAISchedulerRuntimePolicy>('/admin/ops/runtime/openai-scheduler', input)
  return response.data
}

export async function resetOpenAISchedulerRuntimePolicy(expectedRevision: number): Promise<OpenAISchedulerRuntimePolicy> {
  const response = await apiClient.post<OpenAISchedulerRuntimePolicy>('/admin/ops/runtime/openai-scheduler/reset', {
    expected_revision: expectedRevision,
  })
  return response.data
}

export async function getBillingCacheRuntimeConfig(): Promise<BillingCacheRuntimeConfig> {
  const response = await apiClient.get<BillingCacheRuntimeConfig>('/admin/ops/runtime/billing-cache')
  return response.data
}

export async function updateBillingCacheRuntimeConfig(input: UpdateBillingCacheRuntimeConfig): Promise<BillingCacheRuntimeConfig> {
  const response = await apiClient.put<BillingCacheRuntimeConfig>('/admin/ops/runtime/billing-cache', input)
  return response.data
}
