import { describe, expect, it, vi } from 'vitest'

vi.mock('@/api/admin/accounts', () => ({
  getAntigravityDefaultModelMapping: vi.fn()
}))

import {
  buildModelMappingObject,
  getModelsByPlatform,
  getPresetMappingsByPlatform,
  splitModelMappingObject
} from '../useModelWhitelist'

describe('useModelWhitelist', () => {
  it('openai 模型列表包含 GPT-5.4 官方快照', () => {
    const models = getModelsByPlatform('openai')

    expect(models).toContain('gpt-5.4')
    expect(models).toContain('gpt-5.4-mini')
    expect(models).toContain('gpt-5.4-2026-03-05')
    expect(models).toContain('codex-auto-review')
  })

  it('openai 模型列表不再暴露已下线的 ChatGPT 登录 Codex 模型', () => {
    const models = getModelsByPlatform('openai')

    expect(models).not.toContain('gpt-5')
    expect(models).not.toContain('gpt-5.1')
    expect(models).not.toContain('gpt-5.1-codex')
    expect(models).not.toContain('gpt-5.1-codex-max')
    expect(models).not.toContain('gpt-5.1-codex-mini')
    expect(models).not.toContain('gpt-5.2-codex')
    expect(models).not.toContain('gpt-5.3-codex')
  })

  it('openai 模型列表和快捷映射包含 GPT-5.6 通用别名和三个型号', () => {
    const models = getModelsByPlatform('openai')
    const mappings = getPresetMappingsByPlatform('openai')

    for (const model of ['gpt-5.6', 'gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-5.6-luna']) {
      expect(models).toContain(model)
      expect(mappings).toContainEqual(expect.objectContaining({ from: model, to: model }))
    }
  })

  it('antigravity 模型列表包含图片模型兼容项', () => {
    const models = getModelsByPlatform('antigravity')

    expect(models).toContain('gemini-2.5-flash-image')
    expect(models).toContain('gemini-3.1-flash-image')
    expect(models).toContain('gemini-3-pro-image')
  })

  it('Grok 模型列表包含 4.6、通用别名和媒体模型', () => {
    const models = getModelsByPlatform('grok')

    for (const model of [
      'grok-4.6',
      'grok-4.5',
      'grok-latest',
      'grok-4.6-latest',
      'grok-4.5-latest',
      'grok-build-latest',
      'grok-composer-2.5-fast',
      'grok-imagine',
      'grok-imagine-image-quality-20260403',
      'grok-imagine-image-quality-latest',
      'grok-imagine-image-pro',
      'grok-imagine-image-2026-03-02',
      'grok-imagine-edit',
      'grok-imagine-video-1.5',
      'grok-imagine-video-1.5-preview',
      'grok-imagine-video-1.5-2026-05-30'
    ]) {
      expect(models).toContain(model)
    }
  })

  it('Grok 快捷映射与后端默认别名保持一致', () => {
    const mappings = getPresetMappingsByPlatform('grok')

    expect(mappings).toContainEqual(expect.objectContaining({ from: 'grok-4.6', to: 'grok-4.6' }))
    expect(mappings).toContainEqual(expect.objectContaining({ from: 'grok-4.6-latest', to: 'grok-4.6' }))
    expect(mappings).toContainEqual(expect.objectContaining({ from: 'grok-latest', to: 'grok-4.5' }))
    expect(mappings).toContainEqual(expect.objectContaining({ from: 'grok-4.5-latest', to: 'grok-4.5' }))
    expect(mappings).toContainEqual(expect.objectContaining({ from: 'grok-build-latest', to: 'grok-4.5' }))
    expect(mappings).toContainEqual(expect.objectContaining({ from: 'grok-imagine', to: 'grok-imagine-image-quality' }))
    expect(mappings).toContainEqual(expect.objectContaining({ from: 'grok-imagine-image', to: 'grok-imagine-image' }))
    expect(mappings).toContainEqual(expect.objectContaining({ from: 'grok-imagine-image-quality', to: 'grok-imagine-image-quality' }))
    expect(mappings).toContainEqual(expect.objectContaining({ from: 'grok-imagine-image-pro', to: 'grok-imagine-image-quality' }))
    expect(mappings).toContainEqual(expect.objectContaining({ from: 'grok-imagine-image-2026-03-02', to: 'grok-imagine-image' }))
    expect(mappings).toContainEqual(expect.objectContaining({ from: 'grok-imagine-video', to: 'grok-imagine-video' }))
    expect(mappings).toContainEqual(expect.objectContaining({ from: 'grok-imagine-video-1.5', to: 'grok-imagine-video-1.5' }))
    expect(mappings).toContainEqual(expect.objectContaining({ from: 'grok-imagine-video-1.5-preview', to: 'grok-imagine-video-1.5' }))
    expect(mappings).toContainEqual(expect.objectContaining({ from: 'grok-imagine-video-1.5-2026-05-30', to: 'grok-imagine-video-1.5' }))
    expect(mappings).toContainEqual(expect.objectContaining({ from: 'grok-4.20-multi-agent-0309', to: 'grok-4.20-multi-agent-0309' }))
  })

  it('Claude 模型列表包含新发布的 Claude 模型', () => {
    expect(getModelsByPlatform('claude')).toContain('claude-fable-5-1')
    expect(getModelsByPlatform('antigravity')).toContain('claude-fable-5-1')
    expect(getModelsByPlatform('claude')).toContain('claude-fable-5')
    expect(getModelsByPlatform('antigravity')).toContain('claude-fable-5')
    expect(getModelsByPlatform('claude')).toContain('claude-opus-4-8')
    expect(getModelsByPlatform('antigravity')).toContain('claude-opus-4-8')
    expect(getModelsByPlatform('claude')).toContain('claude-sonnet-5')
    expect(getPresetMappingsByPlatform('anthropic')).toContainEqual(
      expect.objectContaining({ from: 'claude-sonnet-5', to: 'claude-sonnet-5' })
    )
  })

  it('gemini 模型列表包含原生生图模型', () => {
    const models = getModelsByPlatform('gemini')

    expect(models).toContain('gemini-2.5-flash-image')
    expect(models).toContain('gemini-3.1-flash-image')
    expect(models.indexOf('gemini-3.1-flash-image')).toBeLessThan(models.indexOf('gemini-2.0-flash'))
    expect(models.indexOf('gemini-2.5-flash-image')).toBeLessThan(models.indexOf('gemini-2.5-flash'))
  })

  it('antigravity 模型列表会把新的 Gemini 图片模型排在前面', () => {
    const models = getModelsByPlatform('antigravity')

    expect(models.indexOf('gemini-3.1-flash-image')).toBeLessThan(models.indexOf('gemini-2.5-flash'))
    expect(models.indexOf('gemini-2.5-flash-image')).toBeLessThan(models.indexOf('gemini-2.5-flash-lite'))
  })

  it('whitelist 模式会忽略通配符条目', () => {
    const mapping = buildModelMappingObject('whitelist', ['claude-*', 'gemini-3.1-flash-image'], [])
    expect(mapping).toEqual({
      'gemini-3.1-flash-image': 'gemini-3.1-flash-image'
    })
  })

  it('whitelist 模式会保留 GPT-5.4 官方快照的精确映射', () => {
    const mapping = buildModelMappingObject('whitelist', ['gpt-5.4-2026-03-05'], [])

    expect(mapping).toEqual({
      'gpt-5.4-2026-03-05': 'gpt-5.4-2026-03-05'
    })
  })

  it('whitelist keeps GPT-5.4 mini exact mappings', () => {
    const mapping = buildModelMappingObject('whitelist', ['gpt-5.4-mini'], [])

    expect(mapping).toEqual({
      'gpt-5.4-mini': 'gpt-5.4-mini'
    })
  })

  it('combined 模式会同时保留白名单身份映射和模型映射', () => {
    const mapping = buildModelMappingObject(
      'combined',
      ['gpt-5.4', 'claude-*'],
      [
        { from: 'gpt-latest', to: 'gpt-5.4' },
        { from: 'gpt-5.4', to: 'gpt-5.4-mini' }
      ]
    )

    expect(mapping).toEqual({
      'gpt-5.4': 'gpt-5.4-mini',
      'gpt-latest': 'gpt-5.4'
    })
  })

  it('splitModelMappingObject 会把身份映射还原成白名单，其余保留为映射', () => {
    const parsed = splitModelMappingObject({
      'gpt-5.4': 'gpt-5.4',
      'gpt-latest': 'gpt-5.4',
      ' ': 'gpt-empty',
      broken: 123
    })

    expect(parsed).toEqual({
      allowedModels: ['gpt-5.4'],
      modelMappings: [{ from: 'gpt-latest', to: 'gpt-5.4' }]
    })
  })
})
