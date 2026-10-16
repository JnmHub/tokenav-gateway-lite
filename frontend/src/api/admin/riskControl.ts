import { apiClient } from '../client'

export type ModerationMode = 'off' | 'observe' | 'pre_block'
export type KeywordBlockingMode = 'keyword_only' | 'keyword_and_api' | 'api_only'
export type ContentModerationModelFilterType = 'all' | 'include' | 'exclude'
export type ContentModerationAction =
  | 'allow'
  | 'block'
  | 'hash_block'
  | 'keyword_block'
  | 'keyword_audit'
  | 'keyword_allow_original'
  | 'keyword_reroute'
  | 'language_block'
  | 'error'
  | 'cyber_policy'

export interface ContentModerationModelFilter {
  type: ContentModerationModelFilterType
  models: string[]
}

export interface ContentModerationConfig {
  enabled: boolean
  mode: ModerationMode
  base_url: string
  model: string
  api_key_configured: boolean
  api_key_masked: string
  api_key_count: number
  api_key_masks: string[]
  api_key_statuses: ContentModerationAPIKeyStatus[]
  timeout_ms: number
  sample_rate: number
  all_groups: boolean
  group_ids: number[]
  record_non_hits: boolean
  thresholds: Record<string, number>
  worker_count: number
  queue_size: number
  block_status: number
  block_message: string
  email_on_hit: boolean
  auto_ban_enabled: boolean
  ban_threshold: number
  deduct_balance_on_block_enabled: boolean
  deduct_balance_on_block_amount: number
  permanent_ban_enabled: boolean
  permanent_ban_threshold: number
  violation_window_hours: number
  retry_count: number
  hit_retention_days: number
  non_hit_retention_days: number
  pre_hash_check_enabled: boolean
  blocked_keywords: string[]
  keyword_blocking_mode: KeywordBlockingMode
  keyword_rule_packs: ContentModerationKeywordRulePack[]
  model_filter: ContentModerationModelFilter
  local_keyword_only: boolean
  allowed_languages: string[]
  scan_latest_user_input: boolean
  scan_top_level_instructions: boolean
  scan_new_skill_content: boolean
  scan_new_tool_output: boolean
  keyword_group_routing_enabled: boolean
  cyber_policy_exclude_from_ban_count: boolean
}

export type ContentModerationKeywordRuleAction = 'keyword_block' | 'keyword_audit'

export interface ContentModerationKeywordRulePack {
  id: string
  name: string
  description: string
  action: ContentModerationKeywordRuleAction
  enabled: boolean
  keywords: string[]
  built_in: boolean
}

export type ContentModerationAPIKeyStatusValue = 'unknown' | 'ok' | 'error' | 'frozen'

export interface ContentModerationAPIKeyStatus {
  index: number
  key_hash: string
  masked: string
  status: ContentModerationAPIKeyStatusValue
  failure_count: number
  success_count: number
  last_error: string
  last_checked_at?: string
  frozen_until?: string
  last_latency_ms: number
  last_http_status: number
  last_tested: boolean
  configured: boolean
}

export interface TestContentModerationAPIKeysPayload {
  api_keys?: string[]
  base_url?: string
  model?: string
  timeout_ms?: number
  prompt?: string
  images?: string[]
}

export interface TestContentModerationAPIKeysResponse {
  items: ContentModerationAPIKeyStatus[]
  audit_result?: ContentModerationTestAuditResult
  image_count: number
}

export interface ContentModerationTestAuditResult {
  flagged: boolean
  highest_category: string
  highest_score: number
  composite_score: number
  category_scores: Record<string, number>
  thresholds: Record<string, number>
}

export interface UpdateContentModerationConfig {
  enabled?: boolean
  mode?: ModerationMode
  base_url?: string
  model?: string
  api_key?: string
  api_keys?: string[]
  api_keys_mode?: 'append' | 'replace'
  delete_api_key_hashes?: string[]
  clear_api_key?: boolean
  timeout_ms?: number
  sample_rate?: number
  all_groups?: boolean
  group_ids?: number[]
  record_non_hits?: boolean
  thresholds?: Record<string, number>
  worker_count?: number
  queue_size?: number
  block_status?: number
  block_message?: string
  email_on_hit?: boolean
  auto_ban_enabled?: boolean
  ban_threshold?: number
  deduct_balance_on_block_enabled?: boolean
  deduct_balance_on_block_amount?: number
  permanent_ban_enabled?: boolean
  permanent_ban_threshold?: number
  violation_window_hours?: number
  retry_count?: number
  hit_retention_days?: number
  non_hit_retention_days?: number
  pre_hash_check_enabled?: boolean
  blocked_keywords?: string[]
  keyword_blocking_mode?: KeywordBlockingMode
  keyword_rule_packs?: ContentModerationKeywordRulePack[]
  model_filter?: ContentModerationModelFilter
  allowed_languages?: string[]
  scan_latest_user_input?: boolean
  scan_top_level_instructions?: boolean
  scan_new_skill_content?: boolean
  scan_new_tool_output?: boolean
  keyword_group_routing_enabled?: boolean
  cyber_policy_exclude_from_ban_count?: boolean
}

export interface ContentModerationRuntimeStatus {
  enabled: boolean
  risk_control_enabled: boolean
  mode: ModerationMode
  worker_count: number
  max_workers: number
  active_workers: number
  idle_workers: number
  queue_size: number
  queue_length: number
  queue_usage_percent: number
  enqueued: number
  dropped: number
  processed: number
  errors: number
  pre_block_active: number
  pre_block_checked: number
  pre_block_allowed: number
  pre_block_blocked: number
  pre_block_errors: number
  pre_block_avg_latency_ms: number
  pre_block_api_key_active: number
  pre_block_api_key_available_count: number
  pre_block_api_key_total_calls: number
  pre_block_api_key_loads: ContentModerationAPIKeyLoad[]
  api_key_statuses: ContentModerationAPIKeyStatus[]
  flagged_hash_count: number
  last_cleanup_at?: string
  last_cleanup_deleted_hit: number
  last_cleanup_deleted_non_hit: number
}

