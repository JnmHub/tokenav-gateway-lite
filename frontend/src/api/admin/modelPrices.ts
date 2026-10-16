import { apiClient } from '../client'

export interface ModelPrice {
  model: string
  provider: string
  mode: string
  billing_mode: 'token' | 'per_request' | 'image' | 'video' | string
  input_cost_per_1m_tokens: number
  input_cost_per_1m_tokens_priority: number
  output_cost_per_1m_tokens: number
  output_cost_per_1m_tokens_priority: number
  cache_creation_cost_per_1m_tokens: number
  cache_creation_cost_per_1m_tokens_priority: number
  cache_read_cost_per_1m_tokens: number
  cache_read_cost_per_1m_tokens_priority: number
  input_cost_per_1m_tokens_above_272k: number
  output_cost_per_1m_tokens_above_272k: number
  cache_creation_cost_per_1m_tokens_above_272k: number
  cache_read_cost_per_1m_tokens_above_272k: number
  long_context_input_token_threshold: number
  output_cost_per_image: number
  output_cost_per_image_token: number
  input_cost_per_image_token: number | null
  per_request_price: number
  video_price_per_second_480p: number
  video_price_per_second_720p: number
  video_price_per_second_1080p: number
  currency: string
  supports_prompt_caching: boolean
  supports_service_tier: boolean
  disabled: boolean
  source: 'catalog' | 'override' | 'static_fallback' | string
  is_override: boolean
}

export interface ModelPriceListParams {
  page?: number
  page_size?: number
  search?: string
  provider?: string
  source?: string
}

export interface ModelPriceListResponse {
  items: ModelPrice[]
  total: number
  page: number
  page_size: number
  pages: number
}

export interface UpsertModelPriceRequest {
  model: string
  provider?: string
  mode?: string
  billing_mode?: 'token' | 'per_request' | 'image' | 'video' | string
  input_cost_per_1m_tokens?: number | null
  input_cost_per_1m_tokens_priority?: number | null
  output_cost_per_1m_tokens?: number | null
  output_cost_per_1m_tokens_priority?: number | null
  cache_creation_cost_per_1m_tokens?: number | null
  cache_creation_cost_per_1m_tokens_priority?: number | null
  cache_read_cost_per_1m_tokens?: number | null
  cache_read_cost_per_1m_tokens_priority?: number | null
  input_cost_per_1m_tokens_above_272k?: number | null
  output_cost_per_1m_tokens_above_272k?: number | null
  cache_creation_cost_per_1m_tokens_above_272k?: number | null
  cache_read_cost_per_1m_tokens_above_272k?: number | null
  long_context_input_token_threshold?: number | null
  output_cost_per_image?: number | null
  output_cost_per_image_token?: number | null
  input_cost_per_image_token?: number | null
  per_request_price?: number | null
  video_price_per_second_480p?: number | null
  video_price_per_second_720p?: number | null
  video_price_per_second_1080p?: number | null
  supports_prompt_caching?: boolean
  supports_service_tier?: boolean
  disabled?: boolean
}

export interface DeleteModelPriceResponse {
  deleted: boolean
  current: ModelPrice
}

export interface ModelPriceSyncStatus {
  enabled: boolean
  model_count: number
  override_count: number
  last_updated?: string | null
  last_success_at?: string | null
  last_error_at?: string | null
  last_error: string
  source: string
  local_hash: string
  remote_url: string
  remote_host: string
  refreshing: boolean
  catalog_version?: string
  catalog_sequence?: number
  remote_model_count?: number
}

export async function list(params: ModelPriceListParams = {}, options?: { signal?: AbortSignal }): Promise<ModelPriceListResponse> {
  const { data } = await apiClient.get<ModelPriceListResponse>('/admin/model-prices', {
    params,
    signal: options?.signal
  })
  return data
}

export async function upsert(payload: UpsertModelPriceRequest): Promise<ModelPrice> {
  const { data } = await apiClient.put<ModelPrice>('/admin/model-prices', payload)
  return data
}

export async function removeOverride(model: string): Promise<DeleteModelPriceResponse> {
  const { data } = await apiClient.delete<DeleteModelPriceResponse>('/admin/model-prices', {
    params: { model }
  })
  return data
}

export async function getSyncStatus(): Promise<ModelPriceSyncStatus> {
  const { data } = await apiClient.get<ModelPriceSyncStatus>('/admin/model-prices/sync/status')
  return data
}

export async function sync(): Promise<ModelPriceSyncStatus> {
  const { data } = await apiClient.post<ModelPriceSyncStatus>('/admin/model-prices/sync')
  return data
}

export default { list, upsert, removeOverride, getSyncStatus, sync }
