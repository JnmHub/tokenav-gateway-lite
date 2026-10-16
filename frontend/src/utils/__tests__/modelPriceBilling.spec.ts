import { describe, expect, it } from 'vitest'
import { normalizeModelPricePayloadForBillingMode } from '@/utils/modelPriceBilling'

const mixedPayload = {
  model: 'grok-imagine-video-1.5',
  billing_mode: 'video',
  input_cost_per_1m_tokens: 2,
  input_cost_per_1m_tokens_priority: 4,
  output_cost_per_1m_tokens: 6,
  cache_creation_cost_per_1m_tokens: 1,
  cache_read_cost_per_1m_tokens: 0.5,
  input_cost_per_1m_tokens_above_272k: 4,
  long_context_input_token_threshold: 272000,
  output_cost_per_image: 0.02,
  input_cost_per_image_token: 0,
  output_cost_per_image_token: 0.000001,
  per_request_price: 0.1,
  video_price_per_second_480p: 0.08,
  video_price_per_second_720p: 0.14,
  video_price_per_second_1080p: 0.25,
  supports_prompt_caching: true,
  supports_service_tier: true
} as const

describe('normalizeModelPricePayloadForBillingMode', () => {
  it('视频计费只保留视频字段', () => {
    const result = normalizeModelPricePayloadForBillingMode(mixedPayload)

    expect(result.video_price_per_second_480p).toBe(0.08)
    expect(result.video_price_per_second_1080p).toBe(0.25)
    expect(result.input_cost_per_1m_tokens).toBeNull()
    expect(result.output_cost_per_image).toBeNull()
    expect(result.input_cost_per_image_token).toBeNull()
    expect(result.per_request_price).toBeNull()
    expect(result.long_context_input_token_threshold).toBe(0)
    expect(result.supports_prompt_caching).toBe(false)
    expect(result.supports_service_tier).toBe(false)
  })

  it('图片计费只保留图片字段', () => {
    const result = normalizeModelPricePayloadForBillingMode({ ...mixedPayload, billing_mode: 'image' })

    expect(result.output_cost_per_image).toBe(0.02)
    expect(result.input_cost_per_image_token).toBe(0)
    expect(result.output_cost_per_image_token).toBe(0.000001)
    expect(result.video_price_per_second_480p).toBeNull()
    expect(result.input_cost_per_1m_tokens).toBeNull()
    expect(result.per_request_price).toBeNull()
  })

  it('按次计费只保留按次字段', () => {
    const result = normalizeModelPricePayloadForBillingMode({ ...mixedPayload, billing_mode: 'per_request' })

    expect(result.per_request_price).toBe(0.1)
    expect(result.output_cost_per_image).toBeNull()
    expect(result.input_cost_per_image_token).toBeNull()
    expect(result.video_price_per_second_480p).toBeNull()
    expect(result.input_cost_per_1m_tokens).toBeNull()
  })

  it('Token 计费保留 Token 字段并清空媒体字段', () => {
    const result = normalizeModelPricePayloadForBillingMode({ ...mixedPayload, billing_mode: 'token' })

    expect(result.input_cost_per_1m_tokens).toBe(2)
    expect(result.long_context_input_token_threshold).toBe(272000)
    expect(result.supports_service_tier).toBe(true)
    expect(result.output_cost_per_image).toBeNull()
    expect(result.input_cost_per_image_token).toBeNull()
    expect(result.video_price_per_second_480p).toBeNull()
    expect(result.per_request_price).toBeNull()
  })
})