export interface ContentModerationAPIKeyLoad {
  index: number
  key_hash: string
  masked: string
  status: ContentModerationAPIKeyStatusValue
  active: number
  total: number
  success: number
  errors: number
  avg_latency_ms: number
  last_latency_ms: number
  last_http_status: number
}

export interface ContentModerationLog {
  id: number
  request_id: string
  user_id: number | null
  user_email: string
  api_key_id: number | null
  api_key_name: string
  group_id: number | null
  group_name: string
  original_group_id: number | null
  original_group_name: string
  effective_group_id: number | null
  effective_group_name: string
  content_source: string
  detected_languages: string[]
  content_fingerprint: string
  language_evidence: ContentModerationLanguageEvidence
  keyword_policy_rerouted: boolean
  keyword_policy_reason: string
  endpoint: string
  provider: string
  model: string
  mode: string
  action: ContentModerationAction
  flagged: boolean
  matched_keyword?: string
  highest_category: string
  highest_score: number
  category_scores: Record<string, number>
  threshold_snapshot: Record<string, number>
  input_excerpt: string
  upstream_latency_ms: number | null
  error: string
  violation_count: number
  auto_banned: boolean
  email_sent: boolean
  user_status: string
  queue_delay_ms: number | null
  created_at: string
}

export interface ContentModerationLanguageEvidence {
  detector_version: string
  content_runes: number
  sampled_runes: number
  total_letter_runes: number
  script_counts: Record<string, number>
  language_counts: Record<string, number>
  language_ratio_bps: Record<string, number>
  latin_evidence_counts: Record<string, number>
  min_language_runes: number
  min_language_ratio_bps: number
}

export interface ListContentModerationLogsParams {
  page?: number
  page_size?: number
  result?: string
  group_id?: number
  original_group_id?: number
  effective_group_id?: number
  content_source?: string
  detected_language?: string
  route_reason?: string
  endpoint?: string
  search?: string
  from?: string
  to?: string
}

export interface ContentModerationLogsResponse {
  items: ContentModerationLog[]
  total: number
  page: number
  page_size: number
  pages: number
}

export interface ContentModerationUnbanUserResponse {
  user_id: number
  status: string
}

export interface DeleteFlaggedHashResponse {
  input_hash: string
  deleted: boolean
}

export interface ClearFlaggedHashesResponse {
  deleted: number
}

export interface KeywordCatalogStatus {
  enabled: boolean
  catalog_version: string
  sequence?: number
  pack_count?: number
  keyword_count?: number
  hash: string
  source: string
  last_checked_at?: string | null
  last_updated_at?: string | null
  last_error: string
}

export async function getConfig(): Promise<ContentModerationConfig> {
  const { data } = await apiClient.get<ContentModerationConfig>('/admin/risk-control/config')
  return data
}

export async function updateConfig(
  payload: UpdateContentModerationConfig
): Promise<ContentModerationConfig> {
  const { data } = await apiClient.put<ContentModerationConfig>('/admin/risk-control/config', payload)
  return data
}

export async function getStatus(): Promise<ContentModerationRuntimeStatus> {
  const { data } = await apiClient.get<ContentModerationRuntimeStatus>('/admin/risk-control/status')
  return data
}

export async function testAPIKeys(
  payload: TestContentModerationAPIKeysPayload = {}
): Promise<TestContentModerationAPIKeysResponse> {
  const { data } = await apiClient.post<TestContentModerationAPIKeysResponse>('/admin/risk-control/api-keys/test', payload)
  return data
}

export async function listLogs(
  params: ListContentModerationLogsParams = {}
): Promise<ContentModerationLogsResponse> {
  const { data } = await apiClient.get<ContentModerationLogsResponse>('/admin/risk-control/logs', {
    params,
  })
  return data
}

export async function unbanUser(userID: number): Promise<ContentModerationUnbanUserResponse> {
  const { data } = await apiClient.post<ContentModerationUnbanUserResponse>(
    `/admin/risk-control/users/${userID}/unban`
  )
  return data
}

export async function deleteFlaggedHash(inputHash: string): Promise<DeleteFlaggedHashResponse> {
  const { data } = await apiClient.delete<DeleteFlaggedHashResponse>('/admin/risk-control/hashes', {
    data: { input_hash: inputHash },
  })
  return data
}

export async function clearFlaggedHashes(): Promise<ClearFlaggedHashesResponse> {
  const { data } = await apiClient.delete<ClearFlaggedHashesResponse>('/admin/risk-control/hashes/all')
  return data
}

export async function getKeywordCatalogStatus(): Promise<KeywordCatalogStatus> {
  const { data } = await apiClient.get<KeywordCatalogStatus>('/admin/risk-control/keyword-catalog/status')
  return data
}

export async function syncKeywordCatalog(): Promise<KeywordCatalogStatus> {
  const { data } = await apiClient.post<KeywordCatalogStatus>('/admin/risk-control/keyword-catalog/sync')
  return data
}

export const riskControlAPI = {
  getConfig,
  updateConfig,
  getStatus,
  testAPIKeys,
  listLogs,
  unbanUser,
  deleteFlaggedHash,
  clearFlaggedHashes,
  getKeywordCatalogStatus,
  syncKeywordCatalog,
}

export default riskControlAPI
