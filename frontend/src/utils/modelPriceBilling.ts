import type { UpsertModelPriceRequest } from '@/api/admin'

const tokenPriceFields = [
  'input_cost_per_1m_tokens',
  'input_cost_per_1m_tokens_priority',
  'output_cost_per_1m_tokens',
  'output_cost_per_1m_tokens_priority',
  'cache_creation_cost_per_1m_tokens',
  'cache_creation_cost_per_1m_tokens_priority',
  'cache_read_cost_per_1m_tokens',
  'cache_read_cost_per_1m_tokens_priority',
  'input_cost_per_1m_tokens_above_272k',
  'output_cost_per_1m_tokens_above_272k',
  'cache_creation_cost_per_1m_tokens_above_272k',
  'cache_read_cost_per_1m_tokens_above_272k'
] as const

const imagePriceFields = ['output_cost_per_image', 'output_cost_per_image_token', 'input_cost_per_image_token'] as const
const videoPriceFields = [
  'video_price_per_second_480p',
  'video_price_per_second_720p',
  'video_price_per_second_1080p'
] as const

export function normalizeModelPricePayloadForBillingMode(
  payload: UpsertModelPriceRequest
): UpsertModelPriceRequest {
  const billingMode = normalizeBillingMode(payload.billing_mode)
  const normalized: UpsertModelPriceRequest = { ...payload, billing_mode: billingMode }

  if (billingMode !== 'token') {
    for (const field of tokenPriceFields) normalized[field] = null
    normalized.long_context_input_token_threshold = 0
    normalized.supports_prompt_caching = false
    normalized.supports_service_tier = false
  }
  if (billingMode !== 'image') {
    for (const field of imagePriceFields) normalized[field] = null
  }
  if (billingMode !== 'video') {
    for (const field of videoPriceFields) normalized[field] = null
  }
  if (billingMode !== 'per_request') normalized.per_request_price = null

  return normalized
}

function normalizeBillingMode(value: UpsertModelPriceRequest['billing_mode']) {
  if (value === 'image' || value === 'video' || value === 'per_request') return value
  return 'token'
}
